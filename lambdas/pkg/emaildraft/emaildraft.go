// Package emaildraft wraps prompts.EmailV1 (Haiku 4.5) with the
// input-shape, message-assembly, cache-key, and post-validation
// conventions in .ralph/specs/07-bedrock-prompts.md § "Prompt:
// email.v1 (Haiku)". Mirrors pkg/spec.
//
// Two things the spec is explicit about:
//
//   - Cache key is (promptId, sha256(business.id + websiteId +
//     contact.id + tone.version)). The passcode is NEVER part of the
//     key or the model input — the model emits the {{PASSCODE}}
//     placeholder and the email-draft Lambda substitutes the
//     KMS-decrypted cleartext afterwards. So the same business/website/
//     contact/tone hits the cache regardless of passcode rotation.
//
//   - The system message embeds the per-vertical EmailToneProfile in an
//     <email_tone> block alongside the base SafetyRulesBlock — composed
//     per-call rather than baked into the package-level Prompt[T].
//
// Run also runs the context-dependent post-validator
// (schemas.ValidateEmailV1Content) — prompts.Invoke only runs the
// intrinsic ValidateEmailV1Structural hook, so the preview-URL /
// opt-out / prohibited-phrase / invented-fact checks live here where
// the per-call context is available. The returned body still carries
// the {{PASSCODE}} placeholder; the caller substitutes the cleartext.
package emaildraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/prompts"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/tone"
)

// Business is the subset the email prompt sees.
type Business struct {
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Vertical string `json:"vertical"`
	Location string `json:"location"`
}

// Contact is the subset the email prompt sees. Empty when contact
// enrichment hasn't run yet (iter 6.x) — the model falls back to a
// generic opener from the tone profile's openerPatterns.
type Contact struct {
	FirstName string `json:"firstName,omitempty"`
	Role      string `json:"role,omitempty"`
}

// AuditSummary motivates the "address this specific issue" framing.
type AuditSummary struct {
	Score   int    `json:"score"`
	Summary string `json:"summary"`
}

// Input is the per-call input to Run.
type Input struct {
	// BusinessID / WebsiteID / ContactID are cache-key segments only —
	// the model never sees them.
	BusinessID string
	WebsiteID  string
	ContactID  string
	// PreviewURL is the public preview link; appears in the body
	// exactly once (enforced by ValidateEmailV1Content).
	PreviewURL string
	Business   Business
	Contact    Contact
	Audit      AuditSummary
	// Tone is composed into the system message as
	// <email_tone>…</email_tone>; its Version is part of the cache
	// key so a bumped/tuned profile invalidates the cached draft.
	Tone tone.Profile
}

// Run invokes prompts.EmailV1 for the given input. capUSD is the
// per-stage budget cap (PipelineSettings.Budgets.DailyEmailUsd via
// pkg/killswitch.CapUSD(StageOutreach)).
//
// Returns the parsed EmailV1 with the {{PASSCODE}} placeholder still in
// the body — the email-draft Lambda substitutes the KMS-decrypted
// cleartext. Errors propagate from bedrock.InvokeStructured (cost cap,
// schema, intrinsic post-validator) and from the context-dependent
// post-validator; the Lambda treats any error as DLQ-worthy.
func Run(ctx context.Context, in Input, capUSD float64) (schemas.EmailV1, error) {
	if in.BusinessID == "" || in.WebsiteID == "" {
		return schemas.EmailV1{}, errors.New("emaildraft: BusinessID and WebsiteID are required (cache key)")
	}
	if in.PreviewURL == "" {
		return schemas.EmailV1{}, errors.New("emaildraft: PreviewURL is required")
	}
	if err := in.Tone.Validate(); err != nil {
		return schemas.EmailV1{}, fmt.Errorf("emaildraft: invalid tone profile: %w", err)
	}

	cacheKey := bedrock.CacheKey(
		prompts.EmailV1.ID,
		prompts.HashInputs(in.BusinessID, in.WebsiteID, in.ContactID, strconv.Itoa(in.Tone.Version)),
	)

	system := composeSystem(in.Tone)
	user, err := buildUserMessage(in)
	if err != nil {
		return schemas.EmailV1{}, err
	}

	// Per-call system message: copy the package-level prompt and
	// override System so prompts.Invoke (and the intrinsic
	// PostValidate hook) fires through the normal framework path.
	perCall := prompts.EmailV1
	perCall.System = system

	out, err := prompts.Invoke(ctx, perCall,
		[]bedrock.Message{{Role: "user", Content: user}}, capUSD, cacheKey)
	if err != nil {
		return schemas.EmailV1{}, err
	}

	// Context-dependent rules (preview URL once, opt-out verbatim,
	// per-vertical prohibited phrases, invented-fact patterns). Runs on
	// the placeholder body — checks {{PASSCODE}}, never cleartext.
	if err := schemas.ValidateEmailV1Content(out, schemas.EmailV1Context{
		PreviewURL:        in.PreviewURL,
		OptOutLine:        in.Tone.OptOutLine,
		ProhibitedPhrases: in.Tone.ProhibitedPhrases,
	}); err != nil {
		return schemas.EmailV1{}, fmt.Errorf("emaildraft: content post-validation: %w", err)
	}
	return out, nil
}

// composeSystem builds the per-call system message:
//
//	<prompt prefix + safety rules>
//	<email_tone>…</email_tone>
func composeSystem(p tone.Profile) string {
	tJSON, _ := json.Marshal(p)
	var b strings.Builder
	b.WriteString(prompts.EmailSystemPrefix)
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("email_tone", string(tJSON)))
	return b.String()
}

// buildUserMessage assembles the user-turn content in XML-ish blocks so
// the model can identify each input without ambiguity. The preview URL
// is its own block so the model copies it verbatim.
func buildUserMessage(in Input) (string, error) {
	bizJSON, err := json.Marshal(in.Business)
	if err != nil {
		return "", err
	}
	contactJSON, err := json.Marshal(in.Contact)
	if err != nil {
		return "", err
	}
	auditJSON, err := json.Marshal(in.Audit)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString(prompts.WrapBlock("business", string(bizJSON)))
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("contact", string(contactJSON)))
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("audit", string(auditJSON)))
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("preview_url", in.PreviewURL))
	return b.String(), nil
}
