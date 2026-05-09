package prompts

import (
	"strings"
	"testing"
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
)

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
		"no ID":        {ModelID: bedrock.ModelHaiku45, ToolName: "x", MaxTokens: 1, Stage: bedrock.StageAudit},
		"no ModelID":   {ID: "x", ToolName: "x", MaxTokens: 1, Stage: bedrock.StageAudit},
		"no ToolName":  {ID: "x", ModelID: bedrock.ModelHaiku45, MaxTokens: 1, Stage: bedrock.StageAudit},
		"no MaxTokens": {ID: "x", ModelID: bedrock.ModelHaiku45, ToolName: "t", Stage: bedrock.StageAudit},
		"no Stage":     {ID: "x", ModelID: bedrock.ModelHaiku45, ToolName: "t", MaxTokens: 1},
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
