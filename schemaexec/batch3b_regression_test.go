package schemaexec

import (
	"reflect"
	"testing"
	"time"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func concreteJQResults(t *testing.T, expression string, input any) []any {
	t.Helper()
	query, err := gojq.Parse(expression)
	if err != nil {
		t.Fatal(err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		t.Fatal(err)
	}
	iterator := code.Run(input)
	var results []any
	for {
		result, ok := iterator.Next()
		if !ok {
			return results
		}
		if err, ok := result.(error); ok {
			t.Fatalf("concrete jq %q: %v", expression, err)
		}
		results = append(results, result)
	}
}

func arrayIndexAcrossAlternatives(schema *oas3.Schema, index int) *oas3.Schema {
	var candidates []*oas3.Schema
	for _, branch := range explodeAlternatives(schema) {
		if getType(branch) == "array" {
			candidates = append(candidates, getArrayElement(branch, index, DefaultOptions()))
		}
	}
	return Union(candidates, DefaultOptions())
}

func TestTupleUnionsPreserveEveryPosition(t *testing.T) {
	expression := `[1,2], [3,4]`
	want := []any{[]any{1, 2}, []any{3, 4}}
	if got := concreteJQResults(t, expression, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete outputs = %#v, want %#v", got, want)
	}
	analysis := analyzeExpr(t, expression, NullType())
	first := arrayIndexAcrossAlternatives(analysis.Output, 0)
	second := arrayIndexAcrossAlternatives(analysis.Output, 1)
	if !schemaAdmitsInteger(first, 1) || !schemaAdmitsInteger(first, 3) ||
		!schemaAdmitsInteger(second, 2) || !schemaAdmitsInteger(second, 4) {
		t.Fatalf("tuple union lost a position: %s", schemaTypeSummary(analysis.Output, 5))
	}

	left := exactTuple(ConstInteger(1), ConstInteger(2))
	right := exactTuple(ConstInteger(3), ConstInteger(4))
	if isSubschemaOf(left, right) || isSubschemaOf(right, left) {
		t.Fatal("distinct exact tuples must not subsume one another")
	}
}

func TestNestedTupleIterationPreservesAlternatives(t *testing.T) {
	tests := []struct {
		expression string
		want       []any
		values     []int64
	}{
		{`[[1,2],[3,4]] | .[][0]`, []any{1, 3}, []int64{1, 3}},
		{`[[1,2],[3,4]] | .[] | .[1]`, []any{2, 4}, []int64{2, 4}},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			if got := concreteJQResults(t, test.expression, nil); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("concrete outputs = %#v, want %#v", got, test.want)
			}
			analysis := analyzeExpr(t, test.expression, NullType())
			for _, value := range test.values {
				if !schemaAdmitsInteger(analysis.Output, value) {
					t.Fatalf("output excludes %d: %s", value, schemaTypeSummary(analysis.Output, 5))
				}
			}
		})
	}

	t.Run("raw tuple iteration", func(t *testing.T) {
		expression := `[[1,2],[3,4]] | .[]`
		want := []any{[]any{1, 2}, []any{3, 4}}
		if got := concreteJQResults(t, expression, nil); !reflect.DeepEqual(got, want) {
			t.Fatalf("concrete outputs = %#v, want %#v", got, want)
		}
		analysis := analyzeExpr(t, expression, NullType())
		first := arrayIndexAcrossAlternatives(analysis.Output, 0)
		second := arrayIndexAcrossAlternatives(analysis.Output, 1)
		if !schemaAdmitsInteger(first, 1) || !schemaAdmitsInteger(first, 3) ||
			!schemaAdmitsInteger(second, 2) || !schemaAdmitsInteger(second, 4) {
			t.Fatalf("iteration lost a tuple: %s", schemaTypeSummary(analysis.Output, 5))
		}
	})

	t.Run("map over tuple heads", func(t *testing.T) {
		expression := `[[1,2],[3,4]] | map(.[0])`
		want := []any{[]any{1, 3}}
		if got := concreteJQResults(t, expression, nil); !reflect.DeepEqual(got, want) {
			t.Fatalf("concrete outputs = %#v, want %#v", got, want)
		}
		analysis := analyzeExpr(t, expression, NullType())
		items := arrayElementUnion(analysis.Output, DefaultOptions())
		if !schemaAdmitsInteger(items, 1) || !schemaAdmitsInteger(items, 3) {
			t.Fatalf("map lost a tuple head: %s", schemaTypeSummary(analysis.Output, 5))
		}
	})
}

func TestMixedLengthTupleUnionPreservesLengths(t *testing.T) {
	expression := `([1], [3,4]) | length`
	if got := concreteJQResults(t, expression, nil); !reflect.DeepEqual(got, []any{1, 2}) {
		t.Fatalf("concrete outputs = %#v, want [1 2]", got)
	}
	analysis := analyzeExpr(t, expression, NullType())
	if !schemaAdmitsInteger(analysis.Output, 1) || !schemaAdmitsInteger(analysis.Output, 2) {
		t.Fatalf("length output excludes 1 or 2: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func batch3bLoopInput() *oas3.Schema {
	item := BuildObject(map[string]*oas3.Schema{"v": IntegerType()}, []string{"v"})
	return BuildObject(map[string]*oas3.Schema{
		"tags":  ArrayType(StringType()),
		"items": ArrayType(item),
		"n":     IntegerType(),
	}, []string{"tags", "items", "n"})
}

func TestReduceScalarAccumulatorReachesFixpoint(t *testing.T) {
	input := batch3bLoopInput()
	tests := []struct {
		name       string
		expression string
		instance   map[string]any
		want       any
		admitted   func(*oas3.Schema) bool
	}{
		{
			name:       "integer count",
			expression: `reduce .tags[] as $t (0; . + 1)`,
			instance:   map[string]any{"tags": []any{"a"}, "items": []any{}, "n": 0},
			want:       1,
			admitted:   func(schema *oas3.Schema) bool { return schemaAdmitsInteger(schema, 1) },
		},
		{
			name:       "string concatenation",
			expression: `reduce .tags[] as $t (""; . + $t)`,
			instance:   map[string]any{"tags": []any{"a"}, "items": []any{}, "n": 0},
			want:       "a",
			admitted:   func(schema *oas3.Schema) bool { return schemaAdmitsString(schema, "a") },
		},
		{
			name:       "null replaced by item",
			expression: `reduce .tags[] as $t (null; $t)`,
			instance:   map[string]any{"tags": []any{"a"}, "items": []any{}, "n": 0},
			want:       "a",
			admitted:   func(schema *oas3.Schema) bool { return schemaAdmitsString(schema, "a") },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := concreteJQResult(t, test.expression, test.instance); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("concrete output = %#v, want %#v", got, test.want)
			}
			analysis := analyzeExpr(t, test.expression, input)
			if analysis.Verdict == VerdictProvenBroken || !test.admitted(analysis.Output) {
				t.Fatalf("output excludes concrete result: verdict=%s output=%s causes=%v",
					analysis.Verdict, schemaTypeSummary(analysis.Output, 4), analysis.Causes)
			}
		})
	}
}

func TestReduceObjectAccumulatorAdmitsUpdatedProperties(t *testing.T) {
	input := batch3bLoopInput()
	instance := map[string]any{
		"tags":  []any{},
		"items": []any{map[string]any{"v": 7}},
		"n":     0,
	}
	for _, expression := range []string{
		`reduce .items[] as $x ({}; . + {a: $x.v})`,
		`reduce .items[] as $x ({}; .total += $x.v)`,
	} {
		t.Run(expression, func(t *testing.T) {
			concrete := concreteJQResult(t, expression, instance)
			object, ok := concrete.(map[string]any)
			if !ok {
				t.Fatalf("concrete output = %#v, want object", concrete)
			}
			analysis := analyzeExpr(t, expression, input)
			if analysis.Verdict == VerdictProvenBroken {
				t.Fatalf("verdict=%s output=%s causes=%v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), analysis.Causes)
			}
			for key, value := range object {
				integer, ok := value.(int)
				if !ok || !schemaAdmitsInteger(GetProperty(analysis.Output, key, DefaultOptions()), int64(integer)) {
					t.Fatalf("output excludes concrete %s=%#v: %s", key, value, schemaTypeSummary(analysis.Output, 4))
				}
			}
		})
	}
}

func TestForeachAccumulatorEmitsLaterRounds(t *testing.T) {
	expression := `[foreach .tags[] as $t (0; . + 1)]`
	instance := map[string]any{"tags": []any{"a", "b"}, "items": []any{}, "n": 0}
	if got := concreteJQResult(t, expression, instance); !reflect.DeepEqual(got, []any{1, 2}) {
		t.Fatalf("concrete output = %#v, want [1 2]", got)
	}
	analysis := analyzeExpr(t, expression, batch3bLoopInput())
	items := arrayElementUnion(analysis.Output, DefaultOptions())
	if analysis.Verdict == VerdictProvenBroken || !schemaAdmitsInteger(items, 2) {
		t.Fatalf("foreach items exclude 2: verdict=%s output=%s causes=%v",
			analysis.Verdict, schemaTypeSummary(analysis.Output, 4), analysis.Causes)
	}
}

func TestForeachExtractAndLabelsPreserveLaterRounds(t *testing.T) {
	instance := map[string]any{"tags": []any{"a", "b", "c", "d"}, "items": []any{}, "n": 0}
	tests := []struct {
		name       string
		expression string
		want       []any
		array      bool
	}{
		{
			name:       "conditional extract in array",
			expression: `[foreach .tags[] as $t (0; . + 1; if . > 2 then . else . end)]`,
			want:       []any{[]any{1, 2, 3, 4}},
			array:      true,
		},
		{
			name:       "conditional extract breaks label",
			expression: `label $out | foreach .tags[] as $t (0; . + 1; if . > 2 then ., break $out else . end)`,
			want:       []any{1, 2, 3},
		},
		{
			name:       "comma extract breaks label",
			expression: `label $out | foreach .tags[] as $t (0; . + 1; ., (if . > 2 then break $out else empty end))`,
			want:       []any{1, 2, 3},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := concreteJQResults(t, test.expression, instance); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("concrete outputs = %#v, want %#v", got, test.want)
			}
			analysis := analyzeExpr(t, test.expression, batch3bLoopInput())
			output := analysis.Output
			if test.array {
				output = arrayElementUnion(output, DefaultOptions())
			}
			for _, value := range []int64{1, 2, 3} {
				if !schemaAdmitsInteger(output, value) {
					t.Fatalf("output excludes round %d: verdict=%s output=%s causes=%v",
						value, analysis.Verdict, schemaTypeSummary(analysis.Output, 5), analysis.Causes)
				}
			}
		})
	}
}

func TestForeachDownstreamAndLimitPreserveLaterRounds(t *testing.T) {
	instance := map[string]any{"tags": []any{"a", "b", "c"}, "items": []any{}, "n": 0}
	tests := []struct {
		expression string
		want       []any
		array      bool
	}{
		{`foreach .tags[] as $t (0; . + 1) | select(. > 1)`, []any{2, 3}, false},
		{`[limit(2; foreach .tags[] as $t (0; . + 1))]`, []any{[]any{1, 2}}, true},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			if got := concreteJQResults(t, test.expression, instance); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("concrete outputs = %#v, want %#v", got, test.want)
			}
			analysis := analyzeExpr(t, test.expression, batch3bLoopInput())
			output := analysis.Output
			if test.array {
				output = arrayElementUnion(output, DefaultOptions())
			}
			if !schemaAdmitsInteger(output, 2) {
				t.Fatalf("output excludes round 2: verdict=%s output=%s causes=%v",
					analysis.Verdict, schemaTypeSummary(analysis.Output, 5), analysis.Causes)
			}
		})
	}
}

func TestReduceFixpointIsIndependentOfDownstreamContext(t *testing.T) {
	instance := map[string]any{"tags": []any{"a", "b"}, "items": []any{}, "n": 0}
	tests := []struct {
		expression string
		want       []any
		admit      int64
	}{
		{`reduce .tags[] as $t (0; . + 1) | select(. > 0)`, []any{2}, 2},
		{`label $out | reduce .tags[] as $t (0; . + 1)`, []any{2}, 2},
		{`try (reduce .tags[] as $t (0; . + 1))`, []any{2}, 2},
		{`range(0; reduce .tags[] as $t (0; . + 1))`, []any{0, 1}, 1},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			if got := concreteJQResults(t, test.expression, instance); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("concrete outputs = %#v, want %#v", got, test.want)
			}
			analysis := analyzeExpr(t, test.expression, batch3bLoopInput())
			if !schemaAdmitsInteger(analysis.Output, test.admit) {
				t.Fatalf("output excludes %d: verdict=%s output=%s causes=%v",
					test.admit, analysis.Verdict, schemaTypeSummary(analysis.Output, 5), analysis.Causes)
			}
		})
	}
}

func TestReduceAccumulatorIncludesZeroIterationCase(t *testing.T) {
	expression := `reduce .tags[] as $t (0; . + 1)`
	instance := map[string]any{"tags": []any{}, "items": []any{}, "n": 0}
	if got := concreteJQResult(t, expression, instance); got != 0 {
		t.Fatalf("concrete output = %#v, want 0", got)
	}
	analysis := analyzeExpr(t, expression, batch3bLoopInput())
	if !schemaAdmitsInteger(analysis.Output, 0) {
		t.Fatalf("output excludes zero-iteration accumulator: %s", schemaTypeSummary(analysis.Output, 4))
	}
}

func TestReduceRetainsIdentityTrackedArrayAccumulator(t *testing.T) {
	expression := `reduce .tags[] as $t ([]; . + [$t])`
	instance := map[string]any{"tags": []any{"a"}, "items": []any{}, "n": 0}
	if got := concreteJQResult(t, expression, instance); !reflect.DeepEqual(got, []any{"a"}) {
		t.Fatalf("concrete output = %#v, want [a]", got)
	}
	analysis := analyzeExpr(t, expression, batch3bLoopInput())
	items := arrayElementUnion(analysis.Output, DefaultOptions())
	if analysis.Verdict == VerdictProvenBroken || !schemaAdmitsString(items, "a") {
		t.Fatalf("identity-tracked accumulator excludes a: verdict=%s output=%s causes=%v",
			analysis.Verdict, schemaTypeSummary(analysis.Output, 4), analysis.Causes)
	}
}

func TestMultiplyDispatchesAcrossJQOverloads(t *testing.T) {
	t.Run("string repeat", func(t *testing.T) {
		expression := `"ab" | . * 2`
		if got := concreteJQResult(t, expression, nil); got != "abab" {
			t.Fatalf("concrete output = %#v, want abab", got)
		}
		analysis := analyzeExpr(t, expression, NullType())
		if !schemaAdmitsString(analysis.Output, "abab") {
			t.Fatalf("output excludes repeated string: %s", schemaTypeSummary(analysis.Output, 4))
		}
	})

	t.Run("zero repetitions", func(t *testing.T) {
		expression := `"ab" * 0`
		if got := concreteJQResult(t, expression, nil); got != "" {
			t.Fatalf("concrete output = %#v, want empty string", got)
		}
		analysis := analyzeExpr(t, expression, NullType())
		if !schemaAdmitsString(analysis.Output, "") {
			t.Fatalf("output excludes empty string: %s", schemaTypeSummary(analysis.Output, 4))
		}
	})

	t.Run("negative repetitions", func(t *testing.T) {
		expression := `"ab" * -1`
		if got := concreteJQResult(t, expression, nil); got != nil {
			t.Fatalf("concrete output = %#v, want null", got)
		}
		analysis := analyzeExpr(t, expression, NullType())
		if !mightBeType(analysis.Output, oas3.SchemaTypeNull) {
			t.Fatalf("output excludes null: %s", schemaTypeSummary(analysis.Output, 4))
		}
	})

	t.Run("union operands", func(t *testing.T) {
		expression := `.value * .count`
		input := BuildObject(map[string]*oas3.Schema{
			"value": Union([]*oas3.Schema{StringType(), NumberType()}, DefaultOptions()),
			"count": IntegerType(),
		}, []string{"value", "count"})
		for _, test := range []struct {
			instance map[string]any
			want     any
		}{
			{map[string]any{"value": "x", "count": 2}, "xx"},
			{map[string]any{"value": 3, "count": 2}, 6},
		} {
			if got := concreteJQResult(t, expression, test.instance); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("concrete output = %#v, want %#v", got, test.want)
			}
		}
		analysis := analyzeExpr(t, expression, input)
		if !schemaAdmitsString(analysis.Output, "xx") || !schemaAdmitsInteger(analysis.Output, 6) {
			t.Fatalf("distributed multiplication excludes a valid type: %s", schemaTypeSummary(analysis.Output, 4))
		}
	})

	t.Run("recursive object merge", func(t *testing.T) {
		expression := `{"a":{"x":1}} * {"a":{"y":2}}`
		want := map[string]any{"a": map[string]any{"x": 1, "y": 2}}
		if got := concreteJQResult(t, expression, nil); !reflect.DeepEqual(got, want) {
			t.Fatalf("concrete output = %#v, want %#v", got, want)
		}
		analysis := analyzeExpr(t, expression, NullType())
		a := GetProperty(analysis.Output, "a", DefaultOptions())
		if !schemaAdmitsInteger(GetProperty(a, "x", DefaultOptions()), 1) ||
			!schemaAdmitsInteger(GetProperty(a, "y", DefaultOptions()), 2) {
			t.Fatalf("recursive merge lost a property: %s", schemaTypeSummary(analysis.Output, 5))
		}
	})
}

func TestDivideDispatchesStringSplitAndNumbers(t *testing.T) {
	expression := `"a,b" | . / ","`
	want := []any{"a", "b"}
	if got := concreteJQResult(t, expression, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete output = %#v, want %#v", got, want)
	}
	analysis := analyzeExpr(t, expression, NullType())
	if getType(analysis.Output) != "array" ||
		!schemaAdmitsString(getArrayElement(analysis.Output, 0, DefaultOptions()), "a") ||
		!schemaAdmitsString(getArrayElement(analysis.Output, 1, DefaultOptions()), "b") {
		t.Fatalf("split output excludes concrete tuple: %s", schemaTypeSummary(analysis.Output, 5))
	}

	numeric := analyzeExpr(t, `20 / 4`, NullType())
	if !schemaAdmitsInteger(numeric.Output, 5) {
		t.Fatalf("numeric division excludes 5: %s", schemaTypeSummary(numeric.Output, 3))
	}
}

func TestMinusModuloAndComparisonTypeAudit(t *testing.T) {
	minusExpression := `[1,2,1] - [1]`
	if got := concreteJQResult(t, minusExpression, nil); !reflect.DeepEqual(got, []any{2}) {
		t.Fatalf("concrete minus output = %#v, want [2]", got)
	}
	minus := analyzeExpr(t, minusExpression, NullType())
	if getType(minus.Output) != "array" || !schemaAdmitsInteger(arrayElementUnion(minus.Output, DefaultOptions()), 2) {
		t.Fatalf("array minus excludes 2: %s", schemaTypeSummary(minus.Output, 4))
	}

	if got := concreteJQResult(t, `5 % 2`, nil); got != 1 {
		t.Fatalf("concrete modulo output = %#v, want 1", got)
	}
	modulo := analyzeExpr(t, `5 % 2`, NullType())
	if !schemaAdmitsInteger(modulo.Output, 1) {
		t.Fatalf("modulo excludes 1: %s", schemaTypeSummary(modulo.Output, 3))
	}

	if got := concreteJQResult(t, `"a" > 1`, nil); got != true {
		t.Fatalf("concrete comparison output = %#v, want true", got)
	}
	comparison := analyzeExpr(t, `"a" > 1`, NullType())
	if getType(comparison.Output) != "boolean" {
		t.Fatalf("comparison output is not boolean: %s", schemaTypeSummary(comparison.Output, 3))
	}
}

func TestTypedRecursiveLoopsTerminateAtAbstractFixpoint(t *testing.T) {
	input := batch3bLoopInput()
	tests := []struct {
		expression string
		wantType   string
	}{
		{`.n | until(. > 3; . + 1)`, "integer"},
		{`.n | while(. < 3; . + 1)`, "integer"},
		{`limit(2; .n | repeat(. + 1))`, "integer"},
		{`[.tags[] | until(length > 3; . + "x")]`, "array"},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			started := time.Now()
			analysis := analyzeExpr(t, test.expression, input)
			elapsed := time.Since(started)
			t.Logf("analysis completed in %s", elapsed)
			if elapsed > 50*time.Millisecond {
				t.Fatalf("analysis took %s, want <50ms", elapsed)
			}
			if analysis.Verdict == VerdictProvenBroken || !mightBeType(analysis.Output, oas3.SchemaType(test.wantType)) {
				t.Fatalf("verdict=%s output=%s causes=%v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), analysis.Causes)
			}
		})
	}
}

func TestToStreamTerminatesWithoutIterationLimit(t *testing.T) {
	input := loadComponentSchema(t, refItemsDoc, "List")
	started := time.Now()
	analysis := analyzeExpr(t, `tostream | length`, input)
	elapsed := time.Since(started)
	t.Logf("analysis completed in %s", elapsed)
	if elapsed > time.Second {
		t.Fatalf("tostream analysis took %s, want <1s", elapsed)
	}
	if analysis.Verdict == VerdictProvenBroken || !MightBeNumber(analysis.Output) {
		t.Fatalf("verdict=%s output=%s causes=%v", analysis.Verdict, schemaTypeSummary(analysis.Output, 4), analysis.Causes)
	}
}
