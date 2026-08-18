package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func analyzeExprWithOptions(t *testing.T, expr string, input *oas3.Schema, opts SchemaExecOptions) *Analysis {
	t.Helper()
	q, err := parseQuery(expr)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(context.Background(), q, input, opts)
	if err != nil {
		t.Fatalf("Analyze(%q): %v", expr, err)
	}
	return a
}

func parseQuery(expr string) (*gojq.Query, error) {
	return gojq.Parse(expr)
}

func exactStringIntegerTuple() *oas3.Schema {
	two := int64(2)
	in := BuildArray(nil, []*oas3.Schema{StringType(), IntegerType()})
	in.MinItems = &two
	in.MaxItems = &two
	return in
}

func assertArrayIndexAdmitsInteger(t *testing.T, schema *oas3.Schema, index int, want int64) {
	t.Helper()
	if schema == nil || getType(schema) != "array" {
		t.Fatalf("output is not an array: %s", schemaTypeSummary(schema, 3))
	}
	elem := getArrayElement(schema, index, DefaultOptions())
	if !schemaAdmitsInteger(elem, want) {
		t.Fatalf("index %d must admit integer %d: %s", index, want, schemaTypeSummary(schema, 3))
	}
}

func TestSetpathNumericIndexWidensArrayItems(t *testing.T) {
	for _, tc := range []struct {
		name     string
		expr     string
		wantNull bool
	}{
		{name: "replace first", expr: `setpath([0]; "x")`},
		{name: "extend with padding", expr: `setpath([2]; "x")`, wantNull: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := analyzeExpr(t, tc.expr, ArrayType(IntegerType()))
			if a.Verdict == VerdictProvenBroken {
				t.Fatal("setpath on an array has a live output")
			}
			if a.Output == nil || getType(a.Output) != "array" {
				t.Fatalf("output is not an array: %s", schemaTypeSummary(a.Output, 3))
			}
			items := arrayElementUnion(a.Output, DefaultOptions())
			if !schemaAdmitsString(items, "x") {
				t.Fatalf("array items must admit written string x: %s", schemaTypeSummary(a.Output, 3))
			}
			if tc.wantNull && !mightBeType(items, oas3.SchemaTypeNull) {
				t.Fatalf("extended array items must admit null padding: %s", schemaTypeSummary(a.Output, 3))
			}
		})
	}
}

func TestAllElementsUpdateReplacesArrayItemsWithoutPadding(t *testing.T) {
	one, three := int64(1), int64(3)
	in := ArrayType(StringType())
	in.MinItems = &one
	in.MaxItems = &three

	a := analyzeExpr(t, `.[] |= ascii_downcase`, in)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if a.Output == nil || getType(a.Output) != "array" {
		t.Fatalf("output is not an array: %s", schemaTypeSummary(a.Output, 3))
	}
	if len(a.Output.PrefixItems) != 0 {
		t.Fatalf("all-elements update retained positional items: %s", schemaTypeSummary(a.Output, 3))
	}
	items := arrayElementUnion(a.Output, DefaultOptions())
	if getType(items) != "string" || mightBeType(items, oas3.SchemaTypeNull) {
		t.Fatalf("items = %s, want non-null string", schemaTypeSummary(items, 3))
	}
	if a.Output.MaxItems == nil || *a.Output.MaxItems != three {
		t.Fatalf("all-elements update changed maxItems: %s", schemaTypeSummary(a.Output, 3))
	}
	if a.Output.MinItems != nil && *a.Output.MinItems > 0 {
		t.Fatalf("all-elements update must admit the empty deletion branch: %s", schemaTypeSummary(a.Output, 3))
	}
}

func TestNestedAllElementsUpdateReplacesPropertyArrayItems(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(IntegerType()),
	}, []string{"tags"})

	a := analyzeExpr(t, `.tags[] |= tostring`, in)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	tagsRef, ok := a.Output.Properties.Get("tags")
	if !ok {
		t.Fatalf("output has no tags property: %s", schemaTypeSummary(a.Output, 3))
	}
	tags := resolvedLeft(tagsRef)
	items := arrayElementUnion(tags, DefaultOptions())
	if getType(items) != "string" || mightBeType(items, oas3.SchemaTypeNull) || mightBeType(items, oas3.SchemaTypeInteger) {
		t.Fatalf("tags items = %s, want non-null string", schemaTypeSummary(items, 3))
	}
}

func TestDiscardedPathUpdateDoesNotChangeOriginalObject(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{"tags"})

	a := analyzeExpr(t, `(.tags[] |= 1) as $x | .tags`, in)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	items := arrayElementUnion(a.Output, DefaultOptions())
	if getType(items) != "string" || mightBeType(items, oas3.SchemaTypeInteger) {
		t.Fatalf("discarded update changed original tags: %s", schemaTypeSummary(a.Output, 3))
	}
}

func TestPathUpdateAndOriginalArrayOutputsAreBothPreserved(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{"tags"})

	a := analyzeExpr(t, `.tags | (.[] |= 1), .`, in)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	items := arrayElementUnion(a.Output, DefaultOptions())
	if !schemaAdmitsString(items, "a") || !schemaAdmitsInteger(items, 1) {
		t.Fatalf("outputs must admit original strings and updated integer 1: %s", schemaTypeSummary(a.Output, 3))
	}
}

func TestPathOperationsDoNotMutateInputSchema(t *testing.T) {
	for _, tc := range []struct {
		name  string
		expr  string
		input func() *oas3.Schema
	}{
		{name: "all elements update", expr: `.[] |= tostring`, input: func() *oas3.Schema { return ArrayType(IntegerType()) }},
		{name: "setpath", expr: `setpath([0]; 1)`, input: func() *oas3.Schema { return ArrayType(StringType()) }},
		{name: "delete all elements", expr: `del(.[])`, input: func() *oas3.Schema { return ArrayType(StringType()) }},
		{name: "index assignment", expr: `.[0] = 1`, input: func() *oas3.Schema { return ArrayType(StringType()) }},
		{name: "dynamic property", expr: `setpath([.key]; 1)`, input: func() *oas3.Schema {
			return BuildObject(map[string]*oas3.Schema{"key": StringType()}, []string{"key"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.input()
			before := schemaFingerprint(in)
			_ = analyzeExpr(t, tc.expr, in)
			if after := schemaFingerprint(in); after != before {
				t.Fatalf("input schema changed\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestEraseArrayPositionsLeavesNonArraySchemasUnchanged(t *testing.T) {
	opts := DefaultOptions()
	top := Top()
	if got := eraseArrayPositions(top, opts); got != top {
		t.Fatalf("Top was rewritten: %s", schemaTypeSummary(got, 3))
	}

	mixed := &oas3.Schema{AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](ArrayType(StringType())),
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](ConstNull()),
	}}
	before := schemaFingerprint(mixed)
	if got := eraseArrayPositions(mixed, opts); got != mixed || schemaFingerprint(got) != before {
		t.Fatalf("mixed schema was rewritten: %s", schemaTypeSummary(got, 3))
	}
}

func TestTuplePositionChangingOperationsEraseStalePrefixes(t *testing.T) {
	for _, expr := range []string{`.[1:]`, `reverse`, `sort`} {
		t.Run(expr, func(t *testing.T) {
			a := analyzeExpr(t, expr, exactStringIntegerTuple())
			if len(a.Output.PrefixItems) != 0 {
				t.Fatalf("operation retained stale prefixItems: %s", schemaTypeSummary(a.Output, 3))
			}
			assertArrayIndexAdmitsInteger(t, a.Output, 0, 1)
		})
	}
}

func TestTupleConcatenationErasesPositionsAndKeepsAllItems(t *testing.T) {
	a := analyzeExpr(t, `. + ["z"]`, exactStringIntegerTuple())
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("array concatenation has a live output")
	}
	if len(a.Output.PrefixItems) != 0 {
		t.Fatalf("concatenation retained stale prefixItems: %s", schemaTypeSummary(a.Output, 3))
	}
	assertArrayIndexAdmitsInteger(t, a.Output, 0, 1)
	if !schemaAdmitsString(arrayElementUnion(a.Output, DefaultOptions()), "z") {
		t.Fatalf("concatenated items must admit z: %s", schemaTypeSummary(a.Output, 3))
	}
}

func TestFlattenUnconstrainedArrayDoesNotBecomeEmpty(t *testing.T) {
	in := &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeArray)}
	a := analyzeExpr(t, `flatten`, in)
	if a.Verdict != VerdictUnverifiable {
		t.Fatalf("verdict = %s, want unverifiable", a.Verdict)
	}
	if a.Output.MaxItems != nil && *a.Output.MaxItems == 0 {
		t.Fatal("unconstrained input admits [1], so flatten output is not provably empty")
	}
	if !schemaAdmitsInteger(arrayElementUnion(a.Output, DefaultOptions()), 1) {
		t.Fatalf("flatten output items must admit integer 1: %s", schemaTypeSummary(a.Output, 3))
	}
}

const nestedArrayReferenceDocument = `openapi: 3.1.0
info:
  title: nested array reference
  version: 1.0.0
paths: {}
components:
  schemas:
    Inner:
      type: array
      items:
        type: integer
    Outer:
      type: array
      items:
        $ref: '#/components/schemas/Inner'
`

func TestFlattenReadsResolvedArrayItemReference(t *testing.T) {
	in := loadComponentSchema(t, nestedArrayReferenceDocument, "Outer")
	a := analyzeExpr(t, `flatten`, in)
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("nested array reference admits [[1]], whose flatten output is [1]")
	}
	if !schemaAdmitsInteger(arrayElementUnion(a.Output, DefaultOptions()), 1) {
		t.Fatalf("flatten output items must admit integer 1: %s", schemaTypeSummary(a.Output, 3))
	}
}
