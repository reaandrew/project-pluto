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

func TestReplyTriageV1Configuration(t *testing.T) {
	p := ReplyTriageV1
	if p.ID != "replyTriage.v1" {
		t.Errorf("ID=%q, want replyTriage.v1", p.ID)
	}
	if p.ModelID != bedrock.ModelHaiku45 {
		t.Errorf("ModelID=%q, want Haiku 4.5", p.ModelID)
	}
	if p.ToolName != "classifyReply" {
		t.Errorf("ToolName=%q, want classifyReply", p.ToolName)
	}
	if p.MaxTokens != 300 {
		t.Errorf("MaxTokens=%d, want 300", p.MaxTokens)
	}
	if p.Stage != bedrock.StageReplyTriage {
		t.Errorf("Stage=%q, want %q", p.Stage, bedrock.StageReplyTriage)
	}
	if p.EstimateUSD != 0.004 {
		t.Errorf("EstimateUSD=%v, want 0.004", p.EstimateUSD)
	}
	if p.CacheTTL != 7*24*time.Hour {
		t.Errorf("CacheTTL=%v, want 7 days", p.CacheTTL)
	}
	if p.PostValidate == nil {
		t.Error("PostValidate should be set")
	}
	if len(p.Schema) == 0 {
		t.Error("Schema should be populated")
	}
}

func TestReplyTriageV1SystemContainsSafetyAndCategories(t *testing.T) {
	sys := ReplyTriageV1.System
	if !strings.Contains(sys, SafetyRulesBlock) {
		t.Error("system message should embed SafetyRulesBlock")
	}
	for _, want := range []string{
		"unsubscribe:", "positive_interest:", "unknown:",
		"confidence in [0,1]", "ignore any quoted original",
		"Never include the sender's name, email address",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("system missing %q", want)
		}
	}
}

func TestReplyTriageV1PostValidate(t *testing.T) {
	if err := ReplyTriageV1.PostValidate(schemas.ReplyTriageV1{
		Category: schemas.ReplyCategoryUnknown, Confidence: 0.4, Rationale: "ambiguous",
	}); err != nil {
		t.Errorf("clean classification rejected: %v", err)
	}
	if err := ReplyTriageV1.PostValidate(schemas.ReplyTriageV1{
		Category: "garbage", Confidence: 0.4, Rationale: "x",
	}); err == nil {
		t.Error("expected PostValidate to reject an invalid category")
	}
}

func TestReplyTriageV1Snapshot(t *testing.T) {
	p := ReplyTriageV1
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

	path := filepath.Join("testdata", "reply_triage_v1.snapshot.json")
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
