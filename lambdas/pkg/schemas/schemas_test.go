package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

// --- example shape used across tests --------------------------------------

type sampleBrand struct {
	Tone        string `json:"tone" jsonschema:"required,maxLength=200"`
	Positioning string `json:"positioning" jsonschema:"required"`
}

type sampleSpec struct {
	Headline string      `json:"headline" jsonschema:"required,maxLength=120"`
	Brand    sampleBrand `json:"brand" jsonschema:"required"`
	Notes    string      `json:"notes,omitempty"`
	Score    int         `json:"score" jsonschema:"required,minimum=0,maximum=100"`
}

// --- JSONSchemaFor --------------------------------------------------------

func TestJSONSchemaForReflectsRequiredAndConstraints(t *testing.T) {
	raw, err := JSONSchemaFor[sampleSpec]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	s := string(raw)
	for _, want := range []string{
		`"type":"object"`,
		`"properties"`,
		`"required"`,
		`"headline"`,
		`"brand"`,
		`"score"`,
		`"maxLength":120`,
		`"minimum":0`,
		`"maximum":100`,
		`"additionalProperties":false`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("schema missing %q\nfull schema: %s", want, s)
		}
	}
}

func TestJSONSchemaForOmitsTopLevelMetadata(t *testing.T) {
	raw, err := JSONSchemaFor[sampleSpec]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("schema not valid JSON: %v", err)
	}
	if _, ok := top["$schema"]; ok {
		t.Errorf("$schema should be stripped at root")
	}
	if _, ok := top["$id"]; ok {
		t.Errorf("$id should be stripped at root")
	}
}

func TestJSONSchemaForOmitsRequiredOnOptionalField(t *testing.T) {
	raw, err := JSONSchemaFor[sampleSpec]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	// "notes" lacks `jsonschema:"required"` AND has omitempty → MUST NOT
	// appear in the top-level "required" array.
	var doc struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal required: %v", err)
	}
	for _, r := range doc.Required {
		if r == "notes" {
			t.Errorf("'notes' should not be required (omitempty + no jsonschema:required tag)")
		}
	}
}

func TestMustJSONSchemaForReturnsSameAsNonMust(t *testing.T) {
	got := MustJSONSchemaFor[sampleSpec]()
	want, err := JSONSchemaFor[sampleSpec]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Must returned different schema than non-Must")
	}
}

// --- Validator: happy + adversarial paths --------------------------------

func TestValidatorAcceptsValidPayload(t *testing.T) {
	schema, _ := JSONSchemaFor[sampleSpec]()
	v := NewValidator()
	good := mustMarshal(t, sampleSpec{
		Headline: "Acme — fast accounting for trades",
		Brand:    sampleBrand{Tone: "warm", Positioning: "fixed-fee"},
		Score:    85,
	})
	if err := v.Validate(schema, good); err != nil {
		t.Errorf("expected valid payload, got %v", err)
	}
}

func TestValidatorRejectsMissingRequired(t *testing.T) {
	schema, _ := JSONSchemaFor[sampleSpec]()
	v := NewValidator()
	// Missing "brand" + "score"
	bad := json.RawMessage(`{"headline":"x"}`)
	err := v.Validate(schema, bad)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "brand") && !strings.Contains(err.Error(), "required") {
		t.Errorf("error should mention missing field: %v", err)
	}
}

func TestValidatorRejectsViolatingConstraints(t *testing.T) {
	schema, _ := JSONSchemaFor[sampleSpec]()
	v := NewValidator()
	cases := map[string]sampleSpec{
		"score above max": {
			Headline: "x",
			Brand:    sampleBrand{Tone: "t", Positioning: "p"},
			Score:    200,
		},
		"headline too long": {
			Headline: strings.Repeat("x", 200),
			Brand:    sampleBrand{Tone: "t", Positioning: "p"},
			Score:    50,
		},
	}
	for name, spec := range cases {
		t.Run(name, func(t *testing.T) {
			payload := mustMarshal(t, spec)
			if err := v.Validate(schema, payload); err == nil {
				t.Errorf("expected validation error for %q", name)
			}
		})
	}
}

func TestValidatorRejectsAdditionalProperties(t *testing.T) {
	schema, _ := JSONSchemaFor[sampleSpec]()
	v := NewValidator()
	// Adds an unknown "secretField" — strict schema should reject.
	bad := json.RawMessage(`{
		"headline":"x",
		"brand":{"tone":"t","positioning":"p"},
		"score":50,
		"secretField":"sneaky"
	}`)
	if err := v.Validate(schema, bad); err == nil {
		t.Error("expected rejection of additional property")
	}
}

func TestValidatorCachesCompiledSchemas(t *testing.T) {
	schema, _ := JSONSchemaFor[sampleSpec]()
	v := NewValidator()
	good := mustMarshal(t, sampleSpec{
		Headline: "x",
		Brand:    sampleBrand{Tone: "t", Positioning: "p"},
		Score:    50,
	})
	for i := 0; i < 5; i++ {
		if err := v.Validate(schema, good); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := len(v.cache); got != 1 {
		t.Errorf("cache size = %d after 5 same-schema calls, want 1", got)
	}
}

func TestValidatorRejectsEmptySchemaOrPayload(t *testing.T) {
	v := NewValidator()
	if err := v.Validate(nil, json.RawMessage(`{}`)); err == nil {
		t.Error("expected error for empty schema")
	}
	if err := v.Validate(json.RawMessage(`{"type":"object"}`), nil); err == nil {
		t.Error("expected error for empty payload")
	}
}

func TestValidatorRejectsMalformedSchema(t *testing.T) {
	v := NewValidator()
	if err := v.Validate(json.RawMessage(`not json`), json.RawMessage(`{}`)); err == nil {
		t.Error("expected schema-compile error")
	}
}

func TestValidatorRejectsMalformedPayload(t *testing.T) {
	v := NewValidator()
	if err := v.Validate(json.RawMessage(`{"type":"object"}`), json.RawMessage(`not json`)); err == nil {
		t.Error("expected payload-parse error")
	}
}

// --- round-trip: struct → schema → validates a payload of that struct ----

func TestRoundTripStructToSchemaToValidatedJSON(t *testing.T) {
	schema, err := JSONSchemaFor[sampleSpec]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	original := sampleSpec{
		Headline: "Acme",
		Brand:    sampleBrand{Tone: "warm", Positioning: "fixed-fee"},
		Notes:    "optional note",
		Score:    77,
	}
	payload := mustMarshal(t, original)

	v := NewValidator()
	if err := v.Validate(schema, payload); err != nil {
		t.Fatalf("validate: %v", err)
	}

	var roundTripped sampleSpec
	if err := json.Unmarshal(payload, &roundTripped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if roundTripped != original {
		t.Errorf("drift: %+v vs %+v", roundTripped, original)
	}
}

// --- SchemaContains -------------------------------------------------------

func TestSchemaContains(t *testing.T) {
	schema, _ := JSONSchemaFor[sampleSpec]()
	if !SchemaContains(schema, "headline") {
		t.Error("expected headline to be present")
	}
	if SchemaContains(schema, "definitelyNotInSchema") {
		t.Error("substring match returned true unexpectedly")
	}
}

// --- helpers --------------------------------------------------------------

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
