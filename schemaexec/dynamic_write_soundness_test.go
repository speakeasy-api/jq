package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// These regression tests pin the soundness contract of dynamic (symbolic)
// writes: whenever the verdict is Proven, every concrete result of the query
// must validate against the output schema.

// A dynamic key write may collide with any declared property; the result must
// admit the overwritten outcome. Concrete: {k:"a", o:{a:1}} | .o | .[$k]="x"
// yields {a:"x"}.
func TestDynamicKeyWriteAdmitsDeclaredPropertyCollision(t *testing.T) {
	inner := BuildObject(map[string]*oas3.Schema{"a": NumberType()}, []string{"a"})
	input := BuildObject(map[string]*oas3.Schema{"k": StringType(), "o": inner}, []string{"k", "o"})
	analysis := analyzeWithOptions(t, `.k as $k | .o | .[$k] = "x"`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	if analysis.Output.Properties == nil {
		return
	}
	wrapper, ok := analysis.Output.Properties.Get("a")
	if !ok {
		return
	}
	if left := resolvedLeft(wrapper); !MightBeString(left) && !isTopSchema(left) {
		t.Fatalf("property a = %s cannot be string, but the dynamic key may be %q", schemaTypeSummary(left, 3), "a")
	}
}

// .[] |= f definitely rewrites every member value; stale declared property
// schemas must not survive. Concrete: {a:1} | .[] |= tostring yields {a:"1"}.
func TestObjectAllElementsUpdateRewritesDeclaredProperties(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"a": NumberType()}, []string{"a"})
	analysis := analyzeWithOptions(t, `.[] |= tostring`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		t.Fatalf("verdict=%s, want proven", analysis.Verdict)
	}
	wrapper, ok := analysis.Output.Properties.Get("a")
	if !ok {
		t.Fatalf("property a missing: %s", schemaTypeSummary(analysis.Output, 4))
	}
	if left := resolvedLeft(wrapper); !MightBeString(left) {
		t.Fatalf("property a = %s cannot be string after .[] |= tostring", schemaTypeSummary(left, 3))
	}
}

// A dynamic string path of unknown length must not be modeled as a depth-1
// write. Concrete: {} | setpath([$k,"b"]; 5) yields {<k>: {b: 5}}.
func TestDeepDynamicPathWidensWrittenValue(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"k": StringType()}, []string{"k"})
	analysis := analyzeWithOptions(t, `.k as $k | {} | setpath([$k, "b"]; 5)`, input, DefaultOptions())
	if analysis.Verdict == VerdictProvenBroken {
		t.Fatalf("verdict=%s for a query that always succeeds", analysis.Verdict)
	}
	ap := analysis.Output.AdditionalProperties
	if ap == nil {
		t.Fatalf("no additionalProperties; dynamic key value unrepresented: %s", schemaTypeSummary(analysis.Output, 5))
	}
	value, possible := schemaFacetValue(ap, DefaultOptions())
	if !possible {
		t.Fatalf("additionalProperties forbids extras but the write adds a key")
	}
	if !MightBeObject(value) && !isTopSchema(value) {
		t.Fatalf("AP = %s cannot admit the nested object {b:5}", schemaTypeSummary(value, 4))
	}
}

// reduce with dynamic keys stays precise: the common accumulator pattern must
// not regress to Top.
func TestReduceDynamicKeyAccumulatorStaysProven(t *testing.T) {
	entry := BuildObject(map[string]*oas3.Schema{"name": StringType(), "value": NumberType()}, []string{"name", "value"})
	input := ArrayType(entry)
	analysis := analyzeWithOptions(t, `reduce .[] as $c ({}; .[$c.name] = $c.value)`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven || getType(analysis.Output) != "object" {
		t.Fatalf("verdict=%s output=%s, want proven object", analysis.Verdict, schemaTypeSummary(analysis.Output, 4))
	}
}

// .[] |= empty deletes every key: nothing may stay required and count lower
// bounds must not survive. Concrete: {a:1} (minProperties 1) becomes {}.
func TestAllElementsEmptyUpdateDropsRequiredAndMinProperties(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"a": NumberType()}, []string{"a"})
	one := int64(1)
	input.MinProperties = &one
	analysis := analyzeWithOptions(t, `.[] |= empty`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	if len(analysis.Output.Required) > 0 {
		t.Fatalf("required=%v but concrete result is {}", analysis.Output.Required)
	}
	if analysis.Output.MinProperties != nil && *analysis.Output.MinProperties > 0 {
		t.Fatalf("minProperties=%d but concrete result is {}", *analysis.Output.MinProperties)
	}
}

// uniqueItems is invalidated by element writes. Concrete: [1,2] | .[] |= 0
// yields [0,0].
func TestElementWriteDropsUniqueItems(t *testing.T) {
	input := ArrayType(NumberType())
	unique := true
	input.UniqueItems = &unique
	analysis := analyzeWithOptions(t, `.[] |= 0`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	if analysis.Output.UniqueItems != nil && *analysis.Output.UniqueItems {
		t.Fatalf("uniqueItems survived an element write: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

// A body that only MAY yield empty must not delete strongly. Concrete:
// n=true -> {}, n=false -> {"n":0}; both outcomes must validate.
func TestConditionalEmptyUpdateKeepsValueOptional(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"n": {Type: oas3.NewTypeFromString(oas3.SchemaTypeBoolean)}}, []string{"n"})
	analysis := analyzeWithOptions(t, `.n |= if . then empty else 0 end`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	if isRequired(analysis.Output.Required, "n") {
		t.Fatalf("n still required, but n=true concretely deletes it: %s", schemaTypeSummary(analysis.Output, 4))
	}
	wrapper, ok := analysis.Output.Properties.Get("n")
	if !ok {
		if analysis.Output.AdditionalProperties == nil {
			t.Fatalf("n absent and object closed, but n=false concretely keeps n=0")
		}
		return
	}
	if left := resolvedLeft(wrapper); !MightBeNumber(left) && !isTopSchema(left) {
		t.Fatalf("n = %s cannot be 0", schemaTypeSummary(left, 3))
	}
}
