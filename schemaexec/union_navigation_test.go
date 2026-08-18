package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func disjunctiveSchema(keyword string, branches ...*oas3.Schema) *oas3.Schema {
	wrapped := make([]*oas3.JSONSchema[oas3.Referenceable], len(branches))
	for i, branch := range branches {
		wrapped[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branch)
	}
	schema := &oas3.Schema{}
	if keyword == "oneOf" {
		schema.OneOf = wrapped
	} else {
		schema.AnyOf = wrapped
	}
	return schema
}

func recursiveProjectionFixture() *oas3.Schema {
	leaf := BuildObject(map[string]*oas3.Schema{
		"c": StringType(),
		"d": IntegerType(),
	}, []string{"c"})
	nested := BuildObject(map[string]*oas3.Schema{
		"b": ArrayType(leaf),
	}, []string{"b"})
	return BuildObject(map[string]*oas3.Schema{
		"a": nested,
		"e": StringType(),
	}, []string{"a", "e"})
}

func TestRecursiveStringsOverOptionalValuesRemainProven(t *testing.T) {
	a := analyzeExpr(t, `[.. | strings] | length`, recursiveProjectionFixture())
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want Proven (causes: %v; output: %s)",
			a.Verdict, a.Causes, schemaTypeSummary(a.Output, 4))
	}
	if !MightBeNumber(a.Output) {
		t.Fatalf("length output must be numeric: %s", schemaTypeSummary(a.Output, 4))
	}
}

func TestOptionalIterationOverScalarUnionProducesNoValues(t *testing.T) {
	input := disjunctiveSchema("anyOf", IntegerType(), NullType())
	query, err := gojq.Parse(`.[]?`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Schema != Bottom() {
		t.Fatalf("scalar/null iteration must produce Bottom, got %s", schemaTypeSummary(result.Schema, 4))
	}

	a := analyzeExpr(t, `[.[]?] | length`, input)
	if a.Verdict != VerdictProven {
		t.Fatalf("empty collection length verdict = %s, want Proven", a.Verdict)
	}
	if isTopSchema(a.Output) || (!MightBeNumber(a.Output) && !mightBeType(a.Output, oas3.SchemaTypeInteger)) {
		t.Fatalf("empty collection length must be a proven number, got %s", schemaTypeSummary(a.Output, 4))
	}
	empty := runQuery(t, `[.[]?]`, input)
	if getType(empty) != "array" || empty.MaxItems == nil || *empty.MaxItems != 0 {
		t.Fatalf("optional scalar iteration must collect an empty array, got %s", schemaTypeSummary(empty, 4))
	}
}

func TestIterationDistributesOverArrayUnions(t *testing.T) {
	for _, keyword := range []string{"anyOf", "oneOf"} {
		t.Run(keyword+"NullableArray", func(t *testing.T) {
			input := disjunctiveSchema(keyword, ArrayType(StringType()), NullType())
			a := analyzeExpr(t, `.[]`, input)
			if a.Verdict != VerdictProven || getType(a.Output) != "string" {
				t.Fatalf("verdict/output = %s/%s, want Proven string (causes: %v)",
					a.Verdict, schemaTypeSummary(a.Output, 4), a.Causes)
			}
		})
	}

	input := disjunctiveSchema("anyOf", ArrayType(StringType()), ArrayType(IntegerType()))
	a := analyzeExpr(t, `.[]`, input)
	if a.Verdict != VerdictProven || !MightBeString(a.Output) || !mightBeType(a.Output, oas3.SchemaTypeInteger) {
		t.Fatalf("array-union iteration must admit string and integer: verdict=%s output=%s",
			a.Verdict, schemaTypeSummary(a.Output, 4))
	}
}

func TestIterationOverNullableArrayUsesItemSchema(t *testing.T) {
	input := ArrayType(StringType())
	nullable := true
	input.Nullable = &nullable
	a := analyzeExpr(t, `.[]`, input)
	if a.Verdict != VerdictProven || getType(a.Output) != "string" {
		t.Fatalf("verdict/output = %s/%s, want Proven string (causes: %v)",
			a.Verdict, schemaTypeSummary(a.Output, 4), a.Causes)
	}
}

func TestNumericIndexAndSliceDistributeOverNullableArrayUnion(t *testing.T) {
	input := disjunctiveSchema("anyOf", ArrayType(StringType()), NullType())

	indexed := analyzeExpr(t, `.[0]`, input)
	if indexed.Verdict != VerdictProven || isTopSchema(indexed.Output) ||
		!MightBeString(indexed.Output) || !mightBeType(indexed.Output, oas3.SchemaTypeNull) {
		t.Fatalf("index must admit string and null without widening: verdict=%s output=%s",
			indexed.Verdict, schemaTypeSummary(indexed.Output, 4))
	}

	sliced := analyzeExpr(t, `.[0:1]`, input)
	if sliced.Verdict != VerdictProven || isTopSchema(sliced.Output) ||
		!MightBeArray(sliced.Output) || !mightBeType(sliced.Output, oas3.SchemaTypeNull) {
		t.Fatalf("slice must admit array and null without widening: verdict=%s output=%s",
			sliced.Verdict, schemaTypeSummary(sliced.Output, 4))
	}

	nullableDisjunction := disjunctiveSchema("anyOf", ArrayType(StringType()))
	nullable := true
	nullableDisjunction.Nullable = &nullable
	indexed = analyzeExpr(t, `.[0]`, nullableDisjunction)
	if indexed.Verdict != VerdictProven || isTopSchema(indexed.Output) ||
		!MightBeString(indexed.Output) || !mightBeType(indexed.Output, oas3.SchemaTypeNull) {
		t.Fatalf("outer nullable must add the null index result: verdict=%s output=%s",
			indexed.Verdict, schemaTypeSummary(indexed.Output, 4))
	}
}

func TestRecurseOverReferencedOptionalOwnerKeepsConcreteArrayRoot(t *testing.T) {
	input := loadComponentSchema(t, refItemsDoc, "List")
	a := analyzeExpr(t, `[recurse(.owner)]`, input)
	if a.Verdict == VerdictProvenBroken || isTopSchema(a.Output) || getType(a.Output) != "array" {
		t.Fatalf("recursive projection must retain a concrete array root: verdict=%s output=%s causes=%v",
			a.Verdict, schemaTypeSummary(a.Output, 4), a.Causes)
	}
}

func TestWalkOverMixedNestedValuesAdmitsObjectOutput(t *testing.T) {
	a := analyzeExpr(t, `walk(if type == "object" then del(.d) else . end)`, recursiveProjectionFixture())
	if a.Verdict == VerdictProvenBroken || !MightBeObject(a.Output) {
		t.Fatalf("walk must admit its concrete object output: verdict=%s output=%s causes=%v",
			a.Verdict, schemaTypeSummary(a.Output, 4), a.Causes)
	}
}

func TestSiblingObjectShapeSurvivesOneOfNavigation(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"x": StringType()}, []string{"x"})
	input.OneOf = disjunctiveSchema("oneOf",
		BuildObject(map[string]*oas3.Schema{"a": IntegerType()}, []string{"a"}),
		BuildObject(map[string]*oas3.Schema{"b": BoolType()}, []string{"b"}),
	).OneOf

	iterated := analyzeExpr(t, `.[]`, input)
	if iterated.Verdict != VerdictProven || !MightBeString(iterated.Output) {
		t.Fatalf("iteration must admit the sibling property value: verdict=%s output=%s",
			iterated.Verdict, schemaTypeSummary(iterated.Output, 4))
	}

	indexed := analyzeExpr(t, `.x`, input)
	if indexed.Verdict != VerdictProven || !MightBeString(indexed.Output) {
		t.Fatalf("property access must admit the sibling property value: verdict=%s output=%s",
			indexed.Verdict, schemaTypeSummary(indexed.Output, 4))
	}

	numeric := analyzeExpr(t, `.[0]`, input)
	if numeric.Verdict == VerdictProvenBroken || !isTopSchema(numeric.Output) {
		t.Fatalf("numeric object indexing must remain conservatively unverifiable: verdict=%s output=%s",
			numeric.Verdict, schemaTypeSummary(numeric.Output, 4))
	}
}

func TestSiblingArrayShapeSurvivesOneOfIndexing(t *testing.T) {
	input := ArrayType(StringType())
	input.OneOf = disjunctiveSchema("oneOf",
		ArrayType(ConstString("a")),
		ArrayType(ConstString("b")),
	).OneOf

	indexed := analyzeExpr(t, `.[0]`, input)
	if indexed.Verdict != VerdictProven || !isSubschemaOf(ConstString("z"), indexed.Output) {
		t.Fatalf("indexing must retain the sibling item schema: verdict=%s output=%s",
			indexed.Verdict, schemaTypeSummary(indexed.Output, 4))
	}
}
