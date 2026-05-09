// Package schemas implements the two halves of the project's tool-use
// contract from .ralph/specs/stdlib/json-output-conventions.md:
//
//   - JSONSchemaFor[T]() generates a JSON Schema from a Go struct (using
//     invopop/jsonschema reflection over `json:` and `jsonschema:` tags).
//     The returned schema is what callers pass to bedrock.InvokeStructured
//     as the tool's input_schema, forcing the model to produce output that
//     fits the Go type.
//
//   - Validator validates a tool-use payload against its declared schema
//     (using santhosh-tekuri/jsonschema). It satisfies bedrock.Validator
//     structurally, so Lambda startups wire it via:
//
//     bedrock.SetValidator(schemas.NewValidator())
//
// Schemas themselves (SpecV1, AuditV1, etc.) live in this package and grow
// as later iterations land. This file holds only the generation +
// validation infrastructure.
package schemas

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/invopop/jsonschema"
	jsonschema5 "github.com/santhosh-tekuri/jsonschema/v5"
)

// JSONSchemaFor reflects over T to produce a JSON Schema document. The
// output is `{"type":"object", "properties":{...}, "additionalProperties":
// false, "required":[...]}` — the strict shape Anthropic's tool-use API
// expects as `input_schema`.
//
// Conventions baked in by Reflector config:
//   - inline `$defs` (no top-level `$ref` indirection in the result)
//   - additionalProperties: false everywhere
//   - required-list driven by `jsonschema:"required"` tags (not all-fields-required)
//   - omits the `$schema` and `$id` headers — Bedrock's tool-use schema
//     wants the bare object shape.
func JSONSchemaFor[T any]() (json.RawMessage, error) {
	r := &jsonschema.Reflector{
		DoNotReference:             true,
		RequiredFromJSONSchemaTags: true,
		Anonymous:                  true,
	}
	var zero T
	schema := r.Reflect(zero)
	if schema == nil {
		return nil, errors.New("schemas: reflector returned nil schema (T must be a struct)")
	}
	// Strip $schema and $id from the root: tool-use input_schema doesn't
	// expect them and some clients reject unknown top-level keys.
	schema.Version = ""
	schema.ID = ""
	return json.Marshal(schema)
}

// MustJSONSchemaFor is the panic-on-error variant of JSONSchemaFor. Use only
// at package init for static schemas (the panic surface gives you a fast
// failure on cold start instead of every InvokeStructured call returning the
// same error).
func MustJSONSchemaFor[T any]() json.RawMessage {
	raw, err := JSONSchemaFor[T]()
	if err != nil {
		panic(fmt.Errorf("schemas.MustJSONSchemaFor: %w", err))
	}
	return raw
}

// Validator validates tool-use payloads against their declared JSON Schema.
// Compiled schemas are cached (keyed on the schema's canonical-JSON string)
// so the same schema is parsed once per warm Lambda container.
type Validator struct {
	mu    sync.Mutex
	cache map[string]*jsonschema5.Schema
}

// NewValidator returns a Validator with an empty cache. Safe for concurrent
// use after construction.
func NewValidator() *Validator {
	return &Validator{cache: make(map[string]*jsonschema5.Schema)}
}

// Validate parses payload as JSON and validates it against schema. Returns
// nil on success, an error describing the violation(s) on failure. Schema
// compilation errors and JSON parse errors are also returned as errors.
//
// This signature matches bedrock.Validator structurally — callers wire the
// validator at Lambda startup with `bedrock.SetValidator(schemas.NewValidator())`.
func (v *Validator) Validate(schema, payload json.RawMessage) error {
	if len(schema) == 0 {
		return errors.New("schemas: empty schema")
	}
	if len(payload) == 0 {
		return errors.New("schemas: empty payload")
	}
	compiled, err := v.getOrCompile(schema)
	if err != nil {
		return err
	}
	var doc any
	if err := json.Unmarshal(payload, &doc); err != nil {
		return fmt.Errorf("schemas: parsing payload: %w", err)
	}
	if err := compiled.Validate(doc); err != nil {
		return fmt.Errorf("schemas: %w", err)
	}
	return nil
}

func (v *Validator) getOrCompile(schema json.RawMessage) (*jsonschema5.Schema, error) {
	key := string(schema)
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.cache[key]; ok {
		return s, nil
	}
	s, err := jsonschema5.CompileString("inline.json", key)
	if err != nil {
		return nil, fmt.Errorf("schemas: compiling schema: %w", err)
	}
	v.cache[key] = s
	return s, nil
}

// SchemaContains is a tiny helper for tests + ops scripts that need to
// assert "this generated schema mentions field X". Returns true if the
// canonical JSON of the schema contains the substring s. Not for validation
// logic — use Validator for that.
func SchemaContains(schema json.RawMessage, s string) bool {
	return strings.Contains(string(schema), s)
}
