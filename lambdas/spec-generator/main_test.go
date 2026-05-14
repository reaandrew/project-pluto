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
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/spec"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/style"
)

// --- DDB fake -----------------------------------------------------------

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

// --- captured + fixtures ------------------------------------------------

type captured struct {
	runCalls   int
	runInput   spec.Input
	putRow     SpecRow
	published  pkgevents.Envelope[SpecGeneratedDetail]
	publishOK  bool
	publishErr error
	runErr     error
}

func validBiz() *BusinessRow {
	return &BusinessRow{
		ID: "biz-1", Name: "Acme Plumbing", Domain: "acme.co.uk",
		Vertical: "trades", Location: "Manchester",
	}
}

func validAudit() *AuditRow {
	return &AuditRow{
		ID: "audit-1", Score: 35,
		Qualitative: &AuditQualitative{
			Summary: "Mobile broken; weak CTA.",
			Issues: []AuditQualIssue{
				{Type: "mobile", Severity: "high", Description: "Viewport missing."},
				{Type: "conversion", Severity: "medium", Description: "No CTA above the fold."},
				{Type: "mobile", Severity: "low", Description: "duplicate type"},
			},
		},
	}
}

func validGuide() style.Guide {
	return style.Guide{
		Vertical: "trades", Tone: "plain-English",
		DoPhrases: []string{"24/7"},
		Palette:   style.Palette{Primary: "#000", Neutral: []string{"#fff"}},
		Version:   2,
	}
}

func validSpecOut() schemas.SpecV1 {
	return schemas.SpecV1{
		Brand: schemas.SpecBrand{
			Tone: "plain", Positioning: "Local plumbers.",
			Palette: schemas.SpecPalette{Primary: "#0F4C81", NeutralDark: "#000", NeutralLight: "#fff"},
		},
		Page: schemas.SpecPage{
			Sections: []schemas.SpecSection{
				{Type: schemas.SectionHero, Headline: "Hi", Subheadline: "Hello",
					PrimaryCta: &schemas.SpecCTA{Label: "Call", Action: "call"}},
				{Type: schemas.SectionServices, Title: "What we do",
					Items: []schemas.SpecSubItem{
						{Name: "a", OneLine: "b"}, {Name: "c", OneLine: "d"}, {Name: "e", OneLine: "f"},
					}},
				{Type: schemas.SectionAbout, Paragraph: "About us."},
				{Type: schemas.SectionContact, Phone: "0161 234 5678"},
			},
		},
		SEO: schemas.SpecSEO{Title: "Acme", Description: "Acme Plumbers."},
		Constraints: schemas.SpecConstraints{
			DoNotInventTestimonials: true, DoNotInventAwards: true, DoNotInventPrices: true,
		},
	}
}

func newDeps(t *testing.T, c *captured) runDeps {
	t.Helper()
	return runDeps{
		GetBusiness: func(context.Context, string) (*BusinessRow, error) { return validBiz(), nil },
		GetAudit:    func(context.Context, string, string) (*AuditRow, error) { return validAudit(), nil },
		GetStyleGuide: func(context.Context, string) (style.Guide, error) {
			return validGuide(), nil
		},
		RunSpec: func(_ context.Context, in spec.Input, _ float64) (schemas.SpecV1, error) {
			c.runCalls++
			c.runInput = in
			if c.runErr != nil {
				return schemas.SpecV1{}, c.runErr
			}
			return validSpecOut(), nil
		},
		PutSpec: func(_ context.Context, row SpecRow) error {
			c.putRow = row
			return nil
		},
		Publish: func(_ context.Context, env pkgevents.Envelope[SpecGeneratedDetail]) error {
			c.publishOK = true
			c.published = env
			return c.publishErr
		},
		CapUSD:    func(context.Context, string) (float64, error) { return 5.0, nil },
		Now:       func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) },
		NewSpecID: func() string { return "spec-1" },
	}
}

func makeEnv(businessID, auditID string) pkgevents.Envelope[WebsiteQualifiedDetail] {
	return pkgevents.New("website.qualified", "qualifier", WebsiteQualifiedDetail{
		BusinessID:      businessID,
		QualificationID: "qual-1",
		PriorityScore:   0.82,
		AuditID:         auditID,
	})
}

// --- happy path --------------------------------------------------------

func TestRunOne_HappyPath(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c)
	env := makeEnv("biz-1", "audit-1")
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.runCalls != 1 {
		t.Errorf("RunSpec called %d times, want 1", c.runCalls)
	}
	if c.putRow.ID != "spec-1" || c.putRow.Status != "draft" || c.putRow.Version != 1 {
		t.Errorf("Spec row drift: %+v", c.putRow)
	}
	if c.putRow.ModelID != "anthropic.claude-sonnet-4-6" || c.putRow.PromptID != "spec.v1" {
		t.Errorf("Spec model/prompt drift: %+v", c.putRow)
	}
	if c.putRow.PK != "BUSINESS#biz-1" || c.putRow.SK != "SPEC#spec-1" {
		t.Errorf("pk/sk drift: pk=%q sk=%q", c.putRow.PK, c.putRow.SK)
	}
	if len(c.putRow.Content.Page.Sections) != 4 {
		t.Errorf("content drift — sections=%d", len(c.putRow.Content.Page.Sections))
	}
	if !c.publishOK {
		t.Fatal("expected spec.generated to be published")
	}
	if c.published.EventName != "spec.generated" {
		t.Errorf("event name = %q", c.published.EventName)
	}
	if c.published.Detail.SpecID != "spec-1" || c.published.Detail.BusinessID != "biz-1" {
		t.Errorf("detail drift: %+v", c.published.Detail)
	}
	if c.published.CorrelationID != env.CorrelationID {
		t.Errorf("correlation drift: got %q want %q", c.published.CorrelationID, env.CorrelationID)
	}
	if c.published.CausationID != env.EventID {
		t.Errorf("causation drift")
	}
}

// --- buildSpecInput dedupes issue types --------------------------------

func TestRunOne_BuildSpecInput_DedupesIssueTypes(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c)
	if err := processRecord(context.Background(), d, makeEnv("biz-1", "audit-1")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	got := c.runInput.AuditSummary.IssueTypes
	if len(got) != 2 {
		t.Errorf("expected 2 unique issue types, got %d: %v", len(got), got)
	}
	if c.runInput.AuditSummary.Summary != "Mobile broken; weak CTA." {
		t.Errorf("summary drift: %q", c.runInput.AuditSummary.Summary)
	}
	if c.runInput.StyleGuide.Version != 2 {
		t.Errorf("style guide version drift: %d", c.runInput.StyleGuide.Version)
	}
}

// --- buildSpecInput tolerates missing qualitative block ----------------

func TestRunOne_NoQualitativeBlock_EmptyAuditSummary(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c)
	d.GetAudit = func(context.Context, string, string) (*AuditRow, error) {
		return &AuditRow{ID: "audit-x", Score: 20, Qualitative: nil}, nil
	}
	if err := processRecord(context.Background(), d, makeEnv("biz-1", "audit-x")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.runInput.AuditSummary.Summary != "" {
		t.Errorf("expected empty summary when qualitative block absent")
	}
	if len(c.runInput.AuditSummary.IssueTypes) != 0 {
		t.Errorf("expected empty issue-type list when qualitative block absent")
	}
}

// --- missing rows short-circuit ----------------------------------------

func TestRunOne_BusinessMissing_SkipsNoSpec(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c)
	d.GetBusiness = func(context.Context, string) (*BusinessRow, error) { return nil, nil }
	if err := processRecord(context.Background(), d, makeEnv("missing", "audit-1")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.runCalls != 0 || c.putRow.ID != "" || c.publishOK {
		t.Errorf("expected no work when business missing; runCalls=%d put=%v pub=%v",
			c.runCalls, c.putRow.ID, c.publishOK)
	}
}

func TestRunOne_AuditMissing_SkipsNoSpec(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c)
	d.GetAudit = func(context.Context, string, string) (*AuditRow, error) { return nil, nil }
	if err := processRecord(context.Background(), d, makeEnv("biz-1", "missing")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.runCalls != 0 || c.publishOK {
		t.Errorf("expected no work when audit missing")
	}
}

// --- run errors surface (→ SQS retry → DLQ) ---------------------------

func TestRunOne_RunSpecError_Surfaces(t *testing.T) {
	setupItemsTable(t)
	c := &captured{runErr: errors.New("bedrock down")}
	d := newDeps(t, c)
	if err := processRecord(context.Background(), d, makeEnv("biz-1", "audit-1")); err == nil {
		t.Fatal("expected RunSpec error to surface")
	}
	if c.putRow.ID != "" {
		t.Error("no Spec should be persisted on RunSpec failure")
	}
}

func TestRunOne_PublishError_SurfacesAfterPersist(t *testing.T) {
	setupItemsTable(t)
	c := &captured{publishErr: errors.New("eventbridge down")}
	d := newDeps(t, c)
	err := processRecord(context.Background(), d, makeEnv("biz-1", "audit-1"))
	if err == nil {
		t.Fatal("expected publish error to surface")
	}
	if c.putRow.ID == "" {
		t.Error("Spec should be persisted before publish attempt")
	}
}

// --- idempotent replay -------------------------------------------------

func TestProcessRecord_IdempotentReplay(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c)
	env := makeEnv("biz-1", "audit-1")
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("first call: %v", err)
	}
	firstCalls := c.runCalls
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c.runCalls != firstCalls {
		t.Errorf("replay should be no-op; runCalls %d → %d", firstCalls, c.runCalls)
	}
}

// --- kill switch off ---------------------------------------------------

func TestHandle_KillSwitchOff_NoWork(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, false)
	rec := makeSQSRecord(t, makeEnv("biz-1", "audit-1"))
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{rec}})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("kill switch off should yield no failures, got %v", resp.BatchItemFailures)
	}
}

// --- bad body --------------------------------------------------------

func TestHandle_BadBody_BatchFailure(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, true)
	t.Setenv("EVENT_BUS_NAME", "test-bus")
	rec := lambdaevents.SQSMessage{MessageId: "msg-bad", Body: "not json"}
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{rec}})
	if err != nil {
		return
	}
	if len(resp.BatchItemFailures) != 1 || resp.BatchItemFailures[0].ItemIdentifier != "msg-bad" {
		t.Errorf("expected BatchItemFailure for msg-bad, got %+v", resp.BatchItemFailures)
	}
}

// --- helpers -------------------------------------------------------------

func makeSQSRecord(t *testing.T, env pkgevents.Envelope[WebsiteQualifiedDetail]) lambdaevents.SQSMessage {
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
