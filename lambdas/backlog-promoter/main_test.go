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
)

// --- ddb fake -----------------------------------------------------------

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

// --- captured + fixtures -------------------------------------------------

type captured struct {
	findCalls   int
	qualLookups []string
	clearCalls  []string
	publishedOK bool
	published   pkgevents.Envelope[WebsiteQualifiedDetail]
	publishErr  error
	findErr     error
	clearErr    error
	topBacklog  *BusinessRow
	latestQual  map[string]*QualificationRow
}

func newDeps(t *testing.T, c *captured) runDeps {
	t.Helper()
	return runDeps{
		FindTopBacklog: func(context.Context) (*BusinessRow, error) {
			c.findCalls++
			if c.findErr != nil {
				return nil, c.findErr
			}
			return c.topBacklog, nil
		},
		LatestQualificationForBiz: func(_ context.Context, businessID string) (*QualificationRow, error) {
			c.qualLookups = append(c.qualLookups, businessID)
			if c.latestQual == nil {
				return nil, nil
			}
			return c.latestQual[businessID], nil
		},
		ClearAwaitingPromotion: func(_ context.Context, businessID string) error {
			c.clearCalls = append(c.clearCalls, businessID)
			return c.clearErr
		},
		PublishQualified: func(_ context.Context, env pkgevents.Envelope[WebsiteQualifiedDetail]) error {
			c.publishedOK = true
			c.published = env
			return c.publishErr
		},
		Now: func() time.Time { return time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC) },
	}
}

func makeEnv(freedBusinessID string) pkgevents.Envelope[QueueSlotFreedDetail] {
	return pkgevents.New("queue.slot.freed", "publisher", QueueSlotFreedDetail{
		BusinessID:     freedBusinessID,
		PreviousStatus: "awaiting_review",
	})
}

// --- empty backlog → no-op ---------------------------------------------

func TestRunOne_EmptyBacklog_NoOp(t *testing.T) {
	setupItemsTable(t)
	c := &captured{topBacklog: nil}
	d := newDeps(t, c)
	if err := processRecord(context.Background(), d, makeEnv("freed-1")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.publishedOK || len(c.clearCalls) > 0 {
		t.Error("nothing should be published or cleared when backlog empty")
	}
}

// --- happy path: promote highest-priority ------------------------------

func TestRunOne_HappyPath(t *testing.T) {
	setupItemsTable(t)
	c := &captured{
		topBacklog: &BusinessRow{ID: "biz-top", AwaitingPromotion: true},
		latestQual: map[string]*QualificationRow{
			"biz-top": {ID: "qual-1", Qualified: true, PriorityScore: 0.85, AuditID: "audit-1"},
		},
	}
	d := newDeps(t, c)
	env := makeEnv("freed-x")
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.findCalls != 1 {
		t.Errorf("FindTopBacklog called %d times, want 1", c.findCalls)
	}
	if len(c.qualLookups) != 1 || c.qualLookups[0] != "biz-top" {
		t.Errorf("qual lookups = %v, want [biz-top]", c.qualLookups)
	}
	if len(c.clearCalls) != 1 || c.clearCalls[0] != "biz-top" {
		t.Errorf("clear calls = %v, want [biz-top]", c.clearCalls)
	}
	if !c.publishedOK {
		t.Fatal("expected website.qualified publish")
	}
	if c.published.Detail.BusinessID != "biz-top" ||
		c.published.Detail.QualificationID != "qual-1" ||
		c.published.Detail.PriorityScore != 0.85 ||
		c.published.Detail.AuditID != "audit-1" {
		t.Errorf("published detail drift: %+v", c.published.Detail)
	}
	if c.published.CausationID != env.EventID {
		t.Errorf("causation = %q, want %q (the queue.slot.freed event)", c.published.CausationID, env.EventID)
	}
	// Promotion starts a new correlation chain — should NOT inherit
	// the freed event's correlationID (that belongs to the OTHER
	// business's chain).
	if c.published.CorrelationID == env.CorrelationID {
		t.Errorf("promoted event should start a new correlation chain (got same id %q)", c.published.CorrelationID)
	}
}

// --- qualification missing → log + skip --------------------------------

func TestRunOne_QualificationMissing_Skips(t *testing.T) {
	setupItemsTable(t)
	c := &captured{
		topBacklog: &BusinessRow{ID: "biz-top", AwaitingPromotion: true},
		latestQual: map[string]*QualificationRow{}, // none
	}
	d := newDeps(t, c)
	if err := processRecord(context.Background(), d, makeEnv("freed-1")); err != nil {
		t.Fatalf("processRecord: %v", err)
	}
	if c.publishedOK || len(c.clearCalls) > 0 {
		t.Error("missing qualification should not publish/clear")
	}
}

// --- find error → surface (retry) ---------------------------------------

func TestRunOne_FindError_Surfaces(t *testing.T) {
	setupItemsTable(t)
	c := &captured{findErr: errors.New("ddb down")}
	d := newDeps(t, c)
	err := processRecord(context.Background(), d, makeEnv("freed-1"))
	if err == nil {
		t.Fatal("expected find error to surface")
	}
}

// --- clear error → surface ----------------------------------------------

func TestRunOne_ClearError_Surfaces(t *testing.T) {
	setupItemsTable(t)
	c := &captured{
		topBacklog: &BusinessRow{ID: "biz-top", AwaitingPromotion: true},
		latestQual: map[string]*QualificationRow{
			"biz-top": {ID: "qual-1", Qualified: true, PriorityScore: 0.7, AuditID: "a"},
		},
		clearErr: errors.New("conditional failed"),
	}
	d := newDeps(t, c)
	if err := processRecord(context.Background(), d, makeEnv("freed-1")); err == nil {
		t.Fatal("expected clear error to surface")
	}
}

// --- publish error → surface --------------------------------------------

func TestRunOne_PublishError_Surfaces(t *testing.T) {
	setupItemsTable(t)
	c := &captured{
		topBacklog: &BusinessRow{ID: "biz-top", AwaitingPromotion: true},
		latestQual: map[string]*QualificationRow{
			"biz-top": {ID: "qual-1", Qualified: true, PriorityScore: 0.7, AuditID: "a"},
		},
		publishErr: errors.New("eventbridge down"),
	}
	d := newDeps(t, c)
	if err := processRecord(context.Background(), d, makeEnv("freed-1")); err == nil {
		t.Fatal("expected publish error to surface")
	}
}

// --- idempotent replay ---------------------------------------------------

func TestProcessRecord_IdempotentReplay(t *testing.T) {
	setupItemsTable(t)
	c := &captured{
		topBacklog: &BusinessRow{ID: "biz-top", AwaitingPromotion: true},
		latestQual: map[string]*QualificationRow{
			"biz-top": {ID: "qual-1", Qualified: true, PriorityScore: 0.7, AuditID: "a"},
		},
	}
	d := newDeps(t, c)
	env := makeEnv("freed-1")
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("first call: %v", err)
	}
	first := c.findCalls
	if err := processRecord(context.Background(), d, env); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c.findCalls != first {
		t.Errorf("replay should be no-op; FindTopBacklog called again (%d → %d)", first, c.findCalls)
	}
}

// --- kill switch off ----------------------------------------------------

func TestHandle_KillSwitchOff(t *testing.T) {
	setupItemsTable(t)
	setupKillSwitch(t, false)
	rec := makeSQSRecord(t, makeEnv("freed-1"))
	resp, err := handle(context.Background(), lambdaevents.SQSEvent{Records: []lambdaevents.SQSMessage{rec}})
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(resp.BatchItemFailures) != 0 {
		t.Errorf("kill switch off should yield no failures, got %v", resp.BatchItemFailures)
	}
}

// --- helpers -------------------------------------------------------------

func makeSQSRecord(t *testing.T, env pkgevents.Envelope[QueueSlotFreedDetail]) lambdaevents.SQSMessage {
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
