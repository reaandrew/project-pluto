package prompts

// SafetyRulesBlock is the standing safety-rules excerpt every prompt embeds in
// its system message verbatim, per .ralph/specs/07-bedrock-prompts.md § Common
// conventions. The content paraphrases .ralph/specs/10-quality-rules.md — the
// non-negotiable list operators rely on.
//
// When 10-quality-rules.md changes, update this constant and bump every
// affected prompt's version (e.g. audit.qualitative.v1 → v2). The bump forces
// cache invalidation across all consumers.
const SafetyRulesBlock = `<safety_rules>
- Never invent business facts you cannot see: testimonials, awards, certifications, prices, or staff names.
- Never claim that a private preview is published, that the recipient asked for it, or that they have an account with us.
- Never use the word "password" in user-facing copy. Use "access code" or "code".
- Never make medical, legal, or financial claims about the business unless the same words appear verbatim in the source data.
- Never write copy that pressures, fakes urgency, or implies prior contact.
- If you cannot verify a fact from the provided inputs, omit it. Silence is always safer than fabrication.
</safety_rules>`
