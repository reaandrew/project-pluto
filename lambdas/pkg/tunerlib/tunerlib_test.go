package tunerlib

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/cost"
)

func fixedNow() time.Time { return time.Date(2026, 5, 16, 2, 0, 0, 0, time.UTC) }

type capture struct {
	deltas []TunerDelta
	events []DeltaDetail
}

func baseDeps(c *capture, fb map[string][]FeedbackRow) Deps {
	return Deps{
		Fetch: func(_ context.Context, vertical, _ string, _ map[string]bool) ([]FeedbackRow, error) {
			return fb[vertical], nil
		},
		Profile:  func(context.Context, string) (json.RawMessage, error) { return json.RawMessage(`{"version":3}`), nil },
		PutDelta: func(_ context.Context, x TunerDelta) error { c.deltas = append(c.deltas, x); return nil },
		Publish:  func(_ context.Context, e DeltaDetail) error { c.events = append(c.events, e); return nil },
		Now:      fixedNow,
	}
}

func cfg(c *capture, proposeErr error, users *[]string) Config {
	return Config{
		Consumer: "tuner-style", Kind: "style", PromptID: "tuner.style.v1",
		Verticals: []string{"default", "accountants"},
		Subjects:  []string{"spec", "website"}, SinceDays: 7,
		Propose: func(_ context.Context, vertical, user string) ([]byte, string, error) {
			if users != nil {
				*users = append(*users, user)
			}
			if proposeErr != nil {
				return nil, "", proposeErr
			}
			return []byte(`{"addDoPhrases":["lead with fee"],"rationale":"r"}`), "r-" + vertical, nil
		},
	}
}

func TestRun_ProposesPerVerticalWithFeedback(t *testing.T) {
	c := &capture{}
	users := &[]string{}
	d := baseDeps(c, map[string][]FeedbackRow{
		"default":     {{Subject: "spec", Action: "edit", CreatedAt: "2026-05-15T10:00:00Z"}},
		"accountants": {}, // no feedback → skipped, no delta
	})
	if err := Run(context.Background(), d, cfg(c, nil, users)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.deltas) != 1 || len(c.events) != 1 {
		t.Fatalf("want 1 delta+event (accountants skipped), got %d/%d", len(c.deltas), len(c.events))
	}
	got := c.deltas[0]
	if got.PK != "DELTA#style#default" || got.Kind != "style" || got.Status != "pending" ||
		got.GSI1PK != "DELTA#STATUS#pending" || got.PromptID != "tuner.style.v1" ||
		got.Rationale != "r-default" {
		t.Errorf("delta drift: %+v", got)
	}
	if c.events[0].DeltaID != got.ID || c.events[0].Kind != "style" {
		t.Errorf("event drift: %+v", c.events[0])
	}
	// The currentProfile + feedbackBatch must reach the model.
	if len(*users) != 1 || !strings.Contains((*users)[0], `"currentProfile":{"version":3}`) ||
		!strings.Contains((*users)[0], `"feedbackBatch":`) {
		t.Errorf("user message drift: %v", *users)
	}
}

func TestRun_NoFeedback_NoProposal(t *testing.T) {
	c := &capture{}
	d := baseDeps(c, map[string][]FeedbackRow{})
	if err := Run(context.Background(), d, cfg(c, nil, nil)); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(c.deltas) != 0 || len(c.events) != 0 {
		t.Error("no feedback ⇒ no deltas or events")
	}
}

func TestRun_BudgetCap_SkipsWeekNoError(t *testing.T) {
	c := &capture{}
	d := baseDeps(c, map[string][]FeedbackRow{
		"default": {{Subject: "spec"}},
	})
	if err := Run(context.Background(), d, cfg(c, cost.ErrBudgetCapExceeded, nil)); err != nil {
		t.Fatalf("budget cap must not error the weekly job: %v", err)
	}
	if len(c.deltas) != 0 {
		t.Error("cap hit ⇒ no delta written")
	}
}

func TestRun_ProposeError_Propagates(t *testing.T) {
	c := &capture{}
	d := baseDeps(c, map[string][]FeedbackRow{"default": {{Subject: "spec"}}})
	err := Run(context.Background(), d, cfg(c, errors.New("bedrock 500"), nil))
	if err == nil {
		t.Fatal("a non-cap propose error must fail the run (so the schedule retries)")
	}
}

func TestWeekID_StablePerWeekKindVertical(t *testing.T) {
	a := weekID(fixedNow(), "default", "style")
	if a != weekID(fixedNow(), "default", "style") {
		t.Error("weekID must be stable for a same-week retry (overwrite, not duplicate)")
	}
	if a == weekID(fixedNow(), "accountants", "style") || a == weekID(fixedNow(), "default", "targeting") {
		t.Error("weekID must vary by vertical and kind")
	}
}
