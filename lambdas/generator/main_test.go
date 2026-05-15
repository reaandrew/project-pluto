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

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	pkgevents "github.com/reaandrew/ai-website-agency/lambdas/pkg/events"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// --- DDB fake ----------------------------------------------------------

type fakeDDB struct {
	idempSeen map[string]bool
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	if in.ConditionExpression != nil && strings.Contains(*in.ConditionExpression, "attribute_not_exists(pk)") {
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

func setupItemsTable(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	f := &fakeDDB{}
	ddb.SetClient(f)
	t.Cleanup(func() { ddb.SetClient(nil) })
	return f
}

func setupKillSwitch(t *testing.T, on bool) {
	t.Helper()
	s := killswitch.Defaults()
	s.Stages.PreviewEnabled = on
	killswitch.SetSettings(&s)
	t.Cleanup(func() { killswitch.SetSettings(nil) })
}

// --- fixtures ----------------------------------------------------------

type captured struct {
	putKey      string
	putBody     []byte
	putErr      error
	websiteRow  WebsiteRow
	publishedOK bool
	published   pkgevents.Envelope[WebsiteGeneratedDetail]
	publishErr  error
}

func validBiz() *BusinessRow {
	return &BusinessRow{
		ID: "biz-1", Name: "Acme Plumbing", Domain: "acme.co.uk", Vertical: "trades",
	}
}

func validSpec(status string) *SpecRow {
	return &SpecRow{
		ID: "spec-1", Status: status,
		Content: schemas.SpecV1{
			Brand: schemas.SpecBrand{
				Tone: "plain", Positioning: "Local.",
				Palette: schemas.SpecPalette{Primary: "#0F4C81", NeutralDark: "#000", NeutralLight: "#fff"},
			},
			Page: schemas.SpecPage{Sections: []schemas.SpecSection{
				{Type: schemas.SectionHero, Headline: "Hi", Subheadline: "Hello",
					PrimaryCta: &schemas.SpecCTA{Label: "Call", Action: "call"}},
				{Type: schemas.SectionServices, Title: "Services",
					Items: []schemas.SpecSubItem{
						{Name: "a", OneLine: "b"}, {Name: "c", OneLine: "d"}, {Name: "e", OneLine: "f"},
					}},
				{Type: schemas.SectionAbout, Paragraph: "About."},
				{Type: schemas.SectionContact, Phone: "0161"},
			}},
			SEO: schemas.SpecSEO{Title: "Acme", Description: "Acme."},
			Constraints: schemas.SpecConstraints{
				DoNotInventTestimonials: true, DoNotInventAwards: true, DoNotInventPrices: true,
			},
		},
	}
}

func newDeps(t *testing.T, c *captured, biz *BusinessRow, spec *SpecRow) runDeps {
	t.Helper()
	return runDeps{
		GetBusiness: func(context.Context, string) (*BusinessRow, error) { return biz, nil },
		GetSpec:     func(context.Context, string, string) (*SpecRow, error) { return spec, nil },
		PutHTML: func(_ context.Context, key string, body []byte) error {
			c.putKey = key
			c.putBody = body
			return c.putErr
		},
		PutWebsite: func(_ context.Context, row WebsiteRow) error {
			c.websiteRow = row
			return nil
		},
		Publish: func(_ context.Context, env pkgevents.Envelope[WebsiteGeneratedDetail]) error {
			c.publishedOK = true
			c.published = env
			return c.publishErr
		},
		Now:          func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) },
		NewWebsiteID: func() string { return "website-1" },
		BlobsBucket:  "test-blobs",
	}
}

func makeEnv(businessID, specID string) pkgevents.Envelope[SpecApprovedDetail] {
	return pkgevents.New("spec.approved", "api-specs", SpecApprovedDetail{
		BusinessID: businessID, SpecID: specID, Version: 1,
	})
}

// --- happy path -------------------------------------------------------

func TestRunOne_HappyPath(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), validSpec("approved"))
	env := makeEnv("biz-1", "spec-1")

	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.putKey != "generated/website-1/index.html" {
		t.Errorf("S3 key drift: %q", c.putKey)
	}
	if !strings.Contains(string(c.putBody), "<!doctype html>") ||
		!strings.Contains(string(c.putBody), "Acme") {
		t.Errorf("HTML body drift: %s", string(c.putBody))
	}
	if c.websiteRow.ID != "website-1" || c.websiteRow.Status != "generated" || c.websiteRow.SpecID != "spec-1" {
		t.Errorf("Website row drift: %+v", c.websiteRow)
	}
	if c.websiteRow.PK != "BUSINESS#biz-1" || c.websiteRow.SK != "WEBSITE#website-1" {
		t.Errorf("pk/sk drift: pk=%q sk=%q", c.websiteRow.PK, c.websiteRow.SK)
	}
	if c.websiteRow.R2Prefix != "sites/website-1/" {
		t.Errorf("r2Prefix drift: %q", c.websiteRow.R2Prefix)
	}
	if !c.publishedOK || c.published.EventName != "website.generated" {
		t.Errorf("publish drift: ok=%v name=%q", c.publishedOK, c.published.EventName)
	}
	if c.published.CorrelationID != env.CorrelationID || c.published.CausationID != env.EventID {
		t.Errorf("correlation/causation drift")
	}
	if c.published.Detail.S3Key != "generated/website-1/index.html" {
		t.Errorf("event s3Key drift: %q", c.published.Detail.S3Key)
	}
}

// --- not-approved spec: defensive guard ------------------------------

func TestRunOne_NonApprovedSpec_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), validSpec("draft"))
	if err := processRecord(context.Background(), d, makeEnv("biz-1", "spec-1")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.putKey != "" || c.websiteRow.ID != "" || c.publishedOK {
		t.Errorf("expected no work on non-approved spec; got key=%q row=%v pub=%v", c.putKey, c.websiteRow.ID, c.publishedOK)
	}
}

// --- missing business / spec ---------------------------------------

func TestRunOne_BusinessMissing_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, nil, validSpec("approved"))
	d.GetBusiness = func(context.Context, string) (*BusinessRow, error) { return nil, nil }
	if err := processRecord(context.Background(), d, makeEnv("missing", "spec-1")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.putKey != "" || c.publishedOK {
		t.Errorf("expected no work")
	}
}

func TestRunOne_SpecMissing_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), nil)
	d.GetSpec = func(context.Context, string, string) (*SpecRow, error) { return nil, nil }
	if err := processRecord(context.Background(), d, makeEnv("biz-1", "missing")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.publishedOK {
		t.Errorf("expected no publish")
	}
}

// --- s3 + publish error surfaces (→ SQS retry → DLQ) -----------------

func TestRunOne_S3Error_SurfacesNoPublish(t *testing.T) {
	setupItemsTable(t)
	c := &captured{putErr: errors.New("s3 down")}
	d := newDeps(t, c, validBiz(), validSpec("approved"))
	err := processRecord(context.Background(), d, makeEnv("biz-1", "spec-1"))
	if err == nil {
		t.Fatal("expected s3 error to surface")
	}
	if c.publishedOK {
		t.Error("publish should not fire when S3 put failed")
	}
}

func TestRunOne_PublishError_Surfaces(t *testing.T) {
	setupItemsTable(t)
	c := &captured{publishErr: errors.New("eventbridge down")}
	d := newDeps(t, c, validBiz(), validSpec("approved"))
	err := processRecord(context.Background(), d, makeEnv("biz-1", "spec-1"))
	if err == nil {
		t.Fatal("expected publish error to surface")
	}
	// HTML + Website row should be persisted before publish failure.
	if c.putKey == "" || c.websiteRow.ID == "" {
		t.Errorf("HTML + Website row should be persisted before publish attempt")
	}
}

// --- idempotent replay -----------------------------------------------

func TestProcessRecord_IdempotentReplay(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), validSpec("approved"))
	env := makeEnv("biz-1", "spec-1")
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("first call: %v", err)
	}
	firstKey := c.putKey
	c.putKey = "" // clear to detect re-write
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c.putKey != "" {
		t.Errorf("replay should be no-op; second putKey=%q (first %q)", c.putKey, firstKey)
	}
}

// --- killswitch off ----------------------------------------------------

func TestHandle_KillSwitchOff_NoWork(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, false)
	rec := makeSQSRecord(t, makeEnv("biz-1", "spec-1"))
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{rec}})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("kill switch off should yield no failures, got %v", resp.BatchItemFailures)
	}
}

func TestHandle_BadBody_BatchFailure(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, true)
	t.Setenv("EVENT_BUS_NAME", "test-bus")
	t.Setenv("BLOBS_BUCKET", "test-blobs")
	rec := lambdaevents.SQSMessage{MessageId: "msg-bad", Body: "not json"}
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{rec}})
	if err != nil {
		return
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-bad" {
		t.Errorf("expected one BatchItemFailure, got %+v", resp.BatchItemFailures)
	}
}

// --- helpers ---------------------------------------------------------

func makeSQSRecord(t *testing.T, env pkgevents.Envelope[SpecApprovedDetail]) lambdaevents.SQSMessage {
	t.Helper()
	body, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	eb := lambdaevents.EventBridgeEvent{
		Source:     pkgevents.Source,
		DetailType: env.EventName,
		Detail:     body,
	}
	raw, err := json.Marshal(eb)
	if err != nil {
		t.Fatalf("marshal eb: %v", err)
	}
	return lambdaevents.SQSMessage{MessageId: env.EventID, Body: string(raw)}
}

// --- iter 5.6b: regenerate reuses the existing websiteId ---------------

func TestRunOne_RegenerateReusesWebsiteID(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, validBiz(), validSpec("approved"))
	// website.regenerate.requested carries the existing websiteId.
	env := pkgevents.New("website.regenerate.requested", "api-website", SpecApprovedDetail{
		BusinessID: "biz-1", SpecID: "spec-1", WebsiteID: "existing-web",
	})
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.websiteRow.ID != "existing-web" {
		t.Errorf("regenerate must reuse websiteId, got %q (NewWebsiteID would be website-1)", c.websiteRow.ID)
	}
	if c.putKey != "generated/existing-web/index.html" {
		t.Errorf("S3 key must use the reused websiteId: %q", c.putKey)
	}
	if c.websiteRow.SK != "WEBSITE#existing-web" || c.websiteRow.Status != "generated" {
		t.Errorf("row drift on regenerate: %+v", c.websiteRow)
	}
	if c.published.Detail.WebsiteID != "existing-web" {
		t.Errorf("website.generated must carry the reused id: %q", c.published.Detail.WebsiteID)
	}
}
