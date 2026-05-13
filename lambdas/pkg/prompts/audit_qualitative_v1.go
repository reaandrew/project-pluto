package prompts

import (
	"fmt"
	"strings"
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// auditQualitativeSystem is the system message for audit.qualitative.v1.
// Mirrors .ralph/specs/07-bedrock-prompts.md verbatim, with SafetyRulesBlock
// substituted for the spec's `<safety_rules>…</safety_rules>` placeholder.
const auditQualitativeSystem = `You are a senior conversion-design reviewer for small business websites in the UK and US.
You score sites for whether a redesign would materially increase enquiries / bookings.
Be concrete and concise. Refer to specific elements you can see in the HTML.
Never invent business facts. If you cannot see something, say so.
` + SafetyRulesBlock

// AuditQualitativeV1 is the qualitative-audit prompt: Haiku 4.5, forced
// tool_use of `recordAudit`. Caching keyed on (domain, html_excerpt) is
// enforced at the caller — see lambdas/pkg/audit/qualitative.Run — so the
// prompt definition itself just sets CacheTTL.
var AuditQualitativeV1 = New(Prompt[schemas.AuditV1]{
	ID:           "audit.qualitative.v1",
	ModelID:      bedrock.ModelHaiku45,
	System:       auditQualitativeSystem,
	ToolName:     "recordAudit",
	MaxTokens:    800,
	Stage:        bedrock.StageAudit,
	EstimateUSD:  0.012,
	CacheTTL:     30 * 24 * time.Hour,
	PostValidate: validateAuditQualitativeV1,
})

// validateAuditQualitativeV1 enforces the few rules JSON Schema can't
// express. The model is told via SafetyRulesBlock; this catches drift.
//
//   - "password" is banned in user-facing copy. Audit Summary +
//     Issue.Description are operator-facing, but they sometimes get
//     surfaced verbatim in the review UI — defensible to reject them
//     here rather than have to filter on render.
//   - Empty Summary / Description fields aren't a schema violation
//     (schema only constrains maxLength), but they'd render as gaps in
//     the review UI; reject those too.
//
// Note on the spec's "rejects a fake-testimonial response" adversarial
// case (07-bedrock-prompts.md § Snapshot tests): that rule applies to
// generative prompts (spec.v1, email.v1) which produce user-facing
// copy. The audit-qualitative prompt produces a critique — describing
// testimonials on the audited site is correct behaviour, not a
// violation. The fabrication-of-testimonials rule is enforced by
// spec.v1's post-validator when that lands (iter 4.x). See the
// `TestAuditQualitativeV1PostValidateAcceptsDescribingTestimonials`
// test in audit_qualitative_v1_test.go for the assertion.
func validateAuditQualitativeV1(a schemas.AuditV1) error {
	if strings.TrimSpace(a.Summary) == "" {
		return fmt.Errorf("summary is empty")
	}
	if containsBannedWord(a.Summary) {
		return fmt.Errorf("summary contains banned word")
	}
	for i, iss := range a.Issues {
		if strings.TrimSpace(iss.Description) == "" {
			return fmt.Errorf("issues[%d].description is empty", i)
		}
		if containsBannedWord(iss.Description) {
			return fmt.Errorf("issues[%d].description contains banned word", i)
		}
	}
	return nil
}

func containsBannedWord(s string) bool {
	return strings.Contains(strings.ToLower(s), "password")
}
