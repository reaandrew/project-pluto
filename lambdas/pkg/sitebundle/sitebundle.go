// Package sitebundle assembles a complete static HTML page from a
// schemas.SpecV1 and renders one document the generator Lambda (iter
// 5.2) uploads to S3. The page is self-contained (inline CSS, no
// external resources) so the publisher (iter 5.3) can copy it to R2
// without an asset-resolution pass and Lighthouse measures pure
// document performance.
package sitebundle

import (
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/components"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// Input bundles what the renderer needs to produce a complete page.
// BusinessName + OptOutLine drive the footer; the rest comes from the
// Spec.
type Input struct {
	Spec         schemas.SpecV1
	BusinessName string
	OptOutLine   string
	// VerifiedBadges constrains Trust sections per
	// components.RenderOptions — leak-defensive nil = drop all.
	VerifiedBadges map[string]bool
	// ImageURLFor maps Hero imagePrompts to real asset URLs. nil
	// leaves data-image-prompt placeholders for a downstream pass.
	ImageURLFor func(prompt string) string
	// Year stamps the footer; injectable for deterministic tests.
	Year int
}

// Render emits the complete HTML document.
//
// Layout:
//
//	<html>
//	  <head>… meta tags, inline CSS …</head>
//	  <body>
//	    … N section fragments …
//	    <footer>…</footer>
//	  </body>
//	</html>
func Render(in Input) (string, error) {
	if err := schemas.ValidateSpecV1Structural(in.Spec); err != nil {
		return "", fmt.Errorf("sitebundle: spec validation: %w", err)
	}
	opts := components.RenderOptions{
		VerifiedBadges: in.VerifiedBadges,
		ImageURLFor:    in.ImageURLFor,
	}

	var sectionsHTML strings.Builder
	for i, sec := range in.Spec.Page.Sections {
		out, err := components.RenderSection(sec, opts)
		if err != nil {
			return "", fmt.Errorf("sitebundle: render section %d (%s): %w", i, sec.Type, err)
		}
		if out == "" {
			// Trust with all-dropped badges renders empty — skip the
			// section entirely rather than emit a hole in the layout.
			continue
		}
		sectionsHTML.WriteString(out)
		sectionsHTML.WriteString("\n")
	}

	footer, err := components.RenderFooter(components.FooterInput{
		BusinessName: in.BusinessName,
		Year:         in.Year,
		OptOutLine:   in.OptOutLine,
	})
	if err != nil {
		return "", fmt.Errorf("sitebundle: render footer: %w", err)
	}

	// The strings we wrap as template.HTML here are NOT user-controlled
	// at this boundary — each one came out of a sibling `html/template`
	// pipeline (components.RenderSection / components.RenderFooter)
	// which has already escaped every user-supplied field (Headline,
	// Paragraph, badge labels, etc.). Re-escaping here would
	// double-encode the per-section markup and corrupt the page. The
	// pattern is "compose pre-escaped fragments into a wrapper
	// template", not "splat raw user input into a template".
	sectionsHTMLSafe := template.HTML(sectionsHTML.String()) // nosemgrep: go.lang.security.audit.xss.template-html-does-not-escape.unsafe-template-type
	footerHTMLSafe := template.HTML(footer)                  // nosemgrep: go.lang.security.audit.xss.template-html-does-not-escape.unsafe-template-type
	tplData := documentData{
		Lang:              "en",
		Title:             in.Spec.SEO.Title,
		Description:       in.Spec.SEO.Description,
		Sections:          sectionsHTMLSafe,
		Footer:            footerHTMLSafe,
		PrimaryColor:      in.Spec.Brand.Palette.Primary,
		NeutralDarkColor:  in.Spec.Brand.Palette.NeutralDark,
		NeutralLightColor: in.Spec.Brand.Palette.NeutralLight,
	}

	var buf strings.Builder
	if err := documentTpl.Execute(&buf, tplData); err != nil {
		return "", fmt.Errorf("sitebundle: document render: %w", err)
	}
	return buf.String(), nil
}

// DefaultYear returns time.Now().Year() — exposed so callers can
// substitute deterministically in tests without monkey-patching
// time.Now.
func DefaultYear() int { return time.Now().Year() }

type documentData struct {
	Lang              string
	Title             string
	Description       string
	Sections          template.HTML
	Footer            template.HTML
	PrimaryColor      string
	NeutralDarkColor  string
	NeutralLightColor string
}

// documentTpl is the page skeleton. Inline CSS keeps the document
// self-contained (no extra HTTP requests, no asset-resolution pass at
// publish time) and lets Lighthouse measure pure document
// performance. The CSS uses CSS variables for the palette so the
// generator can swap a single block without re-parsing.
var documentTpl = template.Must(template.New("document").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="description" content="{{.Description}}">
<title>{{.Title}}</title>
<style>
:root {
  --c-primary: {{.PrimaryColor}};
  --c-dark: {{.NeutralDarkColor}};
  --c-light: {{.NeutralLightColor}};
  --c-text: #111;
}
*, *::before, *::after { box-sizing: border-box; }
html { -webkit-text-size-adjust: 100%; }
body {
  margin: 0;
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  line-height: 1.55;
  color: var(--c-text);
  background: var(--c-light);
}
section { padding: 2.5rem 1.25rem; max-width: 960px; margin: 0 auto; }
section + section { border-top: 1px solid rgba(0,0,0,0.06); }
h1, h2, h3 { color: var(--c-dark); margin: 0 0 0.5rem; line-height: 1.2; }
h1 { font-size: clamp(1.75rem, 3.5vw, 2.5rem); }
h2 { font-size: 1.5rem; margin-top: 0.5rem; }
h3 { font-size: 1.15rem; }
p { margin: 0.5rem 0; }
a { color: var(--c-primary); }
.s-hero { display: grid; gap: 1rem; }
.s-hero__image, .s-hero__image-placeholder {
  width: 100%; height: 220px; background-color: #d8e2ec;
  background-size: cover; background-position: center;
  border-radius: 6px;
}
.s-hero__cta, .s-cta__button {
  display: inline-block; margin-top: 0.5rem;
  padding: 0.65rem 1.1rem; border-radius: 6px;
  background: var(--c-primary); color: #fff !important;
  text-decoration: none; font-weight: 600;
}
.s-services__list, .s-trust__list { list-style: none; padding: 0; margin: 0;
  display: grid; gap: 0.75rem; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); }
.s-services__item, .s-trust__badge {
  padding: 0.75rem 1rem; background: #fff; border: 1px solid rgba(0,0,0,0.08); border-radius: 6px;
}
.s-faq__list dt { font-weight: 600; margin-top: 0.5rem; }
.s-faq__list dd { margin: 0 0 0.5rem; }
.s-contact p { margin: 0.25rem 0; }
.s-footer { padding: 1.25rem; text-align: center; color: #666; font-size: 0.85rem; }
.s-footer__line { margin: 0; }
</style>
</head>
<body>
{{.Sections}}{{.Footer}}
</body>
</html>
`))
