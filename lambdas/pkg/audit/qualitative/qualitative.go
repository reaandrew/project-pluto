// Package qualitative wraps prompts.AuditQualitativeV1 with the
// input-shape, message-assembly, and cache-key conventions specified in
// .ralph/specs/07-bedrock-prompts.md § "audit.qualitative.v1".
//
// Two pieces the spec is explicit about and we encode here:
//
//   - The cache key is (promptId, sha256(domain + htmlExcerpt)). Run
//     builds it via bedrock.CacheKey(prompts.AuditQualitativeV1.ID,
//     prompts.HashInputs(domain, htmlExcerpt)) so the same homepage
//     under the same prompt version reuses the cached audit. The
//     30-day TTL is configured on the Prompt itself.
//
//   - htmlExcerpt is "first 8KB of body text, stripped" — the audit
//     Lambda is responsible for stripping HTML before passing it in.
//     This wrapper enforces the 8KB cap defensively (callers that
//     forget to truncate still get a sensible cache key + cost
//     envelope rather than a 200KB Bedrock payload).
package qualitative

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/audit/technical"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/prompts"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// MaxHTMLExcerptBytes is the byte cap the spec puts on `html_excerpt`
// (first 8KB of stripped body text). Anything longer is truncated by
// Run before the cache key is computed.
const MaxHTMLExcerptBytes = 8 * 1024

// Business is the {name, vertical, location} subset the audit prompt
// looks at — kept narrow so we don't leak unrelated business fields
// into the cache key or the prompt context.
type Business struct {
	Name     string `json:"name"`
	Vertical string `json:"vertical"`
	Location string `json:"location"`
}

// Input is the per-call input to Run.
type Input struct {
	// Domain is the lowercased domain — used as half the cache key.
	Domain string
	// Business is the discovered metadata for the homepage owner.
	Business Business
	// Technical is the cheap pre-audit result (iter 2.1). The qualitative
	// model uses this for context (so it doesn't have to spend tokens
	// re-deriving "site is on HTTP" etc.).
	Technical technical.Result
	// HTMLExcerpt is the stripped homepage body text (no tags). Up to
	// MaxHTMLExcerptBytes; longer inputs are truncated.
	HTMLExcerpt string
}

// Run invokes prompts.AuditQualitativeV1 for the given input, with the
// cache key built per spec. capUSD is the per-stage budget cap from
// PipelineSettings.Budgets.AuditUSD (sourced via pkg/killswitch.CapUSD).
//
// Returns the parsed AuditV1. Errors flow up from bedrock.InvokeStructured
// (cache miss → cost-cap exceeded → schema-validation failure →
// post-validator failure). The audit Lambda treats any error as DLQ-worthy.
func Run(ctx context.Context, in Input, capUSD float64) (schemas.AuditV1, error) {
	if in.Domain == "" {
		return schemas.AuditV1{}, errors.New("qualitative: Domain is required (used as cache key)")
	}

	excerpt := truncateBytes(in.HTMLExcerpt, MaxHTMLExcerptBytes)
	cacheKey := bedrock.CacheKey(
		prompts.AuditQualitativeV1.ID,
		prompts.HashInputs(in.Domain, excerpt),
	)

	user, err := buildUserMessage(in, excerpt)
	if err != nil {
		return schemas.AuditV1{}, err
	}

	return prompts.Invoke(
		ctx,
		prompts.AuditQualitativeV1,
		[]bedrock.Message{{Role: "user", Content: user}},
		capUSD,
		cacheKey,
	)
}

// buildUserMessage assembles the user-turn content. Inputs go in XML-ish
// blocks (matches the pattern used by spec.v1 / email.v1 prompts later)
// so the model knows where each input starts and ends and we can swap
// pieces without re-tuning the prompt body.
func buildUserMessage(in Input, excerpt string) (string, error) {
	bizJSON, err := json.Marshal(in.Business)
	if err != nil {
		return "", err
	}
	techJSON, err := json.Marshal(in.Technical)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(prompts.WrapBlock("business", string(bizJSON)))
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("technical", string(techJSON)))
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("html_excerpt", excerpt))
	return b.String(), nil
}

// truncateBytes cuts s to at most n bytes. Byte-truncation is fine here
// because the result feeds into a hash and a Bedrock message — neither
// cares about UTF-8 boundary safety. A mid-rune cut at the 8KB mark would
// look weird in the prompt but won't break anything.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
