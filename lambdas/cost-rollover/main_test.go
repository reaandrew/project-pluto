package main

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/killswitch"
)

// --- the pure rollover function ---------------------------------------

func TestRolloverNoOpWhenNothingPaused(t *testing.T) {
	in := killswitch.Defaults()
	out, reenabled := rollover(in)
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("rollover mutated input despite no pauses: %+v", out)
	}
	if len(reenabled) != 0 {
		t.Fatalf("re-enabled %v despite no pauses", reenabled)
	}
}

func TestRolloverReenablesBudgetPausedStage(t *testing.T) {
	in := killswitch.Defaults()
	in.Stages.AuditEnabled = false
	in.StagePauseReasons.Audit = killswitch.PauseReasonBudget

	out, reenabled := rollover(in)

	if !out.Stages.AuditEnabled {
		t.Errorf("AuditEnabled should be true after rollover")
	}
	if out.StagePauseReasons.Audit != "" {
		t.Errorf("Audit pause reason should be cleared, got %q", out.StagePauseReasons.Audit)
	}
	if !reflect.DeepEqual(reenabled, []string{killswitch.StageAudit}) {
		t.Errorf("re-enabled = %v, want [audit]", reenabled)
	}
}

func TestRolloverIgnoresOperatorPause(t *testing.T) {
	// Operator manually disabled outreach via /settings — no pause reason.
	// Rollover must not flip it back on.
	in := killswitch.Defaults()
	in.Stages.OutreachEnabled = false
	in.StagePauseReasons.Outreach = "" // explicit no-reason

	out, reenabled := rollover(in)
	if out.Stages.OutreachEnabled {
		t.Error("rollover re-enabled an operator-paused stage")
	}
	if len(reenabled) != 0 {
		t.Errorf("rollover claimed to re-enable %v despite no budget pauses", reenabled)
	}
}

func TestRolloverIgnoresUnknownPauseReason(t *testing.T) {
	in := killswitch.Defaults()
	in.Stages.PreviewEnabled = false
	in.StagePauseReasons.Preview = "quota-exhausted" // future reason not yet handled
	out, reenabled := rollover(in)
	if out.Stages.PreviewEnabled {
		t.Error("rollover re-enabled a stage paused for an unknown reason")
	}
	if len(reenabled) != 0 {
		t.Errorf("re-enabled %v despite unknown reason", reenabled)
	}
}

func TestRolloverHandlesMultipleBudgetPauses(t *testing.T) {
	in := killswitch.Defaults()
	in.Stages.DiscoveryEnabled = false
	in.StagePauseReasons.Discovery = killswitch.PauseReasonBudget
	in.Stages.AuditEnabled = false
	in.StagePauseReasons.Audit = killswitch.PauseReasonBudget
	in.Stages.PreviewEnabled = false
	in.StagePauseReasons.Preview = killswitch.PauseReasonBudget

	_, reenabled := rollover(in)
	got := append([]string{}, reenabled...)
	sort.Strings(got)
	want := []string{killswitch.StageAudit, killswitch.StageDiscovery, killswitch.StagePreview}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("re-enabled = %v, want %v", got, want)
	}
}

// Operator paused outreach manually AND audit was paused for budget on the
// same day. Rollover must re-enable audit but leave outreach alone.
func TestRolloverMixedPauseSources(t *testing.T) {
	in := killswitch.Defaults()
	in.Stages.AuditEnabled = false
	in.StagePauseReasons.Audit = killswitch.PauseReasonBudget
	in.Stages.OutreachEnabled = false
	in.StagePauseReasons.Outreach = "" // operator pause, no reason

	out, reenabled := rollover(in)
	if !out.Stages.AuditEnabled {
		t.Error("audit should be re-enabled (budget pause)")
	}
	if out.Stages.OutreachEnabled {
		t.Error("outreach should remain disabled (operator pause)")
	}
	if !reflect.DeepEqual(reenabled, []string{killswitch.StageAudit}) {
		t.Errorf("re-enabled = %v, want [audit]", reenabled)
	}
}

// --- the handler integration with DDB ---------------------------------

type fakeDDB struct {
	getOut    *dynamodb.GetItemOutput
	getErr    error
	putErr    error
	putInputs []*dynamodb.PutItemInput
}

func (f *fakeDDB) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getOut != nil {
		return f.getOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.putInputs = append(f.putInputs, in)
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}

func itemFor(t *testing.T, s killswitch.Settings) *dynamodb.GetItemOutput {
	t.Helper()
	item, err := attributevalue.MarshalMap(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	item["pk"] = &dtypes.AttributeValueMemberS{Value: killswitch.SettingsPK}
	item["sk"] = &dtypes.AttributeValueMemberS{Value: killswitch.SettingsSK}
	return &dynamodb.GetItemOutput{Item: item}
}

func reset(t *testing.T) *fakeDDB {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	t.Setenv("ENVIRONMENT", "unit-test")
	killswitch.SetSettings(nil)
	fake := &fakeDDB{}
	ddb.SetClient(fake)
	t.Cleanup(func() {
		ddb.SetClient(nil)
		killswitch.SetSettings(nil)
	})
	return fake
}

func TestHandleNoOpDoesNotWrite(t *testing.T) {
	fake := reset(t)
	fake.getOut = itemFor(t, killswitch.Defaults())

	if err := handle(context.Background(), nil); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(fake.putInputs) != 0 {
		t.Fatalf("PutItem called on a no-op rollover (%d times)", len(fake.putInputs))
	}
}

func TestHandlePersistsRolloverWhenBudgetPaused(t *testing.T) {
	fake := reset(t)
	in := killswitch.Defaults()
	in.Stages.AuditEnabled = false
	in.StagePauseReasons.Audit = killswitch.PauseReasonBudget
	fake.getOut = itemFor(t, in)

	if err := handle(context.Background(), nil); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(fake.putInputs) != 1 {
		t.Fatalf("expected exactly 1 PutItem call, got %d", len(fake.putInputs))
	}

	var persisted killswitch.Settings
	if err := attributevalue.UnmarshalMap(fake.putInputs[0].Item, &persisted); err != nil {
		t.Fatalf("unmarshal persisted: %v", err)
	}
	if !persisted.Stages.AuditEnabled {
		t.Error("persisted AuditEnabled = false, want true")
	}
	if persisted.StagePauseReasons.Audit != "" {
		t.Errorf("persisted Audit pause reason = %q, want empty", persisted.StagePauseReasons.Audit)
	}

	// pk/sk must be preserved on the singleton row.
	if pk, _ := fake.putInputs[0].Item["pk"].(*dtypes.AttributeValueMemberS); pk == nil || pk.Value != killswitch.SettingsPK {
		t.Errorf("pk attribute missing/wrong: %+v", fake.putInputs[0].Item["pk"])
	}
}

func TestHandleSurfacesReadError(t *testing.T) {
	fake := reset(t)
	fake.getErr = errors.New("ddb down")
	if err := handle(context.Background(), nil); err == nil {
		t.Fatal("expected error from failed read, got nil")
	}
}

func TestHandleSurfacesWriteError(t *testing.T) {
	fake := reset(t)
	in := killswitch.Defaults()
	in.Stages.OutreachEnabled = false
	in.StagePauseReasons.Outreach = killswitch.PauseReasonBudget
	fake.getOut = itemFor(t, in)
	fake.putErr = errors.New("ddb full")

	if err := handle(context.Background(), nil); err == nil {
		t.Fatal("expected error from failed write, got nil")
	}
}

// After a successful rollover the warm-container settings cache must
// reflect the post-rollover state — a follow-up pkg/killswitch.Allowed
// call from the same container (e.g. another scheduled invocation arriving
// before the 60s TTL elapses) must see the re-enabled stages.
func TestHandlePrimesCacheAfterWrite(t *testing.T) {
	fake := reset(t)
	in := killswitch.Defaults()
	in.Stages.AuditEnabled = false
	in.StagePauseReasons.Audit = killswitch.PauseReasonBudget
	fake.getOut = itemFor(t, in)

	if err := handle(context.Background(), nil); err != nil {
		t.Fatalf("handle: %v", err)
	}

	cached, err := killswitch.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !cached.Stages.AuditEnabled {
		t.Fatal("post-rollover cache still says AuditEnabled=false")
	}
}
