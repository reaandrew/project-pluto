package killswitch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestTerraformSeedMatchesDefaults parses the heredoc inside
// terraform/pipeline-settings.tf, unmarshals the DDB-typed JSON into a
// Settings, and asserts it equals Defaults(). This is the contract guard
// the file's comment promises: when 05-capacity-and-cost.md changes, BOTH
// Defaults() and the seed must update together; this test fails if they
// drift.
func TestTerraformSeedMatchesDefaults(t *testing.T) {
	tfPath := terraformSeedPath(t)
	raw, err := os.ReadFile(tfPath)
	if err != nil {
		t.Fatalf("reading %s: %v", tfPath, err)
	}

	itemJSON := extractItemJSON(t, string(raw))
	got := unmarshalDDBJSON(t, itemJSON)

	want := Defaults()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("seed drifted from Defaults().\n got: %+v\nwant: %+v", got, want)
	}
}

// terraformSeedPath returns the absolute path to terraform/pipeline-settings.tf
// regardless of how `go test` was invoked (from lambdas/, from the repo root,
// or from the package dir).
func terraformSeedPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	// climb up to the repo root by looking for the .ralph/ directory
	dir := wd
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".ralph")); err == nil {
			return filepath.Join(dir, "terraform", "pipeline-settings.tf")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not find repo root above %s", wd)
	return ""
}

// extractItemJSON pulls the heredoc body out of the .tf file. Looks for
// `item = <<-ITEM` ... `ITEM` (or non-indented `<<ITEM`).
var itemHeredocRE = regexp.MustCompile(`(?s)item\s*=\s*<<-?ITEM\s*(.+?)\s*ITEM`)

func extractItemJSON(t *testing.T, tf string) string {
	t.Helper()
	m := itemHeredocRE.FindStringSubmatch(tf)
	if len(m) < 2 {
		t.Fatal("could not find `item = <<-ITEM ... ITEM` heredoc in pipeline-settings.tf")
	}
	return m[1]
}

// unmarshalDDBJSON parses DDB-typed JSON ({"S": "..."} / {"N": "..."} /
// {"BOOL": true} / {"M": {...}}) into a Settings. We don't pull in the
// full DDB attributevalue package here because the test runs without AWS
// SDK initialization; a small custom decoder is enough for this shape.
func unmarshalDDBJSON(t *testing.T, ddbJSON string) Settings {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal([]byte(ddbJSON), &raw); err != nil {
		t.Fatalf("seed JSON not valid: %v\n%s", err, ddbJSON)
	}
	plain := unwrapDDB(t, raw).(map[string]any)
	// re-marshal as plain JSON, then unmarshal into Settings — saves writing
	// a second reflection pass.
	plainJSON, err := json.Marshal(plain)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	var s Settings
	if err := json.Unmarshal(plainJSON, &s); err != nil {
		t.Fatalf("unmarshal Settings: %v\n%s", err, plainJSON)
	}
	return s
}

// unwrapDDB walks a parsed DDB-typed JSON tree and returns the equivalent
// plain Go value: {"S": "x"} → "x"; {"N": "5"} → 5 (or 0.6); {"BOOL": true}
// → true; {"M": {...}} → map[string]any (recursing); top-level objects are
// passed through unchanged at the outer level.
func unwrapDDB(t *testing.T, v any) any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	// DDB attribute-value envelope has exactly one key (S/N/BOOL/M/L/etc.).
	if len(m) == 1 {
		for k, inner := range m {
			switch k {
			case "S":
				return inner
			case "BOOL":
				return inner
			case "N":
				s, _ := inner.(string)
				if strings.Contains(s, ".") {
					f, err := strconv.ParseFloat(s, 64)
					if err != nil {
						t.Fatalf("bad N value %q: %v", s, err)
					}
					return f
				}
				n, err := strconv.Atoi(s)
				if err != nil {
					t.Fatalf("bad N value %q: %v", s, err)
				}
				return n
			case "M":
				inner := inner.(map[string]any)
				out := make(map[string]any, len(inner))
				for k2, v2 := range inner {
					out[k2] = unwrapDDB(t, v2)
				}
				return out
			}
		}
	}
	// top-level: walk children
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = unwrapDDB(t, v)
	}
	return out
}
