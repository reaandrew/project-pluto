// Package main is the audit Lambda. EventBridge routes `business.found`
// events here via an SQS main queue (with DLQ); the Lambda consumes one
// record at a time using partial-batch-failure semantics so a single
// failing record doesn't poison its batch.
//
// Per-record pipeline:
//
//  1. Decode the envelope (events.FromSQS).
//  2. idempotency.WithIdempotency("audit", env.EventID, …) — replays from
//     SQS visibility-timeout misfires become no-ops.
//  3. Look up the Business row to recover {name, vertical, location,
//     domain}.
//  4. technical.Auditor.Audit — fetches homepage via politefetch,
//     computes heuristics, calls PageSpeed (degrades gracefully).
//  5. WorthQualitativeAudit gate vs PipelineSettings.Thresholds
//     .MinTechnicalIssueScore. Below threshold → skip the Bedrock call;
//     the audit stores technical-only + worthRedesigning=false.
//  6. Above threshold → qualitative.Run wraps prompts.AuditQualitativeV1
//     (Haiku 4.5). Cache keyed on (domain, htmlExcerpt); 30d TTL.
//  7. Persist the Audit row + snapshot the homepage HTML to S3 at
//     audits/<auditId>/homepage.html.
//  8. Publish website.audit.completed.
//
// Entry-level killswitch.WithKillSwitch on StageAudit. SQS retry budget
// = 3 (maxReceiveCount=3 on the queue); after that → DLQ. The handler
// surfaces any per-record error as a BatchItemFailure so SQS retries
// just that record.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/audit/qualitative"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/audit/technical"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/config"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	applog "github.com/reaandrew/ai-website-agency/lambdas/pkg/log"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/politefetch"
)

const consumerName = "audit"

// BusinessFoundDetail mirrors lambdas/discover/main.go. Duplicated here
// rather than importing because the discover package is `package main`.
// The shape is locked by .ralph/specs/03-events.md.
type BusinessFoundDetail struct {
	BusinessID string `json:"businessId"`
	Domain     string `json:"domain"`
	Name       string `json:"name"`
	Vertical   string `json:"vertical,omitempty"`
	Location   string `json:"location,omitempty"`
	Source     string `json:"source"`
	ProfileID  string `json:"profileId"`
}

// AuditCompletedDetail is the `detail` payload of website.audit.completed
// per .ralph/specs/03-events.md.
type AuditCompletedDetail struct {
	BusinessID       string   `json:"businessId"`
	AuditID          string   `json:"auditId"`
	Score            int      `json:"score"`
	WorthRedesigning bool     `json:"worthRedesigning"`
	Priority         string   `json:"priority,omitempty"`
	ModelsUsed       []string `json:"modelsUsed"`
}

// S3Uploader is the subset of *s3.Client used to snapshot the homepage.
type S3Uploader interface {
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// runDeps is the testable surface. Tests inject fakes; the production
// `handle` wires real implementations.
type runDeps struct {
	Audit           func(ctx context.Context, pageURL string) (technical.Result, []byte, error)
	RunQualitative  func(ctx context.Context, in qualitative.Input, capUSD float64) (auditQualitative, error)
	GetBusiness     func(ctx context.Context, businessID string) (*BusinessRow, error)
	PutAudit        func(ctx context.Context, row AuditRow) error
	PutHomepageBlob func(ctx context.Context, key string, body []byte) error
	Publish         func(ctx context.Context, env pkgevents.Envelope[AuditCompletedDetail]) error
	Threshold       func(ctx context.Context) (int, error)
	CapUSD          func(ctx context.Context, stage string) (float64, error)
	Now             func() time.Time
	NewAuditID      func() string
	BlobsBucket     string
}

// auditQualitative wraps the result of the qualitative pass for the
// runDeps adapter — `qualitative.Run` returns a schemas.AuditV1; we hold
// the modelId alongside it so the audit row + emitted event don't have
// to recompute which model produced it.
type auditQualitative struct {
	ModelID string
	Summary string
	Issues  []AuditIssue
	Score   int
	Worth   bool
}

func handle(ctx context.Context, raw lambdaevents.SQSEvent) (lambdaevents.SQSEventResponse, error) {
	var resp lambdaevents.SQSEventResponse
	err := killswitch.WithKillSwitch(ctx, killswitch.StageAudit, func(ctx context.Context) error {
		deps, err := buildDeps(ctx)
		if err != nil {
			return err
		}
		out, err := pkgevents.Consume[BusinessFoundDetail](ctx, raw, func(ctx context.Context, env pkgevents.Envelope[BusinessFoundDetail]) error {
			return processRecord(ctx, deps, env)
		})
		resp = out
		return err
	})
	return resp, err
}

// processRecord is the per-record handler. Wrapped by idempotency so
// duplicate deliveries become no-ops.
func processRecord(ctx context.Context, d runDeps, env pkgevents.Envelope[BusinessFoundDetail]) error {
	logger := applog.FromContext(ctx)
	logger = logger.With("eventId", env.EventID, "businessId", env.Detail.BusinessID)

	_, err := idempotency.WithIdempotency(ctx, consumerName, env.EventID, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, runOne(ctx, d, env, logger)
	})
	if errors.Is(err, idempotency.ErrAlreadyProcessed) {
		logger.Info("audit.replay.skipped")
		return nil
	}
	return err
}

// runOne does the work for one business.found event. Returns an error
// to surface as a BatchItemFailure (SQS will retry; after maxReceiveCount
// the message → DLQ).
func runOne(ctx context.Context, d runDeps, env pkgevents.Envelope[BusinessFoundDetail], logger *slog.Logger) error {
	biz, err := d.GetBusiness(ctx, env.Detail.BusinessID)
	if err != nil {
		return fmt.Errorf("audit: lookup business %s: %w", env.Detail.BusinessID, err)
	}
	if biz == nil {
		// Business row missing — usually means the row was deleted
		// after publish. Log + skip; do NOT DLQ (replay won't help).
		logger.Warn("audit.business.missing")
		return nil
	}
	if biz.Domain == "" {
		logger.Warn("audit.business.no_domain")
		return nil
	}

	homepageURL := "https://" + biz.Domain
	techResult, htmlBody, err := d.Audit(ctx, homepageURL)
	if err != nil {
		// Fetch failure → retry. Common cause: transient DNS / 5xx
		// from the homepage. After 3 retries → DLQ; operator review.
		return fmt.Errorf("audit: technical pass: %w", err)
	}

	threshold, err := d.Threshold(ctx)
	if err != nil {
		return fmt.Errorf("audit: lookup threshold: %w", err)
	}

	auditID := d.NewAuditID()
	modelsUsed := []string{"pagespeed"}

	var qual auditQualitative
	if technical.WorthQualitativeAudit(techResult, threshold) {
		capUSD, err := d.CapUSD(ctx, killswitch.StageAudit)
		if err != nil {
			return fmt.Errorf("audit: lookup audit budget cap: %w", err)
		}
		qIn := qualitative.Input{
			Domain: biz.Domain,
			Business: qualitative.Business{
				Name:     biz.Name,
				Vertical: biz.Vertical,
				Location: biz.Location,
			},
			Technical:   techResult,
			HTMLExcerpt: stripToText(htmlBody),
		}
		qual, err = d.RunQualitative(ctx, qIn, capUSD)
		if err != nil {
			return fmt.Errorf("audit: qualitative pass: %w", err)
		}
		modelsUsed = append(modelsUsed, qual.ModelID)
	} else {
		logger.Info("audit.qualitative.skipped_below_threshold",
			"threshold", threshold,
			"metric", "pipeline.audit.skipped_below_threshold",
		)
	}

	// Snapshot the raw HTML so re-runs + manual review have the original.
	snapshotKey := fmt.Sprintf("audits/%s/homepage.html", auditID)
	if err := d.PutHomepageBlob(ctx, snapshotKey, htmlBody); err != nil {
		// Snapshot failure is not fatal — we still want the audit
		// stored; just clear the key so the audit row reflects reality.
		logger.Error("audit.snapshot.failed", "err", err)
		snapshotKey = ""
	}

	now := d.Now().UTC().Format(time.RFC3339)
	row := AuditRow{
		PK:               "BUSINESS#" + biz.ID,
		SK:               "AUDIT#" + auditID,
		Type:             "Audit",
		ID:               auditID,
		Technical:        techResult,
		Qualitative:      buildQualitativeJSON(qual),
		Score:            qual.Score,
		WorthRedesigning: qual.Worth,
		SnapshotS3Key:    snapshotKey,
		CreatedAt:        now,
		Etag:             randomHex(16),
		GSI1PK:           fmt.Sprintf("AUDIT#WORTH_REDESIGNING#%t", qual.Worth),
		GSI1SK:           now,
	}
	if err := d.PutAudit(ctx, row); err != nil {
		return fmt.Errorf("audit: put audit row: %w", err)
	}

	out := pkgevents.New("website.audit.completed", consumerName, AuditCompletedDetail{
		BusinessID:       biz.ID,
		AuditID:          auditID,
		Score:            qual.Score,
		WorthRedesigning: qual.Worth,
		ModelsUsed:       modelsUsed,
	}).WithCorrelation(env.CorrelationID).WithCausation(env.EventID)
	if err := d.Publish(ctx, out); err != nil {
		// Publish failure → the audit row is already persisted; the
		// downstream qualifier can recover from gsi1
		// (AUDIT#WORTH_REDESIGNING#true). Log + surface as failure so
		// SQS retries (this record only).
		return fmt.Errorf("audit: publish website.audit.completed: %w", err)
	}
	logger.Info("audit.completed",
		"auditId", auditID,
		"score", qual.Score,
		"worthRedesigning", qual.Worth,
		"qualitativeRan", len(modelsUsed) > 1,
	)
	return nil
}

// buildQualitativeJSON converts the qualitative summary into the data
// model's `qualitative` block. Returns nil when qualitative was skipped
// so the JSON-marshaled row has either a populated block or no field at
// all (omitempty on the column).
func buildQualitativeJSON(q auditQualitative) *AuditQualitative {
	if q.ModelID == "" {
		return nil
	}
	return &AuditQualitative{
		ModelID: q.ModelID,
		Summary: q.Summary,
		Issues:  q.Issues,
	}
}

// stripToText is a best-effort HTML-tag stripper used to produce the
// htmlExcerpt the qualitative prompt is fed. Real-world HTML has
// scripts, styles, comments — we want only visible text so the cache
// key is stable across re-fetches that only change boilerplate. The
// qualitative wrapper truncates to 8KB so this can over-produce.
func stripToText(body []byte) string {
	s := string(body)
	// Drop <script>…</script> and <style>…</style> blocks (case-insensitive).
	for _, tag := range []string{"script", "style"} {
		s = stripTagBlock(s, tag)
	}
	// Drop remaining tags by removing anything between '<' and '>'.
	var b strings.Builder
	b.Grow(len(s))
	in := false
	for _, r := range s {
		switch r {
		case '<':
			in = true
		case '>':
			in = false
			b.WriteRune(' ')
		default:
			if !in {
				b.WriteRune(r)
			}
		}
	}
	// Collapse whitespace runs.
	return strings.Join(strings.Fields(b.String()), " ")
}

func stripTagBlock(s, tag string) string {
	open := "<" + tag
	close := "</" + tag + ">"
	for {
		lower := strings.ToLower(s)
		start := strings.Index(lower, open)
		if start < 0 {
			return s
		}
		end := strings.Index(lower[start:], close)
		if end < 0 {
			return s[:start]
		}
		s = s[:start] + s[start+end+len(close):]
	}
}

// --- row shapes ----------------------------------------------------------

// BusinessRow is the subset of the Business item we read.
type BusinessRow struct {
	ID       string `dynamodbav:"id"`
	Name     string `dynamodbav:"name"`
	Domain   string `dynamodbav:"domain"`
	Vertical string `dynamodbav:"vertical"`
	Location string `dynamodbav:"location"`
}

// AuditRow is the DynamoDB shape — mirrors .ralph/specs/02-data-model.md
// § "Audit".
type AuditRow struct {
	PK               string            `dynamodbav:"pk"`
	SK               string            `dynamodbav:"sk"`
	Type             string            `dynamodbav:"type"`
	ID               string            `dynamodbav:"id"`
	Technical        technical.Result  `dynamodbav:"technical"`
	Qualitative      *AuditQualitative `dynamodbav:"qualitative,omitempty"`
	Score            int               `dynamodbav:"score"`
	WorthRedesigning bool              `dynamodbav:"worthRedesigning"`
	SnapshotS3Key    string            `dynamodbav:"snapshotS3Key,omitempty"`
	CreatedAt        string            `dynamodbav:"createdAt"`
	Etag             string            `dynamodbav:"etag"`
	GSI1PK           string            `dynamodbav:"gsi1pk"`
	GSI1SK           string            `dynamodbav:"gsi1sk"`
}

// AuditQualitative is the qualitative block on an Audit row.
type AuditQualitative struct {
	ModelID string       `json:"modelId" dynamodbav:"modelId"`
	Summary string       `json:"summary" dynamodbav:"summary"`
	Issues  []AuditIssue `json:"issues"  dynamodbav:"issues"`
}

// AuditIssue is one issue on the qualitative block. Mirrors
// schemas.AuditIssue but kept separate so the DDB tags can differ.
type AuditIssue struct {
	Type        string `json:"type"        dynamodbav:"type"`
	Severity    string `json:"severity"    dynamodbav:"severity"`
	Description string `json:"description" dynamodbav:"description"`
}

// --- AWS wiring (production) ---------------------------------------------

// buildDeps constructs the production runDeps. Cold-start cost is small:
// politefetch + technical.Auditor are pure-Go; the SDK clients are
// already lazily initialised.
func buildDeps(ctx context.Context) (runDeps, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return runDeps{}, fmt.Errorf("audit: loading AWS config: %w", err)
	}
	s3Client := s3.NewFromConfig(cfg)
	publisher, err := pkgevents.NewPublisher(ctx)
	if err != nil {
		return runDeps{}, err
	}
	politeClient := politefetch.New(politefetch.Config{})
	auditor := &technical.Auditor{
		PageSpeedAPIKey: os.Getenv("PAGESPEED_API_KEY"),
		HTTP:            &http.Client{Timeout: technical.DefaultHTTPTimeout},
		Polite:          politeAdapter{client: politeClient},
	}

	blobsBucket := os.Getenv("BLOBS_BUCKET")
	if blobsBucket == "" {
		return runDeps{}, errors.New("audit: BLOBS_BUCKET is not set")
	}

	return runDeps{
		Audit: func(ctx context.Context, pageURL string) (technical.Result, []byte, error) {
			// politefetch already caches the homepage body, but technical
			// only returns the heuristic Result. Refetch once here to get
			// the body for snapshotting + html_excerpt assembly — the
			// politefetch ETag cache means this is usually a 304.
			resp, err := politeClient.Fetch(ctx, pageURL)
			if err != nil {
				return technical.Result{}, nil, err
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 400 {
				return technical.Result{}, nil, fmt.Errorf("audit: homepage returned HTTP %d", resp.StatusCode)
			}
			// Build the technical.Result from the same fetch — feed
			// the body to a fresh in-memory PoliteFetcher so we don't
			// double-fetch (and so the throttle clock doesn't get hit
			// twice for the same URL on the same Lambda invocation).
			a := &technical.Auditor{
				PageSpeedAPIKey: auditor.PageSpeedAPIKey,
				HTTP:            auditor.HTTP,
				Polite:          staticBody{statusCode: resp.StatusCode, body: resp.Body},
			}
			r, err := a.Audit(ctx, pageURL)
			if err != nil {
				return technical.Result{}, nil, err
			}
			return r, resp.Body, nil
		},
		RunQualitative: func(ctx context.Context, in qualitative.Input, capUSD float64) (auditQualitative, error) {
			out, err := qualitative.Run(ctx, in, capUSD)
			if err != nil {
				return auditQualitative{}, err
			}
			issues := make([]AuditIssue, 0, len(out.Issues))
			for _, i := range out.Issues {
				issues = append(issues, AuditIssue{
					Type: i.Type, Severity: i.Severity, Description: i.Description,
				})
			}
			return auditQualitative{
				ModelID: bedrock.ModelHaiku45,
				Summary: out.Summary,
				Issues:  issues,
				Score:   out.Score,
				Worth:   out.WorthRedesigning,
			}, nil
		},
		GetBusiness:     getBusiness,
		PutAudit:        putAudit,
		PutHomepageBlob: putHomepageBlobFn(s3Client, blobsBucket),
		Publish: func(ctx context.Context, env pkgevents.Envelope[AuditCompletedDetail]) error {
			return pkgevents.Publish(ctx, publisher, env)
		},
		Threshold:   thresholdFromSettings,
		CapUSD:      killswitch.CapUSD,
		Now:         time.Now,
		NewAuditID:  func() string { return randomHex(16) },
		BlobsBucket: blobsBucket,
	}, nil
}

// politeAdapter bridges *politefetch.Client to technical.PoliteFetcher.
// politefetch returns Response; technical wants its own PoliteResponse
// shape (so the technical package stays decoupled).
type politeAdapter struct {
	client *politefetch.Client
}

func (p politeAdapter) Fetch(ctx context.Context, urlStr string) (*technical.PoliteResponse, error) {
	resp, err := p.client.Fetch(ctx, urlStr)
	if err != nil {
		return nil, err
	}
	return &technical.PoliteResponse{StatusCode: resp.StatusCode, Body: resp.Body}, nil
}

// staticBody is a one-shot PoliteFetcher used inside the buildDeps
// closure to avoid a double-fetch of the homepage. The first fetch goes
// through politeClient and gets robots + throttle treatment; subsequent
// calls in the same Lambda invocation serve the same body without
// hitting the network again.
type staticBody struct {
	statusCode int
	body       []byte
}

func (s staticBody) Fetch(_ context.Context, _ string) (*technical.PoliteResponse, error) {
	return &technical.PoliteResponse{StatusCode: s.statusCode, Body: s.body}, nil
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

func putAudit(ctx context.Context, row AuditRow) error {
	client, err := ddb.Client(ctx)
	if err != nil {
		return err
	}
	item, err := attributevalue.MarshalMap(row)
	if err != nil {
		return fmt.Errorf("marshal audit: %w", err)
	}
	_, err = client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(ddb.TableName()),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("put audit %s: %w", row.ID, err)
	}
	return nil
}

func putHomepageBlobFn(client S3Uploader, bucket string) func(context.Context, string, []byte) error {
	return func(ctx context.Context, key string, body []byte) error {
		_, err := client.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(key),
			Body:        bytes.NewReader(body),
			ContentType: aws.String("text/html; charset=utf-8"),
		})
		if err != nil {
			return fmt.Errorf("put blob %s: %w", key, err)
		}
		return nil
	}
}

func thresholdFromSettings(ctx context.Context) (int, error) {
	s, err := killswitch.Get(ctx)
	if err != nil {
		return 0, err
	}
	return s.Thresholds.MinTechnicalIssueScore, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Errorf("audit: rand.Read: %w", err))
	}
	return hex.EncodeToString(b)
}

func main() {
	_ = config.MustLoad()
	lambda.Start(handle)
}
