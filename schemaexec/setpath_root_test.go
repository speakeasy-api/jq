package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// jq: setpath([]; v) replaces the whole input with v. The empty path tuple
// ({type: array, maxItems: 0}) must extract as one EMPTY path — not as "no
// paths" (which used to fall into a dynamic-property sentinel that kept the
// input object alive).
func TestSetpathEmptyPathReplacesRoot(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"a": IntegerType()}, []string{"a"})
	a := analyzeExpr(t, `setpath([]; 1)`, input)
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf("verdict = %s, want live output (causes: %v)", a.Verdict, a.Causes)
	}
	if !schemaAdmitsInteger(a.Output, 1) {
		t.Fatalf("output must admit 1: %s", schemaTypeSummary(a.Output, 4))
	}
	// Crucially, the result must NOT still be the input object shape.
	if getType(a.Output) == "object" {
		if _, ok := a.Output.Properties.Get("a"); ok {
			t.Fatalf("setpath([]; 1) returned the input object instead of the value: %s",
				schemaTypeSummary(a.Output, 4))
		}
	}
}

func TestSetpathEmptyPathOnEmptyObject(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{}, nil)
	a := analyzeExpr(t, `setpath([]; {x: 1})`, input)
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf("verdict = %s, want live output (causes: %v)", a.Verdict, a.Causes)
	}
	if getType(a.Output) != "object" {
		t.Fatalf("output must be the value object: %s", schemaTypeSummary(a.Output, 4))
	}
	x := requireObjectProperty(t, a.Output, "x")
	if !schemaAdmitsInteger(x, 1) {
		t.Fatalf("output .x must admit 1: %s", schemaTypeSummary(a.Output, 4))
	}
}

// jq: getpath([]) is identity. getpath shares extractPathsFromSchema with
// setpath, so the empty-tuple fix must make it navigate zero segments and
// return the input, not widen to unverifiable.
func TestGetpathEmptyPathIsIdentity(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"a": IntegerType()}, []string{"a"})
	a := analyzeExpr(t, `getpath([])`, input)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if getType(a.Output) != "object" {
		t.Fatalf("getpath([]) must return the input: %s", schemaTypeSummary(a.Output, 4))
	}
	prop := requireObjectProperty(t, a.Output, "a")
	if getType(prop) != "integer" {
		t.Fatalf("getpath([]) must preserve .a: %s", schemaTypeSummary(a.Output, 4))
	}
}
