// Package qualifier implements PriorityScore — the pure function the
// qualifier Lambda (iter 3.2) uses to rank audited businesses for the
// review queue. Result is in [0, 1]; higher = bump up the queue.
//
// Reference: .ralph/specs/05-capacity-and-cost.md § "priorityScore".
// The TS sketch there is the source of truth; this Go implementation
// matches it term-for-term, with one Go-side hardening:
//
//   - the final score is clamped to [0,1]. Bad operator-supplied
//     weights (sum > 1, negative weights elsewhere in the formula
//     stack) would otherwise let the score escape the bound.
//     targeting.Profile.Validate already rejects weight-sum drift on
//     write, but a defensive clamp here keeps the contract honest for
//     any historical row that pre-dates validation tightening.
//
// Pure function. No I/O. No package-global state. All proxies
// (`sizeProxy`, `websiteAgeProxy`) are pure too and live in this file
// so the whole calculation is auditable at one glance.
package qualifier

import "github.com/reaandrew/ai-website-agency/lambdas/pkg/targeting"

// Audit is the subset of the AuditRow that PriorityScore looks at.
// Defined here (rather than importing the lambdas/audit row shape) so
// the qualifier package stays independent of the audit Lambda's
// internal types.
type Audit struct {
	// Score is 0..100, higher = better site. The formula inverts
	// this to "website need" — a low score means a redesign is more
	// valuable.
	Score int
	// LighthousePerformance feeds websiteAgeProxy. 0 means PageSpeed
	// didn't run (rather than a perfect-zero score), and is treated
	// as "no signal" rather than "very old."
	LighthousePerformance int
	// HasContact reflects whether the technical pass found a contact
	// link on the homepage. Used as a weak modernity signal for
	// websiteAgeProxy — old sites often hide contact info behind
	// /contact-us pages, not above the fold.
	HasContact bool
}

// Business is the subset PriorityScore needs.
type Business struct {
	// Vertical is matched against TargetingProfile.Vertical for the
	// verticalFit term (exact match → 1.0, else 0.5 — never zero).
	Vertical string
	// Confidence is the discovery-source confidence (0..1), reused as
	// the businessSize proxy until iter 6.x lands real headcount/
	// revenue signals via Companies House officer counts.
	Confidence float64
}

// Contact is the subset PriorityScore needs. Pass nil when no contact
// row exists yet (priorityScore treats this as confidence=0, NOT as
// "skip this business").
type Contact struct {
	Confidence float64
}

// Input bundles every input the formula consumes. Passed by value so
// the function stays trivially callable from tests with literals.
type Input struct {
	Audit            Audit
	Business         Business
	Contact          *Contact
	TargetingProfile targeting.Profile
}

// PriorityScore returns the qualifier's priority in [0, 1]. Inputs
// outside their documented ranges (Audit.Score < 0, weights summing
// to >1) are tolerated — the final clamp catches them.
func PriorityScore(in Input) float64 {
	w := in.TargetingProfile.Weights
	websiteNeed := clamp01(float64(100-in.Audit.Score) / 100.0)
	verticalFit := verticalFitScore(in.Business.Vertical, in.TargetingProfile.Vertical)
	businessSize := sizeProxy(in.Business)
	contactConf := 0.0
	if in.Contact != nil {
		contactConf = clamp01(in.Contact.Confidence)
	}
	age := websiteAgeProxy(in.Audit)

	raw := w.AuditScore*websiteNeed +
		w.VerticalFit*verticalFit +
		w.BusinessSize*businessSize +
		w.ContactConfidence*contactConf +
		w.WebsiteAge*age
	return clamp01(raw)
}

// verticalFitScore returns 1.0 on an exact (case-insensitive) match,
// 0.5 otherwise. The spec deliberately never returns 0 — verticals
// shift over time and the qualifier shouldn't black-hole near-misses.
func verticalFitScore(businessVertical, profileVertical string) float64 {
	if eqFold(businessVertical, profileVertical) {
		return 1.0
	}
	return 0.5
}

// sizeProxy maps a Business to a 0..1 size signal. v1 reuses the
// discovery-source confidence: Google Places returns rich listings
// (1.0), Companies House returns thin (0.6), CSV is operator-curated
// (typically 0.8). Iter 6.x adds real signals (officer count, average
// review count) and bumps sizeProxy to a weighted blend.
//
// Returns 0 for a zero-confidence row (impossible in practice but
// keeps the function total).
func sizeProxy(b Business) float64 {
	return clamp01(b.Confidence)
}

// websiteAgeProxy maps an audit signal-set to a 0..1 "site looks old"
// score. Sites with low Lighthouse performance score and no contact
// detected on the homepage skew higher.
//
// Lighthouse.Performance = 0 means PageSpeed didn't run; treat that
// as "no signal" (return 0) rather than "very old".
func websiteAgeProxy(a Audit) float64 {
	if a.LighthousePerformance == 0 {
		return 0
	}
	perfNeed := clamp01(float64(100-a.LighthousePerformance) / 100.0)
	// Modest bump if the site doesn't expose contact details — most
	// modern small-business sites do. Capped so a tiny modernity
	// shortfall doesn't dominate.
	if !a.HasContact {
		perfNeed = clamp01(perfNeed + 0.1)
	}
	return perfNeed
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// eqFold is strings.EqualFold without importing strings into the hot
// path. Vertical strings are short ASCII; the manual fold is fine.
func eqFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
