package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestDepthExhaustionWithUnrelatedArrayAccumulatorWidensOutput(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
		"n":    IntegerType(),
	}, []string{"tags", "n"})

	a := analyzeExpr(t, `[.tags] as $x | 1 | recurse(. + 1) | select(. > 150)`, input)
	if a.Verdict != VerdictUnverifiable || !mightBeType(a.Output, oas3.SchemaTypeInteger) {
		t.Fatalf("depth-exhausted direct output must widen: verdict=%s output=%s causes=%v",
			a.Verdict, schemaTypeSummary(a.Output, 4), a.Causes)
	}
}

func TestDepthExhaustionInsideArrayGeneratorPreservesArrayLengthType(t *testing.T) {
	a := analyzeExpr(t, `[1 | recurse(. + 1)] | length`, NullType())
	if a.Verdict != VerdictProven || !MightBeNumber(a.Output) {
		t.Fatalf("array length must remain proven numeric: verdict=%s output=%s causes=%v",
			a.Verdict, schemaTypeSummary(a.Output, 4), a.Causes)
	}
}

func TestDepthExhaustionInsideArrayGeneratorWidensFirstItem(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"n": IntegerType()}, []string{"n"})
	a := analyzeExpr(t, `[.n | recurse(. + 1)] | first`, input)
	if a.Verdict == VerdictProven || !mightBeType(a.Output, oas3.SchemaTypeInteger) {
		t.Fatalf("first item must admit depth-widened integers: verdict=%s output=%s causes=%v",
			a.Verdict, schemaTypeSummary(a.Output, 4), a.Causes)
	}
}
