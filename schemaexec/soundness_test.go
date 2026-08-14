package schemaexec

import (
	"context"
	"testing"
	"time"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/references"
)

// These tests encode adversarial soundness probes: cases where the executor
// used to silently NARROW the output (discarding possible values), which is
// forbidden by the over-approximation contract and would let the Analyze API
// claim Proven (or ProvenBroken) incorrectly.

// TestSoundness_TopDominatesPropertyMerge: merging {x: <unknown>} with
// {x: "ok"} across branches must NOT narrow x to "ok".
func TestSoundness_TopDominatesPropertyMerge(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"flag": BoolType(),
		"blob": {}, // unconstrained
	}, []string{"flag", "blob"})

	q, err := gojq.Parse(`if .flag then {x: .blob} else {x: "ok"} end`)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(context.Background(), q, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.Verdict == VerdictProven {
		t.Fatalf("branchy object with unconstrained property must not be Proven; got %s (output: %s)",
			a.Verdict, schemaTypeSummary(a.Output, 3))
	}
}

// TestSoundness_UnionKeepsCombinatorBranches: a oneOf-typed value unioned
// with a constant must keep the oneOf alternatives, not collapse to the
// constant.
func TestSoundness_UnionKeepsCombinatorBranches(t *testing.T) {
	oneOf := &oas3.Schema{
		OneOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](BoolType()),
		},
	}

	q, err := gojq.Parse(`., 0`)
	if err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(context.Background(), q, oneOf)
	if err != nil {
		t.Fatal(err)
	}
	// The output must still admit strings and booleans, not just integer 0.
	if getType(a.Output) == "integer" {
		t.Fatalf("union collapsed to integer only, discarding oneOf input branches (output: %s)",
			schemaTypeSummary(a.Output, 3))
	}
	if a.Verdict == VerdictProven && !MightBeString(a.Output) && !mightBeType(a.Output, oas3.SchemaTypeBoolean) {
		t.Fatalf("Proven output must admit the input alternatives; got %s", schemaTypeSummary(a.Output, 3))
	}
}

// TestSoundness_UnresolvedRefShellNotProvenBroken: property access through an
// unresolved $ref alternative must widen, never conclude "provably null".
func TestSoundness_UnresolvedRefShellNotProvenBroken(t *testing.T) {
	ref := references.Reference("#/components/schemas/DoesNotResolve")
	shell := &oas3.Schema{Ref: &ref}
	in := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeObject),
		OneOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](shell),
		},
	}

	q, err := gojq.Parse(".x")
	if err != nil {
		t.Fatal(err)
	}
	a, err := Analyze(context.Background(), q, in)
	if err != nil {
		t.Fatal(err)
	}
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf("unresolved $ref alternative must not make .x provably broken (output: %s)",
			schemaTypeSummary(a.Output, 2))
	}
}

// TestSemantics_RawOpenWorld: under SchemaSemanticsRaw an object without
// additionalProperties is open — a typo'd leaf is Unverifiable, not broken.
func TestSemantics_RawOpenWorld(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"status": StringType(),
	}, []string{"status"})

	q, err := gojq.Parse(".statu")
	if err != nil {
		t.Fatal(err)
	}

	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw
	a, err := Analyze(context.Background(), q, in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf("raw semantics: absent additionalProperties is open-world; .statu must not be ProvenBroken")
	}
	if a.Semantics != SchemaSemanticsRaw {
		t.Errorf("Analysis.Semantics = %v, want SchemaSemanticsRaw", a.Semantics)
	}

	// Default (Speakeasy) semantics: closed world, provably broken.
	b, err := Analyze(context.Background(), q, in)
	if err != nil {
		t.Fatal(err)
	}
	if b.Verdict != VerdictProvenBroken {
		t.Fatalf("speakeasy semantics: .statu should be ProvenBroken, got %s", b.Verdict)
	}
	if b.Semantics != SchemaSemanticsSpeakeasy {
		t.Errorf("Analysis.Semantics = %v, want SchemaSemanticsSpeakeasy", b.Semantics)
	}
}

// TestSemantics_RawNoImpliedDispatch: raw semantics does not imply object
// from properties at dispatch — untyped navigation widens instead of
// resolving.
func TestSemantics_RawNoImpliedDispatch(t *testing.T) {
	in := untypedObject(map[string]*oas3.Schema{"id": StringType()}, []string{"id"})

	q, err := gojq.Parse(".id")
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw
	a, err := Analyze(context.Background(), q, in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("raw semantics must not prove untyped navigation broken")
	}
	if getType(a.Output) == "string" {
		t.Fatal("raw semantics should not have resolved .id through implied object dispatch")
	}
}

// TestSoundness_MergeDoesNotMutateInputs: merging must never mutate the input
// schemas — s1 may be a resolved component schema shared across the document.
func TestSoundness_MergeDoesNotMutateInputs(t *testing.T) {
	s1 := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
	}, []string{"a"})
	s2 := BuildObject(map[string]*oas3.Schema{
		"b": IntegerType(),
	}, []string{"b"})

	merged, err := mergeSchemas(s1, s2)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Properties.Len() != 2 {
		t.Fatalf("merged should have 2 properties, got %d", merged.Properties.Len())
	}
	if s1.Properties.Len() != 1 {
		t.Errorf("merge mutated s1.Properties (now %d entries); shallow clone shared the map", s1.Properties.Len())
	}
	if len(s1.Required) != 1 || s1.Required[0] != "a" {
		t.Errorf("merge mutated s1.Required: %v", s1.Required)
	}
	if s2.Properties.Len() != 1 {
		t.Errorf("merge mutated s2.Properties (now %d entries)", s2.Properties.Len())
	}
}

// TestSoundness_RecursiveMergeTerminates: disjunctively merging two DISTINCT
// self-recursive schemas must terminate (pair-keyed cycle guard).
func TestSoundness_RecursiveMergeTerminates(t *testing.T) {
	mkRec := func(extra string) *oas3.Schema {
		node := BuildObject(map[string]*oas3.Schema{
			extra: StringType(),
		}, []string{extra})
		node.Properties.Set("next", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](node))
		return node
	}
	a := mkRec("a")
	b := mkRec("b")

	done := make(chan error, 1)
	go func() {
		_, err := mergeSchemasMode(a, b, MergeDisjunctive)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("merge failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("recursive disjunctive merge did not terminate")
	}
}
