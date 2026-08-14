package schemaexec

import (
	"context"
	"testing"
	"time"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/references"
	"github.com/speakeasy-api/openapi/sequencedmap"
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

// --- Final round probes ---

// TestSoundness_ExclusiveBoundSubsumption: Union(number,
// number{exclusiveMinimum:10}) must not collapse onto the bounded schema.
func TestSoundness_ExclusiveBoundSubsumption(t *testing.T) {
	bounded := NumberType()
	bounded.ExclusiveMinimum = oas3.NewExclusiveMinimumFromFloat64(10)
	u := Union([]*oas3.Schema{NumberType(), bounded}, DefaultOptions())
	if u != nil && u.ExclusiveMinimum != nil && len(u.AnyOf) == 0 {
		t.Fatalf("plain number was subsumed by exclusively-bounded number: %s", schemaTypeSummary(u, 2))
	}
}

// TestSoundness_BooleanSchemaFingerprint: {not: true} (unsatisfiable) must not
// dedup-collide with an unconstrained schema of the same type.
func TestSoundness_BooleanSchemaFingerprint(t *testing.T) {
	notTrue := StringType()
	notTrue.Not = oas3.NewJSONSchemaFromBool(true)
	plain := StringType()
	u := Union([]*oas3.Schema{notTrue, plain}, DefaultOptions())
	// The plain string branch must survive: either as the merged result
	// without the not-constraint, or as a distinct anyOf branch.
	admitsPlain := false
	if u != nil {
		if u.Not == nil && getType(u) == "string" {
			admitsPlain = true
		}
		for _, br := range u.AnyOf {
			if left := resolvedLeft(br); left != nil && left.Not == nil && getType(left) == "string" {
				admitsPlain = true
			}
		}
	}
	if !admitsPlain {
		t.Fatalf("plain string branch lost against {not:true}: %s", schemaTypeSummary(u, 3))
	}
}

// TestSoundness_NestedTopDominatesPropertyUnion: anyOf[{x:Top},{x:string}]
// must not pin x to string.
func TestSoundness_NestedTopDominatesPropertyUnion(t *testing.T) {
	branchA := BuildObject(map[string]*oas3.Schema{"x": Top()}, []string{"x"})
	branchB := BuildObject(map[string]*oas3.Schema{"x": StringType()}, []string{"x"})
	in := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branchA),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branchB),
		},
	}
	out := execExpr(t, ".", in)
	if out != nil && getType(out) == "object" && out.Properties != nil {
		if xs, ok := out.Properties.Get("x"); ok {
			if left := resolvedLeft(xs); left != nil && getType(left) == "string" && !isTopSchema(left) &&
				len(left.AnyOf) == 0 && len(left.OneOf) == 0 {
				t.Fatalf("x narrowed to string despite a Top branch: %s", schemaTypeSummary(out, 3))
			}
		}
	}
}

// TestSoundness_AnyOfBranchKeepsNullableAndOpenAP: normalizing anyOf branches
// through the empty base must preserve nullable and additionalProperties:true.
func TestSoundness_AnyOfBranchKeepsNullableAndOpenAP(t *testing.T) {
	nullable := true
	nullableStr := StringType()
	nullableStr.Nullable = &nullable

	openObj := ObjectType()
	openObj.AdditionalProperties = oas3.NewJSONSchemaFromBool(true)

	in := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](nullableStr),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](openObj),
		},
	}
	out := execExpr(t, ".", in)
	if out == nil {
		t.Fatal("got Bottom")
	}
	sawNullable := false
	sawOpenAP := false
	check := func(s *oas3.Schema) {
		if s == nil {
			return
		}
		if s.Nullable != nil && *s.Nullable {
			sawNullable = true
		}
		if s.AdditionalProperties != nil &&
			((s.AdditionalProperties.Right != nil && *s.AdditionalProperties.Right) ||
				(s.AdditionalProperties.Left != nil && isTopSchema(s.AdditionalProperties.Left))) {
			sawOpenAP = true
		}
	}
	check(out)
	for _, br := range out.AnyOf {
		check(resolvedLeft(br))
	}
	for _, br := range out.OneOf {
		check(resolvedLeft(br))
	}
	if !sawNullable {
		t.Errorf("nullable:true was dropped during anyOf normalization: %s", schemaTypeSummary(out, 3))
	}
	if !sawOpenAP {
		t.Errorf("additionalProperties:true was dropped during anyOf normalization: %s", schemaTypeSummary(out, 3))
	}
}

// --- Round E probes (Analyze-contract violations) ---

// TestSoundness_AnyOfExclusiveBoundsNotFlattened: anyOf branches with
// different exclusive bounds must not flatten onto the first branch's bound.
func TestSoundness_AnyOfExclusiveBoundsNotFlattened(t *testing.T) {
	b20 := NumberType()
	b20.ExclusiveMinimum = oas3.NewExclusiveMinimumFromFloat64(20)
	b10 := NumberType()
	b10.ExclusiveMinimum = oas3.NewExclusiveMinimumFromFloat64(10)
	in := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](b20),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](b10),
		},
	}
	a := analyzeExpr(t, ".", in)
	// value 15 is valid (second branch); a flattened exclusiveMinimum:20
	// output would exclude it.
	if a.Verdict == VerdictProven && a.Output != nil && len(a.Output.AnyOf) == 0 &&
		a.Output.ExclusiveMinimum != nil && a.Output.ExclusiveMinimum.GetRight() != nil && *a.Output.ExclusiveMinimum.GetRight() > 15 {
		t.Fatalf("anyOf flattened onto the tighter exclusive bound: %s", schemaTypeSummary(a.Output, 2))
	}
}

// TestSoundness_AnyOfNullableBranchSurvives: anyOf[string, string{nullable}]
// must stay nullable.
func TestSoundness_AnyOfNullableBranchSurvives(t *testing.T) {
	nb := true
	nullableStr := StringType()
	nullableStr.Nullable = &nb
	in := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](nullableStr),
		},
	}
	a := analyzeExpr(t, ".", in)
	admitsNull := false
	check := func(s *oas3.Schema) {
		if s == nil {
			return
		}
		if s.Nullable != nil && *s.Nullable {
			admitsNull = true
		}
		if mightBeType(s, oas3.SchemaTypeNull) {
			admitsNull = true
		}
	}
	check(a.Output)
	if a.Output != nil {
		for _, br := range a.Output.AnyOf {
			check(resolvedLeft(br))
		}
	}
	if !admitsNull {
		t.Fatalf("nullable branch lost in anyOf flatten: %s", schemaTypeSummary(a.Output, 3))
	}
}

// TestSoundness_IndexZeroAdmitsNull: .[0] on a possibly-empty array must admit
// null (jq yields null out of bounds); with minItems >= 1 the null goes away.
func TestSoundness_IndexZeroAdmitsNull(t *testing.T) {
	arr := ArrayType(StringType())
	a := analyzeExpr(t, ".[0]", arr)
	if a.Verdict != VerdictProven {
		t.Fatalf(".[0]: verdict = %s, want proven (causes %v)", a.Verdict, a.Causes)
	}
	nullable := a.Output != nil && a.Output.Nullable != nil && *a.Output.Nullable
	if !nullable && !mightBeType(a.Output, oas3.SchemaTypeNull) {
		t.Fatalf(".[0] on possibly-empty array must admit null, got %s", schemaTypeSummary(a.Output, 2))
	}

	one := int64(1)
	nonEmpty := ArrayType(StringType())
	nonEmpty.MinItems = &one
	b := analyzeExpr(t, ".[0]", nonEmpty)
	if got := getType(b.Output); got != "string" {
		t.Errorf(".[0] with minItems:1 should be exactly string, got %q", got)
	}
	if b.Output != nil && b.Output.Nullable != nil && *b.Output.Nullable {
		t.Errorf(".[0] with minItems:1 should not be nullable")
	}
}

// TestSoundness_PatternPropertiesNotProvenBroken: access to a property
// admitted by patternProperties must not be provably null.
func TestSoundness_PatternPropertiesNotProvenBroken(t *testing.T) {
	obj := ObjectType()
	obj.PatternProperties = sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	obj.PatternProperties.Set("^x$", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()))
	obj.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)

	a := analyzeExpr(t, ".x", obj)
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf(".x admitted by patternProperties classified ProvenBroken")
	}
	if !MightBeString(a.Output) {
		t.Errorf(".x should admit the pattern schema's string, got %s", schemaTypeSummary(a.Output, 2))
	}

	// A name NOT matched by any pattern still follows closed-world rules.
	b := analyzeExpr(t, ".y", obj)
	if b.Verdict != VerdictProvenBroken {
		t.Errorf(".y (no pattern match, AP false): verdict = %s, want proven-broken", b.Verdict)
	}
}

// --- Final contract probes ---

// TestSoundness_HasKeysPatternProperties: has()/keys must account for
// patternProperties-admitted keys.
func TestSoundness_HasKeysPatternProperties(t *testing.T) {
	obj := BuildObject(map[string]*oas3.Schema{"known": StringType()}, []string{"known"})
	obj.PatternProperties = sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	obj.PatternProperties.Set("^x$", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()))
	obj.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)

	out := execExpr(t, `has("x")`, obj)
	if len(out.Enum) == 1 && out.Enum[0].Value == "false" {
		t.Fatalf(`has("x") folded to const false despite matching patternProperties`)
	}

	keysOut := execExpr(t, "keys", obj)
	if keysOut != nil && keysOut.Items != nil && keysOut.Items.Left != nil {
		items := keysOut.Items.Left
		if len(items.Enum) > 0 {
			// A fixed key enum under-enumerates pattern-admitted keys.
			t.Fatalf("keys enumerated a closed key set despite patternProperties: %s", schemaTypeSummary(keysOut, 3))
		}
	}
}

// TestSoundness_AnyOfMinPropertiesNotFlattened: anyOf[object{minProperties:2},
// object{}] must not flatten onto the constrained branch.
func TestSoundness_AnyOfMinPropertiesNotFlattened(t *testing.T) {
	two := int64(2)
	constrained := ObjectType()
	constrained.MinProperties = &two
	in := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](constrained),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](ObjectType()),
		},
	}
	a := analyzeExpr(t, ".", in)
	if a.Output != nil && len(a.Output.AnyOf) == 0 && a.Output.MinProperties != nil {
		t.Fatalf("anyOf flattened onto minProperties-constrained branch: %s", schemaTypeSummary(a.Output, 3))
	}
}

// TestSoundness_AppendKeepsNestedArrayItem: [[.], 1] must admit BOTH the
// nested-array element and the integer element.
func TestSoundness_AppendKeepsNestedArrayItem(t *testing.T) {
	a := analyzeExpr(t, "[[.], 1]", StringType())
	if getType(a.Output) != "array" {
		t.Fatalf("expected array output, got %s", schemaTypeSummary(a.Output, 2))
	}
	items := resolvedLeft(a.Output.Items)
	if items == nil {
		t.Fatal("array output lost its items")
	}
	admitsArray := mightBeType(items, oas3.SchemaTypeArray)
	admitsInt := mightBeType(items, oas3.SchemaTypeInteger) || mightBeType(items, oas3.SchemaTypeNumber)
	if !admitsArray || !admitsInt {
		t.Fatalf("items must admit both the nested array and the integer, got %s", schemaTypeSummary(items, 3))
	}
}

// --- Verdict-round probes ---

// TestSoundness_KeysToEntriesOnArrays: jq defines keys/to_entries on arrays
// (index keys); they must not be provably broken.
func TestSoundness_KeysToEntriesOnArrays(t *testing.T) {
	arr := ArrayType(StringType())
	a := analyzeExpr(t, "keys", arr)
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("keys on array must not be ProvenBroken")
	}
	b := analyzeExpr(t, "to_entries", arr)
	if b.Verdict == VerdictProvenBroken {
		t.Fatal("to_entries on array must not be ProvenBroken")
	}
}

// TestSoundness_ToEntriesPatternProperties: an object whose keys come from
// patternProperties has entries.
func TestSoundness_ToEntriesPatternProperties(t *testing.T) {
	obj := ObjectType()
	obj.PatternProperties = sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	obj.PatternProperties.Set("^x$", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()))
	obj.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)

	a := analyzeExpr(t, "to_entries | .[]", obj)
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("to_entries over pattern-keyed object must not be ProvenBroken")
	}
}

// TestSoundness_OneOfSiblingBaseConstraints: sibling properties next to a
// oneOf must keep contributing to property access.
func TestSoundness_OneOfSiblingBaseConstraints(t *testing.T) {
	base := BuildObject(map[string]*oas3.Schema{"x": StringType()}, []string{"x"})
	base.OneOf = []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](&oas3.Schema{}),
	}
	a := analyzeExpr(t, ".x", base)
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf(".x with sibling properties beside oneOf must not be ProvenBroken")
	}
	if !MightBeString(a.Output) {
		t.Errorf(".x should admit the declared string, got %s", schemaTypeSummary(a.Output, 2))
	}
}

// TestSoundness_GetpathSetpathNotProvenBroken: path ops on shapes the model
// does not fully cover must degrade to Unverifiable, never ProvenBroken.
func TestSoundness_GetpathSetpathNotProvenBroken(t *testing.T) {
	obj := BuildObject(map[string]*oas3.Schema{"x": StringType()}, nil) // x optional
	a := analyzeExpr(t, `getpath(["x"])`, obj)
	if a.Verdict == VerdictProvenBroken {
		t.Fatal("getpath on an optional declared property must not be ProvenBroken")
	}

	b := analyzeExpr(t, `setpath(["x"]; 1)`, ConstNull())
	if b.Verdict == VerdictProvenBroken {
		t.Fatal("setpath on null (which creates {x:1}) must not be ProvenBroken")
	}
}

// TestSoundness_NullableFlowsThroughOperations: the {type,nullable:true}
// representation must stay visible to navigation, type, and unions.
func TestSoundness_NullableFlowsThroughOperations(t *testing.T) {
	nb := true

	// Property access on a nullable object: null.x → null is possible.
	innerObj := BuildObject(map[string]*oas3.Schema{"x": StringType()}, []string{"x"})
	innerObj.Nullable = &nb
	in := BuildObject(map[string]*oas3.Schema{"o": innerObj}, []string{"o"})
	a := analyzeExpr(t, ".o.x", in)
	if !mightBeType(a.Output, oas3.SchemaTypeNull) &&
		!(a.Output != nil && a.Output.Nullable != nil && *a.Output.Nullable) {
		t.Fatalf(".o.x on nullable object must admit null, got %s", schemaTypeSummary(a.Output, 2))
	}

	// type on a nullable string must admit "null".
	nullableStr := StringType()
	nullableStr.Nullable = &nb
	b := analyzeExpr(t, "type", nullableStr)
	admitsNullStr := false
	var walk func(s *oas3.Schema)
	walk = func(s *oas3.Schema) {
		if s == nil {
			return
		}
		for _, n := range s.Enum {
			if n != nil && n.Value == "null" {
				admitsNullStr = true
			}
		}
		for _, br := range s.AnyOf {
			walk(resolvedLeft(br))
		}
	}
	walk(b.Output)
	if !admitsNullStr {
		t.Fatalf("type on nullable string must admit \"null\", got %s", schemaTypeSummary(b.Output, 3))
	}

	// Union must not subsume a nullable branch into a non-nullable one.
	c := analyzeExpr(t, "., tostring", nullableStr)
	if !mightBeType(c.Output, oas3.SchemaTypeNull) &&
		!(c.Output != nil && c.Output.Nullable != nil && *c.Output.Nullable) {
		t.Fatalf("union dropped the nullable branch: %s", schemaTypeSummary(c.Output, 3))
	}
}

// TestSoundness_EmptyArrayFolds: add/min over a possibly-empty array admit
// null; a proven-non-empty array does not.
func TestSoundness_EmptyArrayFolds(t *testing.T) {
	arr := ArrayType(NumberType())
	for _, expr := range []string{"add", "min"} {
		a := analyzeExpr(t, expr, arr)
		nullable := a.Output != nil && a.Output.Nullable != nil && *a.Output.Nullable
		if !nullable && !mightBeType(a.Output, oas3.SchemaTypeNull) {
			t.Errorf("%s over possibly-empty array must admit null, got %s", expr, schemaTypeSummary(a.Output, 2))
		}
	}

	one := int64(1)
	nonEmpty := ArrayType(NumberType())
	nonEmpty.MinItems = &one
	b := analyzeExpr(t, "add", nonEmpty)
	if got := getType(b.Output); got != "number" {
		t.Errorf("add over minItems:1 number array should be number, got %q", got)
	}
}

// TestSoundness_SliceClearsMinItems: .[0:0] can be empty regardless of the
// source array's minItems.
func TestSoundness_SliceClearsMinItems(t *testing.T) {
	three := int64(3)
	arr := ArrayType(StringType())
	arr.MinItems = &three
	a := analyzeExpr(t, ".[0:0]", arr)
	if a.Output != nil && a.Output.MinItems != nil && *a.Output.MinItems > 0 {
		t.Fatalf("slice output kept minItems=%d; slices can be empty", *a.Output.MinItems)
	}
}
