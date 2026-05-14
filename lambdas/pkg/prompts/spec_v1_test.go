package prompts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

func TestSpecV1Configuration(t *testing.T) {
	p := SpecV1
	if p.ID != "spec.v1" {
		t.Errorf("ID=%q, want spec.v1", p.ID)
	}
	if p.ModelID != bedrock.ModelSonnet46 {
		t.Errorf("ModelID=%q, want Sonnet 4.6", p.ModelID)
	}
	if p.ToolName != "produceSpec" {
		t.Errorf("ToolName=%q, want produceSpec", p.ToolName)
	}
	if p.MaxTokens != 3000 {
		t.Errorf("MaxTokens=%d, want 3000 (spec value)", p.MaxTokens)
	}
	if p.Stage != bedrock.StageSpec {
		t.Errorf("Stage=%q, want %q", p.Stage, bedrock.StageSpec)
	}
	if p.EstimateUSD != 0.075 {
		t.Errorf("EstimateUSD=%v, want 0.075 (spec value)", p.EstimateUSD)
	}
	if p.CacheTTL != 90*24*time.Hour {
		t.Errorf("CacheTTL=%v, want 90 days", p.CacheTTL)
	}
	if p.PostValidate == nil {
		t.Error("PostValidate should be set")
	}
	if len(p.Schema) == 0 {
		t.Error("Schema should be populated")
	}
	// Pre-supplied schema wins over reflection.
	if string(p.Schema) != string(schemas.SpecV1SchemaRaw) {
		t.Errorf("Schema does not match SpecV1SchemaRaw — New() unexpectedly overwrote pre-supplied schema")
	}
}

func TestSpecV1SystemContainsSafetyRulesAndDesignerVoice(t *testing.T) {
	sys := SpecV1.System
	if !strings.Contains(sys, SafetyRulesBlock) {
		t.Error("system message should embed SafetyRulesBlock")
	}
	for _, want := range []string{
		"UK small-business website designer",
		"strictly from the provided business data",
		"Do not invent facts",
		"do not invent components",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system missing %q", want)
		}
	}
}

func TestSpecV1PostValidateRejectsTestimonialSection(t *testing.T) {
	s := validSpecForPrompt()
	s.Page.Sections[0] = schemas.SpecSection{
		Type: schemas.SectionAbout, Paragraph: "Customer testimonial: 'Great service.'",
	}
	if err := SpecV1.PostValidate(s); err == nil {
		t.Error("expected PostValidate to reject testimonial-shaped section")
	}
}

func TestSpecV1PostValidateAcceptsCleanSpec(t *testing.T) {
	if err := SpecV1.PostValidate(validSpecForPrompt()); err != nil {
		t.Errorf("clean spec rejected: %v", err)
	}
}

// TestSpecV1Snapshot pins the assembled tool-use payload (system +
// model + tool name + max_tokens + stage + estimate + cache TTL + the
// full schema) to testdata so a prompt change must update the golden
// file deliberately.
//
// Required by 07-bedrock-prompts.md § Snapshot tests.
func TestSpecV1Snapshot(t *testing.T) {
	p := SpecV1
	var schemaPretty any
	if err := json.Unmarshal(p.Schema, &schemaPretty); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	snap := map[string]any{
		"id":          p.ID,
		"modelId":     p.ModelID,
		"toolName":    p.ToolName,
		"maxTokens":   p.MaxTokens,
		"stage":       string(p.Stage),
		"estimateUsd": p.EstimateUSD,
		"cacheTTL":    p.CacheTTL.String(),
		"system":      p.System,
		"schema":      schemaPretty,
	}
	got, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	got = append(got, '\n')

	path := filepath.Join("testdata", "spec_v1.snapshot.json")
	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
		t.Logf("wrote %s", path)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot (set UPDATE_SNAPSHOTS=1 to create): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("snapshot drift — re-run with UPDATE_SNAPSHOTS=1 if intentional.\n--- got\n%s\n--- want\n%s", got, want)
	}
}

// validSpecForPrompt mirrors the schemas package's validSpecV1 fixture
// (duplicated rather than imported because that lives in the
// schemas _test.go file, not exported test-helpers).
func validSpecForPrompt() schemas.SpecV1 {
	return schemas.SpecV1{
		Brand: schemas.SpecBrand{
			Tone: "professional, plain-English",
			Palette: schemas.SpecPalette{
				Primary: "#0F4C81", NeutralDark: "#0F172A", NeutralLight: "#F1F5F9",
			},
			Positioning: "Fixed-fee chartered accountants.",
		},
		Page: schemas.SpecPage{
			Sections: []schemas.SpecSection{
				{Type: schemas.SectionHero, Headline: "Hello", Subheadline: "World", PrimaryCta: &schemas.SpecCTA{Label: "Call", Action: "call"}},
				{Type: schemas.SectionServices, Title: "Services", Items: []schemas.SpecSubItem{{Name: "x", OneLine: "y"}, {Name: "a", OneLine: "b"}, {Name: "c", OneLine: "d"}}},
				{Type: schemas.SectionAbout, Paragraph: "We've been at it for years."},
				{Type: schemas.SectionContact, Phone: "0161 234 5678"},
			},
		},
		SEO: schemas.SpecSEO{Title: "Acme", Description: "Acme Accountants."},
		Constraints: schemas.SpecConstraints{
			DoNotInventTestimonials: true, DoNotInventAwards: true, DoNotInventPrices: true,
		},
	}
}
