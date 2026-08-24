package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestPropertyUpdatePreservesOtherProperties(t *testing.T) {
	a := analyzeExpr(t, `.n |= 1`, controlFlowObjectSchema())
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if !schemaAdmitsInteger(requireObjectProperty(t, a.Output, "n"), 1) {
		t.Fatalf("updated n must admit 1: %s", schemaTypeSummary(a.Output, 4))
	}
	tags := requireObjectProperty(t, a.Output, "tags")
	if !schemaAdmitsString(arrayElementUnion(tags, DefaultOptions()), "x") {
		t.Fatalf("update discarded tags: %s", schemaTypeSummary(a.Output, 4))
	}
}

// The |= desugaring's trailing delete accumulator is populated per branch,
// and its shared allocation cardinality cannot prove that EVERY represented
// execution appended a path, so the executor applies these deletes weakly:
// n must stop being required, but its value schema may survive as optional.
// Asserting full removal would require an unsound strong delete (see
// `.n |= if . then empty else 0 end`, where n survives on one branch).
func TestEmptyPropertyUpdateDeletesProperty(t *testing.T) {
	a := analyzeExpr(t, `.n |= empty`, controlFlowObjectSchema())
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if isRequired(a.Output.Required, "n") {
		t.Fatalf("empty update left n required: %s", schemaTypeSummary(a.Output, 4))
	}
	_ = requireObjectProperty(t, a.Output, "tags")
}

func TestDeletePropertyRemovesOnlyTarget(t *testing.T) {
	a := analyzeExpr(t, `del(.n)`, controlFlowObjectSchema())
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if a.Output.Properties != nil {
		if _, ok := a.Output.Properties.Get("n"); ok {
			t.Fatalf("del retained n: %s", schemaTypeSummary(a.Output, 4))
		}
	}
	_ = requireObjectProperty(t, a.Output, "tags")
}

func TestMapUpdateReplacesArrayItems(t *testing.T) {
	in := controlFlowObjectSchema()
	before := schemaFingerprint(in)
	a := analyzeExpr(t, `.tags |= map(1)`, in)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	tags := requireObjectProperty(t, a.Output, "tags")
	items := arrayElementUnion(tags, DefaultOptions())
	if !schemaAdmitsInteger(items, 1) {
		t.Fatalf("mapped items must admit 1: %s", schemaTypeSummary(a.Output, 4))
	}
	if schemaFingerprint(in) != before {
		t.Fatal("map update mutated the input schema")
	}
}

func TestWithEntriesValueUpdateProducesStringValues(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"count": IntegerType(),
	}, []string{"count"})
	a := analyzeExpr(t, `with_entries(.value |= tostring)`, in)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if a.Output == nil || getType(a.Output) != "object" {
		t.Fatalf("output is not an object: %s", schemaTypeSummary(a.Output, 4))
	}
	values := resolvedLeft(a.Output.AdditionalProperties)
	if values == nil || getType(values) != "string" {
		t.Fatalf("additional property values must be strings: %s", schemaTypeSummary(a.Output, 4))
	}
}
