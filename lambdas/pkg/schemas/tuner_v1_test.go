package schemas

import (
	"encoding/json"
	"testing"
)

func TestTunerStyleV1_SchemaRoundTripAndAdversarial(t *testing.T) {
	v := NewValidator()
	good := TunerStyleV1{
		AddDoPhrases: []string{"lead with the fixed fee"}, RemoveDoPhrases: []string{},
		AddDontPhrases: []string{}, RemoveDontPhrases: []string{},
		AddAntiPatterns: []string{"hero with no CTA"},
		Rationale:       "Operators removed the vague tagline in 4/5 specs this week.",
	}
	body, _ := json.Marshal(good)
	if err := v.Validate(TunerStyleV1SchemaRaw, body); err != nil {
		t.Errorf("realistic style delta rejected by schema: %v", err)
	}
	if err := ValidateTunerStyleV1(good); err != nil {
		t.Errorf("clean delta rejected: %v", err)
	}
	// Adversarial: invented business fact in an additive phrase.
	for _, bad := range []string{"award-winning since 2009", "5-star testimonial", "20% cheaper"} {
		if err := ValidateTunerStyleV1(TunerStyleV1{Rationale: "x", AddDoPhrases: []string{bad}}); err == nil {
			t.Errorf("must reject invented-fact phrase %q", bad)
		}
	}
	// The scan must also cover addDontPhrases + addAntiPatterns.
	if err := ValidateTunerStyleV1(TunerStyleV1{Rationale: "x", AddAntiPatterns: []string{"hero without the award badge"}}); err == nil {
		t.Error("must reject an invented-fact addAntiPattern")
	}
	if err := ValidateTunerStyleV1(TunerStyleV1{Rationale: "  "}); err == nil {
		t.Error("must reject empty rationale")
	}
}

func TestTunerEmailToneV1_SchemaRoundTripAndAdversarial(t *testing.T) {
	v := NewValidator()
	good := TunerEmailToneV1{
		AddSubjectPatterns:    []string{"Quick idea for {company}"},
		RemoveSubjectPatterns: []string{}, AddOpenerPatterns: []string{},
		RemoveOpenerPatterns: []string{}, AddProhibitedPhrases: []string{"circle back"},
		RemoveProhibitedPhrases: []string{},
		Rationale:               "Subjects naming the firm got 3 replies; generic ones got 0.",
	}
	body, _ := json.Marshal(good)
	if err := v.Validate(TunerEmailToneV1SchemaRaw, body); err != nil {
		t.Errorf("realistic email-tone delta rejected: %v", err)
	}
	if err := ValidateTunerEmailToneV1(good); err != nil {
		t.Errorf("clean delta rejected: %v", err)
	}
	if err := ValidateTunerEmailToneV1(TunerEmailToneV1{
		Rationale: "x", AddSubjectPatterns: []string{"trusted by 200 clients"},
	}); err == nil {
		t.Error("must reject invented-fact subject pattern")
	}
	// addProhibitedPhrases is additive too — must be scanned.
	if err := ValidateTunerEmailToneV1(TunerEmailToneV1{
		Rationale: "x", AddProhibitedPhrases: []string{"award-winning since 2010"},
	}); err == nil {
		t.Error("must reject invented-fact addProhibitedPhrase")
	}
	if err := ValidateTunerEmailToneV1(TunerEmailToneV1{Rationale: ""}); err == nil {
		t.Error("must reject empty rationale")
	}
}

func TestTunerTargetingV1_SchemaRoundTripAndAdversarial(t *testing.T) {
	v := NewValidator()
	good := TunerTargetingV1{
		AddIncludeKeywords: []string{"chartered"}, AddExcludeKeywords: []string{"recruitment"},
		WeightDeltas: TunerTargetingWeightDeltas{AuditScore: 0.05, VerticalFit: -0.1},
		Rationale:    "5 'not my audience' rejects were recruitment agencies.",
	}
	body, _ := json.Marshal(good)
	if err := v.Validate(TunerTargetingV1SchemaRaw, body); err != nil {
		t.Errorf("realistic targeting delta rejected: %v", err)
	}
	if err := ValidateTunerTargetingV1(good); err != nil {
		t.Errorf("clean delta rejected: %v", err)
	}
	if err := ValidateTunerTargetingV1(TunerTargetingV1{
		Rationale: "x", WeightDeltas: TunerTargetingWeightDeltas{AuditScore: 0.5},
	}); err == nil {
		t.Error("must reject weight delta above +0.2")
	}
	if err := ValidateTunerTargetingV1(TunerTargetingV1{
		Rationale: "x", WeightDeltas: TunerTargetingWeightDeltas{WebsiteAge: -0.5},
	}); err == nil {
		t.Error("must reject weight delta below -0.2")
	}
	if err := ValidateTunerTargetingV1(TunerTargetingV1{Rationale: ""}); err == nil {
		t.Error("must reject empty rationale")
	}
}
