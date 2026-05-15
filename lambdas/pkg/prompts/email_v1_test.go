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

func TestEmailV1Configuration(t *testing.T) {
	p := EmailV1
	if p.ID != "email.v1" {
		t.Errorf("ID=%q, want email.v1", p.ID)
	}
	if p.ModelID != bedrock.ModelHaiku45 {
		t.Errorf("ModelID=%q, want Haiku 4.5", p.ModelID)
	}
	if p.ToolName != "produceEmailDraft" {
		t.Errorf("ToolName=%q, want produceEmailDraft", p.ToolName)
	}
	if p.MaxTokens != 600 {
		t.Errorf("MaxTokens=%d, want 600 (spec value)", p.MaxTokens)
	}
	if p.Stage != bedrock.StageEmail {
		t.Errorf("Stage=%q, want %q", p.Stage, bedrock.StageEmail)
	}
	if p.EstimateUSD != 0.005 {
		t.Errorf("EstimateUSD=%v, want 0.005 (spec value)", p.EstimateUSD)
	}
	if p.CacheTTL != 7*24*time.Hour {
		t.Errorf("CacheTTL=%v, want 7 days", p.CacheTTL)
	}
	if p.PostValidate == nil {
		t.Error("PostValidate should be set")
	}
	if len(p.Schema) == 0 {
		t.Error("Schema should be populated (reflected from schemas.EmailV1)")
	}
}

func TestEmailV1SystemContainsRequiredBlocks(t *testing.T) {
	s := EmailV1.System
	for _, want := range []string{
		"<safety_rules>",
		"{{PASSCODE}}",
		"private preview",
		"never imply it is published",
		"opt-out line",
		"do not call it a \"password\"",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("system message missing %q", want)
		}
	}
	if strings.Contains(s, "<email_tone>") {
		t.Error("system prefix must NOT embed <email_tone> — it's appended per-call by the Lambda")
	}
}

func TestEmailV1Snapshot(t *testing.T) {
	p := EmailV1
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

	path := filepath.Join("testdata", "email_v1.snapshot.json")
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

func validEmailForPrompt() schemas.EmailV1 {
	return schemas.EmailV1{
		Subject: "Quick redesign preview for Acme Accountants",
		Body: "Hi Jane,\n\nYour homepage buries the services list below the fold.\n\n" +
			"I mocked up a private preview: https://previews.example.com/sites/web-1\n" +
			"Use access code {{PASSCODE}} to open it.\n\n" +
			"Reply 'no thanks' and I won't follow up.\n\nAndrew",
		WordCount: 70,
	}
}

func TestEmailV1PostValidateAcceptsCleanDraft(t *testing.T) {
	if err := EmailV1.PostValidate(validEmailForPrompt()); err != nil {
		t.Errorf("clean draft rejected: %v", err)
	}
}

func TestEmailV1PostValidateAdversarial(t *testing.T) {
	cases := map[string]func(*schemas.EmailV1){
		"missing placeholder":   func(e *schemas.EmailV1) { e.Body = strings.ReplaceAll(e.Body, "{{PASSCODE}}", "ABCD1234") },
		"duplicate placeholder": func(e *schemas.EmailV1) { e.Body += " again {{PASSCODE}}" },
		"contains password":     func(e *schemas.EmailV1) { e.Body = strings.ReplaceAll(e.Body, "access code", "password") },
		"wordCount over 200":    func(e *schemas.EmailV1) { e.WordCount = 201 },
		"wordCount under 60":    func(e *schemas.EmailV1) { e.WordCount = 12 },
		"empty subject":         func(e *schemas.EmailV1) { e.Subject = "  " },
		"empty body":            func(e *schemas.EmailV1) { e.Body = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEmailForPrompt()
			mutate(&e)
			if err := EmailV1.PostValidate(e); err == nil {
				t.Errorf("expected PostValidate to reject (%s)", name)
			}
		})
	}
}
