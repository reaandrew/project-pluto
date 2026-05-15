package schemas

import (
	"fmt"
	"regexp"
	"strings"
)

// EmailV1 is the tool-use payload shape for the `email.v1` Bedrock
// prompt (Haiku 4.5). Mirrors the `produceEmailDraft` tool schema in
// .ralph/specs/07-bedrock-prompts.md verbatim — keep them in lockstep.
//
// The model writes the access code as the literal token "{{PASSCODE}}"
// (NOT the real passcode): the email-draft Lambda (iter 7.2b) caches
// the body keyed by the passcode HASH and substitutes the cleartext at
// the very end. This keeps the cleartext out of the cache, the model
// call, logs, and snapshot fixtures.
type EmailV1 struct {
	Subject   string `json:"subject" jsonschema:"required,maxLength=80"`
	Body      string `json:"body" jsonschema:"required,maxLength=1500"`
	WordCount int    `json:"wordCount" jsonschema:"required,minimum=60,maximum=200"`
}

// PasscodePlaceholder is the exact token the model must emit where the
// access code goes. The email-draft Lambda substitutes the KMS-decrypted
// cleartext for this token after the (placeholder-keyed) cache write.
const PasscodePlaceholder = "{{PASSCODE}}"

// ValidateEmailV1Structural enforces the intrinsic email.v1 rules JSON
// Schema can't express — those that depend ONLY on the model output
// (not per-call context). Wired to prompts.EmailV1.PostValidate so it
// runs on every Bedrock call. Context-dependent rules (preview URL,
// opt-out line, per-vertical prohibited phrases) are in
// ValidateEmailV1Content, which the email-draft Lambda runs with
// runtime context.
func ValidateEmailV1Structural(e EmailV1) error {
	if strings.TrimSpace(e.Subject) == "" {
		return fmt.Errorf("email: subject is empty")
	}
	if strings.TrimSpace(e.Body) == "" {
		return fmt.Errorf("email: body is empty")
	}
	if e.WordCount > 200 {
		return fmt.Errorf("email: wordCount %d exceeds 200", e.WordCount)
	}
	if e.WordCount < 60 {
		return fmt.Errorf("email: wordCount %d below minimum 60", e.WordCount)
	}
	// "password" is banned — we say "access code" / "code". Recipients
	// don't have an account with us.
	if strings.Contains(strings.ToLower(e.Body), "password") ||
		strings.Contains(strings.ToLower(e.Subject), "password") {
		return fmt.Errorf("email: contains banned word 'password' (use 'access code')")
	}
	// The access code MUST be the placeholder, exactly once, so the
	// Lambda can substitute the cleartext post-cache. Zero occurrences
	// means the model omitted the code (or invented one); more than one
	// means substitution would duplicate it.
	//
	// This is the project's reading of 07-bedrock-prompts.md § email.v1
	// post-validation rule (c) ("body must contain the literal passcode
	// exactly once"): pre-substitution the "literal passcode" is the
	// {{PASSCODE}} placeholder — reconciled with that section's own
	// caching paragraph, which mandates the placeholder so cleartext
	// never reaches the model, cache, or logs. The post-substitution
	// "real cleartext exactly once" invariant is the email-draft
	// Lambda's responsibility (iter 7.2b), since only it holds the
	// cleartext.
	if n := strings.Count(e.Body, PasscodePlaceholder); n != 1 {
		return fmt.Errorf("email: body must contain %s exactly once, found %d", PasscodePlaceholder, n)
	}
	return nil
}

// EmailV1Context is the per-call data the context-dependent
// post-validation needs. Supplied by the email-draft Lambda (iter
// 7.2b) from the Website + EmailToneProfile.
type EmailV1Context struct {
	PreviewURL        string
	OptOutLine        string
	ProhibitedPhrases []string
}

// inventedFactPatterns mirrors .ralph/specs/10-quality-rules.md § Rule 1
// — the email post-validator rejects fabricated specifics unless the
// operator added them by hand (the Lambda passes through operator
// edits before re-validating).
var inventedFactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\d+\s*%`),
	regexp.MustCompile(`(?i)since\s+\d{4}`),
	regexp.MustCompile(`(?i)\d+\s+(years?|customers?|clients?)`),
	regexp.MustCompile(`(?i)\b(guaranteed|award|award-winning|winner|best-in-class|industry-leading|world-class)\b`),
}

// ValidateEmailV1Content enforces the context-dependent email.v1 rules
// from 07-bedrock-prompts.md § email.v1 post-validation + 10-quality-rules
// Rule 1/3. Runs on the model output BEFORE the {{PASSCODE}}
// substitution, so it checks the placeholder, not the cleartext.
func ValidateEmailV1Content(e EmailV1, ctx EmailV1Context) error {
	if err := ValidateEmailV1Structural(e); err != nil {
		return err
	}
	if ctx.PreviewURL == "" {
		return fmt.Errorf("email: context previewURL is required")
	}
	if n := strings.Count(e.Body, ctx.PreviewURL); n != 1 {
		return fmt.Errorf("email: body must contain the preview URL exactly once, found %d", n)
	}
	if strings.TrimSpace(ctx.OptOutLine) == "" {
		return fmt.Errorf("email: context optOutLine is required")
	}
	if !strings.Contains(e.Body, ctx.OptOutLine) {
		return fmt.Errorf("email: body must contain the opt-out line verbatim")
	}
	lowerBody := strings.ToLower(e.Body)
	for _, phrase := range ctx.ProhibitedPhrases {
		if strings.TrimSpace(phrase) == "" {
			continue
		}
		if strings.Contains(lowerBody, strings.ToLower(phrase)) {
			return fmt.Errorf("email: body contains prohibited phrase %q", phrase)
		}
	}
	for _, re := range inventedFactPatterns {
		if loc := re.FindString(e.Body); loc != "" {
			return fmt.Errorf("email: body contains invented-fact pattern %q (10-quality-rules Rule 1)", loc)
		}
	}
	return nil
}
