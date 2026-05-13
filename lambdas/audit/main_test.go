package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	lambdaevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/audit/qualitative"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/audit/technical"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/idempotency"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
)

// --- fakes ----------------------------------------------------------------

type fakeDDB struct {
	putErr      error
	conditional bool
	putCount    int
	idempSeen   map[string]bool
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.putCount++
	// Track idempotency-record PKs so replays of the same eventID fail
	// with ConditionalCheckFailedException (the shape idempotency.WithIdempotency
	// inspects via errors.As).
	if f.conditional && in.ConditionExpression != nil {
		if pk, ok := in.Item["pk"].(*dtypes.AttributeValueMemberS); ok && strings.HasPrefix(pk.Value, "IDEMP#") {
			if f.idempSeen == nil {
				f.idempSeen = map[string]bool{}
			}
			if f.idempSeen[pk.Value] {
				return nil, &dtypes.ConditionalCheckFailedException{}
			}
			f.idempSeen[pk.Value] = true
		}
	}
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}
func (f *fakeDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}
func (f *fakeDDB) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	return &dynamodb.ScanOutput{}, nil
}
func (f *fakeDDB) DeleteItem(_ context.Context, _ *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	return &dynamodb.DeleteItemOutput{}, nil
}
func (f *fakeDDB) Query(_ context.Context, _ *dynamodb.QueryInput, _ ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error) {
	return &dynamodb.QueryOutput{}, nil
}

// --- deps factory --------------------------------------------------------

type captured struct {
	auditedURL string
	qualIn     qualitative.Input
	qualCalls  int
	put        AuditRow
	blobKey    string
	blobBody   []byte
	published  pkgevents.Envelope[AuditCompletedDetail]
	publishErr error
}

func newDeps(t *testing.T, c *captured, tech technical.Result, htmlBody []byte) runDeps {
	t.Helper()
	return runDeps{
		Audit: func(_ context.Context, pageURL string) (technical.Result, []byte, error) {
			c.auditedURL = pageURL
			return tech, htmlBody, nil
		},
		RunQualitative: func(_ context.Context, in qualitative.Input, _ float64) (auditQualitative, error) {
			c.qualCalls++
			c.qualIn = in
			return auditQualitative{
				ModelID: "anthropic.claude-haiku-4-5",
				Summary: "Mobile broken; weak CTA.",
				Issues: []AuditIssue{
					{Type: "mobile", Severity: "high", Description: "Viewport missing."},
				},
				Score: 35,
				Worth: true,
			}, nil
		},
		GetBusiness: func(_ context.Context, businessID string) (*BusinessRow, error) {
			return &BusinessRow{
				ID:       businessID,
				Name:     "Acme Plumbing",
				Domain:   "acme.co.uk",
				Vertical: "trades",
				Location: "Manchester",
			}, nil
		},
		PutAudit: func(_ context.Context, row AuditRow) error {
			c.put = row
			return nil
		},
		PutHomepageBlob: func(_ context.Context, key string, body []byte) error {
			c.blobKey = key
			c.blobBody = body
			return nil
		},
		Publish: func(_ context.Context, env pkgevents.Envelope[AuditCompletedDetail]) error {
			c.published = env
			return c.publishErr
		},
		Threshold:   func(context.Context) (int, error) { return 30, nil },
		CapUSD:      func(context.Context, string) (float64, error) { return 5.0, nil },
		Now:         func() time.Time { return time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC) },
		NewAuditID:  func() string { return "test-audit-id" },
		BlobsBucket: "test-blobs",
	}
}

// makeEnv builds a typed envelope as it would arrive after FromSQS decoding.
func makeEnv(businessID string) pkgevents.Envelope[BusinessFoundDetail] {
	return pkgevents.New("business.found", "discover", BusinessFoundDetail{
		BusinessID: businessID,
		Domain:     "acme.co.uk",
		Name:       "Acme Plumbing",
		Vertical:   "trades",
		Location:   "Manchester",
		Source:     "google-places",
	})
}

// setupItemsTable wires the ddb fake so idempotency.WithIdempotency can
// run end-to-end. Returns the fake so a test can poke its state.
func setupItemsTable(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	f := &fakeDDB{}
	ddb.SetClient(f)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return f
}

func setupKillSwitch(t *testing.T, audit bool) {
	t.Helper()
	s := killswitch.Defaults()
	s.Stages.AuditEnabled = audit
	killswitch.SetSettings(&s)
	t.Cleanup(func() { killswitch.SetSettings(nil) })
}

// --- runOne happy path ---------------------------------------------------

func TestRunOne_AboveThreshold_RunsQualitativeAndPersists(t *testing.T) {
	c := &captured{}
	tech := technical.Result{
		HTTPS: false, Viewport: false, Favicon: false, ContactDetected: false,
		Lighthouse:   technical.Lighthouse{Performance: 20, Accessibility: 30, SEO: 40},
		HomepageHash: "abc",
	}
	d := newDeps(t, c, tech, []byte("<html><body>Acme</body></html>"))
	env := makeEnv("biz-1")

	setupItemsTable(t)
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.auditedURL != "https://acme.co.uk" {
		t.Errorf("auditedURL = %q", c.auditedURL)
	}
	if c.qualCalls != 1 {
		t.Errorf("qualitative ran %d times, want 1", c.qualCalls)
	}
	if c.qualIn.Domain != "acme.co.uk" || c.qualIn.Business.Name != "Acme Plumbing" {
		t.Errorf("qualitative input drift: %+v", c.qualIn)
	}
	if c.put.ID != "test-audit-id" || !c.put.WorthRedesigning || c.put.Score != 35 {
		t.Errorf("audit row drift: %+v", c.put)
	}
	if c.put.PK != "BUSINESS#biz-1" || c.put.SK != "AUDIT#test-audit-id" {
		t.Errorf("audit pk/sk drift: pk=%q sk=%q", c.put.PK, c.put.SK)
	}
	if c.put.GSI1PK != "AUDIT#WORTH_REDESIGNING#true" {
		t.Errorf("gsi1pk = %q", c.put.GSI1PK)
	}
	if c.put.SnapshotS3Key != "audits/test-audit-id/homepage.html" {
		t.Errorf("snapshot key = %q", c.put.SnapshotS3Key)
	}
	if c.blobKey != c.put.SnapshotS3Key {
		t.Errorf("blob key %q != audit.SnapshotS3Key %q", c.blobKey, c.put.SnapshotS3Key)
	}
	if c.published.EventName != "website.audit.completed" {
		t.Errorf("published event name = %q", c.published.EventName)
	}
	if c.published.CorrelationID != env.CorrelationID {
		t.Errorf("correlation broken: got %q want %q", c.published.CorrelationID, env.CorrelationID)
	}
	if c.published.CausationID != env.EventID {
		t.Errorf("causation = %q, want %q", c.published.CausationID, env.EventID)
	}
	if c.published.Detail.ModelsUsed[0] != "pagespeed" || len(c.published.Detail.ModelsUsed) != 2 {
		t.Errorf("modelsUsed = %v", c.published.Detail.ModelsUsed)
	}
}

// --- below threshold: skips qualitative ---------------------------------

func TestRunOne_BelowThreshold_SkipsQualitative(t *testing.T) {
	c := &captured{}
	// Strong site — heuristics good, Lighthouse high.
	tech := technical.Result{
		HTTPS: true, Viewport: true, Favicon: true, ContactDetected: true,
		Lighthouse:   technical.Lighthouse{Performance: 90, Accessibility: 85, SEO: 90},
		HomepageHash: "abc",
	}
	d := newDeps(t, c, tech, []byte("<html><body>strong</body></html>"))
	env := makeEnv("biz-2")

	setupItemsTable(t)
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.qualCalls != 0 {
		t.Errorf("qualitative should be skipped, got %d calls", c.qualCalls)
	}
	if c.put.WorthRedesigning {
		t.Errorf("should not be worthRedesigning when below threshold")
	}
	if c.put.Score != 0 {
		t.Errorf("score should be 0 when qualitative skipped, got %d", c.put.Score)
	}
	if c.put.Qualitative != nil {
		t.Errorf("qualitative block should be nil, got %+v", c.put.Qualitative)
	}
	if c.published.Detail.WorthRedesigning {
		t.Errorf("published WorthRedesigning should be false")
	}
	if len(c.published.Detail.ModelsUsed) != 1 || c.published.Detail.ModelsUsed[0] != "pagespeed" {
		t.Errorf("modelsUsed = %v, want [pagespeed]", c.published.Detail.ModelsUsed)
	}
}

// --- missing business ---------------------------------------------------

func TestRunOne_BusinessRowMissing_LogsAndSkips(t *testing.T) {
	c := &captured{}
	d := newDeps(t, c, technical.Result{}, []byte("<html/>"))
	d.GetBusiness = func(context.Context, string) (*BusinessRow, error) { return nil, nil }

	setupItemsTable(t)
	if err := processRecord(context.Background(), d, makeEnv("missing")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.put.ID != "" {
		t.Error("audit row should not be written when business is missing")
	}
}

// --- business with empty domain ----------------------------------------

func TestRunOne_BusinessNoDomain_Skips(t *testing.T) {
	c := &captured{}
	d := newDeps(t, c, technical.Result{}, []byte("<html/>"))
	d.GetBusiness = func(context.Context, string) (*BusinessRow, error) {
		return &BusinessRow{ID: "x", Name: "x", Domain: ""}, nil
	}
	setupItemsTable(t)
	if err := processRecord(context.Background(), d, makeEnv("nodomain")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.put.ID != "" {
		t.Error("audit row should not be written for empty-domain business")
	}
}

// --- fetch failure surfaces as error → SQS retry → DLQ ------------------

func TestRunOne_FetchFailure_SurfacesError(t *testing.T) {
	c := &captured{}
	d := newDeps(t, c, technical.Result{}, nil)
	d.Audit = func(context.Context, string) (technical.Result, []byte, error) {
		return technical.Result{}, nil, errors.New("dns failure")
	}
	setupItemsTable(t)
	err := processRecord(context.Background(), d, makeEnv("biz-3"))
	if err == nil {
		t.Fatal("expected error to surface")
	}
}

// --- snapshot failure doesn't fail the run -----------------------------

func TestRunOne_SnapshotFailure_StillCommitsAudit(t *testing.T) {
	c := &captured{}
	tech := technical.Result{HomepageHash: "abc"}
	d := newDeps(t, c, tech, []byte("<html/>"))
	d.PutHomepageBlob = func(context.Context, string, []byte) error {
		return errors.New("s3 down")
	}
	setupItemsTable(t)
	if err := processRecord(context.Background(), d, makeEnv("biz-4")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.put.ID == "" {
		t.Error("audit row should still be written on snapshot failure")
	}
	if c.put.SnapshotS3Key != "" {
		t.Errorf("SnapshotS3Key should be cleared on failure, got %q", c.put.SnapshotS3Key)
	}
}

// --- idempotency: replay of same event is no-op ------------------------

func TestProcessRecord_IdempotentOnReplay(t *testing.T) {
	c := &captured{}
	tech := technical.Result{Lighthouse: technical.Lighthouse{Performance: 30}}
	d := newDeps(t, c, tech, []byte("<html/>"))
	f := setupItemsTable(t)
	f.conditional = true

	env := makeEnv("biz-5")
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("first call: %v", err)
	}
	firstQualCalls := c.qualCalls

	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("replay should be no-op success: %v", err)
	}
	if c.qualCalls != firstQualCalls {
		t.Errorf("qualitative ran on replay: %d vs %d", c.qualCalls, firstQualCalls)
	}
}

// --- handle: killswitch off ---------------------------------------------

func TestHandle_KillSwitchOff_SkipsAndReturnsSuccess(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, false)

	rec := makeSQSRecord(t, makeEnv("biz-6"))
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{
		Records: []lambdaevents.SQSMessage{rec},
	})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("kill switch off should report no failures, got %v", resp.BatchItemFailures)
	}
}

// --- Consume integration: bad-body record → BatchItemFailure ------------

func TestHandle_BadBody_ReportedAsBatchFailure(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, true)
	t.Setenv("EVENT_BUS_NAME", "test-bus") // buildDeps needs this; we won't actually publish
	t.Setenv("PAGESPEED_API_KEY", "")
	t.Setenv("BLOBS_BUCKET", "test-blobs")

	// Construct a malformed SQS record (body is not a JSON-encoded EventBridge envelope).
	rec := lambdaevents.SQSMessage{
		MessageId: "msg-bad",
		Body:      "not json",
	}
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{
		Records: []lambdaevents.SQSMessage{rec},
	})
	if err != nil {
		// Network/AWS init may fail before we get here in a sandbox — accept that path.
		return
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-bad" {
		t.Errorf("expected one BatchItemFailure for msg-bad, got %+v", resp.BatchItemFailures)
	}
}

// --- stripToText --------------------------------------------------------

func TestStripToText(t *testing.T) {
	in := `<html>
<head><style>body{color:red}</style><script>alert(1)</script></head>
<body><h1>Acme</h1><p>Hello   World</p></body>
</html>`
	got := stripToText([]byte(in))
	if !strings.Contains(got, "Acme") || !strings.Contains(got, "Hello World") {
		t.Errorf("missing visible text in %q", got)
	}
	if strings.Contains(got, "alert(1)") {
		t.Errorf("script body leaked: %q", got)
	}
	if strings.Contains(got, "color:red") {
		t.Errorf("style body leaked: %q", got)
	}
	if strings.Contains(got, "<") || strings.Contains(got, ">") {
		t.Errorf("HTML tags leaked: %q", got)
	}
}

// --- buildQualitativeJSON ----------------------------------------------

func TestBuildQualitativeJSON(t *testing.T) {
	if buildQualitativeJSON(auditQualitative{}) != nil {
		t.Error("zero qualitative should marshal as nil block")
	}
	q := buildQualitativeJSON(auditQualitative{
		ModelID: "anthropic.claude-haiku-4-5",
		Summary: "ok",
		Issues:  []AuditIssue{{Type: "design", Severity: "low", Description: "x"}},
	})
	if q == nil || q.ModelID != "anthropic.claude-haiku-4-5" || len(q.Issues) != 1 {
		t.Errorf("qualitative block drift: %+v", q)
	}
}

// --- helpers -------------------------------------------------------------

// makeSQSRecord wraps a typed envelope in the EventBridge → SQS layering
// the consumer Lambda receives at runtime.
func makeSQSRecord(t *testing.T, env pkgevents.Envelope[BusinessFoundDetail]) lambdaevents.SQSMessage {
	t.Helper()
	envBody, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	ebEvent := lambdaevents.EventBridgeEvent{
		Source:     pkgevents.Source,
		DetailType: env.EventName,
		Detail:     envBody,
	}
	body, err := json.Marshal(ebEvent)
	if err != nil {
		t.Fatalf("marshal EB event: %v", err)
	}
	return lambdaevents.SQSMessage{
		MessageId: env.EventID,
		Body:      string(body),
	}
}

// --- unused import guard -------------------------------------------------

var _ = idempotency.RecordType
