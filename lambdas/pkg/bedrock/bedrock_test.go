package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// --- fakes -----------------------------------------------------------------

type fakeBedrock struct {
	gotInput *bedrockruntime.InvokeModelInput
	body     []byte
	err      error
	calls    int
}

func (f *fakeBedrock) InvokeModel(_ context.Context, in *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	f.calls++
	f.gotInput = in
	if f.err != nil {
		return nil, f.err
	}
	return &bedrockruntime.InvokeModelOutput{Body: f.body}, nil
}

type fakeDDB struct {
	getOut      *dynamodb.GetItemOutput
	getErr      error
	putErr      error
	updateErr   error
	gotPut      *dynamodb.PutItemInput
	gotGet      *dynamodb.GetItemInput
	getCalls    int
	putCalls    int
	updateCalls int
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.putCalls++
	f.gotPut = in
	if f.putErr != nil {
		return nil, f.putErr
	}
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	f.getCalls++
	f.gotGet = in
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.getOut != nil {
		return f.getOut, nil
	}
	return &dynamodb.GetItemOutput{}, nil
}

func (f *fakeDDB) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	f.updateCalls++
	if f.updateErr != nil {
		return nil, f.updateErr
	}
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

func setup(t *testing.T) (*fakeBedrock, *fakeDDB) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")

	b := &fakeBedrock{}
	d := &fakeDDB{}
	SetClient(b)
	ddb.SetClient(d)

	frozen := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	prevNow := nowFunc
	nowFunc = func() time.Time { return frozen }

	t.Cleanup(func() {
		SetClient(nil)
		ddb.SetClient(nil)
		nowFunc = prevNow
	})
	return b, d
}

// makeBedrockResponse builds a synthetic Anthropic-on-Bedrock response body
// where the only `tool_use` content is named toolName and carries the given
// input + token counts.
func makeBedrockResponse(t *testing.T, toolName string, toolInput any, inTok, outTok int) []byte {
	t.Helper()
	inputRaw, err := json.Marshal(toolInput)
	if err != nil {
		t.Fatalf("marshal toolInput: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"content": []map[string]any{{
			"type":  "tool_use",
			"name":  toolName,
			"input": json.RawMessage(inputRaw),
		}},
		"usage": map[string]int{
			"input_tokens":  inTok,
			"output_tokens": outTok,
		},
	})
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	return body
}

// --- happy path -----------------------------------------------------------

type sampleResult struct {
	Headline string `json:"headline"`
	Score    int    `json:"score"`
}

func TestInvokeStructuredHappyPath(t *testing.T) {
	bedrockFake, ddbFake := setup(t)

	want := sampleResult{Headline: "Acme Accountants", Score: 42}
	bedrockFake.body = makeBedrockResponse(t, "produceSpec", want, 1_000, 500)

	in := InvokeInput[sampleResult]{
		ModelID:     ModelHaiku45,
		PromptID:    "spec.v1",
		System:      "You write specs.",
		Messages:    []Message{{Role: "user", Content: "test"}},
		ToolName:    "produceSpec",
		ToolSchema:  json.RawMessage(`{"type":"object"}`),
		Stage:       StageSpec,
		EstimateUSD: 0.05,
		CapUSD:      5.0,
		CacheKey:    "abc123",
		MaxTokens:   1000,
	}
	got, err := InvokeStructured(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeStructured: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}

	if bedrockFake.calls != 1 {
		t.Errorf("InvokeModel called %d times, want 1", bedrockFake.calls)
	}
	// 2 GetItems (cache check + cost.Assert→cost.Get), 1 PutItem (cache write),
	// 1 UpdateItem (cost.Record).
	if ddbFake.getCalls != 2 || ddbFake.putCalls != 1 || ddbFake.updateCalls != 1 {
		t.Errorf("ddb calls: get=%d put=%d update=%d, want 2/1/1",
			ddbFake.getCalls, ddbFake.putCalls, ddbFake.updateCalls)
	}

	// Body of the InvokeModel call should force tool_use of the named tool.
	if !strings.Contains(string(bedrockFake.gotInput.Body), `"tool_choice":{"name":"produceSpec","type":"tool"}`) {
		t.Errorf("InvokeModel body missing forced tool_choice: %s", bedrockFake.gotInput.Body)
	}
	if aws.ToString(bedrockFake.gotInput.ModelId) != ModelHaiku45 {
		t.Errorf("ModelId = %s", aws.ToString(bedrockFake.gotInput.ModelId))
	}
}

// --- cache hit ------------------------------------------------------------

func TestInvokeStructuredReturnsCachedAndSkipsBedrock(t *testing.T) {
	bedrockFake, ddbFake := setup(t)

	cached := sampleResult{Headline: "from-cache", Score: 99}
	cachedRaw, _ := json.Marshal(cached)
	ddbFake.getOut = &dynamodb.GetItemOutput{
		Item: map[string]dtypes.AttributeValue{
			"pk":      &dtypes.AttributeValueMemberS{Value: cachePK("spec.v1")},
			"sk":      &dtypes.AttributeValueMemberS{Value: cacheSK("abc123")},
			"payload": &dtypes.AttributeValueMemberS{Value: string(cachedRaw)},
		},
	}

	in := InvokeInput[sampleResult]{
		ModelID:   ModelHaiku45,
		PromptID:  "spec.v1",
		ToolName:  "produceSpec",
		Stage:     StageSpec,
		CapUSD:    5.0,
		CacheKey:  "abc123",
		MaxTokens: 100,
	}
	got, err := InvokeStructured(context.Background(), in)
	if err != nil {
		t.Fatalf("InvokeStructured: %v", err)
	}
	if got != cached {
		t.Errorf("expected cached %+v, got %+v", cached, got)
	}
	if bedrockFake.calls != 0 {
		t.Errorf("Bedrock should not be called on cache hit, got %d calls", bedrockFake.calls)
	}
	if ddbFake.updateCalls != 0 {
		t.Errorf("cost.Record should not be called on cache hit, got %d UpdateItem calls", ddbFake.updateCalls)
	}
}

// --- cap exceeded ---------------------------------------------------------

func TestInvokeStructuredHonoursCostCap(t *testing.T) {
	bedrockFake, ddbFake := setup(t)
	ddbFake.getOut = &dynamodb.GetItemOutput{
		Item: map[string]dtypes.AttributeValue{
			"pk":       &dtypes.AttributeValueMemberS{Value: "CAP#2026-05-09"},
			"sk":       &dtypes.AttributeValueMemberS{Value: "STAGE#spec"},
			"spentUsd": &dtypes.AttributeValueMemberN{Value: "4.99"},
		},
	}

	// no cache key => cache check skipped
	in := InvokeInput[sampleResult]{
		ModelID:     ModelHaiku45,
		PromptID:    "spec.v1",
		ToolName:    "produceSpec",
		Stage:       StageSpec,
		EstimateUSD: 0.5,
		CapUSD:      5.0,
		MaxTokens:   100,
	}
	_, err := InvokeStructured(context.Background(), in)
	if !errors.Is(err, cost.ErrBudgetCapExceeded) {
		t.Errorf("expected ErrBudgetCapExceeded, got %v", err)
	}
	if bedrockFake.calls != 0 {
		t.Errorf("Bedrock should not be invoked when capped, got %d calls", bedrockFake.calls)
	}
}

// --- missing tool_use -----------------------------------------------------

func TestInvokeStructuredReturnsErrNoToolUseOnWrongName(t *testing.T) {
	bedrockFake, _ := setup(t)
	bedrockFake.body = makeBedrockResponse(t, "wrong-tool", sampleResult{Headline: "x", Score: 1}, 100, 50)

	in := InvokeInput[sampleResult]{
		ModelID:   ModelHaiku45,
		PromptID:  "spec.v1",
		ToolName:  "produceSpec",
		Stage:     StageSpec,
		CapUSD:    5.0,
		CacheKey:  "k",
		MaxTokens: 100,
	}
	_, err := InvokeStructured(context.Background(), in)
	if !errors.Is(err, ErrNoToolUse) {
		t.Errorf("expected ErrNoToolUse, got %v", err)
	}
}

// --- validator path -------------------------------------------------------

func TestInvokeStructuredRunsValidator(t *testing.T) {
	bedrockFake, _ := setup(t)
	bedrockFake.body = makeBedrockResponse(t, "produceSpec", sampleResult{Headline: "x", Score: 1}, 100, 50)

	r := &rejectingValidator{err: errors.New("schema invalid")}
	SetValidator(r)
	t.Cleanup(func() { SetValidator(nil) })

	in := InvokeInput[sampleResult]{
		ModelID:    ModelHaiku45,
		PromptID:   "spec.v1",
		ToolName:   "produceSpec",
		ToolSchema: json.RawMessage(`{"type":"object"}`),
		Stage:      StageSpec,
		CapUSD:     5.0,
		CacheKey:   "k",
		MaxTokens:  100,
	}
	_, err := InvokeStructured(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "failed schema") {
		t.Errorf("expected schema-failure error, got %v", err)
	}
	if r.called != 1 {
		t.Errorf("Validator.Validate called %d times, want 1", r.called)
	}
}

// --- input validation ------------------------------------------------------

func TestInvokeStructuredRequiresFields(t *testing.T) {
	setup(t)

	cases := map[string]InvokeInput[sampleResult]{
		"no model":     {PromptID: "p", ToolName: "t", MaxTokens: 1, CapUSD: 1},
		"no promptID":  {ModelID: "m", ToolName: "t", MaxTokens: 1, CapUSD: 1},
		"no toolName":  {ModelID: "m", PromptID: "p", MaxTokens: 1, CapUSD: 1},
		"no maxTokens": {ModelID: "m", PromptID: "p", ToolName: "t", CapUSD: 1},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := InvokeStructured(context.Background(), in); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

// --- buildBody snapshot ---------------------------------------------------

func TestBuildBodyShape(t *testing.T) {
	temp := 0.5
	in := InvokeInput[sampleResult]{
		ModelID:     ModelHaiku45,
		PromptID:    "spec.v1",
		System:      "be brief",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		ToolName:    "produceSpec",
		ToolSchema:  json.RawMessage(`{"type":"object","required":["headline"]}`),
		MaxTokens:   500,
		Temperature: &temp,
	}
	body, err := buildBody(in)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("body not valid JSON: %v", err)
	}
	if parsed["anthropic_version"] != AnthropicVersion {
		t.Errorf("anthropic_version = %v", parsed["anthropic_version"])
	}
	if parsed["temperature"] != 0.5 {
		t.Errorf("temperature = %v, want 0.5 (override)", parsed["temperature"])
	}
	if parsed["max_tokens"] != float64(500) {
		t.Errorf("max_tokens = %v", parsed["max_tokens"])
	}
	tc, ok := parsed["tool_choice"].(map[string]any)
	if !ok || tc["type"] != "tool" || tc["name"] != "produceSpec" {
		t.Errorf("tool_choice malformed: %v", parsed["tool_choice"])
	}
}

func TestBuildBodyDefaultsTemperature(t *testing.T) {
	in := InvokeInput[sampleResult]{
		ModelID:   ModelHaiku45,
		PromptID:  "p",
		ToolName:  "t",
		MaxTokens: 100,
	}
	body, _ := buildBody(in)
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	if parsed["temperature"] != DefaultTemperature {
		t.Errorf("default temperature = %v, want %v", parsed["temperature"], DefaultTemperature)
	}
}

// --- error paths ----------------------------------------------------------

func TestInvokeStructuredWrapsBedrockSDKError(t *testing.T) {
	bedrockFake, _ := setup(t)
	wantErr := errors.New("network down")
	bedrockFake.err = wantErr

	in := InvokeInput[sampleResult]{
		ModelID:   ModelHaiku45,
		PromptID:  "spec.v1",
		ToolName:  "produceSpec",
		Stage:     StageSpec,
		CapUSD:    5.0,
		CacheKey:  "k",
		MaxTokens: 100,
	}
	_, err := InvokeStructured(context.Background(), in)
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrap of %v", err, wantErr)
	}
}

func TestInvokeStructuredEmptyCacheKeySkipsCacheRead(t *testing.T) {
	bedrockFake, ddbFake := setup(t)
	bedrockFake.body = makeBedrockResponse(t, "produceSpec", sampleResult{Headline: "x", Score: 1}, 100, 50)

	in := InvokeInput[sampleResult]{
		ModelID:   ModelHaiku45,
		PromptID:  "spec.v1",
		ToolName:  "produceSpec",
		Stage:     StageSpec,
		CapUSD:    5.0,
		CacheKey:  "", // disables caching
		MaxTokens: 100,
	}
	if _, err := InvokeStructured(context.Background(), in); err != nil {
		t.Fatalf("InvokeStructured: %v", err)
	}
	// No cache read or cache write — only cost.Assert's GetItem and cost.Record's UpdateItem.
	if ddbFake.getCalls != 1 {
		t.Errorf("getCalls = %d, want 1 (cost.Assert only)", ddbFake.getCalls)
	}
	if ddbFake.putCalls != 0 {
		t.Errorf("putCalls = %d, want 0 (cache write skipped)", ddbFake.putCalls)
	}
	if ddbFake.updateCalls != 1 {
		t.Errorf("updateCalls = %d, want 1 (cost.Record)", ddbFake.updateCalls)
	}
}

func TestInvokeStructuredSurfacesCostRecordError(t *testing.T) {
	bedrockFake, ddbFake := setup(t)
	bedrockFake.body = makeBedrockResponse(t, "produceSpec", sampleResult{Headline: "x", Score: 1}, 100, 50)
	ddbFake.updateErr = errors.New("ddb update failed")

	in := InvokeInput[sampleResult]{
		ModelID:   ModelHaiku45,
		PromptID:  "spec.v1",
		ToolName:  "produceSpec",
		Stage:     StageSpec,
		CapUSD:    5.0,
		CacheKey:  "k",
		MaxTokens: 100,
	}
	_, err := InvokeStructured(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "recording spend") {
		t.Errorf("expected recording-spend error, got %v", err)
	}
}

func TestInvokeStructuredSurfacesCacheWriteError(t *testing.T) {
	bedrockFake, ddbFake := setup(t)
	bedrockFake.body = makeBedrockResponse(t, "produceSpec", sampleResult{Headline: "x", Score: 1}, 100, 50)
	ddbFake.putErr = errors.New("ddb put failed")

	in := InvokeInput[sampleResult]{
		ModelID:   ModelHaiku45,
		PromptID:  "spec.v1",
		ToolName:  "produceSpec",
		Stage:     StageSpec,
		CapUSD:    5.0,
		CacheKey:  "k",
		MaxTokens: 100,
	}
	_, err := InvokeStructured(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "caching response") {
		t.Errorf("expected caching-response error, got %v", err)
	}
}

func TestInvokeStructuredRequiresItemsTableForCache(t *testing.T) {
	bedrockFake, ddbFake := setup(t)
	bedrockFake.body = makeBedrockResponse(t, "produceSpec", sampleResult{}, 100, 50)
	t.Setenv("ITEMS_TABLE", "")
	_ = ddbFake // unused

	in := InvokeInput[sampleResult]{
		ModelID:   ModelHaiku45,
		PromptID:  "spec.v1",
		ToolName:  "produceSpec",
		Stage:     StageSpec,
		CapUSD:    5.0,
		CacheKey:  "k",
		MaxTokens: 100,
	}
	if _, err := InvokeStructured(context.Background(), in); err == nil || !strings.Contains(err.Error(), "ITEMS_TABLE") {
		t.Errorf("expected ITEMS_TABLE error, got %v", err)
	}
}

func TestExtractToolUseRejectsBadJSON(t *testing.T) {
	if _, _, err := extractToolUse([]byte("not json"), "x"); err == nil {
		t.Error("expected JSON parse error")
	}
}

// --- Client lazy init -----------------------------------------------------

func TestClientLazyInitsRealClientWhenNotSet(t *testing.T) {
	t.Cleanup(func() { SetClient(nil) })
	SetClient(nil)

	got, err := Client(context.Background())
	if err != nil {
		t.Fatalf("Client: %v", err)
	}
	if got == nil {
		t.Fatal("Client returned nil")
	}
	var _ API = got
}

// --- CacheKey -------------------------------------------------------------

func TestCacheKeyDeterministicAndChangesOnInputChange(t *testing.T) {
	a := CacheKey("spec.v1", "input-A")
	b := CacheKey("spec.v1", "input-A")
	c := CacheKey("spec.v1", "input-B")
	if a != b {
		t.Error("CacheKey not deterministic")
	}
	if a == c {
		t.Error("CacheKey collided across different inputs")
	}
	if len(a) != 64 {
		t.Errorf("CacheKey not a sha256 hex (length %d)", len(a))
	}
}
