package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

func validSpecV1() SpecV1 {
	return SpecV1{
		Brand: SpecBrand{
			Tone: "professional, plain-English",
			Palette: SpecPalette{
				Primary: "#0F4C81", NeutralDark: "#0F172A", NeutralLight: "#F1F5F9",
			},
			Positioning: "Fixed-fee chartered accountants in Manchester.",
		},
		Page: SpecPage{
			Sections: []SpecSection{
				{
					Type: SectionHero, Headline: "Chartered accountants in Manchester",
					Subheadline: "Fixed-fee. No hidden costs.",
					PrimaryCta:  &SpecCTA{Label: "Book a call", Action: "call"},
				},
				{
					Type:  SectionServices,
					Title: "What we do",
					Items: []SpecSubItem{
						{Name: "Self-assessment", OneLine: "Done on time, every time."},
						{Name: "Limited company accounts", OneLine: "Filed with HMRC + Companies House."},
						{Name: "MTD for VAT", OneLine: "Quarterly returns via Xero or FreeAgent."},
					},
				},
				{
					Type:      SectionAbout,
					Paragraph: "Acme has filed for trades businesses in Greater Manchester since 2014.",
				},
				{
					Type: SectionContact, Phone: "0161 234 5678", Email: "hello@acme.co.uk",
				},
			},
		},
		SEO: SpecSEO{
			Title:       "Acme Accountants — Manchester",
			Description: "Fixed-fee chartered accountants in Manchester. MTD for VAT, self-assessment, limited company.",
			Keywords:    []string{"accountants manchester", "mtd vat"},
		},
		Constraints: SpecConstraints{
			DoNotInventTestimonials: true,
			DoNotInventAwards:       true,
			DoNotInventPrices:       true,
		},
	}
}

// --- schema-shape sanity --------------------------------------------------

func TestSpecV1SchemaRaw_IsValidJSON(t *testing.T) {
	var doc any
	if err := json.Unmarshal(SpecV1SchemaRaw, &doc); err != nil {
		t.Fatalf("SpecV1SchemaRaw is not valid JSON: %v", err)
	}
}

func TestSpecV1SchemaRaw_MirrorsSpec(t *testing.T) {
	s := string(SpecV1SchemaRaw)
	for _, want := range []string{
		`"required":["brand","page","seo","constraints"]`,
		`"$defs"`,
		`"const":"hero"`,
		`"const":"services"`,
		`"const":"about"`,
		`"const":"trust"`,
		`"const":"faq"`,
		`"const":"cta"`,
		`"const":"contact"`,
		`"pattern":"^#[0-9a-fA-F]{6}$"`,
		`"maxLength":60`,
		`"maxLength":160`,
		`"minItems":4`,
		`"maxItems":8`,
		`"doNotInventTestimonials"`,
		`"const":true`,
	} {
		// Strip whitespace from both ends to make literal matches tolerant
		// of the formatter's indentation.
		flat := strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "\n", "")
		if !strings.Contains(flat, strings.ReplaceAll(want, " ", "")) {
			t.Errorf("SpecV1SchemaRaw missing %q", want)
		}
	}
}

// --- validator round-trip -------------------------------------------------

func TestSpecV1Validator_AcceptsRealisticPayload(t *testing.T) {
	v := NewValidator()
	body, err := json.Marshal(validSpecV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := v.Validate(SpecV1SchemaRaw, body); err != nil {
		t.Errorf("realistic SpecV1 rejected: %v", err)
	}
}

func TestSpecV1Validator_RejectsMissingTopLevelField(t *testing.T) {
	v := NewValidator()
	s := validSpecV1()
	body, _ := json.Marshal(s)
	// Strip "constraints" — required.
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "constraints")
	body, _ = json.Marshal(m)
	if err := v.Validate(SpecV1SchemaRaw, body); err == nil {
		t.Error("expected validation error for missing 'constraints'")
	}
}

func TestSpecV1Validator_RejectsBadPalettePattern(t *testing.T) {
	v := NewValidator()
	s := validSpecV1()
	s.Brand.Palette.Primary = "blue" // not a 6-hex string
	body, _ := json.Marshal(s)
	if err := v.Validate(SpecV1SchemaRaw, body); err == nil {
		t.Error("expected pattern violation on palette.primary")
	}
}

func TestSpecV1Validator_RejectsTooFewSections(t *testing.T) {
	v := NewValidator()
	s := validSpecV1()
	s.Page.Sections = s.Page.Sections[:3] // < minItems 4
	body, _ := json.Marshal(s)
	if err := v.Validate(SpecV1SchemaRaw, body); err == nil {
		t.Error("expected minItems violation")
	}
}

func TestSpecV1Validator_RejectsUnknownSectionType(t *testing.T) {
	v := NewValidator()
	s := validSpecV1()
	s.Page.Sections[0].Type = "testimonials"
	body, _ := json.Marshal(s)
	if err := v.Validate(SpecV1SchemaRaw, body); err == nil {
		t.Error("expected oneOf violation on unknown section type")
	}
}

func TestSpecV1Validator_RejectsConstraintsFlagFalse(t *testing.T) {
	v := NewValidator()
	s := validSpecV1()
	s.Constraints.DoNotInventTestimonials = false
	body, _ := json.Marshal(s)
	if err := v.Validate(SpecV1SchemaRaw, body); err == nil {
		t.Error("expected const:true violation on doNotInventTestimonials")
	}
}

// --- ValidateSpecV1Structural --------------------------------------------

func TestValidateSpecV1Structural_AcceptsValidSpec(t *testing.T) {
	if err := ValidateSpecV1Structural(validSpecV1()); err != nil {
		t.Errorf("valid spec rejected by structural validator: %v", err)
	}
}

func TestValidateSpecV1Structural_RejectsConstraintsFlagFalse(t *testing.T) {
	s := validSpecV1()
	s.Constraints.DoNotInventAwards = false
	if err := ValidateSpecV1Structural(s); err == nil {
		t.Error("expected error on doNotInventAwards=false")
	}
}

func TestValidateSpecV1Structural_RejectsTooFewSections(t *testing.T) {
	s := validSpecV1()
	s.Page.Sections = s.Page.Sections[:3]
	if err := ValidateSpecV1Structural(s); err == nil {
		t.Error("expected error on count<4")
	}
}

func TestValidateSpecV1Structural_RejectsTestimonialShapedSection(t *testing.T) {
	cases := []SpecSection{
		{Type: SectionAbout, Paragraph: "Our customer testimonials say…"},
		{Type: SectionHero, Headline: "Testimonial from John S."},
		{Type: SectionAbout, Paragraph: "Here's a review from a happy customer."},
	}
	for i, bad := range cases {
		s := validSpecV1()
		s.Page.Sections[0] = bad
		// Pad to 4 sections.
		for len(s.Page.Sections) < 4 {
			s.Page.Sections = append(s.Page.Sections, SpecSection{Type: SectionAbout, Paragraph: "ok"})
		}
		if err := ValidateSpecV1Structural(s); err == nil {
			t.Errorf("case %d: expected testimonial-shape rejection", i)
		}
	}
}

func TestValidateSpecV1Structural_RejectsPasswordWord(t *testing.T) {
	s := validSpecV1()
	s.Page.Sections[0].Headline = "Forgot your password?"
	if err := ValidateSpecV1Structural(s); err == nil {
		t.Error("expected error on 'password' in user-facing copy")
	}
}

func TestValidateSpecV1Structural_RejectsUnknownSectionType(t *testing.T) {
	s := validSpecV1()
	s.Page.Sections[0].Type = "newsletter"
	if err := ValidateSpecV1Structural(s); err == nil {
		t.Error("expected error on unknown section type")
	}
}

func TestAllowedSectionTypes_FullSet(t *testing.T) {
	wantContains := []string{
		SectionHero, SectionServices, SectionAbout, SectionTrust,
		SectionFAQ, SectionCTA, SectionContact,
	}
	if len(AllowedSectionTypes) != 7 {
		t.Errorf("expected 7 section types, got %d", len(AllowedSectionTypes))
	}
	for _, want := range wantContains {
		if !isAllowedSectionType(want) {
			t.Errorf("missing section type %q", want)
		}
	}
}

// --- end-to-end: generated payload validates ----------------------------

func TestSpecV1RoundTrip(t *testing.T) {
	v := NewValidator()
	body, err := json.Marshal(validSpecV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := v.Validate(SpecV1SchemaRaw, body); err != nil {
		t.Fatalf("validate: %v", err)
	}
	var roundTripped SpecV1
	if err := json.Unmarshal(body, &roundTripped); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(roundTripped.Page.Sections) != len(validSpecV1().Page.Sections) {
		t.Errorf("section count drift on round-trip")
	}
	if roundTripped.Page.Sections[0].Type != SectionHero {
		t.Errorf("first section type drift")
	}
	if roundTripped.Page.Sections[0].PrimaryCta == nil ||
		roundTripped.Page.Sections[0].PrimaryCta.Label == "" {
		t.Errorf("primaryCta lost on round-trip: %+v", roundTripped.Page.Sections[0])
	}
}
