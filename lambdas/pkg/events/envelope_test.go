package events

import (
	"encoding/json"
	"testing"
	"time"
)

type sampleDetail struct {
	BusinessID string `json:"businessId"`
	Domain     string `json:"domain"`
}

func TestNewSetsRequiredFields(t *testing.T) {
	d := sampleDetail{BusinessID: "b-1", Domain: "acme.test"}
	env := New("business.found", "discovery", d)

	if env.EventID == "" {
		t.Fatal("eventId not generated")
	}
	if env.EventName != "business.found" {
		t.Errorf("eventName = %q", env.EventName)
	}
	if env.EmittedBy != "discovery" {
		t.Errorf("emittedBy = %q", env.EmittedBy)
	}
	if env.EventVersion != CurrentEventVersion {
		t.Errorf("eventVersion = %d, want %d", env.EventVersion, CurrentEventVersion)
	}
	if env.CorrelationID != env.EventID {
		t.Errorf("correlationId should default to eventId, got %q vs %q", env.CorrelationID, env.EventID)
	}
	if env.CausationID != "" {
		t.Errorf("causationId should default empty, got %q", env.CausationID)
	}
	if time.Since(env.EmittedAt) > time.Second {
		t.Errorf("emittedAt looks stale: %v", env.EmittedAt)
	}
	if env.Detail != d {
		t.Errorf("detail not set: %#v", env.Detail)
	}
}

func TestWithCorrelationAndCausationAreImmutable(t *testing.T) {
	a := New("x", "svc", sampleDetail{})
	b := a.WithCorrelation("corr-1").WithCausation("cause-1")

	if a.CorrelationID == "corr-1" || a.CausationID == "cause-1" {
		t.Fatal("WithCorrelation/Causation mutated original envelope")
	}
	if b.CorrelationID != "corr-1" || b.CausationID != "cause-1" {
		t.Fatalf("setters did not apply: %+v", b)
	}
}

func TestValidate(t *testing.T) {
	good := New("x", "svc", sampleDetail{})
	if err := good.Validate(); err != nil {
		t.Fatalf("expected valid, got %v", err)
	}

	cases := map[string]func(e *Envelope[sampleDetail]){
		"missing eventId":       func(e *Envelope[sampleDetail]) { e.EventID = "" },
		"missing eventName":     func(e *Envelope[sampleDetail]) { e.EventName = "" },
		"zero eventVersion":     func(e *Envelope[sampleDetail]) { e.EventVersion = 0 },
		"zero emittedAt":        func(e *Envelope[sampleDetail]) { e.EmittedAt = time.Time{} },
		"missing emittedBy":     func(e *Envelope[sampleDetail]) { e.EmittedBy = "" },
		"missing correlationID": func(e *Envelope[sampleDetail]) { e.CorrelationID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			env := New("x", "svc", sampleDetail{})
			mutate(&env)
			if err := env.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	original := New("business.found", "discovery", sampleDetail{BusinessID: "b-1", Domain: "acme.test"}).
		WithCorrelation("corr-1").
		WithCausation("cause-1")

	b, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed Envelope[sampleDetail]
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed.EventID != original.EventID || parsed.CorrelationID != "corr-1" || parsed.CausationID != "cause-1" {
		t.Errorf("round-trip drift: %+v", parsed)
	}
	if parsed.Detail.BusinessID != "b-1" || parsed.Detail.Domain != "acme.test" {
		t.Errorf("detail not round-tripped: %+v", parsed.Detail)
	}
}

func TestCausationIDOmittedWhenEmpty(t *testing.T) {
	env := New("x", "svc", sampleDetail{})
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); contains(got, "causationId") {
		t.Errorf("causationId should be omitted when empty, got %s", got)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
