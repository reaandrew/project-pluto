package components

import (
	"strings"
	"testing"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// --- Hero --------------------------------------------------------------

func TestRenderHero_BasicShape(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionHero, Headline: "Chartered accountants",
		Subheadline: "Fixed-fee. No hidden costs.",
		PrimaryCta:  &schemas.SpecCTA{Label: "Book a call", Action: "call"},
	}
	out, err := RenderSection(s, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSection: %v", err)
	}
	for _, want := range []string{
		`data-section="hero"`,
		`<h1`, `Chartered accountants`,
		`<p`, `Fixed-fee. No hidden costs.`,
		`<a`, `Book a call`, `data-action="call"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("hero missing %q\n%s", want, out)
		}
	}
}

func TestRenderHero_EscapesHTML(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionHero, Headline: `<script>alert(1)</script>`,
		Subheadline: "ok",
		PrimaryCta:  &schemas.SpecCTA{Label: "Call", Action: "call"},
	}
	out, _ := RenderSection(s, RenderOptions{})
	if strings.Contains(out, "<script>") {
		t.Errorf("HTML escaping failed: %s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected entity-encoded <script>: %s", out)
	}
}

func TestRenderHero_ImagePromptPlaceholder(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionHero, Headline: "x", Subheadline: "y",
		PrimaryCta:  &schemas.SpecCTA{Label: "Call", Action: "call"},
		ImagePrompt: "trades-van-in-front-of-house",
	}
	out, _ := RenderSection(s, RenderOptions{})
	if !strings.Contains(out, `data-image-prompt="trades-van-in-front-of-house"`) {
		t.Errorf("expected data-image-prompt placeholder when ImageURLFor is nil:\n%s", out)
	}
	if strings.Contains(out, "background-image:") {
		t.Errorf("placeholder path should not set a background-image: %s", out)
	}
}

func TestRenderHero_ImageURLForResolves(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionHero, Headline: "x", Subheadline: "y",
		PrimaryCta:  &schemas.SpecCTA{Label: "Call", Action: "call"},
		ImagePrompt: "trades-van",
	}
	opts := RenderOptions{
		ImageURLFor: func(prompt string) string {
			return "https://images.example.com/" + prompt + ".jpg"
		},
	}
	out, _ := RenderSection(s, opts)
	if !strings.Contains(out, `background-image:url('https://images.example.com/trades-van.jpg')`) {
		t.Errorf("expected resolved image URL: %s", out)
	}
}

func TestRenderHero_MissingCtaIsError(t *testing.T) {
	s := schemas.SpecSection{Type: schemas.SectionHero, Headline: "x", Subheadline: "y"}
	if _, err := RenderSection(s, RenderOptions{}); err == nil {
		t.Error("expected error for hero with no primaryCta")
	}
}

// --- Services ----------------------------------------------------------

func TestRenderServices_RendersItems(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionServices, Title: "What we do",
		Items: []schemas.SpecSubItem{
			{Name: "Self-assessment", OneLine: "Done on time, every time."},
			{Name: "Limited company accounts", OneLine: "Filed with HMRC."},
			{Name: "MTD for VAT", OneLine: "Quarterly returns."},
		},
	}
	out, err := RenderSection(s, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSection: %v", err)
	}
	for _, want := range []string{
		`data-section="services"`,
		`What we do`,
		`Self-assessment`, `Done on time, every time.`,
		`Limited company accounts`, `Filed with HMRC.`,
		`MTD for VAT`, `Quarterly returns.`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("services missing %q\n%s", want, out)
		}
	}
}

func TestRenderServices_EmptyItemsIsError(t *testing.T) {
	s := schemas.SpecSection{Type: schemas.SectionServices, Title: "x"}
	if _, err := RenderSection(s, RenderOptions{}); err == nil {
		t.Error("expected error for services with no items")
	}
}

// --- About -------------------------------------------------------------

func TestRenderAbout(t *testing.T) {
	s := schemas.SpecSection{Type: schemas.SectionAbout, Paragraph: "We've been at it for years."}
	out, err := RenderSection(s, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSection: %v", err)
	}
	if !strings.Contains(out, "We&#39;ve been at it for years.") &&
		!strings.Contains(out, "We've been at it for years.") {
		t.Errorf("about missing paragraph: %s", out)
	}
}

func TestRenderAbout_EmptyParagraphIsError(t *testing.T) {
	s := schemas.SpecSection{Type: schemas.SectionAbout, Paragraph: "   "}
	if _, err := RenderSection(s, RenderOptions{}); err == nil {
		t.Error("expected error for about with empty paragraph")
	}
}

// --- Trust -------------------------------------------------------------

func TestRenderTrust_DropsUnverifiedBadges(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionTrust,
		Badges: []schemas.SpecBadge{
			{Label: "ICAEW chartered"},
			{Label: "Fake Award 2024"},
			{Label: "Xero certified"},
		},
	}
	opts := RenderOptions{VerifiedBadges: map[string]bool{
		"ICAEW chartered": true,
		"Xero certified":  true,
	}}
	out, err := RenderSection(s, opts)
	if err != nil {
		t.Fatalf("RenderSection: %v", err)
	}
	if !strings.Contains(out, "ICAEW chartered") || !strings.Contains(out, "Xero certified") {
		t.Errorf("verified badges missing: %s", out)
	}
	if strings.Contains(out, "Fake Award 2024") {
		t.Errorf("unverified badge leaked: %s", out)
	}
}

func TestRenderTrust_AllBadgesDroppedReturnsEmpty(t *testing.T) {
	s := schemas.SpecSection{
		Type:   schemas.SectionTrust,
		Badges: []schemas.SpecBadge{{Label: "Fake Award"}},
	}
	out, err := RenderSection(s, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSection: %v", err)
	}
	if out != "" {
		t.Errorf("expected empty output when all badges dropped; got %q", out)
	}
}

func TestRenderTrust_NoVerifiedBadgesEqualsAllDropped(t *testing.T) {
	// Defensive: VerifiedBadges nil means "I don't have a verified
	// list" — drop everything rather than leak.
	s := schemas.SpecSection{
		Type:   schemas.SectionTrust,
		Badges: []schemas.SpecBadge{{Label: "ICAEW"}},
	}
	out, _ := RenderSection(s, RenderOptions{VerifiedBadges: nil})
	if out != "" {
		t.Errorf("expected empty output with nil VerifiedBadges; got %q", out)
	}
}

// --- FAQ ---------------------------------------------------------------

func TestRenderFAQ(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionFAQ,
		Items: []schemas.SpecSubItem{
			{Q: "What's MTD?", A: "Making Tax Digital — HMRC's quarterly digital filing rule."},
			{Q: "Fixed fee?", A: "Yes, no hidden costs."},
			{Q: "Where are you?", A: "Manchester city centre."},
		},
	}
	out, err := RenderSection(s, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSection: %v", err)
	}
	for _, want := range []string{`<dl`, `<dt`, `<dd`, "What&#39;s MTD?", "Making Tax Digital", "Fixed fee?"} {
		if !strings.Contains(out, want) {
			t.Errorf("faq missing %q\n%s", want, out)
		}
	}
}

// --- CTA ---------------------------------------------------------------

func TestRenderCTA(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionCTA, Headline: "Book today",
		Button: &schemas.SpecCTA{Label: "Get a quote", Action: "form"},
	}
	out, err := RenderSection(s, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSection: %v", err)
	}
	for _, want := range []string{`data-section="cta"`, `Book today`, `Get a quote`, `data-action="form"`} {
		if !strings.Contains(out, want) {
			t.Errorf("cta missing %q\n%s", want, out)
		}
	}
}

func TestRenderCTA_MissingButtonIsError(t *testing.T) {
	s := schemas.SpecSection{Type: schemas.SectionCTA, Headline: "x"}
	if _, err := RenderSection(s, RenderOptions{}); err == nil {
		t.Error("expected error for cta with no button")
	}
}

// --- Contact -----------------------------------------------------------

func TestRenderContact_OmitsAbsentFields(t *testing.T) {
	s := schemas.SpecSection{
		Type:  schemas.SectionContact,
		Phone: "0161 234 5678",
		Email: "hello@acme.co.uk",
	}
	out, err := RenderSection(s, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderSection: %v", err)
	}
	// html/template URL-escapes the phone for the tel: URI (correct behaviour;
	// spaces in tel: are valid but encoded as %20 by the browser anyway).
	if !strings.Contains(out, `href="tel:0161%20234%205678"`) {
		t.Errorf("tel: link missing: %s", out)
	}
	if !strings.Contains(out, `mailto:hello@acme.co.uk`) {
		t.Errorf("mailto: link missing: %s", out)
	}
	if strings.Contains(out, "s-contact__address") || strings.Contains(out, "s-contact__hours") {
		t.Errorf("address/hours should not render when fields are empty: %s", out)
	}
}

func TestRenderContact_RendersAllWhenPresent(t *testing.T) {
	s := schemas.SpecSection{
		Type: schemas.SectionContact, Phone: "0161 234 5678",
		Email: "hi@x.co", Address: "1 High St, Manchester", Hours: "Mon–Fri 9–17",
	}
	out, _ := RenderSection(s, RenderOptions{})
	if !strings.Contains(out, "1 High St, Manchester") || !strings.Contains(out, "Mon") {
		t.Errorf("contact section dropped address/hours: %s", out)
	}
}

// --- Footer ------------------------------------------------------------

func TestRenderFooter(t *testing.T) {
	out, err := RenderFooter(FooterInput{
		BusinessName: "Acme Plumbing", Year: 2026,
		OptOutLine: "Reply 'no thanks' and I won't follow up.",
	})
	if err != nil {
		t.Fatalf("RenderFooter: %v", err)
	}
	for _, want := range []string{`<footer`, `Acme Plumbing`, `2026`, "no thanks"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q\n%s", want, out)
		}
	}
}

func TestRenderFooter_OmitsOptOutWhenEmpty(t *testing.T) {
	out, _ := RenderFooter(FooterInput{BusinessName: "x", Year: 2026})
	if strings.Contains(out, "s-footer__opt-out") {
		t.Errorf("opt-out paragraph should not render when empty: %s", out)
	}
}

// --- unknown section type ---------------------------------------------

func TestRenderSection_UnknownTypeIsError(t *testing.T) {
	s := schemas.SpecSection{Type: "newsletter"}
	if _, err := RenderSection(s, RenderOptions{}); err == nil {
		t.Error("expected error for unknown section type")
	}
}

// --- end-to-end: every spec section type renders --------------------

// TestRenderSection_AllSectionTypes_RoundTrip iterates the closed
// AllowedSectionTypes set and confirms each renders without error
// when fed a sensible fixture. Acts as a smoke test for the
// generator (iter 5.2) which calls RenderSection for every section
// in a SpecV1.
func TestRenderSection_AllSectionTypes_RoundTrip(t *testing.T) {
	fixtures := map[string]schemas.SpecSection{
		schemas.SectionHero: {
			Type: schemas.SectionHero, Headline: "h", Subheadline: "s",
			PrimaryCta: &schemas.SpecCTA{Label: "L", Action: "call"},
		},
		schemas.SectionServices: {
			Type: schemas.SectionServices, Title: "T",
			Items: []schemas.SpecSubItem{
				{Name: "n1", OneLine: "o1"}, {Name: "n2", OneLine: "o2"}, {Name: "n3", OneLine: "o3"},
			},
		},
		schemas.SectionAbout: {Type: schemas.SectionAbout, Paragraph: "p"},
		schemas.SectionTrust: {
			Type:   schemas.SectionTrust,
			Badges: []schemas.SpecBadge{{Label: "B"}},
		},
		schemas.SectionFAQ: {
			Type: schemas.SectionFAQ,
			Items: []schemas.SpecSubItem{
				{Q: "q1", A: "a1"}, {Q: "q2", A: "a2"}, {Q: "q3", A: "a3"},
			},
		},
		schemas.SectionCTA: {
			Type: schemas.SectionCTA, Headline: "h",
			Button: &schemas.SpecCTA{Label: "L", Action: "form"},
		},
		schemas.SectionContact: {Type: schemas.SectionContact, Phone: "0161"},
	}
	opts := RenderOptions{VerifiedBadges: map[string]bool{"B": true}}
	for _, kind := range schemas.AllowedSectionTypes {
		t.Run(kind, func(t *testing.T) {
			s, ok := fixtures[kind]
			if !ok {
				t.Fatalf("missing fixture for section type %q", kind)
			}
			out, err := RenderSection(s, opts)
			if err != nil {
				t.Fatalf("RenderSection: %v", err)
			}
			if !strings.Contains(out, `data-section="`+kind+`"`) {
				t.Errorf("output missing data-section=%q\n%s", kind, out)
			}
		})
	}
}
