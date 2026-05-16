package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

func validReplyTriageV1() ReplyTriageV1 {
	return ReplyTriageV1{
		Category:   ReplyCategoryUnsubscribe,
		Confidence: 0.92,
		Rationale:  "Sender wrote 'please remove me, not interested'.",
	}
}

func TestReplyTriageV1Schema_ValidAndRoundTrips(t *testing.T) {
	v := NewValidator()
	raw := MustJSONSchemaFor[ReplyTriageV1]()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("reflected schema is not valid JSON: %v", err)
	}
	body, err := json.Marshal(validReplyTriageV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := v.Validate(raw, body); err != nil {
		t.Errorf("realistic ReplyTriageV1 rejected by reflected schema: %v", err)
	}
	var rt ReplyTriageV1
	if err := json.Unmarshal(body, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rt != validReplyTriageV1() {
		t.Errorf("round-trip drift: %+v", rt)
	}
	if err := ValidateReplyTriageV1(validReplyTriageV1()); err != nil {
		t.Errorf("valid fixture rejected by validator: %v", err)
	}
}

func TestReplyTriageV1Schema_RejectsMissingAndOversize(t *testing.T) {
	v := NewValidator()
	raw := MustJSONSchemaFor[ReplyTriageV1]()

	// Missing required field (no category).
	b, _ := json.Marshal(map[string]any{"confidence": 0.5, "rationale": "x"})
	if err := v.Validate(raw, b); err == nil {
		t.Error("expected schema rejection for missing category")
	}
	// Rationale over 200 chars.
	b, _ = json.Marshal(map[string]any{
		"category": "unknown", "confidence": 0.5, "rationale": strings.Repeat("a", 201),
	})
	if err := v.Validate(raw, b); err == nil {
		t.Error("expected schema rejection for oversize rationale")
	}
}

func TestValidateReplyTriageV1_AdversarialBranches(t *testing.T) {
	cases := []struct {
		name string
		in   ReplyTriageV1
	}{
		{"bad category", ReplyTriageV1{Category: "spam", Confidence: 0.5, Rationale: "x"}},
		{"confidence > 1", ReplyTriageV1{Category: ReplyCategoryUnknown, Confidence: 1.5, Rationale: "x"}},
		{"confidence < 0", ReplyTriageV1{Category: ReplyCategoryUnknown, Confidence: -0.1, Rationale: "x"}},
		{"empty rationale", ReplyTriageV1{Category: ReplyCategoryUnknown, Confidence: 0.5, Rationale: "  "}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := ValidateReplyTriageV1(c.in); err == nil {
				t.Errorf("%s: expected validation error", c.name)
			}
		})
	}
	// All three valid categories pass.
	for _, cat := range []string{ReplyCategoryUnsubscribe, ReplyCategoryPositiveInterest, ReplyCategoryUnknown} {
		if err := ValidateReplyTriageV1(ReplyTriageV1{Category: cat, Confidence: 0.5, Rationale: "ok"}); err != nil {
			t.Errorf("category %q should be valid: %v", cat, err)
		}
	}
}
