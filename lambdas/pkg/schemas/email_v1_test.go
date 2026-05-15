package schemas

import (
	"encoding/json"
	"strings"
	"testing"
)

func validEmailV1() EmailV1 {
	return EmailV1{
		Subject: "Quick redesign preview for Acme Accountants",
		Body: "Hi Jane,\n\nYour homepage buries the services list below the fold.\n\n" +
			"I mocked up a private preview: https://previews.example.com/sites/web-1\n" +
			"Use access code {{PASSCODE}} to open it.\n\n" +
			"Reply 'no thanks' and I won't follow up.\n\nAndrew",
		WordCount: 70,
	}
}

func validEmailCtx() EmailV1Context {
	return EmailV1Context{
		PreviewURL:        "https://previews.example.com/sites/web-1",
		OptOutLine:        "Reply 'no thanks' and I won't follow up.",
		ProhibitedPhrases: []string{"industry-leading", "as you requested"},
	}
}

func TestEmailV1Schema_IsValidJSONAndRoundTrips(t *testing.T) {
	v := NewValidator()
	raw := MustJSONSchemaFor[EmailV1]()
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("reflected EmailV1 schema is not valid JSON: %v", err)
	}
	body, err := json.Marshal(validEmailV1())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := v.Validate(raw, body); err != nil {
		t.Errorf("realistic EmailV1 rejected by reflected schema: %v", err)
	}
	var rt EmailV1
	if err := json.Unmarshal(body, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if rt != validEmailV1() {
		t.Errorf("round-trip drift: %+v", rt)
	}
}

func TestEmailV1Schema_RejectsMissingAndOversize(t *testing.T) {
	v := NewValidator()
	raw := MustJSONSchemaFor[EmailV1]()
	// Missing required field.
	m := map[string]any{"subject": "x", "body": "y"} // no wordCount
	b, _ := json.Marshal(m)
	if err := v.Validate(raw, b); err == nil {
		t.Error("expected schema rejection for missing wordCount")
	}
	// wordCount out of range.
	bad := validEmailV1()
	bad.WordCount = 999
	b, _ = json.Marshal(bad)
	if err := v.Validate(raw, b); err == nil {
		t.Error("expected schema rejection for wordCount 999 (>200)")
	}
}

func TestValidateEmailV1Structural_AcceptsValid(t *testing.T) {
	if err := ValidateEmailV1Structural(validEmailV1()); err != nil {
		t.Errorf("valid email rejected: %v", err)
	}
}

func TestValidateEmailV1Structural_Rejects(t *testing.T) {
	cases := map[string]func(*EmailV1){
		"empty subject":      func(e *EmailV1) { e.Subject = " " },
		"empty body":         func(e *EmailV1) { e.Body = "" },
		"wordCount over 200": func(e *EmailV1) { e.WordCount = 250 },
		"wordCount under 60": func(e *EmailV1) { e.WordCount = 10 },
		"password in body":   func(e *EmailV1) { e.Body = strings.ReplaceAll(e.Body, "access code", "password") },
		"no placeholder":     func(e *EmailV1) { e.Body = strings.ReplaceAll(e.Body, "{{PASSCODE}}", "1234") },
		"two placeholders":   func(e *EmailV1) { e.Body += " {{PASSCODE}}" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEmailV1()
			mutate(&e)
			if err := ValidateEmailV1Structural(e); err == nil {
				t.Errorf("expected rejection (%s)", name)
			}
		})
	}
}

func TestValidateEmailV1Content_AcceptsValid(t *testing.T) {
	if err := ValidateEmailV1Content(validEmailV1(), validEmailCtx()); err != nil {
		t.Errorf("valid email+ctx rejected: %v", err)
	}
}

func TestValidateEmailV1Content_Rejects(t *testing.T) {
	cases := map[string]struct {
		mutate    func(*EmailV1)
		mutateCtx func(*EmailV1Context)
	}{
		"preview url missing from body": {
			mutateCtx: func(c *EmailV1Context) { c.PreviewURL = "https://other.example.com/x" },
		},
		"preview url twice": {
			mutate: func(e *EmailV1) {
				e.Body += "\nagain https://previews.example.com/sites/web-1"
			},
		},
		"opt-out line missing": {
			mutate: func(e *EmailV1) {
				e.Body = strings.ReplaceAll(e.Body, "Reply 'no thanks' and I won't follow up.", "")
			},
		},
		"prohibited phrase present": {
			mutate: func(e *EmailV1) {
				e.Body = strings.ReplaceAll(e.Body, "mocked up", "this industry-leading rebuild")
			},
		},
		"invented percentage": {
			mutate: func(e *EmailV1) {
				e.Body = strings.ReplaceAll(e.Body, "below the fold", "loses 40% of visitors")
			},
		},
		"invented since-year": {
			mutate: func(e *EmailV1) {
				e.Body = strings.ReplaceAll(e.Body, "below the fold", "unchanged since 2011")
			},
		},
		"invented client count": {
			mutate: func(e *EmailV1) {
				e.Body = strings.ReplaceAll(e.Body, "below the fold", "ignored by 500 customers")
			},
		},
		"invented guarantee/award": {
			mutate: func(e *EmailV1) {
				e.Body = strings.ReplaceAll(e.Body, "below the fold", "missing your award-winning guaranteed results")
			},
		},
		"empty ctx previewURL": {
			mutateCtx: func(c *EmailV1Context) { c.PreviewURL = "" },
		},
		"empty ctx optOutLine": {
			mutateCtx: func(c *EmailV1Context) { c.OptOutLine = "" },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := validEmailV1()
			ctx := validEmailCtx()
			if tc.mutate != nil {
				tc.mutate(&e)
			}
			if tc.mutateCtx != nil {
				tc.mutateCtx(&ctx)
			}
			if err := ValidateEmailV1Content(e, ctx); err == nil {
				t.Errorf("expected rejection (%s)", name)
			}
		})
	}
}
