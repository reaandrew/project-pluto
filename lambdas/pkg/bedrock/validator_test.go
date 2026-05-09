package bedrock

import (
	"encoding/json"
	"errors"
	"testing"
)

type rejectingValidator struct {
	called int
	err    error
}

func (r *rejectingValidator) Validate(_, _ json.RawMessage) error {
	r.called++
	return r.err
}

func TestSetValidatorOverridesAndResetsToNop(t *testing.T) {
	t.Cleanup(func() { SetValidator(nil) })

	r := &rejectingValidator{err: errors.New("nope")}
	SetValidator(r)

	if err := currentValidator().Validate(nil, nil); err == nil {
		t.Error("expected rejecting validator to reject, got nil")
	}
	if r.called != 1 {
		t.Errorf("validator called %d times, want 1", r.called)
	}

	SetValidator(nil)
	if err := currentValidator().Validate(nil, nil); err != nil {
		t.Errorf("default nop validator should accept, got %v", err)
	}
}

func TestNopValidatorIsDefault(t *testing.T) {
	// fresh process: default validator is nop
	if err := currentValidator().Validate(nil, nil); err != nil {
		t.Errorf("default validator rejected: %v", err)
	}
}
