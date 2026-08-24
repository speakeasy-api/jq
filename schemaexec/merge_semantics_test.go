package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
)

func concreteJQOutputs(t *testing.T, expression string, input any) []any {
	t.Helper()
	query, err := gojq.Parse(expression)
	if err != nil {
		t.Fatalf("parse concrete jq %q: %v", expression, err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		t.Fatalf("compile concrete jq %q: %v", expression, err)
	}
	outputs := make([]any, 0)
	iterator := code.Run(input)
	for {
		value, ok := iterator.Next()
		if !ok {
			return outputs
		}
		if runErr, ok := value.(error); ok {
			t.Fatalf("run concrete jq %q: %v", expression, runErr)
		}
		outputs = append(outputs, value)
	}
}

func analyzeWithOptions(t *testing.T, expression string, input *oas3.Schema, opts SchemaExecOptions) *Analysis {
	t.Helper()
	query, err := gojq.Parse(expression)
	if err != nil {
		t.Fatalf("parse symbolic jq %q: %v", expression, err)
	}
	analysis, err := Analyze(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("analyze %q: %v", expression, err)
	}
	return analysis
}

func anyOfSchemas(branches ...*oas3.Schema) *oas3.Schema {
	wrappers := make([]*oas3.JSONSchema[oas3.Referenceable], len(branches))
	for i, branch := range branches {
		wrappers[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branch)
	}
	return &oas3.Schema{AnyOf: wrappers}
}

func TestObjectMergesPreservePlainPropertyPointerIdentity(t *testing.T) {
	property := ArrayType(StringType())
	withProperty := BuildObject(map[string]*oas3.Schema{"configs": property}, []string{"configs"})

	tests := []struct {
		name  string
		merge func() *oas3.Schema
	}{
		{
			name: "MergeObjects",
			merge: func() *oas3.Schema {
				return MergeObjects(ObjectType(), withProperty, DefaultOptions())
			},
		},
		{
			name: "tryMergeObjects",
			merge: func() *oas3.Schema {
				return tryMergeObjects([]*oas3.Schema{withProperty, ObjectType()}, DefaultOptions())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			merged := tt.merge()
			if merged == nil || merged.Properties == nil {
				t.Fatalf("merged schema has no properties: %s", schemaTypeSummary(merged, 3))
			}
			wrapper, ok := merged.Properties.Get("configs")
			if !ok || wrapper == nil {
				t.Fatal("merged schema has no configs property")
			}
			if wrapper.Left != property {
				t.Fatalf("configs property pointer = %p, want original %p", wrapper.Left, property)
			}
		})
	}
}

func TestDisjunctiveArrayItemsBooleanLatticeIsOrderIndependent(t *testing.T) {
	closed := &oas3.Schema{
		Type:  oas3.NewTypeFromString(oas3.SchemaTypeArray),
		Items: oas3.NewJSONSchemaFromBool(false),
	}
	open := &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeArray)}
	concrete := concreteJQOutputs(t, `.[]`, []any{42})
	if len(concrete) != 1 || concrete[0] != 42 {
		t.Fatalf("concrete evidence = %#v, want [42]", concrete)
	}

	for _, branches := range [][]*oas3.Schema{{closed, open}, {open, closed}} {
		analysis := analyzeWithOptions(t, `.[]`, anyOfSchemas(branches...), DefaultOptions())
		if analysis.Verdict == VerdictProvenBroken || !MightBeNumber(analysis.Output) {
			t.Fatalf("order %s then %s: verdict=%s output=%s", schemaTypeSummary(branches[0], 2), schemaTypeSummary(branches[1], 2), analysis.Verdict, schemaTypeSummary(analysis.Output, 3))
		}
	}
}

func TestDisjunctivePropertyBooleansAreOrderIndependent(t *testing.T) {
	trueProperties := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	trueProperties.Set("x", oas3.NewJSONSchemaFromBool(true))
	trueBranch := ObjectType()
	trueBranch.Properties = trueProperties
	trueBranch.Required = []string{"x"}
	trueBranch.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)

	falseProperties := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	falseProperties.Set("x", oas3.NewJSONSchemaFromBool(false))
	falseBranch := ObjectType()
	falseBranch.Properties = falseProperties
	falseBranch.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)

	concrete := concreteJQOutputs(t, `.x`, map[string]any{"x": 42})
	if len(concrete) != 1 || concrete[0] != 42 {
		t.Fatalf("concrete evidence = %#v, want [42]", concrete)
	}
	for _, branches := range [][]*oas3.Schema{{trueBranch, falseBranch}, {falseBranch, trueBranch}} {
		analysis := analyzeWithOptions(t, `.x`, anyOfSchemas(branches...), DefaultOptions())
		if analysis.Verdict == VerdictProvenBroken || !MightBeNumber(analysis.Output) {
			t.Fatalf("boolean property order produced verdict=%s output=%s", analysis.Verdict, schemaTypeSummary(analysis.Output, 3))
		}
	}
}

func TestDisjunctiveObjectMissingPropertyUsesAdditionalProperties(t *testing.T) {
	additionalStrings := StringType()
	openBranch := ObjectType()
	openBranch.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](additionalStrings)
	declaredBranch := BuildObject(map[string]*oas3.Schema{"x": IntegerType()}, []string{"x"})
	declaredBranch.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](additionalStrings)

	analysis := analyzeWithOptions(t, `.x`, anyOfSchemas(openBranch, declaredBranch), DefaultOptions())
	concrete := concreteJQOutputs(t, `.x`, map[string]any{"x": "live"})
	if len(concrete) != 1 || concrete[0] != "live" || !MightBeString(analysis.Output) || !MightBeNumber(analysis.Output) {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}

func TestRawDisjunctiveObjectMissingPropertyStaysOpen(t *testing.T) {
	openBranch := ObjectType()
	declaredBranch := BuildObject(map[string]*oas3.Schema{"x": StringType()}, []string{"x"})
	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw

	analysis := analyzeWithOptions(t, `.x`, anyOfSchemas(openBranch, declaredBranch), opts)
	concrete := concreteJQOutputs(t, `.x`, map[string]any{"x": 42})
	if len(concrete) != 1 || concrete[0] != 42 || analysis.Verdict == VerdictProvenBroken || !MightBeNumber(analysis.Output) {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}

func TestRawNormalizationDoesNotInferBranchTypes(t *testing.T) {
	left := &oas3.Schema{Properties: sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()}
	left.Properties.Set("x", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()))
	right := &oas3.Schema{Properties: sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()}
	right.Properties.Set("x", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](IntegerType()))
	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw

	analysis := analyzeWithOptions(t, `.x`, anyOfSchemas(left, right), opts)
	concrete := concreteJQOutputs(t, `.x`, map[string]any{"x": 42})
	if len(concrete) != 1 || concrete[0] != 42 || analysis.Verdict != VerdictUnverifiable {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}

func TestObjectUnionPreservesNullableAdmission(t *testing.T) {
	input := ObjectType()
	nullable := true
	input.Nullable = &nullable

	analysis := analyzeWithOptions(t, `., {x: 1}`, input, DefaultOptions())
	concrete := concreteJQOutputs(t, `., {x: 1}`, nil)
	if len(concrete) != 2 || concrete[0] != nil || !mightBeType(analysis.Output, oas3.SchemaTypeNull) {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}

func TestObjectUnionKeepsPatternPropertiesBranch(t *testing.T) {
	patterns := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	patterns.Set("^x", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](IntegerType()))
	input := ObjectType()
	input.PatternProperties = patterns
	input.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	analysis := analyzeWithOptions(t, `., {a: 1}`, input, DefaultOptions())
	concrete := concreteJQOutputs(t, `., {a: 1}`, map[string]any{"x-live": 42})
	if len(concrete) != 2 || !schemaContainsPattern(analysis.Output, "^x") {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}

func schemaContainsPattern(schema *oas3.Schema, pattern string) bool {
	if schema == nil {
		return false
	}
	if schema.PatternProperties != nil {
		if _, ok := schema.PatternProperties.Get(pattern); ok {
			return true
		}
	}
	for _, branch := range schema.AnyOf {
		if schemaContainsPattern(resolvedLeft(branch), pattern) {
			return true
		}
	}
	return false
}

func TestWidenedUnionRetainsNull(t *testing.T) {
	branches := make([]*oas3.Schema, 0, 11)
	for i := 0; i < 11; i++ {
		pattern := fmt.Sprintf("^v%d$", i)
		branch := StringType()
		branch.Pattern = &pattern
		if i == 0 {
			nullable := true
			branch.Nullable = &nullable
		}
		branches = append(branches, branch)
	}
	analysis := analyzeWithOptions(t, `., 0`, anyOfSchemas(branches...), DefaultOptions())
	concrete := concreteJQOutputs(t, `., 0`, nil)
	if len(concrete) != 2 || concrete[0] != nil || !mightBeType(analysis.Output, oas3.SchemaTypeNull) {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}

func TestDynamicSetpathPreservesOpenAdditionalProperties(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"key": StringType()}, []string{"key"})
	input.AdditionalProperties = oas3.NewJSONSchemaFromBool(true)
	analysis := analyzeWithOptions(t, `setpath([.key]; 1)`, input, DefaultOptions())
	concrete := concreteJQOutputs(t, `setpath([.key]; 1)`, map[string]any{"key": "new", "other": "live"})
	ap, possible := schemaFacetValue(analysis.Output.AdditionalProperties, DefaultOptions())
	if len(concrete) != 1 || !possible || !isTopSchema(ap) {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}

func TestNestedDynamicSetpathBuildsChildContainer(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"key": StringType()}, []string{"key"})
	input.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)
	analysis := analyzeWithOptions(t, `setpath([.key, "x"]; 1)`, input, DefaultOptions())
	concrete := concreteJQOutputs(t, `setpath([.key, "x"]; 1)`, map[string]any{"key": "new"})
	ap, possible := schemaFacetValue(analysis.Output.AdditionalProperties, DefaultOptions())
	if len(concrete) != 1 || !possible || !MightBeObject(ap) {
		t.Fatalf("verdict=%s output=%s AP=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), schemaTypeSummary(ap, 4), concrete)
	}
}

func TestObjectOperatorsKeepLeftValueForOptionalRightProperty(t *testing.T) {
	left := BuildObject(map[string]*oas3.Schema{"x": StringType()}, []string{"x"})
	right := BuildObject(map[string]*oas3.Schema{"x": IntegerType()}, nil)
	input := BuildObject(map[string]*oas3.Schema{"left": left, "right": right}, []string{"left", "right"})
	instance := map[string]any{"left": map[string]any{"x": "live"}, "right": map[string]any{}}

	for _, operator := range []string{"+", "*"} {
		expression := `.left ` + operator + ` .right`
		analysis := analyzeWithOptions(t, expression, input, DefaultOptions())
		concrete := concreteJQOutputs(t, expression, instance)
		x := requireObjectProperty(t, analysis.Output, "x")
		if len(concrete) != 1 || !MightBeString(x) || !MightBeNumber(x) {
			t.Fatalf("operator %s: verdict=%s output=%s concrete=%#v", operator, analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
		}
	}
}

func TestObjectOperatorsIncludeRightAdditionalPropertyOverrides(t *testing.T) {
	left := BuildObject(map[string]*oas3.Schema{"x": StringType()}, []string{"x"})
	right := ObjectType()
	right.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](IntegerType())
	input := BuildObject(map[string]*oas3.Schema{"left": left, "right": right}, []string{"left", "right"})
	instance := map[string]any{"left": map[string]any{"x": "old"}, "right": map[string]any{"x": 42}}

	for _, operator := range []string{"+", "*"} {
		expression := `.left ` + operator + ` .right`
		analysis := analyzeWithOptions(t, expression, input, DefaultOptions())
		concrete := concreteJQOutputs(t, expression, instance)
		x := requireObjectProperty(t, analysis.Output, "x")
		if len(concrete) != 1 || !MightBeString(x) || !MightBeNumber(x) {
			t.Fatalf("operator %s: verdict=%s output=%s concrete=%#v", operator, analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
		}
	}
}

func TestArrayPlusIncludesUnconstrainedOperandItems(t *testing.T) {
	left := &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeArray)}
	right := ArrayType(StringType())
	input := BuildObject(map[string]*oas3.Schema{"left": left, "right": right}, []string{"left", "right"})
	analysis := analyzeWithOptions(t, `.left + .right`, input, DefaultOptions())
	concrete := concreteJQOutputs(t, `.left + .right`, map[string]any{"left": []any{42}, "right": []any{"x"}})
	items := arrayElementUnion(analysis.Output, DefaultOptions())
	if len(concrete) != 1 || !isTopSchema(items) || !MightBeNumber(items) {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}

func TestArrayMinusIncludesBooleanTrueTail(t *testing.T) {
	left := BuildArray(nil, []*oas3.Schema{ConstString("head")})
	left.Items = oas3.NewJSONSchemaFromBool(true)
	two := int64(2)
	left.MinItems = &two
	right := ArrayType(Bottom())
	input := BuildObject(map[string]*oas3.Schema{"left": left, "right": right}, []string{"left", "right"})
	analysis := analyzeWithOptions(t, `.left - .right`, input, DefaultOptions())
	concrete := concreteJQOutputs(t, `.left - .right`, map[string]any{"left": []any{"head", 42}, "right": []any{}})
	items := arrayElementUnion(analysis.Output, DefaultOptions())
	if len(concrete) != 1 || !isTopSchema(items) || !MightBeNumber(items) {
		t.Fatalf("verdict=%s output=%s concrete=%#v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), concrete)
	}
}
