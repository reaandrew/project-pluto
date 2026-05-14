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
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/targeting"
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
	s.Stages.AuditEnabled = on
	killswitch.SetSettings(&s)
	t.Cleanup(func() { killswitch.SetSettings(nil) })
}

// --- fixtures + deps factory --------------------------------------------

type captured struct {
	qualification     QualificationRow
	statusUpdates     []string
	lastUpdate        BusinessUpdate
	publishedQualOK   bool
	publishedRejectOK bool
	publishedQual     pkgevents.Envelope[WebsiteQualifiedDetail]
	publishedReject   pkgevents.Envelope[WebsiteRejectedDetail]
	publishErr        error
	scoreInputs       int
}

func weakAudit() *AuditRow {
	// Severely-weak fixture. Acceptance criterion says auditScore<50
	// qualifies; at the v1 reality where Contact is always nil
	// (iter 6.x adds enrichment), contactConfidence's 20% weight is
	// zero. Compensate by maxing the other signals to make sure a
	// genuinely weak site clears the 70 threshold.
	return &AuditRow{
		ID:    "audit-1",
		Score: 10,
		Technical: AuditTechnical{
			HTTPS: false, Viewport: false, ContactDetected: false,
			Lighthouse: AuditLighthouse{Performance: 10, Accessibility: 30, SEO: 25},
		},
		Qualitative: &AuditQualitative{
			ModelID: "anthropic.claude-haiku-4-5",
			Summary: "Mobile broken; weak CTA.",
			Issues: []AuditQualIssue{
				{Type: "mobile", Severity: "high", Description: "Viewport missing."},
				{Type: "conversion", Severity: "high", Description: "No CTA above the fold."},
			},
		},
	}
}

func strongAudit() *AuditRow {
	return &AuditRow{
		ID:    "audit-2",
		Score: 92,
		Technical: AuditTechnical{
			HTTPS: true, Viewport: true, Favicon: true, ContactDetected: true,
			Lighthouse: AuditLighthouse{Performance: 90, Accessibility: 88, SEO: 92},
		},
	}
}

func business(vertical string, confidence float64) *BusinessRow {
	return &BusinessRow{
		ID: "biz-1", Name: "Acme Plumbing", Domain: "acme.co.uk",
		Vertical: vertical, Location: "Manchester", Source: "google-places",
		Confidence: confidence, Status: "new",
	}
}

func enabledProfile(vertical string) targeting.Profile {
	return targeting.Profile{
		ID:        "prof-1",
		Vertical:  vertical,
		Location:  "Manchester",
		Enabled:   true,
		UpdatedAt: "2026-05-12T10:00:00Z",
		Weights: targeting.Weights{
			AuditScore: 0.3, VerticalFit: 0.1, BusinessSize: 0.2,
			ContactConfidence: 0.2, WebsiteAge: 0.2,
		},
	}
}

func newDeps(t *testing.T, c *captured, audit *AuditRow, biz *BusinessRow, profiles []targeting.Profile, threshold int) runDeps {
	t.Helper()
	return runDeps{
		GetAudit: func(_ context.Context, _, auditID string) (*AuditRow, error) {
			if audit != nil && audit.ID == auditID {
				return audit, nil
			}
			return audit, nil
		},
		GetBusiness: func(_ context.Context, _ string) (*BusinessRow, error) {
			return biz, nil
		},
		ListProfiles: func(_ context.Context) ([]targeting.Profile, error) {
			c.scoreInputs++
			return profiles, nil
		},
		PutQualification: func(_ context.Context, row QualificationRow) error {
			c.qualification = row
			return nil
		},
		UpdateBusiness: func(_ context.Context, in BusinessUpdate) error {
			c.statusUpdates = append(c.statusUpdates, in.NewStatus)
			c.lastUpdate = in
			return nil
		},
		PublishQualified: func(_ context.Context, env pkgevents.Envelope[WebsiteQualifiedDetail]) error {
			c.publishedQualOK = true
			c.publishedQual = env
			return c.publishErr
		},
		PublishRejected: func(_ context.Context, env pkgevents.Envelope[WebsiteRejectedDetail]) error {
			c.publishedRejectOK = true
			c.publishedReject = env
			return c.publishErr
		},
		Threshold:        func(context.Context) (int, error) { return threshold, nil },
		MaxReviewQueue:   func(context.Context) (int, error) { return 20, nil },
		CountActiveSlots: func(context.Context) (int, error) { return 0, nil },
		Now:              func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) },
		NewQualID:        func() string { return "qual-1" },
	}
}

func makeEnv(businessID, auditID string) pkgevents.Envelope[AuditCompletedDetail] {
	return pkgevents.New("website.audit.completed", "audit", AuditCompletedDetail{
		BusinessID:       businessID,
		AuditID:          auditID,
		Score:            35,
		WorthRedesigning: true,
		ModelsUsed:       []string{"pagespeed", "anthropic.claude-haiku-4-5"},
	})
}

// --- runOne: weak site → qualified --------------------------------------

func TestRunOne_WeakSiteQualifies(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	a, b := weakAudit(), business("accountants", 0.9)
	d := newDeps(t, c, a, b, []targeting.Profile{enabledProfile("accountants")}, 70)

	if err := processRecord(context.Background(), d, makeEnv(b.ID, a.ID)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if !c.qualification.Qualified {
		t.Errorf("weak site should qualify; got Qualified=false (score %.4f)", c.qualification.PriorityScore)
	}
	if c.qualification.PK != "BUSINESS#biz-1" || c.qualification.SK != "QUAL#qual-1" {
		t.Errorf("qual row keys drift: %+v", c.qualification)
	}
	if len(c.qualification.Reasons) == 0 {
		t.Error("Reasons should be populated")
	}
	if !c.publishedQualOK || c.publishedRejectOK {
		t.Errorf("expected website.qualified publish only; got qual=%v rej=%v", c.publishedQualOK, c.publishedRejectOK)
	}
	if c.publishedQual.Detail.PriorityScore != c.qualification.PriorityScore {
		t.Errorf("published score (%.4f) != row score (%.4f)",
			c.publishedQual.Detail.PriorityScore, c.qualification.PriorityScore)
	}
	if len(c.statusUpdates) != 1 || c.statusUpdates[0] != "qualified" {
		t.Errorf("status update = %v, want [qualified]", c.statusUpdates)
	}
	if c.lastUpdate.AwaitingPromotion {
		t.Errorf("unbacklogged path should not set awaitingPromotion")
	}
	if c.lastUpdate.PriorityScore != c.qualification.PriorityScore {
		t.Errorf("BusinessUpdate.PriorityScore drift: %.4f vs %.4f", c.lastUpdate.PriorityScore, c.qualification.PriorityScore)
	}
}

// --- backlog gating -----------------------------------------------------

func TestRunOne_QueueAtCap_QualifiedGoesToBacklog(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	a, b := weakAudit(), business("accountants", 0.9)
	d := newDeps(t, c, a, b, []targeting.Profile{enabledProfile("accountants")}, 70)
	// Queue is at cap — qualifier should backlog rather than publish.
	d.MaxReviewQueue = func(context.Context) (int, error) { return 20, nil }
	d.CountActiveSlots = func(context.Context) (int, error) { return 20, nil }

	if err := processRecord(context.Background(), d, makeEnv(b.ID, a.ID)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if !c.qualification.Qualified {
		t.Error("backlog should still record qualified=true on the Qualification row")
	}
	if !c.lastUpdate.AwaitingPromotion {
		t.Error("Business row should be marked awaitingPromotion=true")
	}
	if c.lastUpdate.NewStatus != "qualified" {
		t.Errorf("Business status = %q, want qualified", c.lastUpdate.NewStatus)
	}
	if c.publishedQualOK || c.publishedRejectOK {
		t.Errorf("backlogged item should NOT publish website.qualified; got qual=%v rej=%v",
			c.publishedQualOK, c.publishedRejectOK)
	}
}

func TestRunOne_QueueBelowCap_QualifiedPublishesNormally(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	a, b := weakAudit(), business("accountants", 0.9)
	d := newDeps(t, c, a, b, []targeting.Profile{enabledProfile("accountants")}, 70)
	d.MaxReviewQueue = func(context.Context) (int, error) { return 20, nil }
	d.CountActiveSlots = func(context.Context) (int, error) { return 19, nil }

	if err := processRecord(context.Background(), d, makeEnv(b.ID, a.ID)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.lastUpdate.AwaitingPromotion {
		t.Error("just-below-cap should NOT trigger backlog")
	}
	if !c.publishedQualOK {
		t.Error("just-below-cap should publish website.qualified")
	}
}

func TestRunOne_RejectedSkipsBacklogCheck(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	a, b := strongAudit(), business("accountants", 0.8)
	called := 0
	d := newDeps(t, c, a, b, []targeting.Profile{enabledProfile("accountants")}, 70)
	d.CountActiveSlots = func(context.Context) (int, error) {
		called++
		return 99, nil
	}
	if err := processRecord(context.Background(), d, makeEnv(b.ID, a.ID)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if called != 0 {
		t.Errorf("rejected path should not query active slot count; got %d calls", called)
	}
	if !c.publishedRejectOK {
		t.Error("expected website.rejected publish")
	}
}

// --- runOne: strong site → rejected --------------------------------------

func TestRunOne_StrongSiteRejected(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	a, b := strongAudit(), business("accountants", 0.8)
	d := newDeps(t, c, a, b, []targeting.Profile{enabledProfile("accountants")}, 70)

	if err := processRecord(context.Background(), d, makeEnv(b.ID, a.ID)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.qualification.Qualified {
		t.Errorf("strong site should be rejected; got Qualified=true (score %.4f)", c.qualification.PriorityScore)
	}
	if c.publishedRejectOK == false || c.publishedQualOK {
		t.Errorf("expected website.rejected publish only; got qual=%v rej=%v", c.publishedQualOK, c.publishedRejectOK)
	}
	if len(c.publishedReject.Detail.Reasons) == 0 {
		t.Error("rejected event should carry reasons")
	}
	if c.statusUpdates[0] != "rejected" {
		t.Errorf("status update = %q, want rejected", c.statusUpdates[0])
	}
}

// --- borderline threshold drives the decision ---------------------------

func TestRunOne_BorderlineThresholdDrivesDecision(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	a, b := weakAudit(), business("accountants", 0.8)
	// Threshold so high nothing qualifies.
	d := newDeps(t, c, a, b, []targeting.Profile{enabledProfile("accountants")}, 99)
	if err := processRecord(context.Background(), d, makeEnv(b.ID, a.ID)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.qualification.Qualified {
		t.Errorf("threshold 99 should reject; got qualified")
	}
	// Now drop the threshold.
	c2 := &captured{}
	d2 := newDeps(t, c2, a, b, []targeting.Profile{enabledProfile("accountants")}, 10)
	if err := processRecord(context.Background(), d2, makeEnv(b.ID, "audit-2")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if !c2.qualification.Qualified {
		t.Errorf("threshold 10 should qualify; got rejected")
	}
}

// --- audit missing → skip ----------------------------------------------

func TestRunOne_AuditMissing_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, nil, business("accountants", 0.5), []targeting.Profile{enabledProfile("accountants")}, 70)
	d.GetAudit = func(context.Context, string, string) (*AuditRow, error) { return nil, nil }

	if err := processRecord(context.Background(), d, makeEnv("biz-x", "audit-x")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.qualification.ID != "" {
		t.Error("no qualification should be written when audit missing")
	}
}

// --- business missing → skip --------------------------------------------

func TestRunOne_BusinessMissing_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, weakAudit(), nil, []targeting.Profile{enabledProfile("accountants")}, 70)
	d.GetBusiness = func(context.Context, string) (*BusinessRow, error) { return nil, nil }

	if err := processRecord(context.Background(), d, makeEnv("biz-x", "audit-1")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.qualification.ID != "" {
		t.Error("no qualification should be written when business missing")
	}
}

// --- no matching profile → skip ----------------------------------------

func TestRunOne_NoMatchingProfile_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	// Profile in a different vertical only.
	d := newDeps(t, c, weakAudit(), business("accountants", 0.7), []targeting.Profile{enabledProfile("dentists")}, 70)
	if err := processRecord(context.Background(), d, makeEnv("biz-1", "audit-1")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.qualification.ID != "" {
		t.Error("no qualification expected when no matching profile")
	}
	if c.publishedQualOK || c.publishedRejectOK {
		t.Error("nothing should be published")
	}
}

// --- profile selection: case-insensitive, newest wins -------------------

func TestRunOne_ProfileSelection_CaseInsensitive_NewestWins(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	a, b := weakAudit(), business("Accountants", 0.7)
	older := enabledProfile("accountants")
	older.ID = "old"
	older.UpdatedAt = "2026-01-01T00:00:00Z"
	newer := enabledProfile("ACCOUNTANTS")
	newer.ID = "new"
	newer.UpdatedAt = "2026-05-01T00:00:00Z"
	disabled := enabledProfile("accountants")
	disabled.ID = "dis"
	disabled.UpdatedAt = "2026-06-01T00:00:00Z"
	disabled.Enabled = false

	d := newDeps(t, c, a, b, []targeting.Profile{older, newer, disabled}, 70)
	if err := processRecord(context.Background(), d, makeEnv(b.ID, a.ID)); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.qualification.TargetingProfileID != "new" {
		t.Errorf("expected newest enabled match (new), got %q", c.qualification.TargetingProfileID)
	}
}

// --- idempotency: replay skips --------------------------------------------

func TestProcessRecord_IdempotentReplay(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	a, b := weakAudit(), business("accountants", 0.7)
	d := newDeps(t, c, a, b, []targeting.Profile{enabledProfile("accountants")}, 70)

	env := makeEnv(b.ID, a.ID)
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("first call: %v", err)
	}
	first := c.qualification.ID
	// Second call with same EventID → idempotency short-circuits → no
	// new qualification written.
	c.qualification = QualificationRow{}
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c.qualification.ID != "" {
		t.Errorf("replay should be no-op; second qualification id = %q (first %q)", c.qualification.ID, first)
	}
}

// --- correlation / causation propagate ----------------------------------

func TestRunOne_PropagatesCorrelationAndCausation(t *testing.T) {
	setupItemsTable(t)
	c := &captured{}
	d := newDeps(t, c, weakAudit(), business("accountants", 0.9), []targeting.Profile{enabledProfile("accountants")}, 70)

	env := makeEnv("biz-1", "audit-1")
	env.CorrelationID = "corr-xyz"
	env.EventID = "evt-abc"
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.publishedQual.CorrelationID != "corr-xyz" {
		t.Errorf("correlation drift: got %q", c.publishedQual.CorrelationID)
	}
	if c.publishedQual.CausationID != "evt-abc" {
		t.Errorf("causation drift: got %q", c.publishedQual.CausationID)
	}
}

// --- publish failure surfaces (→ SQS retry → DLQ) -----------------------

func TestRunOne_PublishFailureSurfacesError(t *testing.T) {
	setupItemsTable(t)
	c := &captured{publishErr: errors.New("eventbridge down")}
	d := newDeps(t, c, weakAudit(), business("accountants", 0.7), []targeting.Profile{enabledProfile("accountants")}, 70)

	if err := processRecord(context.Background(), d, makeEnv("biz-1", "audit-1")); err == nil {
		t.Fatal("expected publish error to surface")
	}
}

// --- kill switch off → no work --------------------------------------------

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

// --- bad-body record becomes BatchItemFailure ---------------------------

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
		t.Errorf("expected one BatchItemFailure for msg-bad, got %+v", resp.BatchItemFailures)
	}
}

// --- buildReasons --------------------------------------------------------

func TestBuildReasons(t *testing.T) {
	a := weakAudit()
	b := business("accountants", 0.8)
	p := enabledProfile("accountants")
	got := buildReasons(a, b, p, true)
	wantSubstrings := []string{
		"high-severity audit issues",
		"very slow site",
		"contact details hard to find",
		"site is not HTTPS",
		"vertical fit high",
		"high-confidence discovery source",
	}
	joined := strings.Join(got, " | ")
	for _, w := range wantSubstrings {
		if !strings.Contains(joined, w) {
			t.Errorf("reasons missing %q in %s", w, joined)
		}
	}
}

func TestBuildReasons_DefaultWhenNothingFires(t *testing.T) {
	a := &AuditRow{
		ID: "x", Score: 50,
		Technical: AuditTechnical{HTTPS: true, ContactDetected: true,
			Lighthouse: AuditLighthouse{Performance: 70}},
	}
	b := business("dentists", 0.5)
	p := enabledProfile("accountants")
	got := buildReasons(a, b, p, false)
	if len(got) != 1 || got[0] != "score below threshold" {
		t.Errorf("expected single fallback reason, got %v", got)
	}
}

func TestBuildReasons_WellConvertedRejectedFlag(t *testing.T) {
	a := strongAudit()
	b := business("accountants", 0.7)
	p := enabledProfile("accountants")
	got := buildReasons(a, b, p, false)
	if !containsString(got, "site is already well-converted") {
		t.Errorf("missing well-converted reason; got %v", got)
	}
}

// --- helpers ----

func makeSQSRecord(t *testing.T, env pkgevents.Envelope[AuditCompletedDetail]) lambdaevents.SQSMessage {
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

func containsString(s []string, want string) bool {
	for _, x := range s {
		if x == want {
			return true
		}
	}
	return false
}
