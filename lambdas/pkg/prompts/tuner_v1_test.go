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

func TestTunerStyleV1Configuration(t *testing.T) {
	p := TunerStyleV1
	if p.ID != "tuner.style.v1" || p.ModelID != bedrock.ModelSonnet46 ||
		p.ToolName != "proposeStyleDelta" || p.Stage != bedrock.StageTunerStyle ||
		p.EstimateUSD != 0.05 || p.MaxTokens != 1200 || p.PostValidate == nil {
		t.Errorf("config drift: %+v", p)
	}
	if string(p.Schema) != string(schemas.TunerStyleV1SchemaRaw) {
		t.Error("Schema must be the hand-written verbatim spec schema")
	}
	if !strings.Contains(p.System, "Stylistic only") || !strings.Contains(p.System, SafetyRulesBlock) {
		t.Error("system prose drift")
	}
}

func TestTunerEmailToneV1Configuration(t *testing.T) {
	p := TunerEmailToneV1
	if p.ID != "tuner.email-tone.v1" || p.ModelID != bedrock.ModelHaiku45 ||
		p.ToolName != "proposeEmailToneDelta" || p.Stage != bedrock.StageTunerEmailTone ||
		p.MaxTokens != 600 || p.PostValidate == nil {
		t.Errorf("config drift: %+v", p)
	}
	if string(p.Schema) != string(schemas.TunerEmailToneV1SchemaRaw) {
		t.Error("Schema drift")
	}
}

func TestTunerTargetingV1Configuration(t *testing.T) {
	p := TunerTargetingV1
	if p.ID != "tuner.targeting.v1" || p.ModelID != bedrock.ModelHaiku45 ||
		p.ToolName != "proposeTargetingDelta" || p.Stage != bedrock.StageTunerTargeting ||
		p.MaxTokens != 400 || p.PostValidate == nil {
		t.Errorf("config drift: %+v", p)
	}
	if string(p.Schema) != string(schemas.TunerTargetingV1SchemaRaw) {
		t.Error("Schema drift")
	}
}

func TestTunerV1PostValidateWiring(t *testing.T) {
	if err := TunerStyleV1.PostValidate(schemas.TunerStyleV1{Rationale: "ok"}); err != nil {
		t.Errorf("clean style delta rejected: %v", err)
	}
	if err := TunerStyleV1.PostValidate(schemas.TunerStyleV1{
		Rationale: "x", AddDoPhrases: []string{"award-winning team"},
	}); err == nil {
		t.Error("style PostValidate must reject an invented-fact phrase")
	}
	if err := TunerEmailToneV1.PostValidate(schemas.TunerEmailToneV1{Rationale: ""}); err == nil {
		t.Error("email-tone PostValidate must reject empty rationale")
	}
	if err := TunerTargetingV1.PostValidate(schemas.TunerTargetingV1{
		Rationale: "x", WeightDeltas: schemas.TunerTargetingWeightDeltas{AuditScore: 0.9},
	}); err == nil {
		t.Error("targeting PostValidate must reject an out-of-range weight")
	}
}

func snapOf(id, model, tool, system string, maxTok int, stage string, est float64, ttl time.Duration, schema json.RawMessage) []byte {
	var sp any
	_ = json.Unmarshal(schema, &sp)
	b, _ := json.MarshalIndent(map[string]any{
		"id": id, "modelId": model, "toolName": tool, "maxTokens": maxTok,
		"stage": stage, "estimateUsd": est, "cacheTTL": ttl.String(),
		"system": system, "schema": sp,
	}, "", "  ")
	return append(b, '\n')
}

func checkSnap(t *testing.T, file string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", file)
	if os.Getenv("UPDATE_SNAPSHOTS") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write snapshot: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read snapshot (set UPDATE_SNAPSHOTS=1): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("snapshot drift for %s — re-run with UPDATE_SNAPSHOTS=1 if intentional", file)
	}
}

func TestTunerStyleV1Snapshot(t *testing.T) {
	p := TunerStyleV1
	checkSnap(t, "tuner_style_v1.snapshot.json",
		snapOf(p.ID, p.ModelID, p.ToolName, p.System, p.MaxTokens, string(p.Stage), p.EstimateUSD, p.CacheTTL, p.Schema))
}

func TestTunerEmailToneV1Snapshot(t *testing.T) {
	p := TunerEmailToneV1
	checkSnap(t, "tuner_email_tone_v1.snapshot.json",
		snapOf(p.ID, p.ModelID, p.ToolName, p.System, p.MaxTokens, string(p.Stage), p.EstimateUSD, p.CacheTTL, p.Schema))
}

func TestTunerTargetingV1Snapshot(t *testing.T) {
	p := TunerTargetingV1
	checkSnap(t, "tuner_targeting_v1.snapshot.json",
		snapOf(p.ID, p.ModelID, p.ToolName, p.System, p.MaxTokens, string(p.Stage), p.EstimateUSD, p.CacheTTL, p.Schema))
}
