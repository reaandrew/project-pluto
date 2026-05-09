package schemas_test

import (
	"testing"

	"github.com/reaandrew/ai-website-agency/lambdas/pkg/bedrock"
	"github.com/reaandrew/ai-website-agency/lambdas/pkg/schemas"
)

// TestValidatorSatisfiesBedrockValidator verifies the structural-typing
// contract documented in pkg/bedrock/validator.go: a *schemas.Validator can
// be passed to bedrock.SetValidator without an explicit interface assertion
// in the call site.
func TestValidatorSatisfiesBedrockValidator(t *testing.T) {
	v := schemas.NewValidator()
	// If this compiles, the contract holds. Reset on cleanup.
	bedrock.SetValidator(v)
	t.Cleanup(func() { bedrock.SetValidator(nil) })
}
