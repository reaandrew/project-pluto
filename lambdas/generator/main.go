// Package main is the generator Lambda. EventBridge routes
// `spec.approved` events here via an SQS main queue (with DLQ).
// Per-record pipeline:
//
//  1. Decode the envelope (events.FromSQS).
//  2. idempotency.WithIdempotency("generator", env.EventID).
//  3. Load Business + Spec (status must be "approved").
//  4. Compose the page via pkg/sitebundle.Render — applies the
//     renderer guarantees (testimonial stripping is upstream;
//     unverified-badge dropping here; image-prompt placeholder for
//     the publisher to swap).
//  5. Upload the HTML to S3 at `generated/<websiteId>/index.html`
//     (the publisher in iter 5.3 copies S3 → R2 + issues the
//     passcode).
//  6. Persist a Website row (status="generated", id=websiteId,
//     specId, r2Prefix="sites/<websiteId>/").
//  7. Publish `website.generated`.
//
// Entry-level killswitch wraps StagePreview (generator rolls up to
// preview per killswitch.StageMap). SQS retry budget = 3 → DLQ.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/sitebundle"
)

const consumerName = "generator"

// SpecApprovedDetail mirrors api-specs's emitted spec.approved
// payload. Duplicated because that lives in package main.
type SpecApprovedDetail struct {
	BusinessID string `json:"businessId"`
	SpecID     string `json:"specId"`
	Version    int    `json:"version"`
}

// WebsiteGeneratedDetail is the `website.generated` event payload.
type WebsiteGeneratedDetail struct {
	BusinessID string `json:"businessId"`
	SpecID     string `json:"specId"`
	WebsiteID  string `json:"websiteId"`
	S3Key      string `json:"s3Key"`
}

// S3Uploader is the subset of *s3.Client we depend on.
type S3Uploader interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// runDeps is the testable surface.
type runDeps struct {
	GetBusiness  func(ctx context.Context, businessID string) (*BusinessRow, error)
	GetSpec      func(ctx context.Context, businessID, specID string) (*SpecRow, error)
	PutHTML      func(ctx context.Context, key string, body []byte) error
	PutWebsite   func(ctx context.Context, row WebsiteRow) error
	Publish      func(ctx context.Context, env pkgevents.Envelope[WebsiteGeneratedDetail]) error
	Now          func() time.Time
	NewWebsiteID func() string
	BlobsBucket  string
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StagePreview, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[SpecApprovedDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[SpecApprovedDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[SpecApprovedDetail]) error {
	logger := applog.FromContext(ctx).With(
		"eventId", env.EventID,
		"businessId", env.Detail.BusinessID,
		"specId", env.Detail.SpecID,
	)
	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("generator.replay.skipped")
		return nil
	}
	return err
}

func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[SpecApprovedDetail], logger *slog.Logger) error {
	biz, err := d.GetBusiness(ctx, env.Detail.BusinessID)
	if err != nil {
		return fmt.Errorf("generator: get business: %w", err)
	}
	if biz == nil {
		logger.Warn("generator.business.missing")
		return nil
	}

	spec, err := d.GetSpec(ctx, env.Detail.BusinessID, env.Detail.SpecID)
	if err != nil {
		return fmt.Errorf("generator: get spec: %w", err)
	}
	if spec == nil {
		logger.Warn("generator.spec.missing")
		return nil
	}
	// Defensive guard — the spec MUST be approved before we burn
	// bytes generating HTML. A race-y approve→reject would otherwise
	// generate from a no-longer-approved Spec.
	if spec.Status != "approved" {
		logger.Warn("generator.spec.not_approved", "status", spec.Status)
		return nil
	}

	html, err := sitebundle.Render(sitebundle.Input{
		Spec:         spec.Content,
		BusinessName: biz.Name,
		Year:         d.Now().UTC().Year(),
		// OptOutLine + VerifiedBadges + ImageURLFor come from iter
		// 6.x once contact + asset-pipeline land. For now: leave
		// opt-out blank; drop all Trust badges (defensive); leave
		// image placeholders for the publisher.
	})
	if err != nil {
		return fmt.Errorf("generator: sitebundle.Render: %w", err)
	}

	websiteID := d.NewWebsiteID()
	s3Key := fmt.Sprintf("generated/%s/index.html", websiteID)
	if err := d.PutHTML(ctx, s3Key, []byte(html)); err != nil {
		return fmt.Errorf("generator: put html: %w", err)
	}

	now := d.Now().UTC().Format(time.RFC3339)
	row := WebsiteRow{
		PK:        "BUSINESS#" + biz.ID,
		SK:        "WEBSITE#" + websiteID,
		Type:      "Website",
		ID:        websiteID,
		SpecID:    spec.ID,
		R2Prefix:  fmt.Sprintf("sites/%s/", websiteID),
		Status:    "generated",
		CreatedAt: now,
		UpdatedAt: now,
		Etag:      randomHex(16),
	}
	if err := d.PutWebsite(ctx, row); err != nil {
		return fmt.Errorf("generator: put website: %w", err)
	}

	out := pkgevents.New("website.generated", consumerName, WebsiteGeneratedDetail{
		BusinessID: biz.ID,
		SpecID:     spec.ID,
		WebsiteID:  websiteID,
		S3Key:      s3Key,
	}).WithCorrelation(env.CorrelationID).WithCausation(env.EventID)
	if err := d.Publish(ctx, out); err != nil {
		return fmt.Errorf("generator: publish website.generated: %w", err)
	}
	logger.Info("generator.completed",
		"websiteId", websiteID,
		"s3Key", s3Key,
		"htmlBytes", len(html),
	)
	return nil
}

// --- row shapes ---------------------------------------------------------

type BusinessRow struct {
	ID       string `dynamodbav:"id"`
	Name     string `dynamodbav:"name"`
	Domain   string `dynamodbav:"domain"`
	Vertical string `dynamodbav:"vertical"`
}

// SpecRow mirrors the subset of lambdas/spec-generator's SpecRow we read.
type SpecRow struct {
	ID      string         `dynamodbav:"id"`
	Status  string         `dynamodbav:"status"`
	Content schemas.SpecV1 `dynamodbav:"content"`
}

// WebsiteRow mirrors 02-data-model.md § "Website (the generated
// preview)". passcode* fields are populated by the publisher (iter
// 5.3) — left empty here.
type WebsiteRow struct {
	PK        string `dynamodbav:"pk"`
	SK        string `dynamodbav:"sk"`
	Type      string `dynamodbav:"type"`
	ID        string `dynamodbav:"id"`
	SpecID    string `dynamodbav:"specId"`
	R2Prefix  string `dynamodbav:"r2Prefix"`
	Status    string `dynamodbav:"status"`
	CreatedAt string `dynamodbav:"createdAt"`
	UpdatedAt string `dynamodbav:"updatedAt"`
	Etag      string `dynamodbav:"etag"`
}

// --- AWS wiring (production) -------------------------------------------

func buildDeps(ctx context.Context) (runDeps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("generator: loading AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(cfg)
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	bucket := os.Getenv("BLOBS_BUCKET")
	if bucket == "" {
		return runDeps{}, errors.New("generator: BLOBS_BUCKET is not set")
	}
	return runDeps{
		GetBusiness: getBusiness,
		GetSpec:     getSpec,
		PutHTML:     putHTMLFn(s3Client, bucket),
		PutWebsite:  putWebsite,
		Publish: func(ctx context.Context, env pkgevents.Envelope[WebsiteGeneratedDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		Now:          time.Now,
		NewWebsiteID: func() string { return randomHex(16) },
		BlobsBucket:  bucket,
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
	var b BusinessRow
	if err := attributevalue.UnmarshalMap(out.Item, &b); err != nil {
		return nil, fmt.Errorf("unmarshal business %s: %w", businessID, err)
	}
	return &b, nil
}

func getSpec(ctx context.Context, businessID, specID string) (*SpecRow, error) {
	client, err := ddb.Client(ctx)
	if err != nil {
		return nil, err
	}
	out, err := client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(ddb.TableName()),
		Key: map[string]dtypes.AttributeValue{
			"pk": &dtypes.AttributeValueMemberS{Value: "BUSINESS#" + businessID},
			"sk": &dtypes.AttributeValueMemberS{Value: "SPEC#" + specID},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("get spec %s/%s: %w", businessID, specID, err)
	}
	if len(out.Item) == 0 {
		return nil, nil
	}
	var s SpecRow
	if err := attributevalue.UnmarshalMap(out.Item, &s); err != nil {
		return nil, fmt.Errorf("unmarshal spec %s: %w", specID, err)
	}
	return &s, nil
}

func putHTMLFn(client S3Uploader, bucket string) func(context.Context, string, []byte) error {
	return func(ctx context.Context, key string, body []byte) error {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:       aws.String(bucket),
			Key:          aws.String(key),
			Body:         bytes.NewReader(body),
			ContentType:  aws.String("text/html; charset=utf-8"),
			CacheControl: aws.String("public, max-age=300"),
		})
		if err != nil {
			return fmt.Errorf("put html %s: %w", key, err)
		}
		return nil
	}
}

func putWebsite(ctx context.Context, row WebsiteRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal website: %w", err)
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("put website %s: %w", row.ID, err)
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("generator: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
