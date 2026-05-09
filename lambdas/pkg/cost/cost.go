// Package cost implements the daily-spend ledger and budget-cap wrapper from
// .ralph/specs/05-capacity-and-cost.md. Every paid call (Bedrock, Google
// Places, screenshot rendering, etc.) wraps its invocation in WithCostCap so
// today's spend never exceeds the configured per-stage budget.
//
// Spend is recorded in the items table at:
//
//	pk = CAP#YYYY-MM-DD
//	sk = STAGE#<stage>
//
// with `spentUsd` (running sum) and `callCount` (running call count). A 30-day
// TTL on `expires_at` keeps the table bounded.
//
// The per-stage cap is supplied by the caller (it lives in PipelineSettings
// and is read by pkg/killswitch in iter 0.E.9). Pass capUsd <= 0 to disable
// the cap check (useful for tests or for stages with metered-but-uncapped
// spend).
package cost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// RecordType is written to the `type` attribute, distinguishing cost-ledger
// rows from business data when scanning the table.
const RecordType = "CostRecord"

// RetentionTTL keeps cost-ledger rows for 30 days; tuners + audits can read
// the trailing window for spend analysis.
const RetentionTTL = 30 * 24 * time.Hour

// ErrBudgetCapExceeded is returned by Assert (and propagated from
// WithCostCap) when today's spend plus the estimate would exceed the cap.
var ErrBudgetCapExceeded = errors.New("cost: daily budget cap exceeded")

// nowFunc is overridable in tests for deterministic dates.
var nowFunc = func() time.Time { return time.Now().UTC() }

// CostRecord is the DynamoDB shape for a stage's daily spend.
type CostRecord struct {
	PK        string  `dynamodbav:"pk"`
	SK        string  `dynamodbav:"sk"`
	Type      string  `dynamodbav:"type"`
	Stage     string  `dynamodbav:"stage"`
	Date      string  `dynamodbav:"date"`
	SpentUsd  float64 `dynamodbav:"spentUsd"`
	CallCount int     `dynamodbav:"callCount"`
	UpdatedAt string  `dynamodbav:"updatedAt"`
	ExpiresAt int64   `dynamodbav:"expires_at"`
}

// PKForDate returns the partition key for a UTC date.
func PKForDate(t time.Time) string {
	return "CAP#" + t.UTC().Format("2006-01-02")
}

// SKForStage returns the sort key for a stage.
func SKForStage(stage string) string {
	return "STAGE#" + stage
}

// Get returns today's cost record for stage. If no record exists yet, returns
// a zero-valued CostRecord (SpentUsd = 0) with no error — callers treat
// "no record" as "no spend yet".
func Get(ctx context.Context, stage string) (CostRecord, error) {
	var rec CostRecord
	if stage == "" {
		return rec, errors.New("cost: stage is required")
	}
	client, err := ddb.Client(ctx)
	if err != nil {
		return rec, fmt.Errorf("cost: getting DynamoDB client: %w", err)
	}
	table := ddb.TableName()
	if table == "" {
		return rec, errors.New("cost: ITEMS_TABLE is not set")
	}
	now := nowFunc()
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(table),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: PKForDate(now)},
			"sk": &dtypes.AttributeValueMemberS{Value: SKForStage(stage)},
		},
	})
	if err != nil {
		return rec, fmt.Errorf("cost: GetItem: %w", err)
	}
	if len(out.Item) == 0 {
		return CostRecord{Stage: stage, Date: now.Format("2006-01-02")}, nil
	}
	if err := attributevalue.UnmarshalMap(out.Item, &rec); err != nil {
		return rec, fmt.Errorf("cost: unmarshalling record: %w", err)
	}
	return rec, nil
}

// Record adds usd to today's spend counter for stage and increments callCount
// by 1. Atomic via UpdateItem ADD; counter slop on retry is acceptable per
// the spec.
func Record(ctx context.Context, stage string, usd float64) error {
	if stage == "" {
		return errors.New("cost: stage is required")
	}
	if usd < 0 {
		return errors.New("cost: usd must be >= 0")
	}
	client, err := ddb.Client(ctx)
	if err != nil {
		return fmt.Errorf("cost: getting DynamoDB client: %w", err)
	}
	table := ddb.TableName()
	if table == "" {
		return errors.New("cost: ITEMS_TABLE is not set")
	}
	now := nowFunc()
	expiresAt := now.Add(RetentionTTL).Unix()

	_, err = client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(table),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: PKForDate(now)},
			"sk": &dtypes.AttributeValueMemberS{Value: SKForStage(stage)},
		},
		UpdateExpression: aws.String("ADD spentUsd :usd, callCount :one " +
			"SET #t = :type, stage = :stage, #date = :date, updatedAt = :now, expires_at = :exp"),
		ExpressionAttributeNames: map[string]string{
			"#t":    "type",
			"#date": "date",
		},
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":usd":   &dtypes.AttributeValueMemberN{Value: formatFloat(usd)},
			":one":   &dtypes.AttributeValueMemberN{Value: "1"},
			":type":  &dtypes.AttributeValueMemberS{Value: RecordType},
			":stage": &dtypes.AttributeValueMemberS{Value: stage},
			":date":  &dtypes.AttributeValueMemberS{Value: now.Format("2006-01-02")},
			":now":   &dtypes.AttributeValueMemberS{Value: now.Format(time.RFC3339)},
			":exp":   &dtypes.AttributeValueMemberN{Value: formatInt(expiresAt)},
		},
	})
	if err != nil {
		return fmt.Errorf("cost: UpdateItem: %w", err)
	}
	return nil
}

// Assert returns ErrBudgetCapExceeded if today's spent + estimateUsd would
// exceed capUsd. capUsd <= 0 disables the check (returns nil).
func Assert(ctx context.Context, stage string, estimateUsd, capUsd float64) error {
	if capUsd <= 0 {
		return nil
	}
	if estimateUsd < 0 {
		return errors.New("cost: estimateUsd must be >= 0")
	}
	rec, err := Get(ctx, stage)
	if err != nil {
		return err
	}
	if rec.SpentUsd+estimateUsd > capUsd {
		return fmt.Errorf("%w: stage=%s spent=%.4f estimate=%.4f cap=%.4f",
			ErrBudgetCapExceeded, stage, rec.SpentUsd, estimateUsd, capUsd)
	}
	return nil
}

// WithCostCap orchestrates the budget-cap dance: Assert before invocation,
// run fn, Record actualUsd on success. fn returns (result, actualUsd, err).
//
// If Assert returns ErrBudgetCapExceeded, fn is NOT called and the error is
// returned to the caller (typically translated into pipeline.skipped_capped).
//
// On fn-error the actualUsd is NOT recorded — failed paid calls don't bill
// (the assumption holds for Bedrock and Places; SES is post-paid but not
// caller-cancellable).
func WithCostCap[T any](
	ctx context.Context,
	stage string,
	estimateUsd, capUsd float64,
	fn func(ctx context.Context) (T, float64, error),
) (T, error) {
	var zero T
	if err := Assert(ctx, stage, estimateUsd, capUsd); err != nil {
		return zero, err
	}
	result, actualUsd, err := fn(ctx)
	if err != nil {
		return zero, err
	}
	if recErr := Record(ctx, stage, actualUsd); recErr != nil {
		// The paid call succeeded; record failure shouldn't poison the
		// caller's result. Return the result and the record-error wrapped
		// so the caller can decide.
		return result, fmt.Errorf("cost: recording spend (call already succeeded): %w", recErr)
	}
	return result, nil
}

// formatFloat renders a USD value as a DynamoDB Number string with 6 decimal
// places (1e-6 USD precision — adequate for sub-cent metering).
func formatFloat(v float64) string {
	return fmt.Sprintf("%.6f", v)
}

// formatInt renders an int64 as a DynamoDB Number string.
func formatInt(v int64) string {
	return fmt.Sprintf("%d", v)
}
