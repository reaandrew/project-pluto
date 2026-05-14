// Package spec wraps prompts.SpecV1 with the input-shape,
// message-assembly, and cache-key conventions specified in
// .ralph/specs/07-bedrock-prompts.md § "Prompt: spec.v1 (Sonnet)".
//
// Two pieces the spec is explicit about:
//
//   - The cache key is (promptId, sha256(business.id + audit.id +
//     verticalStyleGuide.version)). Run builds it via
//     bedrock.CacheKey(prompts.SpecV1.ID, prompts.HashInputs(...)).
//     Bumping the style-guide version invalidates the cached spec
//     automatically.
//
//   - The system message embeds the per-vertical style guide in a
//     <style_guide> block alongside the base SafetyRulesBlock — Run
//     composes the final system message per-call rather than baking
//     a single vertical's guide into the Prompt[T] at package init.
package spec

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
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/style"
)

// Business is the subset the spec prompt looks at.
type Business struct {
	Name     string `json:"name"`
	Domain   string `json:"domain"`
	Vertical string `json:"vertical"`
	Location string `json:"location"`
}

// AuditSummary is the subset of the AuditRow the spec prompt sees —
// the model uses these to motivate its design choices ("you said
// mobile is broken — Hero has a mobile-first CTA"). Kept narrow so
// large audit fields (raw HTML hash, etc.) don't bloat the prompt.
type AuditSummary struct {
	Score      int      `json:"score"`
	Summary    string   `json:"summary"`
	IssueTypes []string `json:"issueTypes"`
}

// ExtractedContent is the structured data the audit Lambda extracts
// from the source site (services list, contact details, hours). The
// prompt instruction is explicit that the model uses this rather
// than inventing facts. Empty fields just become missing JSON keys.
type ExtractedContent struct {
	Services []string `json:"services,omitempty"`
	Phone    string   `json:"phone,omitempty"`
	Email    string   `json:"email,omitempty"`
	Address  string   `json:"address,omitempty"`
	Hours    string   `json:"hours,omitempty"`
}

// Input is the per-call input to Run.
type Input struct {
	// BusinessID is half the cache key. The model never sees it.
	BusinessID string
	// AuditID is the other half. The model never sees it.
	AuditID string
	// Business is the metadata the model sees.
	Business Business
	// AuditSummary feeds the model's "address this issue" framing.
	AuditSummary AuditSummary
	// StyleGuide is composed into the system message as
	// <style_guide>…</style_guide>; its Version is part of the
	// cache key so a bumped guide invalidates the cached spec.
	StyleGuide style.Guide
	// ExtractedContent is the structured data pulled from the
	// source site so the model doesn't invent it.
	ExtractedContent ExtractedContent
}

// Run invokes prompts.SpecV1 for the given input. capUSD is the
// per-stage budget cap (PipelineSettings.Budgets.DailyBedrockUsd
// sourced via pkg/killswitch.CapUSD).
//
// Returns the parsed SpecV1. Errors propagate from bedrock.InvokeStructured
// (cost cap, schema, post-validator). The spec-generator Lambda treats
// any error as DLQ-worthy.
func Run(ctx context.Context, in Input, capUSD float64) (schemas.SpecV1, error) {
	if in.BusinessID == "" {
		return schemas.SpecV1{}, errors.New("spec: BusinessID is required (cache key)")
	}
	if in.AuditID == "" {
		return schemas.SpecV1{}, errors.New("spec: AuditID is required (cache key)")
	}
	if err := in.StyleGuide.Validate(); err != nil {
		return schemas.SpecV1{}, fmt.Errorf("spec: invalid style guide: %w", err)
	}

	cacheKey := bedrock.CacheKey(
		prompts.SpecV1.ID,
		prompts.HashInputs(in.BusinessID, in.AuditID, strconv.Itoa(in.StyleGuide.Version)),
	)

	// Compose the per-call system message: base prefix + safety
	// rules (embedded in prefix) + style guide block.
	system := composeSystem(in.StyleGuide)

	user, err := buildUserMessage(in)
	if err != nil {
		return schemas.SpecV1{}, err
	}

	// Per-call system message: copy the package-level prompt and
	// override System so prompts.Invoke (and therefore the
	// PostValidate hook) fires through the normal framework path.
	// Using Apply + manual PostValidate would silently drop
	// validation if the manual call were ever removed.
	perCall := prompts.SpecV1
	perCall.System = system

	return prompts.Invoke(ctx, perCall,
		[]bedrock.Message{{Role: "user", Content: user}}, capUSD, cacheKey)
}

// composeSystem builds the per-call system message:
//
//	<prompt prefix + safety rules>
//	<style_guide>…</style_guide>
func composeSystem(g style.Guide) string {
	gJSON, _ := json.Marshal(g)
	var b strings.Builder
	b.WriteString(prompts.SpecSystemPrefix)
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("style_guide", string(gJSON)))
	return b.String()
}

// buildUserMessage assembles the user-turn content in XML-ish blocks
// so the model can identify each input without ambiguity.
func buildUserMessage(in Input) (string, error) {
	bizJSON, err := json.Marshal(in.Business)
	if err != nil {
		return "", err
	}
	auditJSON, err := json.Marshal(in.AuditSummary)
	if err != nil {
		return "", err
	}
	extractedJSON, err := json.Marshal(in.ExtractedContent)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString(prompts.WrapBlock("business", string(bizJSON)))
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("audit", string(auditJSON)))
	b.WriteString("\n")
	b.WriteString(prompts.WrapBlock("extracted_content", string(extractedJSON)))
	return b.String(), nil
}
