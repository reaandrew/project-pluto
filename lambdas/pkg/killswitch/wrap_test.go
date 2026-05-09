package killswitch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// installCapturingLogger swaps the package-level logger lookup for a
// JSON-handler logger that writes into the returned buffer.
func installCapturingLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := loggerFromContext
	loggerFromContext = func(_ context.Context) *slog.Logger {
		return slog.New(slog.NewJSONHandler(buf, nil))
	}
	t.Cleanup(func() { loggerFromContext = prev })
	return buf
}

// --- happy path ---------------------------------------------------------

func TestWithKillSwitchInvokesFnWhenAllowed(t *testing.T) {
	fake, _ := reset(t)
	fake.getOut = itemFor(t, Defaults())

	called := false
	err := WithKillSwitch(context.Background(), StageAudit, func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithKillSwitch returned err: %v", err)
	}
	if !called {
		t.Fatal("fn was not invoked when stage was enabled")
	}
}

func TestWithKillSwitchSurfacesFnError(t *testing.T) {
	fake, _ := reset(t)
	fake.getOut = itemFor(t, Defaults())

	want := errors.New("audit blew up")
	got := WithKillSwitch(context.Background(), StageAudit, func(_ context.Context) error {
		return want
	})
	if !errors.Is(got, want) {
		t.Fatalf("err=%v, want %v", got, want)
	}
}

// --- skipped path -------------------------------------------------------

func TestWithKillSwitchSkipsWhenStageDisabled(t *testing.T) {
	fake, _ := reset(t)
	off := Defaults()
	off.Stages.AuditEnabled = false
	fake.getOut = itemFor(t, off)

	logBuf := installCapturingLogger(t)

	called := false
	err := WithKillSwitch(context.Background(), StageAudit, func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithKillSwitch returned err: %v", err)
	}
	if called {
		t.Fatal("fn was invoked when stage was disabled")
	}

	// Verify the skipped_killed log line was emitted with the right shape.
	line := logBuf.String()
	if !strings.Contains(line, `"msg":"pipeline.skipped_killed"`) {
		t.Errorf("expected skipped_killed msg, got: %s", line)
	}
	if !strings.Contains(line, `"stage":"audit"`) {
		t.Errorf("expected stage=audit, got: %s", line)
	}
	if !strings.Contains(line, `"metric":"pipeline.audit.skipped_killed"`) {
		t.Errorf("expected metric=pipeline.audit.skipped_killed, got: %s", line)
	}
}

func TestWithKillSwitchSkipsWhenMasterDisabled(t *testing.T) {
	fake, _ := reset(t)
	off := Defaults()
	off.PipelineEnabled = false
	fake.getOut = itemFor(t, off)
	installCapturingLogger(t)

	called := false
	err := WithKillSwitch(context.Background(), StageAudit, func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("WithKillSwitch returned err: %v", err)
	}
	if called {
		t.Fatal("fn was invoked when master kill switch was off")
	}
}

// --- error path ---------------------------------------------------------

func TestWithKillSwitchSurfacesGetError(t *testing.T) {
	fake, _ := reset(t)
	fake.getErr = errors.New("ddb hiccup")

	called := false
	err := WithKillSwitch(context.Background(), StageAudit, func(_ context.Context) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected error from failed pre-flight, got nil")
	}
	if called {
		t.Fatal("fn must not be invoked when the pre-flight check fails")
	}
	if !strings.Contains(err.Error(), "killswitch: pre-flight check for stage \"audit\"") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestWithKillSwitchRejectsUnknownStage(t *testing.T) {
	fake, _ := reset(t)
	fake.getOut = itemFor(t, Defaults())

	called := false
	err := WithKillSwitch(context.Background(), "no-such-stage", func(_ context.Context) error {
		called = true
		return nil
	})
	if err == nil {
		t.Fatal("expected error for unknown stage, got nil")
	}
	if called {
		t.Fatal("fn was invoked despite unknown stage")
	}
}

// --- cancellation propagation ------------------------------------------

func TestWithKillSwitchPassesContextToFn(t *testing.T) {
	fake, _ := reset(t)
	fake.getOut = itemFor(t, Defaults())

	type ctxKey string
	const key ctxKey = "trace"
	parent := context.WithValue(context.Background(), key, "abc")

	var got string
	err := WithKillSwitch(parent, StageAudit, func(c context.Context) error {
		got, _ = c.Value(key).(string)
		return nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != "abc" {
		t.Errorf("ctx value did not propagate: got %q, want %q", got, "abc")
	}
}

// Sanity: the JSON log line is well-formed (catches future formatter regressions).
func TestSkippedKilledLogIsValidJSON(t *testing.T) {
	fake, _ := reset(t)
	off := Defaults()
	off.Stages.OutreachEnabled = false
	off.PipelineEnabled = true // master ON, only outreach OFF
	fake.getOut = itemFor(t, off)

	logBuf := installCapturingLogger(t)

	if err := WithKillSwitch(context.Background(), StageOutreach, func(_ context.Context) error {
		return nil
	}); err != nil {
		t.Fatalf("err: %v", err)
	}

	line := strings.TrimSpace(logBuf.String())
	var parsed map[string]any
	if err := json.Unmarshal([]byte(line), &parsed); err != nil {
		t.Fatalf("log line is not valid JSON: %v\nline=%s", err, line)
	}
	if parsed["msg"] != "pipeline.skipped_killed" {
		t.Errorf("msg=%v", parsed["msg"])
	}
	if parsed["stage"] != "outreach" {
		t.Errorf("stage=%v", parsed["stage"])
	}
	if parsed["metric"] != "pipeline.outreach.skipped_killed" {
		t.Errorf("metric=%v", parsed["metric"])
	}
}
