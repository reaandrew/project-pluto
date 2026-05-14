// Package main is the spec-generator Lambda. EventBridge routes
// `website.qualified` events here via an SQS main queue (with DLQ).
// Per-record pipeline:
//
//  1. Decode the envelope (events.FromSQS).
//  2. idempotency.WithIdempotency("spec-generator", env.EventID).
//  3. Load Business + Audit + Qualification rows + the
//     vertical-appropriate style guide (falls back to "default" when
//     no vertical-specific row exists).
//  4. spec.Run — wraps prompts.SpecV1 (Sonnet 4.6) with the spec's
//     cache key (businessId, auditId, styleGuide.Version) and
//     post-validator chain. Cost capped at
//     PipelineSettings.Budgets.DailyBedrockUsd via the bedrock stage.
//  5. Persist the Spec row per 02-data-model.md § "Spec" (status =
//     "draft" — operator approves via the iter 4.3 UI).
//  6. Publish `spec.generated`.
//
// Entry-level killswitch wraps StagePreview (the spec-generator rolls
// up to the preview stage — operators pause preview generation by
// flipping `previewEnabled` even though the actual Bedrock spend
// accrues against the spec stage).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/spec"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/style"
)

const consumerName = "spec-generator"

// WebsiteQualifiedDetail mirrors the lambdas/qualifier emitted shape.
// Duplicated rather than imported (sibling package main).
type WebsiteQualifiedDetail struct {
	BusinessID      string  `json:"businessId"`
	QualificationID string  `json:"qualificationId"`
	PriorityScore   float64 `json:"priorityScore"`
	AuditID         string  `json:"auditId"`
}

// SpecGeneratedDetail is the `spec.generated` event payload per
// 03-events.md. Token counts are best-effort: bedrock.InvokeStructured
// records actual spend via cost.Record but doesn't surface the count
// back, so we emit zeros for now and revisit when tuner-style needs
// per-prompt token analytics (iter 9.x).
type SpecGeneratedDetail struct {
	BusinessID string `json:"businessId"`
	SpecID     string `json:"specId"`
	TokensIn   int    `json:"tokensIn"`
	TokensOut  int    `json:"tokensOut"`
	ModelID    string `json:"modelId"`
}

// runDeps is the testable surface.
type runDeps struct {
	GetBusiness   func(ctx context.Context, businessID string) (*BusinessRow, error)
	GetAudit      func(ctx context.Context, businessID, auditID string) (*AuditRow, error)
	GetStyleGuide func(ctx context.Context, vertical string) (style.Guide, error)
	RunSpec       func(ctx context.Context, in spec.Input, capUSD float64) (schemas.SpecV1, error)
	PutSpec       func(ctx context.Context, row SpecRow) error
	Publish       func(ctx context.Context, env pkgevents.Envelope[SpecGeneratedDetail]) error
	CapUSD        func(ctx context.Context, stage string) (float64, error)
	Now           func() time.Time
	NewSpecID     func() string
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StagePreview, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[WebsiteQualifiedDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[WebsiteQualifiedDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[WebsiteQualifiedDetail]) error {
	logger := applog.FromContext(ctx).With(
		"eventId", env.EventID,
		"businessId", env.Detail.BusinessID,
		"auditId", env.Detail.AuditID,
	)
	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("spec-generator.replay.skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[WebsiteQualifiedDetail], logger *slog.Logger) error {
	biz, err := d.GetBusiness(ctx, env.Detail.BusinessID)
	if err != nil {
		return fmt.Errorf("spec-generator: get business: %w", err)
	}
	if biz == nil {
		logger.Warn("spec-generator.business.missing")
		return nil
	}

	audit, err := d.GetAudit(ctx, env.Detail.BusinessID, env.Detail.AuditID)
	if err != nil {
		return fmt.Errorf("spec-generator: get audit: %w", err)
	}
	if audit == nil {
		logger.Warn("spec-generator.audit.missing")
		return nil
	}

	guide, err := d.GetStyleGuide(ctx, biz.Vertical)
	if err != nil {
		return fmt.Errorf("spec-generator: get style guide: %w", err)
	}

	// Bedrock budget cap is shared across audit + spec under the
	// "preview" operator-rollup (both pull from
	// Budgets.DailyBedrockUsd). cost.Assert inside InvokeStructured
	// uses the prompt's Stage ("spec") as the ledger key.
	capUSD, err := d.CapUSD(ctx, killswitch.StagePreview)
	if err != nil {
		return fmt.Errorf("spec-generator: lookup spec budget: %w", err)
	}

	in := buildSpecInput(biz, audit, guide)
	content, err := d.RunSpec(ctx, in, capUSD)
	if err != nil {
		return fmt.Errorf("spec-generator: spec.Run: %w", err)
	}

	specID := d.NewSpecID()
	now := d.Now().UTC().Format(time.RFC3339)
	row := SpecRow{
		PK:        "BUSINESS#" + biz.ID,
		SK:        "SPEC#" + specID,
		Type:      "Spec",
		ID:        specID,
		Version:   1,
		Status:    "draft",
		Content:   content,
		ModelID:   bedrock.ModelSonnet46,
		PromptID:  "spec.v1",
		CreatedAt: now,
		UpdatedAt: now,
		Etag:      randomHex(16),
	}
	if err := d.PutSpec(ctx, row); err != nil {
		return fmt.Errorf("spec-generator: put spec: %w", err)
	}

	out := pkgevents.New("spec.generated", consumerName, SpecGeneratedDetail{
		BusinessID: biz.ID,
		SpecID:     specID,
		ModelID:    bedrock.ModelSonnet46,
	}).WithCorrelation(env.CorrelationID).WithCausation(env.EventID)
	if err := d.Publish(ctx, out); err != nil {
		// Spec row is persisted — downstream (operator UI) can recover
		// from the spec list. Surface the error so SQS retries this
		// record only.
		return fmt.Errorf("spec-generator: publish spec.generated: %w", err)
	}
	logger.Info("spec-generator.completed",
		"specId", specID,
		"sections", len(content.Page.Sections),
	)
	return nil
}

// buildSpecInput projects the Business + Audit + style guide onto the
// spec.Input shape. The AuditSummary squeezes the audit row's
// qualitative summary + issue-type list (no raw HTML, no per-issue
// description) so the prompt stays compact.
func buildSpecInput(biz *BusinessRow, audit *AuditRow, guide style.Guide) spec.Input {
	summary := ""
	issueTypes := []string{}
	if audit.Qualitative != nil {
		summary = audit.Qualitative.Summary
		seen := map[string]bool{}
		for _, iss := range audit.Qualitative.Issues {
			if iss.Type != "" && !seen[iss.Type] {
				seen[iss.Type] = true
				issueTypes = append(issueTypes, iss.Type)
			}
		}
	}
	return spec.Input{
		BusinessID: biz.ID,
		AuditID:    audit.ID,
		Business: spec.Business{
			Name: biz.Name, Domain: biz.Domain,
			Vertical: biz.Vertical, Location: biz.Location,
		},
		AuditSummary: spec.AuditSummary{
			Score:      audit.Score,
			Summary:    summary,
			IssueTypes: issueTypes,
		},
		StyleGuide: guide,
		// ExtractedContent left empty — iter 2.x's politefetch only
		// stashes the HTML snapshot; a structured services/contact
		// extractor lands with iter 6.x (contact enrichment). The
		// prompt tolerates empty extracted_content; the model leans on
		// the audit summary instead.
	}
}

// --- row shapes ---------------------------------------------------------

// BusinessRow mirrors the subset of lambdas/discover's businessRow we read.
type BusinessRow struct {
	ID       string `dynamodbav:"id"`
	Name     string `dynamodbav:"name"`
	Domain   string `dynamodbav:"domain"`
	Vertical string `dynamodbav:"vertical"`
	Location string `dynamodbav:"location"`
}

// AuditRow mirrors the subset of lambdas/audit's AuditRow we read.
type AuditRow struct {
	ID          string            `dynamodbav:"id"`
	Score       int               `dynamodbav:"score"`
	Qualitative *AuditQualitative `dynamodbav:"qualitative,omitempty"`
}

type AuditQualitative struct {
	Summary string           `dynamodbav:"summary"`
	Issues  []AuditQualIssue `dynamodbav:"issues"`
}

type AuditQualIssue struct {
	Type        string `dynamodbav:"type"`
	Severity    string `dynamodbav:"severity"`
	Description string `dynamodbav:"description"`
}

// SpecRow mirrors 02-data-model.md § "Spec".
type SpecRow struct {
	PK        string         `dynamodbav:"pk"`
	SK        string         `dynamodbav:"sk"`
	Type      string         `dynamodbav:"type"`
	ID        string         `dynamodbav:"id"`
	Version   int            `dynamodbav:"version"`
	Status    string         `dynamodbav:"status"`
	Content   schemas.SpecV1 `dynamodbav:"content"`
	ModelID   string         `dynamodbav:"modelId"`
	PromptID  string         `dynamodbav:"promptId"`
	CreatedAt string         `dynamodbav:"createdAt"`
	UpdatedAt string         `dynamodbav:"updatedAt"`
	Etag      string         `dynamodbav:"etag"`
}

// --- AWS wiring (production) -------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	return runDeps{
		GetBusiness:   getBusiness,
		GetAudit:      getAudit,
		GetStyleGuide: style.GetOrDefault,
		RunSpec:       spec.Run,
		PutSpec:       putSpec,
		Publish: func(ctx context.Context, env pkgevents.Envelope[SpecGeneratedDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		CapUSD:    killswitch.CapUSD,
		Now:       time.Now,
		NewSpecID: func() string { return randomHex(16) },
	}, nil
}

func getBusiness(ctx context.Context, businessID string) (*BusinessRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "PROFILE"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get business %s: %w", businessID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var row BusinessRow
	if err := attributevalue.UnmarshalMap(out.Item, &row); err != nil {
		return nil, fmt.Errorf("unmarshal business %s: %w", businessID, err)
	}
	return &row, nil
}

func getAudit(ctx context.Context, businessID, auditID string) (*AuditRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "AUDIT#" + auditID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get audit %s/%s: %w", businessID, auditID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var row AuditRow
	if err := attributevalue.UnmarshalMap(out.Item, &row); err != nil {
		return nil, fmt.Errorf("unmarshal audit %s: %w", auditID, err)
	}
	return &row, nil
}

func putSpec(ctx context.Context, row SpecRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal spec: %w", err)
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("put spec %s: %w", row.ID, err)
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("spec-generator: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
