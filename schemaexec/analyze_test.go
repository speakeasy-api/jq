package schemaexec

import (
	"context"
	"strings"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func analyzeExpr(t *testing.T, expr string, input *oas3.Schema) *Analysis {
	t.Helper()
	q, err := gojq.Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	a, err := Analyze(context.Background(), q, input)
	if err != nil {
		t.Fatalf("Analyze(%q): %v", expr, err)
	}
	return a
}

// personSchema is a typical closed response object.
func personSchema() *oas3.Schema {
	return BuildObject(map[string]*oas3.Schema{
		"id":     StringType(),
		"name":   StringType(),
		"age":    IntegerType(),
		"status": StringType(),
	}, []string{"id", "status"})
}

// TestAnalyze_Proven: a valid projection over declared fields yields a
// concrete schema and VerdictProven.
func TestAnalyze_Proven(t *testing.T) {
	a := analyzeExpr(t, ".id", personSchema())
	if a.Verdict != VerdictProven {
		t.Fatalf(".id: verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if got := getType(a.Output); got != "string" {
		t.Errorf(".id: output type = %q, want string", got)
	}
	if len(a.Causes) != 0 {
		t.Errorf(".id: expected no causes, got %v", a.Causes)
	}
}

// TestAnalyze_ProvenNullUnion: jq missing-key access on an OPTIONAL property
// legitimately yields null ∪ the declared type. That is Proven, not broken.
func TestAnalyze_ProvenNullUnion(t *testing.T) {
	a := analyzeExpr(t, ".name", personSchema()) // name is optional
	if a.Verdict != VerdictProven {
		t.Fatalf(".name (optional): verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if !MightBeString(a.Output) {
		t.Errorf(".name (optional): output should admit string, got %s", schemaTypeSummary(a.Output, 2))
	}
	// The null branch may be represented as anyOf-with-null or as
	// {type: string, nullable: true} (Union's nullable optimization).
	nullable := a.Output != nil && a.Output.Nullable != nil && *a.Output.Nullable
	if !nullable && !mightBeType(a.Output, oas3.SchemaTypeNull) {
		t.Errorf(".name (optional): output should admit null, got %s", schemaTypeSummary(a.Output, 2))
	}
}

// TestAnalyze_ProvenBroken_LeafTypo: accessing an undeclared key on a closed
// object provably yields null for every input.
func TestAnalyze_ProvenBroken_LeafTypo(t *testing.T) {
	a := analyzeExpr(t, ".statu", personSchema())
	if a.Verdict != VerdictProvenBroken {
		t.Fatalf(".statu: verdict = %s, want proven-broken (output: %s)",
			a.Verdict, schemaTypeSummary(a.Output, 2))
	}
	if len(a.Causes) == 0 {
		t.Error(".statu: expected a cause explaining the null output")
	}
}

// TestAnalyze_ProvenBroken_DeadPaths: iterating a provably-null value kills
// every execution path (Bottom): no output for any input.
func TestAnalyze_ProvenBroken_DeadPaths(t *testing.T) {
	a := analyzeExpr(t, ".missing[].name", personSchema())
	if a.Verdict != VerdictProvenBroken {
		t.Fatalf(".missing[].name: verdict = %s, want proven-broken (output: %s)",
			a.Verdict, schemaTypeSummary(a.Output, 2))
	}
}

// TestAnalyze_Unverifiable_TopWithCause: property access on a completely
// unconstrained schema widens to Top; the verdict must carry a cause.
func TestAnalyze_Unverifiable_TopWithCause(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"blob": {}, // unconstrained
	}, []string{"blob"})

	a := analyzeExpr(t, ".blob.x", in)
	if a.Verdict != VerdictUnverifiable {
		t.Fatalf(".blob.x: verdict = %s, want unverifiable", a.Verdict)
	}
	if len(a.Causes) == 0 {
		t.Fatal(".blob.x: expected causes")
	}
	found := false
	for _, c := range a.Causes {
		if strings.Contains(c, "property access on non-object type") {
			found = true
		}
	}
	if !found {
		t.Errorf(".blob.x: causes should mention the recorded Top cause, got %v", a.Causes)
	}
}

// TestAnalyze_Unverifiable_DeepNestedTop: an array whose ITEMS are Top must be
// Unverifiable — the walk is deep, not top-level-only.
func TestAnalyze_Unverifiable_DeepNestedTop(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"blob": {}, // unconstrained
	}, []string{"blob"})

	// Array literal wrapping a Top-yielding access: output is array[Top].
	a := analyzeExpr(t, "[.blob.x]", in)
	if got := getType(a.Output); got != "array" {
		t.Fatalf("[.blob.x]: output type = %q, want array", got)
	}
	if a.Verdict != VerdictUnverifiable {
		t.Fatalf("[.blob.x]: verdict = %s, want unverifiable (array-of-Top is not proven)", a.Verdict)
	}
}

// TestAnalyze_Unverifiable_TopInsideObjectProperty: Top nested in an object
// property must be found by the deep walk.
func TestAnalyze_Unverifiable_TopInsideObjectProperty(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"blob": {},
	}, []string{"blob"})

	a := analyzeExpr(t, "{value: .blob.x}", in)
	if got := getType(a.Output); got != "object" {
		t.Fatalf("{value: .blob.x}: output type = %q, want object", got)
	}
	if a.Verdict != VerdictUnverifiable {
		t.Fatalf("{value: .blob.x}: verdict = %s, want unverifiable", a.Verdict)
	}
	found := false
	for _, c := range a.Causes {
		if strings.Contains(c, ".properties.value") {
			found = true
		}
	}
	if !found {
		t.Errorf("causes should locate the Top at $.properties.value, got %v", a.Causes)
	}
}

// TestAnalyze_NestedNullInsideContainerIsNotBroken: an array that always
// contains null is still an array — only an ALL-null root output is broken.
func TestAnalyze_NestedNullInsideContainerIsNotBroken(t *testing.T) {
	a := analyzeExpr(t, "[.statu]", personSchema())
	if got := getType(a.Output); got != "array" {
		t.Fatalf("[.statu]: output type = %q, want array", got)
	}
	if a.Verdict == VerdictProvenBroken {
		t.Errorf("[.statu]: nested null must not classify the container as broken")
	}
}

// TestAnalyze_OneOfInput: projecting a oneOf-typed field must not crash and
// must not be classified broken; Unverifiable or better is acceptable.
func TestAnalyze_OneOfInput(t *testing.T) {
	oneOf := &oas3.Schema{
		OneOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](BuildObject(map[string]*oas3.Schema{
				"kind": StringType(),
			}, []string{"kind"})),
		},
	}
	in := BuildObject(map[string]*oas3.Schema{"format": oneOf}, []string{"format"})

	a := analyzeExpr(t, ".format", in)
	if a.Verdict == VerdictProvenBroken {
		t.Errorf(".format (oneOf): must not be proven-broken, got %s (output: %s)",
			a.Verdict, schemaTypeSummary(a.Output, 2))
	}
}

// TestAnalyze_UntypedInputSchemas: implied types and the verdict API compose —
// untyped-with-properties inputs still produce Proven results.
func TestAnalyze_UntypedInputSchemas(t *testing.T) {
	in := untypedObject(map[string]*oas3.Schema{
		"id": StringType(),
	}, []string{"id"})

	a := analyzeExpr(t, ".id", in)
	if a.Verdict != VerdictProven {
		t.Fatalf(".id on untyped object: verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if got := getType(a.Output); got != "string" {
		t.Errorf(".id on untyped object: output = %q, want string", got)
	}
}

// TestAnalyze_RefItems: $ref'd array items resolve and stay Proven end-to-end.
func TestAnalyze_RefItems(t *testing.T) {
	list := loadComponentSchema(t, refItemsDoc, "List")
	a := analyzeExpr(t, ".items[].name", list)
	if a.Verdict != VerdictProven {
		t.Fatalf(".items[].name: verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
	if got := getType(a.Output); got != "string" {
		t.Errorf(".items[].name: output = %q, want string", got)
	}
}

// TestAnalyze_StrictModeIgnored: Analyze runs lenient even when the caller
// passes StrictMode; classification needs the completed output schema.
func TestAnalyze_StrictModeIgnored(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{"blob": {}}, []string{"blob"})
	q, err := gojq.Parse(".blob.x")
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.StrictMode = true
	a, err := Analyze(context.Background(), q, in, opts)
	if err != nil {
		t.Fatalf("Analyze must not fail in strict mode: %v", err)
	}
	if a.Verdict != VerdictUnverifiable {
		t.Errorf("verdict = %s, want unverifiable", a.Verdict)
	}
}

// TestClassifyOutput_Direct exercises the classifier over hand-built schemas.
func TestClassifyOutput_Direct(t *testing.T) {
	tests := []struct {
		name   string
		schema *oas3.Schema
		want   Verdict
	}{
		{"bottom", nil, VerdictProvenBroken},
		{"const null", ConstNull(), VerdictProvenBroken},
		{"null union string", func() *oas3.Schema {
			return Union([]*oas3.Schema{ConstNull(), StringType()}, DefaultOptions())
		}(), VerdictProven},
		{"plain string", StringType(), VerdictProven},
		{"top", Top(), VerdictUnverifiable},
		{"array of top", ArrayType(Top()), VerdictUnverifiable},
		{"array of string", ArrayType(StringType()), VerdictProven},
		{"object with top property", BuildObject(map[string]*oas3.Schema{
			"x": Top(),
		}, []string{"x"}), VerdictUnverifiable},
		{"object with nested array of top", BuildObject(map[string]*oas3.Schema{
			"list": ArrayType(Top()),
		}, []string{"list"}), VerdictUnverifiable},
		{"union of nulls collapses to broken", func() *oas3.Schema {
			return Union([]*oas3.Schema{ConstNull(), ConstNull()}, DefaultOptions())
		}(), VerdictProvenBroken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, causes := classifyOutput(tt.schema, nil)
			if got != tt.want {
				t.Errorf("classifyOutput() = %s, want %s (causes: %v)", got, tt.want, causes)
			}
		})
	}
}

// TestCollectSchemaIssues_CycleSafe: the deep walk must terminate on cyclic
// schemas (collapse breaks cycles by embedding original pointers).
func TestCollectSchemaIssues_CycleSafe(t *testing.T) {
	node := BuildObject(map[string]*oas3.Schema{"name": StringType()}, []string{"name"})
	// Introduce a self-reference: node.properties.next = node
	node.Properties.Set("next", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](node))

	issues := collectSchemaIssues(node, nil)
	if len(issues) != 0 {
		t.Errorf("cyclic clean schema: expected no issues, got %v", issues)
	}
}
