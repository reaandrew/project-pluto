// Package prompts hosts the project's versioned Bedrock prompt templates per
// .ralph/specs/07-bedrock-prompts.md. One file per prompt-version named
// `<name>_v<n>.go` (e.g. audit_qualitative_v1.go), each declaring a single
// exported `Prompt[T]` value. Consumer Lambdas call:
//
//	out, err := prompts.Invoke(ctx, prompt, messages, capUSD, cacheKey)
//
// which runs bedrock.InvokeStructured AND the prompt's PostValidate hook so
// neither step can be silently skipped. Apply is the low-level helper that
// just builds the bedrock.InvokeInput; prefer Invoke.
//
// The schema for the tool's output (T) is computed at package init via
// schemas.MustJSONSchemaFor[T] — fast-fail at cold start beats a stream of
// per-call errors when a bad struct ships.
package prompts

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// Prompt bundles a versioned prompt template with everything Bedrock needs.
// T is the Go type the tool_use response unmarshals into.
type Prompt[T any] struct {
	// ID is the canonical promptId (e.g. "audit.qualitative.v1"). Cache
	// rows + cost-ledger entries both reference this.
	ID string
	// ModelID is the Bedrock model this prompt is designed for. Use the
	// constants from pkg/bedrock (ModelHaiku45, ModelSonnet46).
	ModelID string
	// System is the system message. Typically embeds SafetyRulesBlock and
	// any per-prompt fragments (style-guide, tone, etc.).
	System string
	// ToolName is the forced tool name (Bedrock tool_choice = tool).
	ToolName string
	// MaxTokens caps the response. Sized per-prompt; no global default.
	MaxTokens int
	// Stage threads through to bedrock.InvokeInput → cost.WithCostCap.
	Stage bedrock.Stage
	// EstimateUSD is the pre-call cost estimate used by cost.Assert.
	EstimateUSD float64
	// CacheTTL is how long a cached response stays fresh. Zero falls back
	// to bedrock.DefaultCacheTTL (30d).
	CacheTTL time.Duration

	// PostValidate is run on the unmarshalled tool-use output after schema
	// validation passes. Use it for rules JSON Schema can't express — for
	// example email.v1's "body must contain previewUrl exactly once",
	// spec.v1's "no testimonial-shaped sections", or "wordCount <= 200".
	// A non-nil error fails Invoke and the consumer Lambda routes the
	// event to DLQ. Optional — set nil when JSON Schema covers everything.
	PostValidate func(T) error

	// Schema is filled by New[T]; do not set directly.
	Schema json.RawMessage
}

// New finalises a Prompt[T] by populating its Schema field via
// schemas.MustJSONSchemaFor[T]. Call once at package init for each prompt:
//
//	var AuditQualitativeV1 = prompts.New(prompts.Prompt[schemas.AuditV1]{
//	    ID: "audit.qualitative.v1", ...
//	})
//
// Validates required fields and panics on misconfiguration so a bad prompt
// fails Lambda cold-start instead of every InvokeStructured call.
func New[T any](p Prompt[T]) Prompt[T] {
	switch {
	case p.ID == "":
		panic("prompts.New: ID required")
	case p.ModelID == "":
		panic("prompts.New: ModelID required")
	case p.ToolName == "":
		panic("prompts.New: ToolName required")
	case p.MaxTokens <= 0:
		panic("prompts.New: MaxTokens must be > 0")
	case p.Stage == "":
		panic("prompts.New: Stage required")
	case p.EstimateUSD <= 0:
		panic("prompts.New: EstimateUSD must be > 0 (zero defeats cost.Assert)")
	}
	p.Schema = schemas.MustJSONSchemaFor[T]()
	return p
}

// Invoke is the canonical caller entry: runs bedrock.InvokeStructured AND
// the prompt's PostValidate hook in one call. Consumer Lambdas should always
// use Invoke rather than calling Apply + bedrock.InvokeStructured separately
// — that way no caller can silently skip the post-validator a prompt
// declares.
//
//	out, err := prompts.Invoke(ctx, AuditQualitativeV1, msgs, settings.AuditCap, cacheKey)
//
// Errors propagate from Bedrock (network, schema, validator) or from
// PostValidate (post-schema rules). The handler treats either as DLQ-worthy.
func Invoke[T any](
	ctx context.Context,
	p Prompt[T],
	messages []bedrock.Message,
	capUSD float64,
	cacheKey string,
) (T, error) {
	var zero T
	in := Apply(p, messages, capUSD, cacheKey)
	out, err := bedrock.InvokeStructured(ctx, in)
	if err != nil {
		return zero, err
	}
	if p.PostValidate != nil {
		if err := p.PostValidate(out); err != nil {
			return zero, fmt.Errorf("prompts: %s post-validation: %w", p.ID, err)
		}
	}
	return out, nil
}

// Apply assembles a bedrock.InvokeInput ready for InvokeStructured. Most
// callers should use Invoke instead — Apply does NOT run PostValidate, so
// it's only appropriate for tests or unusual paths that intentionally
// bypass post-validation.
//
// `messages` is the caller-built user/assistant turn list. `capUSD` is the
// per-stage budget cap (sourced from PipelineSettings via pkg/killswitch
// in iter 0.E.9). `cacheKey` is the already-hashed deterministic key the
// caller computed per the prompt's caching policy.
func Apply[T any](p Prompt[T], messages []bedrock.Message, capUSD float64, cacheKey string) bedrock.InvokeInput[T] {
	return bedrock.InvokeInput[T]{
		ModelID:     p.ModelID,
		PromptID:    p.ID,
		System:      p.System,
		Messages:    messages,
		ToolName:    p.ToolName,
		ToolSchema:  p.Schema,
		Stage:       p.Stage,
		EstimateUSD: p.EstimateUSD,
		CapUSD:      capUSD,
		CacheKey:    cacheKey,
		CacheTTL:    p.CacheTTL,
		MaxTokens:   p.MaxTokens,
	}
}

// HashInputs is the canonical helper for building the input-hash portion of
// a cache key. It writes each segment with a separator so distinct inputs
// can never collide via concatenation. The result is a hex SHA-256 string
// suitable for passing through bedrock.CacheKey(promptID, inputHash).
//
// Example (audit.qualitative.v1):
//
//	cacheKey := bedrock.CacheKey(prompts.AuditQualitativeV1.ID,
//	    prompts.HashInputs(domain, htmlExcerpt))
func HashInputs(segments ...string) string {
	h := sha256.New()
	for i, s := range segments {
		if i > 0 {
			_, _ = h.Write([]byte{0x1f}) // unit separator — never legal in our inputs
		}
		_, _ = h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// WrapBlock wraps text in an XML-ish block tag, used to compose the System
// message from reusable fragments (e.g. WrapBlock("style_guide", styleText)).
// Returns an empty string when text is empty so optional blocks fold out
// without leaving stray empty tags in the prompt.
func WrapBlock(tag, text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "<" + tag + ">\n" + text + "\n</" + tag + ">"
}
