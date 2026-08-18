package schemaexec

import (
	"context"
	"reflect"
	"strings"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
	"gopkg.in/yaml.v3"
)

func concreteJQResult(t *testing.T, expression string, input any) any {
	t.Helper()
	query, err := gojq.Parse(expression)
	if err != nil {
		t.Fatal(err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := code.Run(input).Next()
	if !ok {
		t.Fatalf("concrete jq %q produced no output", expression)
	}
	if err, ok := result.(error); ok {
		t.Fatalf("concrete jq %q: %v", expression, err)
	}
	return result
}

func exactTuple(elements ...*oas3.Schema) *oas3.Schema {
	length := int64(len(elements))
	result := BuildArray(nil, elements)
	result.MinItems = &length
	result.MaxItems = &length
	result.Items = oas3.NewJSONSchemaFromBool(false)
	return result
}

func hasOpenAdditionalProperties(schema *oas3.Schema) bool {
	if schema == nil || schema.AdditionalProperties == nil {
		return false
	}
	if value, ok := resolvedBooleanSchema(schema.AdditionalProperties); ok {
		return value
	}
	return isTopSchema(resolvedLeft(schema.AdditionalProperties))
}

func TestTruthinessRefinementKeepsSiblingBindingIndependent(t *testing.T) {
	nullable := StringType()
	value := true
	nullable.Nullable = &value
	input := BuildObject(map[string]*oas3.Schema{
		"a": nullable,
		"b": nullable,
	}, []string{"a", "b"})
	expression := `.b as $b | .a as $a | if $a then $b else "fallback" end`
	if got := concreteJQResult(t, expression, map[string]any{"a": "truthy", "b": nil}); got != nil {
		t.Fatalf("concrete output = %#v, want nil", got)
	}
	analysis := analyzeExpr(t, expression, input)
	if !mightBeType(analysis.Output, oas3.SchemaTypeNull) {
		t.Fatalf("output excludes concrete null: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestTruthinessRefinementKeepsRepeatedAccessPrecise(t *testing.T) {
	nullable := StringType()
	value := true
	nullable.Nullable = &value
	input := BuildObject(map[string]*oas3.Schema{"a": nullable}, []string{"a"})
	for _, expression := range []string{
		`if .a then .a else "d" end`,
		`.a as $a | if $a then $a else "d" end`,
	} {
		t.Run(expression, func(t *testing.T) {
			analysis := analyzeExpr(t, expression, input)
			if analysis.Verdict != VerdictProven || getType(analysis.Output) != "string" ||
				mightBeType(analysis.Output, oas3.SchemaTypeNull) {
				t.Fatalf("output = %s, verdict=%s causes=%v", schemaTypeSummary(analysis.Output, 4), analysis.Verdict, analysis.Causes)
			}
		})
	}
}

func TestAlternativeRefinementKeepsSiblingBindingIndependent(t *testing.T) {
	nullable := StringType()
	value := true
	nullable.Nullable = &value
	input := BuildObject(map[string]*oas3.Schema{
		"a": nullable,
		"b": nullable,
	}, []string{"a", "b"})
	expression := `.b as $b | .a as $a | $a // $b`
	want := "kept"
	if got := concreteJQResult(t, expression, map[string]any{"a": nil, "b": want}); got != want {
		t.Fatalf("concrete output = %#v, want %q", got, want)
	}
	analysis := analyzeExpr(t, expression, input)
	if !schemaAdmitsString(analysis.Output, want) {
		t.Fatalf("output excludes concrete string: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestSetpathDoesNotRebindSavedInput(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"k": ConstString("original")}, []string{"k"})
	expression := `. as $o | (.k = 1) | $o | .k`
	if got := concreteJQResult(t, expression, map[string]any{"k": "original"}); got != "original" {
		t.Fatalf("concrete output = %#v, want original", got)
	}
	analysis := analyzeExpr(t, expression, input)
	if value, ok := extractConstString(analysis.Output); analysis.Verdict != VerdictProven || !ok || value != "original" {
		t.Fatalf("saved input output = %s, verdict=%s causes=%v", schemaTypeSummary(analysis.Output, 4), analysis.Verdict, analysis.Causes)
	}
}

func TestSetpathDoesNotRebindSharedSibling(t *testing.T) {
	shared := BuildObject(map[string]*oas3.Schema{"k": ConstString("old")}, []string{"k"})
	input := BuildObject(map[string]*oas3.Schema{
		"a": shared,
		"b": shared,
	}, []string{"a", "b"})
	expression := `.b as $b | .a | (.k = 1) | $b | .k`
	instance := map[string]any{
		"a": map[string]any{"k": "old"},
		"b": map[string]any{"k": "old"},
	}
	if got := concreteJQResult(t, expression, instance); got != "old" {
		t.Fatalf("concrete output = %#v, want old", got)
	}
	analysis := analyzeExpr(t, expression, input)
	if value, ok := extractConstString(analysis.Output); analysis.Verdict != VerdictProven || !ok || value != "old" {
		t.Fatalf("shared sibling output = %s, verdict=%s causes=%v", schemaTypeSummary(analysis.Output, 4), analysis.Verdict, analysis.Causes)
	}
}

func TestDynamicKeyReducerRetainsAccumulatorPrecision(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{"tags"})
	expression := `reduce .tags[] as $t ({}; .[$t] = 1)`
	want := map[string]any{"x": 1}
	if got := concreteJQResult(t, expression, map[string]any{"tags": []any{"x"}}); !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete output = %#v, want %#v", got, want)
	}
	analysis := analyzeExpr(t, expression, input)
	if analysis.Verdict != VerdictProven || getType(analysis.Output) != "object" {
		t.Fatalf("output = %s, verdict=%s causes=%v", schemaTypeSummary(analysis.Output, 4), analysis.Verdict, analysis.Causes)
	}
	if !schemaAdmitsInteger(GetProperty(analysis.Output, "x", DefaultOptions()), 1) {
		t.Fatalf("dynamic reducer value excludes 1: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestDynamicDeleteDropsRequiredProperties(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{
		"key": StringType(),
		"a":   ConstString("present"),
	}, []string{"key", "a"})
	expression := `.key as $k | del(.[$k])`
	want := map[string]any{"key": "a"}
	if got := concreteJQResult(t, expression, map[string]any{"key": "a", "a": "present"}); !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete output = %#v, want %#v", got, want)
	}
	analysis := analyzeExpr(t, expression, input)
	if isRequired(analysis.Output.Required, "a") {
		t.Fatalf("dynamic delete still requires a: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestDeleteTupleIndexShiftsPositionsAndBounds(t *testing.T) {
	input := exactTuple(ConstInteger(1), ConstInteger(2), ConstInteger(3))
	if got := concreteJQResult(t, `del(.[0])`, []any{1, 2, 3}); !reflect.DeepEqual(got, []any{2, 3}) {
		t.Fatalf("concrete output = %#v", got)
	}
	analysis := analyzeExpr(t, `del(.[0])`, input)
	if analysis.Output.MaxItems == nil || *analysis.Output.MaxItems != 2 {
		t.Fatalf("maxItems was not decremented: %s", schemaTypeSummary(analysis.Output, 4))
	}
	if !schemaAdmitsInteger(getArrayElement(analysis.Output, 0, DefaultOptions()), 2) {
		t.Fatalf("first output position excludes 2: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestNestedSetpathSynthesizesIntermediateObject(t *testing.T) {
	want := map[string]any{"a": map[string]any{"b": 1}}
	if got := concreteJQResult(t, `setpath(["a", "b"]; 1)`, map[string]any{}); !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete output = %#v, want %#v", got, want)
	}
	analysis := analyzeExpr(t, `setpath(["a", "b"]; 1)`, ObjectType())
	a := requireObjectProperty(t, analysis.Output, "a")
	b := requireObjectProperty(t, a, "b")
	if !schemaAdmitsInteger(b, 1) {
		t.Fatalf("nested value excludes 1: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestFromEntriesReadsExactTupleItems(t *testing.T) {
	entry := BuildObject(map[string]*oas3.Schema{
		"key":   ConstString("x"),
		"value": ConstInteger(1),
	}, []string{"key", "value"})
	analysis := analyzeExpr(t, `from_entries`, exactTuple(entry))
	if !schemaAdmitsInteger(requireObjectProperty(t, analysis.Output, "x"), 1) ||
		!isRequired(analysis.Output.Required, "x") {
		t.Fatalf("from_entries lost exact tuple entry: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestLevelOneObjectWideningStaysOpen(t *testing.T) {
	opts := DefaultOptions()
	opts.AnyOfLimit = 1
	opts.WideningLevel = 1
	analysis := analyzeExprWithOptions(t, `{a: 1}, "x"`, Top(), opts)
	foundOpenObject := false
	branches, _ := disjunctiveBranches(analysis.Output)
	for _, branch := range branches {
		if getType(branch) == "object" && hasOpenAdditionalProperties(branch) {
			foundOpenObject = true
		}
	}
	if !foundOpenObject {
		t.Fatalf("widened object branch is not open: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestConstantArrayLiteralPreservesEveryPosition(t *testing.T) {
	literal := buildArrayFromLiteral([]any{1, 2})
	want := []int64{1, 2}
	if literal.MinItems == nil || *literal.MinItems > int64(len(want)) ||
		literal.MaxItems != nil && *literal.MaxItems < int64(len(want)) {
		t.Fatalf("literal bounds exclude the concrete array: %s", schemaTypeSummary(literal, 4))
	}
	for index, value := range want {
		if !schemaAdmitsInteger(getArrayElement(literal, index, DefaultOptions()), value) {
			t.Fatalf("literal position %d excludes %d: %s", index, value, schemaTypeSummary(literal, 4))
		}
	}
	analysis := analyzeExpr(t, `[1, 2] | .[1]`, Top())
	if !schemaAdmitsInteger(analysis.Output, 2) {
		t.Fatalf("second literal position excludes 2: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestArrayBuiltinsDistributeOverReceiverUnion(t *testing.T) {
	tuple := exactTuple(ConstInteger(1), ConstInteger(1))
	input := &oas3.Schema{AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](tuple),
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
	}}
	unique := analyzeExpr(t, `unique`, input)
	if getType(unique.Output) != "array" || unique.Output.MinItems != nil && *unique.Output.MinItems >= 2 ||
		!schemaAdmitsInteger(arrayElementUnion(unique.Output, DefaultOptions()), 1) {
		t.Fatalf("unique left an untransformed branch: %s", schemaTypeSummary(unique.Output, 4))
	}

	nullableInput := &oas3.Schema{AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](exactTuple(ConstInteger(2), ConstInteger(1))),
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](ConstNull()),
	}}
	for _, expression := range []string{"reverse", "sort"} {
		analysis := analyzeExpr(t, expression, nullableInput)
		if getType(analysis.Output) != "array" || mightBeType(analysis.Output, oas3.SchemaTypeNull) ||
			!schemaAdmitsInteger(arrayElementUnion(analysis.Output, DefaultOptions()), 1) ||
			!schemaAdmitsInteger(arrayElementUnion(analysis.Output, DefaultOptions()), 2) {
			t.Fatalf("%s did not transform only the array branch: %s", expression, schemaTypeSummary(analysis.Output, 4))
		}
	}
}

func TestObjectValueOperationsIncludePatternProperties(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"known": StringType()}, nil)
	input.PatternProperties = sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	input.PatternProperties.Set("^x", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](IntegerType()))
	input.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)
	if got := concreteJQResult(t, `.[]`, map[string]any{"x1": 1}); got != 1 {
		t.Fatalf("concrete output = %#v, want 1", got)
	}
	iterated := analyzeExpr(t, `.[]`, input)
	if !schemaAdmitsInteger(iterated.Output, 1) {
		t.Fatalf("iteration excludes pattern value: %s", schemaTypeSummary(iterated.Output, 4))
	}
	values := analyzeExpr(t, `values`, input)
	if !schemaAdmitsInteger(arrayElementUnion(values.Output, DefaultOptions()), 1) {
		t.Fatalf("values excludes pattern value: %s", schemaTypeSummary(values.Output, 4))
	}
	entries := analyzeExpr(t, `to_entries`, input)
	entry := arrayElementUnion(entries.Output, DefaultOptions())
	if !schemaAdmitsInteger(requireObjectProperty(t, entry, "value"), 1) {
		t.Fatalf("to_entries excludes pattern value: %s", schemaTypeSummary(entries.Output, 4))
	}
}

func TestRawObjectAdditionKeepsAbsentAdditionalPropertiesOpen(t *testing.T) {
	left := ObjectType()
	left.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)
	right := ObjectType()
	input := BuildObject(map[string]*oas3.Schema{"left": left, "right": right}, []string{"left", "right"})
	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw
	raw := analyzeExprWithOptions(t, `.left + .right`, input, opts)
	if !hasOpenAdditionalProperties(raw.Output) {
		t.Fatalf("raw object addition did not preserve open AP: %s", schemaTypeSummary(raw.Output, 4))
	}
	defaultMode := analyzeExpr(t, `.left + .right`, input)
	if defaultMode.Output.AdditionalProperties == nil {
		t.Fatalf("default semantics lost explicit closed AP: %s", schemaTypeSummary(defaultMode.Output, 4))
	}
	if value, ok := resolvedBooleanSchema(defaultMode.Output.AdditionalProperties); !ok || value {
		t.Fatalf("default semantics unexpectedly opened AP: %s", schemaTypeSummary(defaultMode.Output, 4))
	}
}

func TestAnalyzeRejectsNilQuery(t *testing.T) {
	_, err := Analyze(context.Background(), nil, ObjectType())
	if err == nil || !strings.Contains(err.Error(), "query cannot be nil") {
		t.Fatalf("error = %v", err)
	}
}

func TestUntypedNullConstHasNoImpliedType(t *testing.T) {
	schema := &oas3.Schema{Const: &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}}
	if got := impliedTypeOf(schema); got != "" {
		t.Fatalf("impliedTypeOf(const null) = %q", got)
	}
}

func TestPathAllElementsMarkerIsNotPublic(t *testing.T) {
	analysis := analyzeExpr(t, `path(.[])`, ArrayType(StringType()))
	if analysis.Output == nil || len(analysis.Output.PrefixItems) == 0 {
		t.Fatalf("path output has no index position: %s", schemaTypeSummary(analysis.Output, 4))
	}
	index := resolvedLeft(analysis.Output.PrefixItems[0])
	if index == nil || index.Format != nil {
		t.Fatalf("path index exposes internal format: %#v", index)
	}
}

func TestEnvironmentObjectsAreOpenStringMaps(t *testing.T) {
	for _, expression := range []string{`env.HOME`, `$ENV.HOME`} {
		analysis := analyzeExpr(t, expression, ObjectType())
		if analysis.Verdict == VerdictProvenBroken || !MightBeString(analysis.Output) {
			t.Fatalf("%s: verdict=%s output=%s causes=%v", expression, analysis.Verdict, schemaTypeSummary(analysis.Output, 4), analysis.Causes)
		}
	}
}

func TestStringEncodingBuiltinsReturnPreciseTypes(t *testing.T) {
	tests := []struct {
		expression string
		input      *oas3.Schema
		wantType   string
	}{
		{`@base64`, StringType(), "string"},
		{`@base64d`, StringType(), "string"},
		{`@uri`, StringType(), "string"},
		{`@html`, StringType(), "string"},
		{`@sh`, StringType(), "string"},
		{`@csv`, ArrayType(StringType()), "string"},
		{`@tsv`, ArrayType(StringType()), "string"},
		{`@json`, StringType(), "string"},
		{`@text`, IntegerType(), "string"},
		{`@base32`, StringType(), "string"},
		{`@base32d`, StringType(), "string"},
		{`tojson`, ObjectType(), "string"},
		{`explode`, StringType(), "array"},
		{`implode`, ArrayType(IntegerType()), "string"},
		{`tostring`, IntegerType(), "string"},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			analysis := analyzeExpr(t, test.expression, test.input)
			if got := getType(analysis.Output); got != test.wantType {
				t.Fatalf("type = %q, want %q; output=%s causes=%v", got, test.wantType, schemaTypeSummary(analysis.Output, 4), analysis.Causes)
			}
			if test.expression == `explode` && getType(arrayElementUnion(analysis.Output, DefaultOptions())) != "integer" {
				t.Fatalf("explode items are not integers: %s", schemaTypeSummary(analysis.Output, 4))
			}
		})
	}
	base64Analysis := analyzeExpr(t, `@base64`, ConstString("x"))
	if !schemaAdmitsString(base64Analysis.Output, "eA==") {
		t.Fatalf("const base64 was not folded: %s", schemaTypeSummary(base64Analysis.Output, 3))
	}
}
