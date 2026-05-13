package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuditV1SchemaMirrorsSpec(t *testing.T) {
	raw, err := JSONSchemaFor[AuditV1]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	s := string(raw)
	// Must mention every top-level field + its constraint per 07-bedrock-prompts.md.
	for _, want := range []string{
		`"score"`, `"minimum":0`, `"maximum":100`,
		`"worthRedesigning"`, `"type":"boolean"`,
		`"summary"`, `"maxLength":400`,
		`"issues"`, `"maxItems":8`,
		// Item fields
		`"description"`, `"maxLength":200`,
		`"additionalProperties":false`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("schema missing %q\nfull schema: %s", want, s)
		}
	}
}

func TestAuditV1IssueEnums(t *testing.T) {
	raw, err := JSONSchemaFor[AuditV1]()
	if err != nil {
		t.Fatalf("JSONSchemaFor: %v", err)
	}
	// Walk the schema to find the enum lists on issues[].type and issues[].severity.
	var doc struct {
		Properties struct {
			Issues struct {
				Items struct {
					Properties struct {
						Type     struct{ Enum []string } `json:"type"`
						Severity struct{ Enum []string } `json:"severity"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"issues"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantTypes := []string{"conversion", "design", "performance", "mobile", "trust", "seo", "accessibility"}
	if !sameSet(doc.Properties.Issues.Items.Properties.Type.Enum, wantTypes) {
		t.Errorf("issues[].type enum drift\n got: %v\nwant: %v",
			doc.Properties.Issues.Items.Properties.Type.Enum, wantTypes)
	}
	wantSeverity := []string{"low", "medium", "high"}
	if !sameSet(doc.Properties.Issues.Items.Properties.Severity.Enum, wantSeverity) {
		t.Errorf("issues[].severity enum drift\n got: %v\nwant: %v",
			doc.Properties.Issues.Items.Properties.Severity.Enum, wantSeverity)
	}
}

func TestAuditV1ValidatorAcceptsRealisticPayload(t *testing.T) {
	schema, _ := JSONSchemaFor[AuditV1]()
	v := NewValidator()
	good := mustMarshal(t, AuditV1{
		Score:            42,
		WorthRedesigning: true,
		Summary:          "Mobile layout is broken; no clear CTA; performance is poor.",
		Issues: []AuditIssue{
			{Type: "mobile", Severity: "high", Description: "Viewport meta missing — layout breaks on phones."},
			{Type: "conversion", Severity: "medium", Description: "No phone or email above the fold."},
		},
	})
	if err := v.Validate(schema, good); err != nil {
		t.Errorf("expected valid payload, got %v", err)
	}
}

func TestAuditV1ValidatorRejectsBadEnumValue(t *testing.T) {
	schema, _ := JSONSchemaFor[AuditV1]()
	v := NewValidator()
	bad := json.RawMessage(`{
		"score": 50, "worthRedesigning": true, "summary": "x",
		"issues": [{"type": "ergonomics", "severity": "low", "description": "x"}]
	}`)
	if err := v.Validate(schema, bad); err == nil {
		t.Error("expected enum-violation error for issues[].type='ergonomics'")
	}
}

func TestAuditV1ValidatorRejectsTooManyIssues(t *testing.T) {
	schema, _ := JSONSchemaFor[AuditV1]()
	v := NewValidator()
	issues := make([]AuditIssue, 9)
	for i := range issues {
		issues[i] = AuditIssue{Type: "design", Severity: "low", Description: "x"}
	}
	bad := mustMarshal(t, AuditV1{
		Score: 50, WorthRedesigning: false, Summary: "x", Issues: issues,
	})
	if err := v.Validate(schema, bad); err == nil {
		t.Error("expected maxItems=8 violation for 9-issue payload")
	}
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(b))
	for _, s := range b {
		set[s] = struct{}{}
	}
	for _, s := range a {
		if _, ok := set[s]; !ok {
			return false
		}
	}
	return true
}
