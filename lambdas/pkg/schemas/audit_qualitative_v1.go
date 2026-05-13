package schemas

// AuditV1 is the tool-use payload shape for the `audit.qualitative.v1`
// Bedrock prompt (Haiku 4.5). Mirrors the `recordAudit` tool schema in
// .ralph/specs/07-bedrock-prompts.md verbatim — keep them in lockstep.
//
// `Score` runs 0..100, higher = better; `WorthRedesigning` is the model's
// boolean call that's used downstream as one input to the qualifier
// (iter 3). `Issues` is capped at 8 — anything beyond that and the model
// is editorialising, not reviewing.
type AuditV1 struct {
	Score            int          `json:"score" jsonschema:"required,minimum=0,maximum=100"`
	WorthRedesigning bool         `json:"worthRedesigning" jsonschema:"required"`
	Summary          string       `json:"summary" jsonschema:"required,maxLength=400"`
	Issues           []AuditIssue `json:"issues" jsonschema:"required,maxItems=8"`
}

// AuditIssue is a single problem the audit identifies. The `Type` and
// `Severity` enums are the closed sets the qualifier (iter 3) discriminates
// on — adding values requires bumping the prompt version (v1 → v2) so the
// cache is invalidated.
type AuditIssue struct {
	Type        string `json:"type" jsonschema:"required,enum=conversion,enum=design,enum=performance,enum=mobile,enum=trust,enum=seo,enum=accessibility"`
	Severity    string `json:"severity" jsonschema:"required,enum=low,enum=medium,enum=high"`
	Description string `json:"description" jsonschema:"required,maxLength=200"`
}
