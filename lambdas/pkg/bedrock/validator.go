package bedrock

import (
	"encoding/json"
	"sync"
)

// Validator validates a tool-use payload against its declared schema. The
// concrete implementation lives in pkg/schemas (iter 0.E.6) — a JSON-Schema
// validator built from Go structs. Until that lands, the default Validator is
// nopValidator (always passes). Lambdas wire the real one at startup:
//
//	bedrock.SetValidator(schemas.NewValidator())
type Validator interface {
	Validate(schema, payload json.RawMessage) error
}

// nopValidator is the default — accepts any payload. Iter 0.E.6 replaces it.
type nopValidator struct{}

func (nopValidator) Validate(_, _ json.RawMessage) error { return nil }

var (
	validatorMu sync.RWMutex
	validator   Validator = nopValidator{}
)

// SetValidator overrides the package-level validator. Pass nil to reset to the
// no-op default. Intended for Lambda startup wiring + tests.
func SetValidator(v Validator) {
	validatorMu.Lock()
	defer validatorMu.Unlock()
	if v == nil {
		validator = nopValidator{}
		return
	}
	validator = v
}

// currentValidator returns the validator the next InvokeStructured call will use.
func currentValidator() Validator {
	validatorMu.RLock()
	defer validatorMu.RUnlock()
	return validator
}
