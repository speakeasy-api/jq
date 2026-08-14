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

// --- Round C probes: each encodes a narrowing found by adversarial review. ---

// TestSoundness_TopSurvivesDedupAgainstCombinator: Union([oneOf[...], Top])
// must be Top — dedup fingerprinting must not collide a combinator schema
// with Top and drop either branch.
func TestSoundness_TopSurvivesDedupAgainstCombinator(t *testing.T) {
	oneOf := &oas3.Schema{
		OneOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](BoolType()),
		},
	}
	u := Union([]*oas3.Schema{oneOf, Top()}, DefaultOptions())
	if !isTopSchema(u) {
		t.Fatalf("Union with a Top branch must be Top, got %s", schemaTypeSummary(u, 3))
	}
}

// TestSoundness_SubsumptionDoesNotAssumeTypelessAcceptsAll: a typeless
// combinator schema must not be treated as universal in subsumption:
// Union([oneOf[string,bool], integer]) must keep the integer branch.
func TestSoundness_SubsumptionDoesNotAssumeTypelessAcceptsAll(t *testing.T) {
	oneOf := &oas3.Schema{
		OneOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](BoolType()),
		},
	}
	u := Union([]*oas3.Schema{oneOf, IntegerType()}, DefaultOptions())
	if !mightBeType(u, oas3.SchemaTypeInteger) {
		t.Fatalf("integer branch was discarded: %s", schemaTypeSummary(u, 3))
	}
	// And the combinator branch must survive too.
	found := false
	for _, br := range u.AnyOf {
		if br.Left != nil && len(br.Left.OneOf) > 0 {
			found = true
		}
	}
	if len(u.OneOf) > 0 {
		found = true
	}
	if !found {
		t.Fatalf("oneOf branch was discarded: %s", schemaTypeSummary(u, 3))
	}
}

// TestSoundness_AnyOfEnumBranchesNotNarrowed: collapsing same-type anyOf
// branches must union differing const/enum values, not keep only the first.
func TestSoundness_AnyOfEnumBranchesNotNarrowed(t *testing.T) {
	branchA := BuildObject(map[string]*oas3.Schema{"x": ConstString("a")}, []string{"x"})
	branchB := BuildObject(map[string]*oas3.Schema{"x": ConstString("b")}, []string{"x"})
	in := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branchA),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branchB),
		},
	}
	out := execExpr(t, ".x", in)
	admitsValue := func(s *oas3.Schema, v string) bool {
		var walk func(s *oas3.Schema) bool
		walk = func(s *oas3.Schema) bool {
			if s == nil {
				return false
			}
			if len(s.Enum) == 0 && s.Const == nil && len(s.AnyOf) == 0 && len(s.OneOf) == 0 {
				return MightBeString(s) // unconstrained string admits any value
			}
			for _, n := range s.Enum {
				if n != nil && n.Value == v {
					return true
				}
			}
			if s.Const != nil && s.Const.Value == v {
				return true
			}
			for _, br := range s.AnyOf {
				if br.Left != nil && walk(br.Left) {
					return true
				}
			}
			for _, br := range s.OneOf {
				if br.Left != nil && walk(br.Left) {
					return true
				}
			}
			return false
		}
		return walk(s)
	}
	if !admitsValue(out, "a") || !admitsValue(out, "b") {
		t.Fatalf(".x must admit both \"a\" and \"b\", got %s", schemaTypeSummary(out, 3))
	}
}

// TestSemantics_RawObjectMergeKeepsOpenWorld: under raw semantics, unioning an
// open object with an object literal must not pin undeclared properties to the
// literal's type.
func TestSemantics_RawObjectMergeKeepsOpenWorld(t *testing.T) {
	in := ObjectType() // type: object, no properties, no additionalProperties

	q, err := gojq.Parse(`., {"x": "fixed"}`)
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw
	a, err := Analyze(context.Background(), q, in, opts)
	if err != nil {
		t.Fatal(err)
	}
	// The identity branch admits {"x": 0}; a Proven output narrowed to
	// x:string would exclude it.
	if a.Verdict == VerdictProven {
		if props := a.Output.Properties; props != nil {
			if xs, ok := props.Get("x"); ok && xs.Left != nil && getType(xs.Left) == "string" {
				t.Fatalf("raw semantics: undeclared x narrowed to string: %s", schemaTypeSummary(a.Output, 3))
			}
		}
	}
}

// TestSemantics_RawIterationIncludesUndeclared: `.[]` over an object with a
// declared property but absent additionalProperties must admit more than the
// declared type under raw semantics.
func TestSemantics_RawIterationIncludesUndeclared(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{"known": StringType()}, []string{"known"})

	q, err := gojq.Parse(".[]")
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw
	a, err := Analyze(context.Background(), q, in, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.Verdict == VerdictProven && getType(a.Output) == "string" {
		t.Fatalf("raw semantics: iteration excluded undeclared property values: %s", schemaTypeSummary(a.Output, 2))
	}
}

// TestSemantics_RawBuiltinGuardsDoNotPrune: `split(",")` on an untyped schema
// with properties must not be provably broken under raw semantics (the value
// may legitimately be a string).
func TestSemantics_RawBuiltinGuardsDoNotPrune(t *testing.T) {
	in := untypedObject(map[string]*oas3.Schema{"id": StringType()}, []string{"id"})
	inWrap := BuildObject(map[string]*oas3.Schema{"blob": in}, []string{"blob"})

	q, err := gojq.Parse(`.blob | split(",")`)
	if err != nil {
		t.Fatal(err)
	}
	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw
	a, err := Analyze(context.Background(), q, inWrap, opts)
	if err != nil {
		t.Fatal(err)
	}
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf("raw semantics: split on untyped value must not be proven broken")
	}
}

// TestSoundness_HasRespectsAdditionalProperties: has("extra") on an object
// with additionalProperties: true may be true — never provably false.
func TestSoundness_HasRespectsAdditionalProperties(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{"known": StringType()}, []string{"known"})
	in.AdditionalProperties = oas3.NewJSONSchemaFromBool(true)

	out := execExpr(t, `has("extra")`, in)
	if len(out.Enum) == 1 && out.Enum[0].Value == "false" {
		t.Fatalf(`has("extra") folded to const false despite additionalProperties: true`)
	}
}

// TestSoundness_AddResolvesRefItems: add over an array whose items are a
// $ref'd string schema must produce a string, not the numeric fallback.
func TestSoundness_AddResolvesRefItems(t *testing.T) {
	list := loadComponentSchema(t, refItemsDoc, "List")
	// .items is array of $ref Item (objects) — use map(.name)|add for strings
	out := runQuery(t, ".items | map(.name) | add", list)
	if out == nil {
		t.Fatal("got Bottom")
	}
	if getType(out) == "number" || getType(out) == "integer" {
		t.Fatalf("add fell into numeric fallback on string items: %s", schemaTypeSummary(out, 2))
	}
}

// TestSoundness_EmptyArrayNotDroppedAgainstNonArray: Union([[], string]) must
// keep the empty-array alternative.
func TestSoundness_EmptyArrayNotDroppedAgainstNonArray(t *testing.T) {
	zero := int64(0)
	emptyArr := &oas3.Schema{
		Type:     oas3.NewTypeFromString(oas3.SchemaTypeArray),
		MaxItems: &zero,
	}
	u := Union([]*oas3.Schema{emptyArr, StringType()}, DefaultOptions())
	if !mightBeType(u, oas3.SchemaTypeArray) {
		t.Fatalf("empty-array branch was discarded: %s", schemaTypeSummary(u, 3))
	}
}

// TestSoundness_NullableRewriteSkipsEnums: Union([const "x", null]) must still
// admit null explicitly — {enum:["x"], nullable:true} would not.
func TestSoundness_NullableRewriteSkipsEnums(t *testing.T) {
	u := Union([]*oas3.Schema{ConstString("x"), ConstNull()}, DefaultOptions())
	if u.Nullable != nil && *u.Nullable && len(u.Enum) > 0 {
		hasNull := false
		for _, n := range u.Enum {
			if n != nil && n.Tag == "!!null" {
				hasNull = true
			}
		}
		if !hasNull {
			t.Fatalf("nullable+enum without null value excludes null: %s", schemaTypeSummary(u, 2))
		}
	}
	if !mightBeType(u, oas3.SchemaTypeNull) {
		t.Fatalf("null branch lost: %s", schemaTypeSummary(u, 2))
	}
}

// --- Round D probes ---

// TestSoundness_UnionItemlessArrayNotNarrowed: Union(array<any>, array<string>)
// must keep any-typed items.
func TestSoundness_UnionItemlessArrayNotNarrowed(t *testing.T) {
	anyArr := &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeArray)}
	u := Union([]*oas3.Schema{anyArr, ArrayType(StringType())}, DefaultOptions())
	if getType(u) == "array" {
		if left := resolvedLeft(u.Items); left != nil && getType(left) == "string" && !isTopSchema(left) {
			// items narrowed to string only — the itemless branch admitted anything
			if len(left.AnyOf) == 0 && len(left.OneOf) == 0 {
				t.Fatalf("array items narrowed to string; itemless branch discarded: %s", schemaTypeSummary(u, 3))
			}
		}
	}
}

// TestSoundness_AnyOfArrayItemsNotNarrowed: collapsing anyOf[array<any>,
// array<string>] must not pin items to string.
func TestSoundness_AnyOfArrayItemsNotNarrowed(t *testing.T) {
	anyArr := &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeArray)}
	in := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](anyArr),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](ArrayType(StringType())),
		},
	}
	out := execExpr(t, ".", in)
	if getType(out) == "array" {
		if left := resolvedLeft(out.Items); left != nil && getType(left) == "string" &&
			len(left.AnyOf) == 0 && len(left.OneOf) == 0 && !isTopSchema(left) {
			t.Fatalf("anyOf array collapse narrowed items to string: %s", schemaTypeSummary(out, 3))
		}
	}
}

// TestSoundness_EnumSubsumptionRespectsFacets: Union(const "x",
// string{minLength:10}) must keep the const — "x" does not satisfy the
// minLength, so the string schema does not subsume it.
func TestSoundness_EnumSubsumptionRespectsFacets(t *testing.T) {
	ten := int64(10)
	longStr := StringType()
	longStr.MinLength = &ten
	u := Union([]*oas3.Schema{ConstString("x"), longStr}, DefaultOptions())
	admitsX := false
	var walk func(s *oas3.Schema)
	walk = func(s *oas3.Schema) {
		if s == nil || admitsX {
			return
		}
		for _, n := range s.Enum {
			if n != nil && n.Value == "x" {
				admitsX = true
			}
		}
		if s.Const != nil && s.Const.Value == "x" {
			admitsX = true
		}
		for _, br := range s.AnyOf {
			walk(resolvedLeft(br))
		}
		for _, br := range s.OneOf {
			walk(resolvedLeft(br))
		}
		if getType(s) == "string" && len(s.Enum) == 0 && s.Const == nil && s.MinLength == nil {
			admitsX = true // unconstrained string admits "x"
		}
	}
	walk(u)
	if !admitsX {
		t.Fatalf("const \"x\" was discarded against string{minLength:10}: %s", schemaTypeSummary(u, 3))
	}
}

// TestSoundness_ToEntriesOpenObjectNotEmpty: to_entries on an object with
// additionalProperties: true must not fold to the empty array.
func TestSoundness_ToEntriesOpenObjectNotEmpty(t *testing.T) {
	in := ObjectType()
	in.AdditionalProperties = oas3.NewJSONSchemaFromBool(true)
	out := execExpr(t, "to_entries", in)
	if out != nil && out.MaxItems != nil && *out.MaxItems == 0 {
		t.Fatalf("to_entries on an open object folded to empty array")
	}
}

// TestSoundness_AddUnknownItemsNotNumber: add over an array with unknown item
// type must not narrow to number.
func TestSoundness_AddUnknownItemsNotNumber(t *testing.T) {
	anyArr := &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeArray)}
	out := execExpr(t, "add", anyArr)
	if got := getType(out); got == "number" || got == "integer" {
		t.Fatalf("add over unknown items narrowed to %s", got)
	}
}

// TestSemantics_RawAPMergeKeepsOpen: merging an open (absent-AP) object with
// an AP-schema object under raw semantics must not impose the AP schema on
// the open branch.
func TestSemantics_RawAPMergeKeepsOpen(t *testing.T) {
	open := ObjectType() // absent AP: open under raw
	apObj := ObjectType()
	apObj.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw
	u := Union([]*oas3.Schema{open, apObj}, opts)
	if u != nil && getType(u) == "object" && u.AdditionalProperties != nil {
		if left := resolvedLeft(u.AdditionalProperties); left != nil && getType(left) == "string" && !isTopSchema(left) {
			t.Fatalf("raw semantics: merged AP narrowed to string despite an open branch: %s", schemaTypeSummary(u, 3))
		}
	}
}
