package prompts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	bedrockruntime "github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/ddb"
)

// --- test helpers (Bedrock + DDB fakes) ----------------------------------

type fakeBedrockClient struct {
	body []byte
	err  error
}

func (f *fakeBedrockClient) InvokeModel(_ context.Context, _ *bedrockruntime.InvokeModelInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &bedrockruntime.InvokeModelOutput{Body: f.body}, nil
}

type fakeDDBClient struct{}

func (fakeDDBClient) PutItem(_ context.Context, _ *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	return &dynamodb.PutItemOutput{}, nil
}
func (fakeDDBClient) GetItem(_ context.Context, _ *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	return &dynamodb.GetItemOutput{}, nil
}
func (fakeDDBClient) UpdateItem(_ context.Context, _ *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	return &dynamodb.UpdateItemOutput{}, nil
}

// setupInvoke wires fakes for Bedrock + DDB for the duration of one test.
// Returns the bedrock fake so the test can preload its response body or
// error.
func setupInvoke(t *testing.T) (*fakeBedrockClient, fakeDDBClient) {
	t.Helper()
	t.Setenv("ITEMS_TABLE", "items-test")
	bedrockFake := &fakeBedrockClient{}
	ddbFake := fakeDDBClient{}
	bedrock.SetClient(bedrockFake)
	ddb.SetClient(ddbFake)
	t.Cleanup(func() {
		bedrock.SetClient(nil)
		ddb.SetClient(nil)
	})
	return bedrockFake, ddbFake
}

// makeBedrockResponse builds a synthetic Anthropic-on-Bedrock response body
// where the only tool_use content is the named tool with the given input.
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

// dummyOut is a synthetic schema-bearing struct used to exercise the
// framework. Real prompts use real schemas from pkg/schemas.
type dummyOut struct {
	Headline string `json:"headline" jsonschema:"required,maxLength=80"`
	Score    int    `json:"score"    jsonschema:"required,minimum=0,maximum=100"`
}

// validPrompt is a Prompt[dummyOut] used by most tests.
func validPrompt() Prompt[dummyOut] {
	return New(Prompt[dummyOut]{
		ID:          "dummy.v1",
		ModelID:     bedrock.ModelHaiku45,
		System:      "You output a headline and score.\n" + SafetyRulesBlock,
		ToolName:    "produceDummy",
		MaxTokens:   500,
		Stage:       bedrock.StageAudit,
		EstimateUSD: 0.012,
		CacheTTL:    7 * 24 * time.Hour,
	})
}

// --- New ------------------------------------------------------------------

func TestNewFillsSchema(t *testing.T) {
	p := validPrompt()
	if len(p.Schema) == 0 {
		t.Fatal("Schema not populated")
	}
	if !strings.Contains(string(p.Schema), `"headline"`) {
		t.Errorf("schema does not look like dummyOut's: %s", p.Schema)
	}
	if !strings.Contains(string(p.Schema), `"required"`) {
		t.Errorf("schema missing required[]: %s", p.Schema)
	}
}

func TestNewPanicsOnMissingFields(t *testing.T) {
	cases := map[string]Prompt[dummyOut]{
		"no ID":          {ModelID: bedrock.ModelHaiku45, ToolName: "x", MaxTokens: 1, Stage: bedrock.StageAudit, EstimateUSD: 0.01},
		"no ModelID":     {ID: "x", ToolName: "x", MaxTokens: 1, Stage: bedrock.StageAudit, EstimateUSD: 0.01},
		"no ToolName":    {ID: "x", ModelID: bedrock.ModelHaiku45, MaxTokens: 1, Stage: bedrock.StageAudit, EstimateUSD: 0.01},
		"no MaxTokens":   {ID: "x", ModelID: bedrock.ModelHaiku45, ToolName: "t", Stage: bedrock.StageAudit, EstimateUSD: 0.01},
		"no Stage":       {ID: "x", ModelID: bedrock.ModelHaiku45, ToolName: "t", MaxTokens: 1, EstimateUSD: 0.01},
		"no EstimateUSD": {ID: "x", ModelID: bedrock.ModelHaiku45, ToolName: "t", MaxTokens: 1, Stage: bedrock.StageAudit},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for %q", name)
				}
			}()
			_ = New(p)
		})
	}
}

// --- Apply ----------------------------------------------------------------

func TestApplyTranscribesPromptIntoInvokeInput(t *testing.T) {
	p := validPrompt()
	messages := []bedrock.Message{{Role: "user", Content: "hi"}}

	in := Apply(p, messages, 5.0, "cache-key-abc")

	if in.ModelID != p.ModelID || in.PromptID != p.ID || in.ToolName != p.ToolName {
		t.Errorf("identity fields drifted: %+v", in)
	}
	if in.MaxTokens != p.MaxTokens || in.CacheTTL != p.CacheTTL {
		t.Errorf("config drifted: %+v", in)
	}
	if in.Stage != p.Stage || in.EstimateUSD != p.EstimateUSD {
		t.Errorf("stage/estimate drifted: %+v", in)
	}
	if in.CapUSD != 5.0 || in.CacheKey != "cache-key-abc" {
		t.Errorf("apply-time params not threaded: cap=%v key=%q", in.CapUSD, in.CacheKey)
	}
	if string(in.ToolSchema) != string(p.Schema) {
		t.Errorf("schema not transcribed")
	}
	if len(in.Messages) != 1 || in.Messages[0].Content != "hi" {
		t.Errorf("messages drifted: %+v", in.Messages)
	}
	if !strings.Contains(in.System, SafetyRulesBlock) {
		t.Error("system message should embed SafetyRulesBlock")
	}
}

// --- HashInputs ------------------------------------------------------------

func TestHashInputsIsDeterministic(t *testing.T) {
	a := HashInputs("acme.test", "<html>...</html>")
	b := HashInputs("acme.test", "<html>...</html>")
	if a != b {
		t.Error("HashInputs not deterministic")
	}
	if len(a) != 64 {
		t.Errorf("not a hex sha256 (length %d)", len(a))
	}
}

func TestHashInputsSeparatorAvoidsConcatenationCollision(t *testing.T) {
	// Without a unit-separator, ("ab", "cd") and ("a", "bcd") would collide.
	a := HashInputs("ab", "cd")
	b := HashInputs("a", "bcd")
	if a == b {
		t.Error("HashInputs collided across different segmentations")
	}
}

func TestHashInputsEmptyAndSingle(t *testing.T) {
	if got := HashInputs(); len(got) != 64 {
		t.Errorf("HashInputs() = %q, expected 64-char hex", got)
	}
	a := HashInputs("only")
	b := HashInputs("only")
	if a != b {
		t.Error("single-arg HashInputs not deterministic")
	}
}

// --- WrapBlock -------------------------------------------------------------

func TestWrapBlock(t *testing.T) {
	got := WrapBlock("style_guide", "Tone: warm.")
	wanted := "<style_guide>\nTone: warm.\n</style_guide>"
	if got != wanted {
		t.Errorf("WrapBlock = %q, want %q", got, wanted)
	}
}

func TestWrapBlockReturnsEmptyForBlankText(t *testing.T) {
	for _, blank := range []string{"", "   ", "\n\n\t"} {
		if got := WrapBlock("style_guide", blank); got != "" {
			t.Errorf("WrapBlock(%q) = %q, want empty", blank, got)
		}
	}
}

// --- SafetyRulesBlock ------------------------------------------------------

func TestSafetyRulesBlockMentionsTheNonNegotiables(t *testing.T) {
	for _, want := range []string{
		"<safety_rules>",
		"</safety_rules>",
		"testimonials",
		"awards",
		`"access code"`,
		"private preview",
	} {
		if !strings.Contains(SafetyRulesBlock, want) {
			t.Errorf("SafetyRulesBlock missing %q", want)
		}
	}
}

// --- end-to-end: Apply produces something bedrock would accept ------------

func TestApplyResultIsAcceptableInputForBedrockBuildBody(t *testing.T) {
	// We don't actually call Bedrock here; we just confirm that the
	// InvokeInput produced by Apply has the fields InvokeStructured will
	// reject if missing (per its required-fields check).
	p := validPrompt()
	in := Apply(p, []bedrock.Message{{Role: "user", Content: "x"}}, 1.0, "key")

	if in.ModelID == "" || in.PromptID == "" || in.ToolName == "" || in.MaxTokens <= 0 {
		t.Errorf("Apply produced an InvokeInput InvokeStructured will reject: %+v", in)
	}
}

// --- Invoke ---------------------------------------------------------------

func TestInvokeRunsBedrockAndPostValidate(t *testing.T) {
	bedrockFake, _ := setupInvoke(t)
	bedrockFake.body = makeBedrockResponse(t, "produceDummy", dummyOut{Headline: "ok", Score: 50}, 100, 50)

	postCalls := 0
	p := New(Prompt[dummyOut]{
		ID:          "dummy.v1",
		ModelID:     bedrock.ModelHaiku45,
		System:      "system",
		ToolName:    "produceDummy",
		MaxTokens:   500,
		Stage:       bedrock.StageAudit,
		EstimateUSD: 0.012,
		PostValidate: func(d dummyOut) error {
			postCalls++
			if d.Headline == "" {
				return errors.New("blank headline")
			}
			return nil
		},
	})

	out, err := Invoke(context.Background(), p,
		[]bedrock.Message{{Role: "user", Content: "x"}}, 5.0, "k")
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if out.Headline != "ok" || out.Score != 50 {
		t.Errorf("output drift: %+v", out)
	}
	if postCalls != 1 {
		t.Errorf("PostValidate called %d times, want 1", postCalls)
	}
}

func TestInvokeSurfacesPostValidateError(t *testing.T) {
	bedrockFake, _ := setupInvoke(t)
	bedrockFake.body = makeBedrockResponse(t, "produceDummy", dummyOut{Headline: "fake-testimonial", Score: 50}, 100, 50)

	p := New(Prompt[dummyOut]{
		ID:          "dummy.v1",
		ModelID:     bedrock.ModelHaiku45,
		ToolName:    "produceDummy",
		MaxTokens:   500,
		Stage:       bedrock.StageAudit,
		EstimateUSD: 0.012,
		PostValidate: func(d dummyOut) error {
			if strings.Contains(d.Headline, "fake") {
				return errors.New("contains a banned word")
			}
			return nil
		},
	})
	_, err := Invoke(context.Background(), p,
		[]bedrock.Message{{Role: "user", Content: "x"}}, 5.0, "k")
	if err == nil {
		t.Fatal("expected post-validation error")
	}
	if !strings.Contains(err.Error(), "post-validation") || !strings.Contains(err.Error(), "dummy.v1") {
		t.Errorf("error should reference the prompt + post-validation: %v", err)
	}
}

func TestInvokeSkipsPostValidateWhenNil(t *testing.T) {
	bedrockFake, _ := setupInvoke(t)
	bedrockFake.body = makeBedrockResponse(t, "produceDummy", dummyOut{Headline: "ok", Score: 50}, 100, 50)

	p := New(Prompt[dummyOut]{
		ID:          "dummy.v1",
		ModelID:     bedrock.ModelHaiku45,
		ToolName:    "produceDummy",
		MaxTokens:   500,
		Stage:       bedrock.StageAudit,
		EstimateUSD: 0.012,
		// PostValidate intentionally nil.
	})
	if _, err := Invoke(context.Background(), p,
		[]bedrock.Message{{Role: "user", Content: "x"}}, 5.0, "k"); err != nil {
		t.Errorf("Invoke without PostValidate: %v", err)
	}
}

func TestInvokeSurfacesBedrockError(t *testing.T) {
	bedrockFake, _ := setupInvoke(t)
	bedrockFake.err = errors.New("bedrock down")

	p := validPrompt()
	_, err := Invoke(context.Background(), p,
		[]bedrock.Message{{Role: "user", Content: "x"}}, 5.0, "k")
	if err == nil {
		t.Fatal("expected bedrock error")
	}
}
