package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func controlFlowObjectSchema() *oas3.Schema {
	three := int64(3)
	tags := BuildArray(nil, []*oas3.Schema{
		ConstString("x"),
		ConstString("y"),
		ConstString("z"),
	})
	tags.MinItems = &three
	tags.MaxItems = &three
	return BuildObject(map[string]*oas3.Schema{
		"a":    ConstString("A"),
		"b":    ConstString("B"),
		"n":    IntegerType(),
		"tags": tags,
	}, []string{"a", "b", "n", "tags"})
}

func requireObjectProperty(t *testing.T, schema *oas3.Schema, name string) *oas3.Schema {
	t.Helper()
	if schema == nil || getType(schema) != "object" || schema.Properties == nil {
		t.Fatalf("output is not an object with properties: %s", schemaTypeSummary(schema, 4))
	}
	property, ok := schema.Properties.Get(name)
	if !ok {
		t.Fatalf("output has no %q property: %s", name, schemaTypeSummary(schema, 4))
	}
	return resolvedLeft(property)
}

func TestLabeledBreakOverapproximatesQueuedAlternatives(t *testing.T) {
	a := analyzeExpr(t, `label $out | (.a, break $out, .b)`, controlFlowObjectSchema())
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf("verdict = ProvenBroken (causes: %v)", a.Causes)
	}
	if !schemaAdmitsString(a.Output, "A") {
		t.Fatalf("output must admit the value before break: %s", schemaTypeSummary(a.Output, 4))
	}
	// The independently queued .b branch is retained as a sound over-approximation.
	if !schemaAdmitsString(a.Output, "B") {
		t.Fatalf("output must admit the queued alternative after break: %s", schemaTypeSummary(a.Output, 4))
	}
}

func TestFirstOverArrayIterationReturnsAnItem(t *testing.T) {
	a := analyzeExpr(t, `first(.tags[])`, controlFlowObjectSchema())
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("first over a non-empty array has a live output")
	}
	if !schemaAdmitsString(a.Output, "x") {
		t.Fatalf("first output must admit an array item: %s", schemaTypeSummary(a.Output, 4))
	}
}

func TestLimitOverArrayIterationCollectsItems(t *testing.T) {
	a := analyzeExpr(t, `[limit(2; .tags[])]`, controlFlowObjectSchema())
	if a.Verdict == VerdictProvenBroken || getType(a.Output) != "array" {
		t.Fatalf("limit output must be a live array: %s", schemaTypeSummary(a.Output, 4))
	}
	items := arrayElementUnion(a.Output, DefaultOptions())
	if !schemaAdmitsString(items, "x") || !schemaAdmitsString(items, "y") {
		t.Fatalf("limited array must admit source items: %s", schemaTypeSummary(a.Output, 4))
	}
}

func TestIsEmptyOverNonEmptyArrayIterationReturnsFalse(t *testing.T) {
	a := analyzeExpr(t, `isempty(.tags[])`, controlFlowObjectSchema())
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("isempty produces a boolean")
	}
	if getType(a.Output) != "boolean" {
		t.Fatalf("isempty output is not boolean: %s", schemaTypeSummary(a.Output, 4))
	}
	if value, ok := extractConstValue(a.Output); ok && value != false {
		t.Fatalf("isempty output must admit false: %s", schemaTypeSummary(a.Output, 4))
	}
}

func TestAnyOverArrayIterationAdmitsTrue(t *testing.T) {
	a := analyzeExpr(t, `any(.tags[]; . == "x")`, controlFlowObjectSchema())
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("any produces a boolean")
	}
	if value, ok := extractConstValue(a.Output); ok && value == false {
		t.Fatalf("any must admit true when an item can equal x: %s", schemaTypeSummary(a.Output, 4))
	}
	if !mightBeType(a.Output, oas3.SchemaTypeBoolean) {
		t.Fatalf("any output is not boolean: %s", schemaTypeSummary(a.Output, 4))
	}
}

func TestAllOverArrayIterationReturnsBoolean(t *testing.T) {
	a := analyzeExpr(t, `all(.tags[]; . == "x")`, controlFlowObjectSchema())
	if a.Verdict == VerdictProvenBroken || getType(a.Output) != "boolean" {
		t.Fatalf("all output must be a live boolean: %s", schemaTypeSummary(a.Output, 4))
	}
	if value, ok := extractConstValue(a.Output); ok && value != false {
		t.Fatalf("all must admit false for non-x items: %s", schemaTypeSummary(a.Output, 4))
	}
}

func TestNestedLabelsKeepBreakTargetsDistinct(t *testing.T) {
	a := analyzeExpr(t,
		`label $outer | ((label $inner | (.a, break $inner, .b)), .b)`,
		controlFlowObjectSchema(),
	)
	if !schemaAdmitsString(a.Output, "A") || !schemaAdmitsString(a.Output, "B") {
		t.Fatalf("inner break must retain the outer alternative: %s", schemaTypeSummary(a.Output, 4))
	}

	b := analyzeExpr(t,
		`label $outer | ((label $inner | (.a, break $outer, .b)), .b)`,
		controlFlowObjectSchema(),
	)
	if b.Verdict == VerdictProvenBroken || !schemaAdmitsString(b.Output, "A") {
		t.Fatalf("outer break must retain the value produced before it: %s", schemaTypeSummary(b.Output, 4))
	}
	// Queued alternatives belonging to both labels remain as a sound over-approximation.
	if !schemaAdmitsString(b.Output, "B") {
		t.Fatalf("output must admit the queued nested alternatives: %s", schemaTypeSummary(b.Output, 4))
	}
}

func TestBreakInReduceAndForeachBodiesKeepsEarlierOutput(t *testing.T) {
	for _, expr := range []string{
		`label $out | ("before", reduce [1][] as $x (0; break $out), "after")`,
		`label $out | ("before", foreach [1][] as $x (0; . + $x; break $out), "after")`,
	} {
		t.Run(expr, func(t *testing.T) {
			a := analyzeExpr(t, expr, controlFlowObjectSchema())
			if a.Verdict == VerdictProvenBroken || !schemaAdmitsString(a.Output, "before") {
				t.Fatalf("pre-break output was lost: %s", schemaTypeSummary(a.Output, 4))
			}
		})
	}
}

func TestBreakInsideTryIsNotLost(t *testing.T) {
	a := analyzeExpr(t,
		`try (label $out | ("before", break $out, "after")) catch "caught"`,
		controlFlowObjectSchema(),
	)
	if a.Verdict == VerdictProvenBroken || !schemaAdmitsString(a.Output, "before") {
		t.Fatalf("pre-break try output was lost: %s", schemaTypeSummary(a.Output, 4))
	}
}
