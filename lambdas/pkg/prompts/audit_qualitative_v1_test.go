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

func TestAuditQualitativeV1Configuration(t *testing.T) {
	p := AuditQualitativeV1
	if p.ID != "audit.qualitative.v1" {
		t.Errorf("ID=%q, want audit.qualitative.v1", p.ID)
	}
	if p.ModelID != bedrock.ModelHaiku45 {
		t.Errorf("ModelID=%q, want Haiku 4.5 (%q)", p.ModelID, bedrock.ModelHaiku45)
	}
	if p.ToolName != "recordAudit" {
		t.Errorf("ToolName=%q, want recordAudit", p.ToolName)
	}
	if p.MaxTokens != 800 {
		t.Errorf("MaxTokens=%d, want 800 (spec value)", p.MaxTokens)
	}
	if p.Stage != bedrock.StageAudit {
		t.Errorf("Stage=%q, want %q", p.Stage, bedrock.StageAudit)
	}
	if p.EstimateUSD != 0.012 {
		t.Errorf("EstimateUSD=%v, want 0.012 (spec value)", p.EstimateUSD)
	}
	if p.CacheTTL != 30*24*time.Hour {
		t.Errorf("CacheTTL=%v, want 30 days", p.CacheTTL)
	}
	if len(p.Schema) == 0 {
		t.Error("Schema not populated")
	}
	if p.PostValidate == nil {
		t.Error("PostValidate should be set")
	}
}

func TestAuditQualitativeV1SystemEmbedsSafetyAndReviewerVoice(t *testing.T) {
	sys := AuditQualitativeV1.System
	if !strings.Contains(sys, SafetyRulesBlock) {
		t.Error("system message should embed SafetyRulesBlock")
	}
	for _, want := range []string{
		"senior conversion-design reviewer",
		"small business websites",
		"Be concrete and concise",
		"Never invent business facts",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system message missing %q", want)
		}
	}
}

func TestAuditQualitativeV1SchemaContainsKeyFields(t *testing.T) {
	s := string(AuditQualitativeV1.Schema)
	for _, want := range []string{`"score"`, `"worthRedesigning"`, `"summary"`, `"issues"`, `"recordAudit"`} {
		// "recordAudit" is the tool name, not in the schema body — only check the field set here.
		if want == `"recordAudit"` {
			continue
		}
		if !strings.Contains(s, want) {
			t.Errorf("schema missing %q\n%s", want, s)
		}
	}
}

func TestAuditQualitativeV1PostValidateAcceptsCleanOutput(t *testing.T) {
	good := schemas.AuditV1{
		Score:            55,
		WorthRedesigning: true,
		Summary:          "Mobile layout breaks; CTAs are unclear.",
		Issues: []schemas.AuditIssue{
			{Type: "mobile", Severity: "high", Description: "Viewport meta missing."},
			{Type: "conversion", Severity: "medium", Description: "Phone number buried below the fold."},
		},
	}
	if err := AuditQualitativeV1.PostValidate(good); err != nil {
		t.Errorf("unexpected error on clean payload: %v", err)
	}
}

// TestAuditQualitativeV1Snapshot asserts the assembled tool-use payload
// (system + tool name + tool schema + model + max_tokens + stage +
// estimate + cache TTL) is byte-stable for the prompt's current
// definition. Required by 07-bedrock-prompts.md § Snapshot tests: "a
// prompt change must update the snapshot deliberately."
//
// Golden file lives under testdata/audit_qualitative_v1.snapshot.json.
// Set UPDATE_SNAPSHOTS=1 to regenerate after an intentional prompt
// change.
func TestAuditQualitativeV1Snapshot(t *testing.T) {
	p := AuditQualitativeV1
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

	path := filepath.Join("testdata", "audit_qualitative_v1.snapshot.json")
	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
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
		t.Errorf("snapshot drift — re-run with UPDATE_SNAPSHOTS=1 if the change is intentional.\n"+
			"--- got\n%s\n--- want\n%s", got, want)
	}
}

func TestAuditQualitativeV1PostValidateRejectsBannedWord(t *testing.T) {
	cases := map[string]schemas.AuditV1{
		"banned word in summary": {
			Summary: "Site has a password input that leaks.",
			Issues:  []schemas.AuditIssue{{Description: "Mobile layout"}},
		},
		"banned word in issue description": {
			Summary: "Site is broken.",
			Issues: []schemas.AuditIssue{
				{Description: "User must enter their password without https."},
			},
		},
		"banned word capitalised": {
			Summary: "Password form is plain text.",
			Issues:  []schemas.AuditIssue{{Description: "Mobile layout"}},
		},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			if err := AuditQualitativeV1.PostValidate(a); err == nil {
				t.Errorf("expected post-validation error for %q", name)
			}
		})
	}
}

// Audit output describes the site being reviewed. If the site has
// testimonials, the audit may describe them — that's correct behaviour,
// not a safety violation. The "no fake testimonials" rule applies to
// generative prompts (spec.v1, email.v1), not to critique prompts.
func TestAuditQualitativeV1PostValidateAcceptsDescribingTestimonials(t *testing.T) {
	a := schemas.AuditV1{
		Score:            45,
		WorthRedesigning: true,
		Summary:          "The site features two customer testimonials but they lack attribution photos, which weakens trust.",
		Issues: []schemas.AuditIssue{
			{Type: "trust", Severity: "medium", Description: "Testimonials without named attribution feel generic."},
		},
	}
	if err := AuditQualitativeV1.PostValidate(a); err != nil {
		t.Errorf("audit critiquing testimonials on the audited site should pass: %v", err)
	}
}

func TestAuditQualitativeV1PostValidateRejectsEmptyFields(t *testing.T) {
	cases := map[string]schemas.AuditV1{
		"empty summary": {
			Summary: "   ",
			Issues:  []schemas.AuditIssue{{Description: "ok"}},
		},
		"empty issue description": {
			Summary: "ok",
			Issues:  []schemas.AuditIssue{{Description: ""}},
		},
	}
	for name, a := range cases {
		t.Run(name, func(t *testing.T) {
			if err := AuditQualitativeV1.PostValidate(a); err == nil {
				t.Errorf("expected post-validation error for %q", name)
			}
		})
	}
}
