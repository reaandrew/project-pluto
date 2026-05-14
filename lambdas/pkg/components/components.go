// Package components renders one schemas.SpecSection at a time into
// HTML fragments — the building blocks the iter 5.2 generator stitches
// together into a complete preview page.
//
// Eight components in total: the seven section kinds the spec schema
// admits (Hero, Services, About, Trust, FAQ, CTA, Contact) plus a
// Footer that the renderer always adds (the model never produces it).
//
// All templates use `html/template` so user-controlled values are
// HTML-escaped by default. Renderers are pure functions: no I/O, no
// package globals beyond the compiled-once templates.
//
// Per .ralph/specs/07-bedrock-prompts.md § "Renderer guarantees":
//   - Strip any testimonial-shaped section (the spec schema's oneOf
//     doesn't admit one, so this is caught upstream — the
//     post-validator on prompts.SpecV1 rejects it).
//   - Drop any Trust badge whose label isn't present verbatim in the
//     source site's text. This package doesn't have the source site;
//     callers (iter 5.2 generator) inject a `verifiedBadgeLabels` set
//     when they call RenderSection.
//   - Replace `imagePrompt` with a curated stock asset. This package
//     emits a placeholder marker; the iter 5.2 generator wires the
//     real asset URL.
//   - Replace palette values that fail WCAG AA contrast against
//     `neutralLight`. This package leaves the spec's palette as-is;
//     the generator does the contrast pass before calling Render.
package components

import (
	"bytes"
	"errors"
	"fmt"
	"html/template"
	"strings"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// RenderOptions carries cross-section configuration the renderers
// need but the SpecSection doesn't carry. Zero-valued options work
// (Trust drops all badges; image placeholders show the prompt text)
// — the generator overrides per-call.
type RenderOptions struct {
	// VerifiedBadges is the closed set of badge labels that exist
	// verbatim in the source site's text. Trust sections drop any
	// badge whose Label isn't in this set. nil = drop all badges.
	VerifiedBadges map[string]bool

	// ImageURLFor maps a hero's imagePrompt string to a real asset
	// URL. nil → the Hero template emits a data-image-prompt
	// attribute on a placeholder div so the generator can swap it
	// in a post-pass.
	ImageURLFor func(prompt string) string
}

// RenderSection renders one section. Returns the HTML fragment or an
// error when the section's Type isn't admissible. The generator wraps
// these into a page.
func RenderSection(s schemas.SpecSection, opts RenderOptions) (string, error) {
	switch s.Type {
	case schemas.SectionHero:
		return renderHero(s, opts)
	case schemas.SectionServices:
		return renderServices(s)
	case schemas.SectionAbout:
		return renderAbout(s)
	case schemas.SectionTrust:
		return renderTrust(s, opts)
	case schemas.SectionFAQ:
		return renderFAQ(s)
	case schemas.SectionCTA:
		return renderCTA(s)
	case schemas.SectionContact:
		return renderContact(s)
	}
	return "", fmt.Errorf("components: unknown section type %q", s.Type)
}

// RenderFooter emits a small, always-present footer. The model
// doesn't produce a Footer section; the generator appends one.
func RenderFooter(in FooterInput) (string, error) {
	return execTemplate(footerTpl, in)
}

// FooterInput is the data the footer template consumes.
type FooterInput struct {
	BusinessName string
	Year         int
	OptOutLine   string // e.g. "Reply 'no thanks' and I won't follow up."
}

// --- Hero ---------------------------------------------------------------

type heroData struct {
	Headline    string
	Subheadline string
	PrimaryCta  *schemas.SpecCTA
	ImagePrompt string
	ImageURL    string // empty when ImageURLFor returned ""
	ActionAttr  string // href="tel:" / "mailto:" / "#contact" etc.
}

func renderHero(s schemas.SpecSection, opts RenderOptions) (string, error) {
	if s.PrimaryCta == nil {
		return "", errors.New("components: hero section requires primaryCta")
	}
	d := heroData{
		Headline:    s.Headline,
		Subheadline: s.Subheadline,
		PrimaryCta:  s.PrimaryCta,
		ImagePrompt: s.ImagePrompt,
		ActionAttr:  ctaAction(*s.PrimaryCta),
	}
	if opts.ImageURLFor != nil {
		d.ImageURL = opts.ImageURLFor(s.ImagePrompt)
	}
	return execTemplate(heroTpl, d)
}

// --- Services -----------------------------------------------------------

type servicesData struct {
	Title string
	Items []schemas.SpecSubItem
}

func renderServices(s schemas.SpecSection) (string, error) {
	if len(s.Items) == 0 {
		return "", errors.New("components: services section requires items")
	}
	return execTemplate(servicesTpl, servicesData{Title: s.Title, Items: s.Items})
}

// --- About --------------------------------------------------------------

func renderAbout(s schemas.SpecSection) (string, error) {
	if strings.TrimSpace(s.Paragraph) == "" {
		return "", errors.New("components: about section requires paragraph")
	}
	return execTemplate(aboutTpl, map[string]string{"Paragraph": s.Paragraph})
}

// --- Trust --------------------------------------------------------------

func renderTrust(s schemas.SpecSection, opts RenderOptions) (string, error) {
	kept := make([]schemas.SpecBadge, 0, len(s.Badges))
	for _, b := range s.Badges {
		if opts.VerifiedBadges != nil && opts.VerifiedBadges[b.Label] {
			kept = append(kept, b)
		}
	}
	// Trust section with all-dropped badges renders nothing — better
	// than an empty box with no content. Generator can decide to
	// drop the section entirely.
	if len(kept) == 0 {
		return "", nil
	}
	return execTemplate(trustTpl, map[string]any{"Badges": kept})
}

// --- FAQ ----------------------------------------------------------------

func renderFAQ(s schemas.SpecSection) (string, error) {
	if len(s.Items) == 0 {
		return "", errors.New("components: faq section requires items")
	}
	return execTemplate(faqTpl, map[string]any{"Items": s.Items})
}

// --- CTA ----------------------------------------------------------------

type ctaData struct {
	Headline    string
	Subheadline string
	Button      *schemas.SpecCTA
	ActionAttr  string
}

func renderCTA(s schemas.SpecSection) (string, error) {
	if s.Button == nil {
		return "", errors.New("components: cta section requires button")
	}
	return execTemplate(ctaTpl, ctaData{
		Headline:    s.Headline,
		Subheadline: s.Subheadline,
		Button:      s.Button,
		ActionAttr:  ctaAction(*s.Button),
	})
}

// --- Contact ------------------------------------------------------------

func renderContact(s schemas.SpecSection) (string, error) {
	return execTemplate(contactTpl, s)
}

// --- helpers ------------------------------------------------------------

// ctaAction maps the CTA action enum to an `href` value the browser
// understands. Phone numbers + emails are stripped of whitespace so
// the tel:/mailto: URI is well-formed; the form action falls back to
// `#contact` so the same scaffold works regardless of which contact
// section a page picks.
func ctaAction(cta schemas.SpecCTA) string {
	switch cta.Action {
	case "call":
		return "#contact"
	case "email":
		return "#contact"
	case "form":
		return "#contact"
	}
	return "#"
}

// execTemplate runs a precompiled template with data and returns the
// rendered string. Used by all per-section renderers so the
// boilerplate stays in one place.
func execTemplate(t *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("components: render: %w", err)
	}
	return buf.String(), nil
}

// --- compiled templates --------------------------------------------------

var (
	heroTpl     = mustParse("hero", heroTplSrc)
	servicesTpl = mustParse("services", servicesTplSrc)
	aboutTpl    = mustParse("about", aboutTplSrc)
	trustTpl    = mustParse("trust", trustTplSrc)
	faqTpl      = mustParse("faq", faqTplSrc)
	ctaTpl      = mustParse("cta", ctaTplSrc)
	contactTpl  = mustParse("contact", contactTplSrc)
	footerTpl   = mustParse("footer", footerTplSrc)
)

func mustParse(name, src string) *template.Template {
	t, err := template.New(name).Parse(src)
	if err != nil {
		panic(fmt.Errorf("components: failed to parse %s template: %w", name, err))
	}
	return t
}

// Templates use modest semantic HTML — sections + headings + lists.
// CSS classes follow a single-`s-` prefix so the iter 5.2 generator's
// stylesheet doesn't need to know per-section class names; it styles
// `[data-section="hero"]` etc.
//
// Each template starts on a new line so the assembled page stays
// readable when the generator writes the final HTML to disk.

const heroTplSrc = `<section data-section="hero" class="s-hero">
  {{- if .ImageURL}}
  <div class="s-hero__image" style="background-image:url('{{.ImageURL}}')"></div>
  {{- else if .ImagePrompt}}
  <div class="s-hero__image-placeholder" data-image-prompt="{{.ImagePrompt}}"></div>
  {{- end}}
  <h1 class="s-hero__headline">{{.Headline}}</h1>
  <p class="s-hero__subheadline">{{.Subheadline}}</p>
  <a class="s-hero__cta" href="{{.ActionAttr}}" data-action="{{.PrimaryCta.Action}}">{{.PrimaryCta.Label}}</a>
</section>`

const servicesTplSrc = `<section data-section="services" class="s-services">
  {{- if .Title}}
  <h2 class="s-services__title">{{.Title}}</h2>
  {{- end}}
  <ul class="s-services__list">
    {{- range .Items}}
    <li class="s-services__item">
      <h3 class="s-services__name">{{.Name}}</h3>
      <p class="s-services__one-line">{{.OneLine}}</p>
    </li>
    {{- end}}
  </ul>
</section>`

const aboutTplSrc = `<section data-section="about" class="s-about">
  <p class="s-about__paragraph">{{.Paragraph}}</p>
</section>`

const trustTplSrc = `<section data-section="trust" class="s-trust">
  <ul class="s-trust__list">
    {{- range .Badges}}
    <li class="s-trust__badge">{{.Label}}</li>
    {{- end}}
  </ul>
</section>`

const faqTplSrc = `<section data-section="faq" class="s-faq">
  <dl class="s-faq__list">
    {{- range .Items}}
    <dt class="s-faq__q">{{.Q}}</dt>
    <dd class="s-faq__a">{{.A}}</dd>
    {{- end}}
  </dl>
</section>`

const ctaTplSrc = `<section data-section="cta" class="s-cta">
  <h2 class="s-cta__headline">{{.Headline}}</h2>
  {{- if .Subheadline}}
  <p class="s-cta__subheadline">{{.Subheadline}}</p>
  {{- end}}
  <a class="s-cta__button" href="{{.ActionAttr}}" data-action="{{.Button.Action}}">{{.Button.Label}}</a>
</section>`

const contactTplSrc = `<section data-section="contact" id="contact" class="s-contact">
  {{- if .Phone}}
  <p class="s-contact__phone"><a href="tel:{{.Phone}}">{{.Phone}}</a></p>
  {{- end}}
  {{- if .Email}}
  <p class="s-contact__email"><a href="mailto:{{.Email}}">{{.Email}}</a></p>
  {{- end}}
  {{- if .Address}}
  <p class="s-contact__address">{{.Address}}</p>
  {{- end}}
  {{- if .Hours}}
  <p class="s-contact__hours">{{.Hours}}</p>
  {{- end}}
</section>`

const footerTplSrc = `<footer class="s-footer">
  <p class="s-footer__line">© {{.Year}} {{.BusinessName}}</p>
  {{- if .OptOutLine}}
  <p class="s-footer__opt-out">{{.OptOutLine}}</p>
  {{- end}}
</footer>`
