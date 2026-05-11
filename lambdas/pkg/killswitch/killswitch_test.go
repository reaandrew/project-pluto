package killswitch

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// --- fakes / setup -------------------------------------------------------

type fakeDDB struct {
	getCalls  int32
	getOut    *dynamodb.GetItemOutput
	getErr    error
	gotInputs []*dynamodb.GetItemInput
}

func (f *fakeDDB) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	atomic.AddInt32(&f.getCalls, 1)
	f.gotInputs = append(f.gotInputs, in)
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getOut != nil {
		return f.getOut, nil
	}
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

// itemFor builds a DDB GetItemOutput for the given Settings.
func itemFor(t *testing.T, s Settings) *dynamodb.GetItemOutput {
	t.Helper()
	item, err := attributevalue.MarshalMap(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// ensure pk + sk are present (MarshalMap does not include unexported fields)
	item["pk"] = &dtypes.AttributeValueMemberS{Value: SettingsPK}
	item["sk"] = &dtypes.AttributeValueMemberS{Value: SettingsSK}
	return &dynamodb.GetItemOutput{Item: item}
}

// reset wipes the in-process cache + fakes a frozen clock for one test.
// Returns the fake DDB so tests can preload its response.
func reset(t *testing.T) (*fakeDDB, time.Time) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")

	frozen := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	prevNow := nowFunc
	nowFunc = func() time.Time { return frozen }

	prevCache := cacheTTL
	SetCacheTTL(60 * time.Second)
	SetSettings(nil)

	fake := &fakeDDB{}
	ddb.SetClient(fake)

	t.Cleanup(func() {
		ddb.SetClient(nil)
		SetSettings(nil)
		nowFunc = prevNow
		cacheMu.Lock()
		cacheTTL = prevCache
		cacheMu.Unlock()
	})
	return fake, frozen
}

// --- Get -----------------------------------------------------------------

func TestGetReadsAndCachesSettings(t *testing.T) {
	fake, _ := reset(t)
	fake.getOut = itemFor(t, Defaults())

	for i := 0; i < 5; i++ {
		s, err := Get(context.Background())
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !s.PipelineEnabled || !s.Stages.AuditEnabled {
			t.Errorf("call %d: defaults drifted: %+v", i, s)
		}
	}
	if got := atomic.LoadInt32(&fake.getCalls); got != 1 {
		t.Errorf("DDB GetItem called %d times, want 1 across 5 reads", got)
	}
}

func TestGetUsesStrongConsistency(t *testing.T) {
	fake, _ := reset(t)
	fake.getOut = itemFor(t, Defaults())

	if _, err := Get(context.Background()); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(fake.gotInputs) == 0 {
		t.Fatal("no GetItem call captured")
	}
	in := fake.gotInputs[0]
	if in.ConsistentRead == nil || !*in.ConsistentRead {
		t.Errorf("ConsistentRead should be true (operator-toggle latency)")
	}
}

func TestGetRefreshesAfterTTLExpires(t *testing.T) {
	fake, frozen := reset(t)
	fake.getOut = itemFor(t, Defaults())

	if _, err := Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	// advance just under TTL: still cached
	nowFunc = func() time.Time { return frozen.Add(59 * time.Second) }
	if _, err := Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&fake.getCalls); got != 1 {
		t.Errorf("under TTL: GetItem calls = %d, want 1", got)
	}

	// advance past TTL: refresh
	nowFunc = func() time.Time { return frozen.Add(61 * time.Second) }
	if _, err := Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&fake.getCalls); got != 2 {
		t.Errorf("past TTL: GetItem calls = %d, want 2", got)
	}
}

func TestGetSurfacesMissingRowError(t *testing.T) {
	fake, _ := reset(t)
	fake.getOut = &dynamodb.GetItemOutput{} // empty item — row missing

	_, err := Get(context.Background())
	if err == nil || !strings.Contains(err.Error(), "PipelineSettings row not found") {
		t.Errorf("expected missing-row error, got %v", err)
	}
}

func TestGetSurfacesItemsTableError(t *testing.T) {
	reset(t)
	t.Setenv("ITEMS_TABLE", "")
	if _, err := Get(context.Background()); err == nil || !strings.Contains(err.Error(), "ITEMS_TABLE") {
		t.Errorf("expected ITEMS_TABLE error, got %v", err)
	}
}

func TestGetSurfacesSDKError(t *testing.T) {
	fake, _ := reset(t)
	fake.getErr = errors.New("ddb down")
	if _, err := Get(context.Background()); !errors.Is(err, fake.getErr) {
		t.Errorf("expected SDK error to be wrapped, got %v", err)
	}
}

// --- Allowed -------------------------------------------------------------

func TestAllowedHonoursMasterKillSwitch(t *testing.T) {
	reset(t)
	s := Defaults()
	s.PipelineEnabled = false
	SetSettings(&s)

	for _, stage := range []string{StageDiscovery, StageAudit, StagePreview, StageOutreach} {
		ok, err := Allowed(context.Background(), stage)
		if err != nil {
			t.Errorf("%s: %v", stage, err)
		}
		if ok {
			t.Errorf("%s: expected false when pipelineEnabled=false", stage)
		}
	}
}

func TestAllowedHonoursPerStageFlag(t *testing.T) {
	reset(t)
	s := Defaults()
	// Defaults: outreach is OFF; everything else ON.
	SetSettings(&s)

	cases := map[string]bool{
		StageDiscovery: true,
		StageAudit:     true,
		StagePreview:   true,
		StageOutreach:  false,
	}
	for stage, want := range cases {
		t.Run(stage, func(t *testing.T) {
			got, err := Allowed(context.Background(), stage)
			if err != nil {
				t.Fatalf("%s: %v", stage, err)
			}
			if got != want {
				t.Errorf("%s: got %v, want %v", stage, got, want)
			}
		})
	}
}

func TestAllowedRejectsUnknownStage(t *testing.T) {
	reset(t)
	SetSettings(func() *Settings { d := Defaults(); return &d }())

	_, err := Allowed(context.Background(), "publisher")
	if err == nil || !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("expected unknown-stage error, got %v", err)
	}
}

// --- CapUSD --------------------------------------------------------------

func TestCapUSDReturnsBudgetForBedrockStages(t *testing.T) {
	reset(t)
	SetSettings(func() *Settings { d := Defaults(); return &d }())

	cases := map[string]float64{
		StageAudit:     5.0, // DailyBedrockUsd default
		StagePreview:   5.0,
		StageOutreach:  1.0, // DailyEmailUsd default
		StagePlaces:    2.0, // DailyPlacesUsd default
		StageDiscovery: 0,   // discovery has no Bedrock cost
	}
	for stage, want := range cases {
		t.Run(stage, func(t *testing.T) {
			got, err := CapUSD(context.Background(), stage)
			if err != nil {
				t.Fatalf("CapUSD(%s): %v", stage, err)
			}
			if got != want {
				t.Errorf("CapUSD(%s) = %v, want %v", stage, got, want)
			}
		})
	}
}

func TestCapUSDRejectsUnknownStage(t *testing.T) {
	reset(t)
	SetSettings(func() *Settings { d := Defaults(); return &d }())

	_, err := CapUSD(context.Background(), "qualifier")
	if err == nil || !strings.Contains(err.Error(), "unknown stage") {
		t.Errorf("expected unknown-stage error, got %v", err)
	}
}

// --- StageFor + StageMap -------------------------------------------------

func TestStageForKnownConsumers(t *testing.T) {
	cases := map[string]string{
		"discover":         StageDiscovery,
		"audit":            StageAudit,
		"qualifier":        StageAudit,
		"spec-generator":   StagePreview,
		"generator":        StagePreview,
		"publisher":        StagePreview,
		"email-draft":      StageOutreach,
		"sender":           StageOutreach,
		"tuner-style":      "", // tuners are not stage-gated
		"tuner-email-tone": "",
		"tuner-targeting":  "",
	}
	for consumer, want := range cases {
		t.Run(consumer, func(t *testing.T) {
			if got := StageFor(consumer); got != want {
				t.Errorf("StageFor(%q) = %q, want %q", consumer, got, want)
			}
		})
	}
}

func TestStageForUnknownConsumerReturnsEmpty(t *testing.T) {
	if got := StageFor("nonsense"); got != "" {
		t.Errorf("StageFor(unknown) = %q, want empty", got)
	}
}

// --- SetSettings test hook ------------------------------------------------

func TestSetSettingsBypassesDDB(t *testing.T) {
	fake, _ := reset(t)

	override := Defaults()
	override.PipelineEnabled = false
	SetSettings(&override)

	got, err := Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.PipelineEnabled {
		t.Errorf("override not honoured: %+v", got)
	}
	if c := atomic.LoadInt32(&fake.getCalls); c != 0 {
		t.Errorf("DDB called %d times despite SetSettings, want 0", c)
	}
}

func TestSetSettingsNilClearsCache(t *testing.T) {
	fake, _ := reset(t)
	fake.getOut = itemFor(t, Defaults())

	override := Defaults()
	SetSettings(&override)
	SetSettings(nil) // clear

	if _, err := Get(context.Background()); err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if c := atomic.LoadInt32(&fake.getCalls); c != 1 {
		t.Errorf("expected 1 DDB call after cache clear, got %d", c)
	}
}

func TestSetCacheTTLAffectsExpiry(t *testing.T) {
	fake, frozen := reset(t)
	fake.getOut = itemFor(t, Defaults())

	SetCacheTTL(10 * time.Second)
	if _, err := Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	nowFunc = func() time.Time { return frozen.Add(11 * time.Second) }
	if _, err := Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&fake.getCalls); got != 2 {
		t.Errorf("with 10s TTL after 11s: calls = %d, want 2", got)
	}
}
