package sitebundle

import (
	"strings"
	"testing"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

func validSpec() schemas.SpecV1 {
	return schemas.SpecV1{
		Brand: schemas.SpecBrand{
			Tone: "professional, plain-English",
			Palette: schemas.SpecPalette{
				Primary: "#0F4C81", NeutralDark: "#0F172A", NeutralLight: "#F1F5F9",
			},
			Positioning: "Fixed-fee chartered accountants.",
		},
		Page: schemas.SpecPage{Sections: []schemas.SpecSection{
			{Type: schemas.SectionHero, Headline: "Hi there", Subheadline: "Local. Honest.",
				PrimaryCta: &schemas.SpecCTA{Label: "Call now", Action: "call"}},
			{Type: schemas.SectionServices, Title: "What we do",
				Items: []schemas.SpecSubItem{
					{Name: "Audits", OneLine: "Fast turnaround"},
					{Name: "Specs", OneLine: "Quality copy"},
					{Name: "Sites", OneLine: "Static + fast"},
				}},
			{Type: schemas.SectionAbout, Paragraph: "Since 2014."},
			{Type: schemas.SectionContact, Phone: "0161 234 5678"},
		}},
		SEO: schemas.SpecSEO{Title: "Acme Accountants — Manchester", Description: "Fixed-fee local accountants."},
		Constraints: schemas.SpecConstraints{
			DoNotInventTestimonials: true, DoNotInventAwards: true, DoNotInventPrices: true,
		},
	}
}

func TestRender_HappyPath(t *testing.T) {
	out, err := Render(Input{
		Spec: validSpec(), BusinessName: "Acme Accountants", Year: 2026,
		OptOutLine: "Reply 'no thanks' and I won't follow up.",
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{
		"<!doctype html>", `lang="en"`,
		`<meta name="viewport"`, `<meta name="description"`,
		`Fixed-fee local accountants.`, // SEO description
		`<title>Acme Accountants — Manchester</title>`,
		`<style>`, `--c-primary: #0F4C81`,
		`data-section="hero"`, `Hi there`,
		`data-section="services"`, `Audits`, `Specs`, `Sites`,
		`data-section="about"`, `Since 2014.`,
		`data-section="contact"`, `tel:0161%20234%205678`,
		`<footer`, `Acme Accountants`, `2026`, "no thanks",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Render output missing %q", want)
		}
	}
}

func TestRender_EmbedsPaletteAsCSSVars(t *testing.T) {
	in := Input{Spec: validSpec(), BusinessName: "x", Year: 2026}
	in.Spec.Brand.Palette = schemas.SpecPalette{
		Primary: "#ABCDEF", NeutralDark: "#111111", NeutralLight: "#EEEEEE",
	}
	out, err := Render(in)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, want := range []string{`--c-primary: #ABCDEF`, `--c-dark: #111111`, `--c-light: #EEEEEE`} {
		if !strings.Contains(out, want) {
			t.Errorf("palette var missing %q", want)
		}
	}
}

func TestRender_TrustWithoutVerifiedBadgesIsSkippedNotErrored(t *testing.T) {
	spec := validSpec()
	// Replace About with a Trust section the renderer should drop.
	spec.Page.Sections[2] = schemas.SpecSection{
		Type:   schemas.SectionTrust,
		Badges: []schemas.SpecBadge{{Label: "Fake Award 2024"}},
	}
	out, err := Render(Input{Spec: spec, BusinessName: "x", Year: 2026})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "Fake Award 2024") {
		t.Errorf("unverified badge leaked: %s", out)
	}
	// The Trust section's tag should NOT appear (whole section dropped).
	if strings.Contains(out, `data-section="trust"`) {
		t.Errorf("expected empty Trust section to be skipped entirely")
	}
	// Hero, Services, Contact all still rendered.
	for _, want := range []string{`data-section="hero"`, `data-section="services"`, `data-section="contact"`} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q to remain after Trust drop", want)
		}
	}
}

func TestRender_VerifiedBadgesAreKept(t *testing.T) {
	spec := validSpec()
	spec.Page.Sections[2] = schemas.SpecSection{
		Type: schemas.SectionTrust,
		Badges: []schemas.SpecBadge{
			{Label: "ICAEW chartered"},
			{Label: "Fake Award 2024"},
		},
	}
	out, err := Render(Input{
		Spec: spec, BusinessName: "x", Year: 2026,
		VerifiedBadges: map[string]bool{"ICAEW chartered": true},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, "ICAEW chartered") {
		t.Errorf("verified badge missing: %s", out)
	}
	if strings.Contains(out, "Fake Award 2024") {
		t.Errorf("unverified badge leaked: %s", out)
	}
}

func TestRender_ImageURLForResolves(t *testing.T) {
	spec := validSpec()
	spec.Page.Sections[0].ImagePrompt = "trades-van"
	out, err := Render(Input{
		Spec: spec, BusinessName: "x", Year: 2026,
		ImageURLFor: func(p string) string {
			return "https://img.example.com/" + p + ".jpg"
		},
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(out, `https://img.example.com/trades-van.jpg`) {
		t.Errorf("expected resolved hero image URL: %s", out)
	}
}

func TestRender_InvalidSpecIsRejected(t *testing.T) {
	bad := validSpec()
	bad.Constraints.DoNotInventTestimonials = false
	if _, err := Render(Input{Spec: bad, BusinessName: "x", Year: 2026}); err == nil {
		t.Error("expected error when spec fails structural validation")
	}
}

func TestRender_FooterCarriesYearAndOptOut(t *testing.T) {
	out, _ := Render(Input{
		Spec: validSpec(), BusinessName: "Acme", Year: 2030,
		OptOutLine: "Reply 'no thanks'.",
	})
	if !strings.Contains(out, "2030") {
		t.Error("footer year missing")
	}
	if !strings.Contains(out, "no thanks") {
		t.Error("opt-out line missing")
	}
}

func TestDefaultYear_ReturnsNonZero(t *testing.T) {
	if DefaultYear() < 2020 {
		t.Errorf("DefaultYear unexpectedly small: %d", DefaultYear())
	}
}

// TestRender_Output_IsValidHTMLFraction is a smoke sanity-check: the
// output should at least have the <!doctype>, opening <html> and
// closing </html> tags balanced. Not a parser-level check; just
// catches gross template breakage.
func TestRender_Output_BalancedRootTags(t *testing.T) {
	out, _ := Render(Input{Spec: validSpec(), BusinessName: "x", Year: 2026})
	if !strings.HasPrefix(strings.TrimSpace(out), "<!doctype html>") {
		t.Errorf("output should start with <!doctype html>; got prefix: %.40s", out)
	}
	if strings.Count(out, "<html") != 1 || strings.Count(out, "</html>") != 1 {
		t.Errorf("unbalanced <html>/</html>")
	}
	if strings.Count(out, "<body") != 1 || strings.Count(out, "</body>") != 1 {
		t.Errorf("unbalanced <body>/</body>")
	}
}
