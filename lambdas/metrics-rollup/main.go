// Package main is the metrics-rollup Lambda (iter 11.1). An hourly
// EventBridge-scheduled job that snapshots the pipeline funnel
// (Business count per status, via the gsi1 status partitions) and the
// day's spend (the cost ledger's CAP#<date> stage rows), and writes a
// single Metric item per UTC date (pk=METRIC#<date> sk=ROLLUP,
// overwritten each run). /metrics (iter 11.2/11.3) reads these
// precomputed rows instead of scanning the live table.
//
// Hourly more than satisfies the spec's "refreshes within 5 minutes"
// intent without a per-event hot path (the funnel is a slow-moving
// aggregate). NOT kill-switch gated and no paid call — pure read +
// one PutItem; idempotent (the date row is overwritten).
//
// Spec note: .ralph/specs/02-data-model.md enumerates the Business
// status set + the CAP#<date> cost rows but does not define a Metric
// item; the .ralph tree is read-only, so this shape is the canonical
// definition, driven by .ralph/fix_plan.md 11.1.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
)

// FunnelStatuses is the ordered Business.status journey from
// .ralph/specs/02-data-model.md — the funnel the operator sees.
var FunnelStatuses = []string{
	"new", "auditing", "qualified", "rejected",
	"awaiting_review", "approved", "regenerate_requested",
	"rejected_after_review", "email_drafted", "emailed",
	"responded", "converted",
}

const metricTTLDays = 400

// MetricRow is the daily roll-up snapshot.
type MetricRow struct {
	PK          string             `dynamodbav:"pk"`
	SK          string             `dynamodbav:"sk"`
	Type        string             `dynamodbav:"type"`
	Date        string             `dynamodbav:"date"`
	Funnel      map[string]int     `dynamodbav:"funnel"`
	CostByStage map[string]float64 `dynamodbav:"costByStage"`
	TotalCost   float64            `dynamodbav:"totalCostUsd"`
	GeneratedAt string             `dynamodbav:"generatedAt"`
	ExpiresAt   int64              `dynamodbav:"expires_at"`
}

type runDeps struct {
	CountByStatus func(ctx context.Context, status string) (int, error)
	CostForDate   func(ctx context.Context, date string) (map[string]float64, error)
	PutMetric     func(ctx context.Context, m MetricRow) error
	Now           func() time.Time
}

func handle(ctx context.Context) error {
	deps, err := buildDeps(ctx)
	if err != nil {
		return err
	}
	return rollup(ctx, deps)
}

func rollup(ctx context.Context, d runDeps) error {
	logger := applog.FromContext(ctx)
	now := d.Now().UTC()
	date := now.Format("2006-01-02")

	funnel := make(map[string]int, len(FunnelStatuses))
	for _, s := range FunnelStatuses {
		n, err := d.CountByStatus(ctx, s)
		if err != nil {
			return fmt.Errorf("count status %s: %w", s, err)
		}
		funnel[s] = n
	}

	costByStage, err := d.CostForDate(ctx, date)
	if err != nil {
		return fmt.Errorf("cost for %s: %w", date, err)
	}
	var total float64
	for _, v := range costByStage {
		total += v
	}

	m := MetricRow{
		PK:          "METRIC#" + date,
		SK:          "ROLLUP",
		Type:        "Metric",
		Date:        date,
		Funnel:      funnel,
		CostByStage: costByStage,
		TotalCost:   total,
		GeneratedAt: now.Format(time.RFC3339),
		ExpiresAt:   now.AddDate(0, 0, metricTTLDays).Unix(),
	}
	if err := d.PutMetric(ctx, m); err != nil {
		return fmt.Errorf("put metric %s: %w", date, err)
	}
	logger.Info("metrics_rollup.done", "date", date,
		"converted", funnel["converted"], "totalCostUsd", total)
	return nil
}

// --- AWS wiring -------------------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	if _, err := awsconfig.LoadDefaultConfig(ctx); err != nil {
		return runDeps{}, fmt.Errorf("metrics-rollup: AWS config: %w", err)
	}
	return runDeps{
		CountByStatus: countByStatus,
		CostForDate:   costForDate,
		PutMetric:     putMetric,
		Now:           time.Now,
	}, nil
}

func countByStatus(ctx context.Context, status string) (int, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return 0, err
	}
	var total int
	var lastKey map[string]dtypes.AttributeValue
	for {
		out, err := client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(ddb.TableName()),
			IndexName:              aws.String("gsi1"),
			KeyConditionExpression: aws.String("gsi1pk = :pk"),
			ExpressionAttributeValues: map[string]dtypes.AttributeValue{
				":pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#STATUS#" + status},
			},
			Select:            dtypes.SelectCount, // no item transfer
			ExclusiveStartKey: lastKey,
		})
		if err != nil {
			return 0, fmt.Errorf("query gsi1 %s: %w", status, err)
		}
		total += int(out.Count)
		if len(out.LastEvaluatedKey) == 0 {
			return total, nil
		}
		lastKey = out.LastEvaluatedKey
	}
}

func costForDate(ctx context.Context, date string) (map[string]float64, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(ddb.TableName()),
		KeyConditionExpression: aws.String("pk = :pk"),
		ExpressionAttributeValues: map[string]dtypes.AttributeValue{
			":pk": &dtypes.AttributeValueMemberS{Value: "CAP#" + date},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("query CAP#%s: %w", date, err)
	}
	byStage := map[string]float64{}
	for _, it := range out.Items {
		var r cost.CostRecord
		if err := attributevalue.UnmarshalMap(it, &r); err != nil {
			return nil, fmt.Errorf("unmarshal cost record: %w", err)
		}
		if r.Stage != "" {
			byStage[r.Stage] += r.SpentUsd
		}
	}
	return byStage, nil
}

func putMetric(ctx context.Context, m MetricRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(m)
	if err != nil {
		return fmt.Errorf("marshal Metric: %w", err)
	}
	if _, err := client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()), Item: item,
	}); err != nil {
		return fmt.Errorf("put Metric: %w", err)
	}
	return nil
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
