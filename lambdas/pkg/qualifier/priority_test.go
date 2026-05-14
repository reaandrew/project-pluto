package qualifier

import (
	"math"
	"testing"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/targeting"
)

// standardWeights mirrors the default TargetingProfile in
// .ralph/specs/02-data-model.md (websiteAge 0.2, auditScore 0.3,
// businessSize 0.2, contactConfidence 0.2, verticalFit 0.1; sums to 1.0).
func standardWeights() targeting.Weights {
	return targeting.Weights{
		WebsiteAge:        0.2,
		AuditScore:        0.3,
		BusinessSize:      0.2,
		ContactConfidence: 0.2,
		VerticalFit:       0.1,
	}
}

func standardProfile(vertical string) targeting.Profile {
	return targeting.Profile{
		Vertical: vertical,
		Weights:  standardWeights(),
	}
}

// approxEq compares two floats within 1e-9 — looser than full float
// equality but tight enough to catch real drift in the formula.
func approxEq(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}

// ---------------------------------------------------------------------------
// Golden cases — 10 fixtures spanning weak/strong/borderline sites and the
// presence/absence of contact + vertical match. Each fixture's `want` is
// computed by hand from the formula and pinned here; any change to
// PriorityScore that drifts a result must update the corresponding line.
// ---------------------------------------------------------------------------

func TestPriorityScore_Golden(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		want float64
	}{
		{
			// 1. Strong-need lead. Bad audit (score 10) + matching vertical +
			//    high-confidence contact + Companies-House-style business
			//    (confidence 0.6) + slow site → priorityScore should sit
			//    high.
			//    websiteNeed=0.9; verticalFit=1.0; businessSize=0.6;
			//    contactConf=0.8; age = (100-20)/100 + 0.1 contact bump = 0.9
			//    raw = 0.3*0.9 + 0.1*1.0 + 0.2*0.6 + 0.2*0.8 + 0.2*0.9 = 0.83
			name: "weak site, vertical match, high-confidence contact",
			in: Input{
				Audit:            Audit{Score: 10, LighthousePerformance: 20, HasContact: false},
				Business:         Business{Vertical: "accountants", Confidence: 0.6},
				Contact:          &Contact{Confidence: 0.8},
				TargetingProfile: standardProfile("accountants"),
			},
			want: 0.83,
		},
		{
			// 2. Strong site. score=95, lighthouse 90, contact yes, matched.
			//    websiteNeed=0.05; verticalFit=1.0; businessSize=0.6;
			//    contactConf=0.5; age=(100-90)/100=0.1
			//    raw = 0.3*0.05 + 0.1*1.0 + 0.2*0.6 + 0.2*0.5 + 0.2*0.1
			//        = 0.015 + 0.1 + 0.12 + 0.1 + 0.02 = 0.355
			name: "strong site, vertical match, average contact",
			in: Input{
				Audit:            Audit{Score: 95, LighthousePerformance: 90, HasContact: true},
				Business:         Business{Vertical: "accountants", Confidence: 0.6},
				Contact:          &Contact{Confidence: 0.5},
				TargetingProfile: standardProfile("accountants"),
			},
			want: 0.355,
		},
		{
			// 3. Vertical mismatch costs 0.5 on verticalFit but
			//    everything else carries the score.
			//    websiteNeed=0.7; verticalFit=0.5; businessSize=0.8;
			//    contactConf=0.7; age=(100-40)/100=0.6
			//    raw = 0.3*0.7 + 0.1*0.5 + 0.2*0.8 + 0.2*0.7 + 0.2*0.6
			//        = 0.21 + 0.05 + 0.16 + 0.14 + 0.12 = 0.68
			name: "vertical mismatch, otherwise strong",
			in: Input{
				Audit:            Audit{Score: 30, LighthousePerformance: 40, HasContact: true},
				Business:         Business{Vertical: "tradespeople", Confidence: 0.8},
				Contact:          &Contact{Confidence: 0.7},
				TargetingProfile: standardProfile("accountants"),
			},
			want: 0.68,
		},
		{
			// 4. No contact row at all (Contact = nil).
			//    contactConf collapses to 0 — the term zeros.
			//    websiteNeed=0.6; verticalFit=1.0; businessSize=0.6;
			//    contactConf=0; age=(100-50)/100+0.1(no contact)=0.6
			//    raw = 0.3*0.6 + 0.1*1.0 + 0.2*0.6 + 0.2*0 + 0.2*0.6
			//        = 0.18 + 0.1 + 0.12 + 0 + 0.12 = 0.52
			name: "no contact row, vertical match",
			in: Input{
				Audit:            Audit{Score: 40, LighthousePerformance: 50, HasContact: false},
				Business:         Business{Vertical: "accountants", Confidence: 0.6},
				Contact:          nil,
				TargetingProfile: standardProfile("accountants"),
			},
			want: 0.52,
		},
		{
			// 5. PageSpeed didn't run (LighthousePerformance=0) → age proxy
			//    is 0 — we have no signal, treat as no contribution.
			//    websiteNeed=0.5; verticalFit=1.0; businessSize=0.7;
			//    contactConf=0.6; age=0
			//    raw = 0.3*0.5 + 0.1*1.0 + 0.2*0.7 + 0.2*0.6 + 0.2*0
			//        = 0.15 + 0.1 + 0.14 + 0.12 + 0 = 0.51
			name: "PageSpeed didn't run; age proxy zero",
			in: Input{
				Audit:            Audit{Score: 50, LighthousePerformance: 0, HasContact: true},
				Business:         Business{Vertical: "accountants", Confidence: 0.7},
				Contact:          &Contact{Confidence: 0.6},
				TargetingProfile: standardProfile("accountants"),
			},
			want: 0.51,
		},
		{
			// 6. Borderline result: score = 50 dead-centre.
			//    websiteNeed=0.5; verticalFit=1.0; businessSize=0.5;
			//    contactConf=0.5; age=(100-50)/100=0.5
			//    raw = 0.3*0.5 + 0.1*1.0 + 0.2*0.5 + 0.2*0.5 + 0.2*0.5
			//        = 0.15 + 0.1 + 0.1 + 0.1 + 0.1 = 0.55
			name: "median across the board",
			in: Input{
				Audit:            Audit{Score: 50, LighthousePerformance: 50, HasContact: true},
				Business:         Business{Vertical: "accountants", Confidence: 0.5},
				Contact:          &Contact{Confidence: 0.5},
				TargetingProfile: standardProfile("accountants"),
			},
			want: 0.55,
		},
		{
			// 7. Heavy AuditScore weighting (operator profile tuned to
			//    favour redesign-worthy sites over everything else).
			//    Weights: auditScore 0.7, others 0.075 each (sum 1.0).
			//    websiteNeed=0.9 (score 10), verticalFit=1.0,
			//    businessSize=0.6, contactConf=0.8, age=0.5
			//    raw = 0.7*0.9 + 0.075*(1.0+0.6+0.8+0.5)
			//        = 0.63 + 0.075*2.9 = 0.63 + 0.2175 = 0.8475
			name: "audit-heavy weights",
			in: Input{
				Audit:    Audit{Score: 10, LighthousePerformance: 50, HasContact: true},
				Business: Business{Vertical: "accountants", Confidence: 0.6},
				Contact:  &Contact{Confidence: 0.8},
				TargetingProfile: targeting.Profile{
					Vertical: "accountants",
					Weights: targeting.Weights{
						AuditScore:        0.7,
						VerticalFit:       0.075,
						BusinessSize:      0.075,
						ContactConfidence: 0.075,
						WebsiteAge:        0.075,
					},
				},
			},
			want: 0.8475,
		},
		{
			// 8. Zero everywhere except verticalFit. With all signals
			//    at zero, the floor is verticalFit (0.5 for mismatch,
			//    1.0 for match) * its weight.
			//    websiteNeed=0; verticalFit=0.5; businessSize=0;
			//    contactConf=0; age=0
			//    raw = 0.1*0.5 = 0.05
			name: "everything zero, vertical mismatch",
			in: Input{
				Audit:            Audit{Score: 100, LighthousePerformance: 100, HasContact: true},
				Business:         Business{Vertical: "tradespeople", Confidence: 0},
				Contact:          nil,
				TargetingProfile: standardProfile("accountants"),
			},
			want: 0.05,
		},
		{
			// 9. Negative audit score (impossible in practice but the
			//    formula tolerates it via the clamp). Score = -50 →
			//    websiteNeed clamped to 1.0.
			//    websiteNeed=1.0; verticalFit=1.0; businessSize=0.6;
			//    contactConf=0.6; age=(100-30)/100=0.7
			//    raw = 0.3*1.0 + 0.1*1.0 + 0.2*0.6 + 0.2*0.6 + 0.2*0.7
			//        = 0.3 + 0.1 + 0.12 + 0.12 + 0.14 = 0.78
			name: "out-of-range audit score clamps to 1.0",
			in: Input{
				Audit:            Audit{Score: -50, LighthousePerformance: 30, HasContact: true},
				Business:         Business{Vertical: "accountants", Confidence: 0.6},
				Contact:          &Contact{Confidence: 0.6},
				TargetingProfile: standardProfile("accountants"),
			},
			want: 0.78,
		},
		{
			// 10. Case-insensitive vertical match. "Accountants" ==
			//     "accountants" → verticalFit=1.0, not 0.5.
			//     Same numbers as fixture #6 but with the
			//     mixed-case vertical to confirm the matcher.
			name: "case-insensitive vertical match",
			in: Input{
				Audit:            Audit{Score: 50, LighthousePerformance: 50, HasContact: true},
				Business:         Business{Vertical: "Accountants", Confidence: 0.5},
				Contact:          &Contact{Confidence: 0.5},
				TargetingProfile: standardProfile("ACCOUNTANTS"),
			},
			want: 0.55,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PriorityScore(c.in)
			if !approxEq(got, c.want) {
				t.Errorf("PriorityScore = %.6f, want %.6f (delta %.6f)",
					got, c.want, got-c.want)
			}
			if got < 0 || got > 1 {
				t.Errorf("PriorityScore = %.6f outside [0,1]", got)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Property + edge-case tests on top of the golden cases.
// ---------------------------------------------------------------------------

func TestPriorityScore_AlwaysInRange(t *testing.T) {
	// Adversarial weights — sum > 1, and a malicious audit score that
	// drives websiteNeed to the top. The final clamp must keep us in
	// [0,1].
	in := Input{
		Audit:    Audit{Score: -100, LighthousePerformance: 1, HasContact: false},
		Business: Business{Vertical: "x", Confidence: 1.0},
		Contact:  &Contact{Confidence: 1.0},
		TargetingProfile: targeting.Profile{
			Vertical: "x",
			Weights: targeting.Weights{
				AuditScore:        2.0,
				VerticalFit:       2.0,
				BusinessSize:      2.0,
				ContactConfidence: 2.0,
				WebsiteAge:        2.0,
			},
		},
	}
	got := PriorityScore(in)
	if got != 1.0 {
		t.Errorf("expected clamp to 1.0 for adversarial weights, got %.6f", got)
	}
}

func TestPriorityScore_NoContactCollapsesContactTerm(t *testing.T) {
	withContact := Input{
		Audit:            Audit{Score: 50, LighthousePerformance: 50, HasContact: true},
		Business:         Business{Vertical: "x", Confidence: 0.5},
		Contact:          &Contact{Confidence: 1.0},
		TargetingProfile: standardProfile("x"),
	}
	withoutContact := withContact
	withoutContact.Contact = nil

	a := PriorityScore(withContact)
	b := PriorityScore(withoutContact)
	// The only difference is contactConf 1.0 vs 0.0 against weight 0.2
	// → expected delta is exactly 0.2.
	if !approxEq(a-b, 0.2) {
		t.Errorf("contact-present-vs-nil delta = %.6f, want 0.2", a-b)
	}
}

func TestPriorityScore_VerticalFitNeverBelowHalf(t *testing.T) {
	// Even with arbitrary vertical strings, verticalFitScore returns
	// 0.5 — never 0. This is by design (see priority.go doc comment).
	for _, mis := range []string{"", "x", "DENTIST", "tradespeople"} {
		got := verticalFitScore(mis, "accountants")
		if got != 0.5 {
			t.Errorf("verticalFitScore(%q, accountants) = %.2f, want 0.5", mis, got)
		}
	}
}

func TestSizeProxy_BoundedByConfidence(t *testing.T) {
	cases := map[float64]float64{
		-0.5: 0,
		0:    0,
		0.5:  0.5,
		1.0:  1.0,
		1.5:  1.0,
	}
	for in, want := range cases {
		if got := sizeProxy(Business{Confidence: in}); got != want {
			t.Errorf("sizeProxy(confidence=%.2f) = %.2f, want %.2f", in, got, want)
		}
	}
}

func TestWebsiteAgeProxy(t *testing.T) {
	cases := []struct {
		name       string
		audit      Audit
		wantApprox float64
	}{
		{"PSI didn't run → zero signal", Audit{LighthousePerformance: 0, HasContact: true}, 0},
		{"strong perf + contact = nearly 0", Audit{LighthousePerformance: 95, HasContact: true}, 0.05},
		{"weak perf + no contact = bumped", Audit{LighthousePerformance: 30, HasContact: false}, 0.8},
		{"weak perf + contact = unbumped", Audit{LighthousePerformance: 30, HasContact: true}, 0.7},
		{"ceiling caps at 1.0", Audit{LighthousePerformance: 1, HasContact: false}, 1.0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := websiteAgeProxy(c.audit)
			if !approxEq(got, c.wantApprox) {
				t.Errorf("websiteAgeProxy = %.6f, want %.6f", got, c.wantApprox)
			}
		})
	}
}

func TestClamp01(t *testing.T) {
	cases := map[float64]float64{-1: 0, 0: 0, 0.5: 0.5, 1: 1, 2: 1}
	for in, want := range cases {
		if got := clamp01(in); got != want {
			t.Errorf("clamp01(%.2f) = %.2f, want %.2f", in, got, want)
		}
	}
}

func TestEqFold(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"a", "A", true},
		{"Accountants", "ACCOUNTANTS", true},
		{"accountants", "tradespeople", false},
		{"x", "xx", false},
	}
	for _, c := range cases {
		if got := eqFold(c.a, c.b); got != c.want {
			t.Errorf("eqFold(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}
