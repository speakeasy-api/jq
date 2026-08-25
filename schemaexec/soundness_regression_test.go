package schemaexec

import (
	"strings"
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

func schemaAdmitsString(s *oas3.Schema, want string) bool {
	if s == nil {
		return false
	}
	if isTopSchema(s) {
		return true
	}
	if s.Const != nil {
		return s.Const.Value == want
	}
	if len(s.Enum) > 0 {
		for _, node := range s.Enum {
			if node != nil && node.Value == want {
				return true
			}
		}
		return false
	}
	for _, branch := range s.AnyOf {
		if schemaAdmitsString(resolvedLeft(branch), want) {
			return true
		}
	}
	for _, branch := range s.OneOf {
		if schemaAdmitsString(resolvedLeft(branch), want) {
			return true
		}
	}
	return getType(s) == "string"
}

func schemaAdmitsInteger(s *oas3.Schema, want int64) bool {
	if s == nil {
		return false
	}
	if isTopSchema(s) {
		return true
	}
	values := s.Enum
	if s.Const != nil {
		values = []*yaml.Node{s.Const}
	}
	if len(values) > 0 {
		for _, node := range values {
			var got int64
			if node != nil && node.Decode(&got) == nil && got == want {
				return true
			}
		}
		return false
	}
	for _, branch := range s.AnyOf {
		if schemaAdmitsInteger(resolvedLeft(branch), want) {
			return true
		}
	}
	for _, branch := range s.OneOf {
		if schemaAdmitsInteger(resolvedLeft(branch), want) {
			return true
		}
	}
	typ := getType(s)
	return typ == "integer" || typ == "number"
}

func TestIterationOverUnknownSchemaIsUnverifiable(t *testing.T) {
	for _, expr := range []string{`.[]`, `.[]?`} {
		t.Run(expr, func(t *testing.T) {
			a := analyzeExpr(t, expr, Top())
			if a.Verdict != VerdictUnverifiable {
				t.Fatalf("verdict = %s, want unverifiable", a.Verdict)
			}
			found := false
			for _, cause := range a.Causes {
				found = found || strings.Contains(cause, "iteration")
			}
			if !found {
				t.Fatalf("causes = %v, want an iteration cause", a.Causes)
			}
		})
	}
}

func TestIterationOverUnionContainingUnknownSchemaIsUnverifiable(t *testing.T) {
	in := &oas3.Schema{AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](ArrayType(IntegerType())),
		oas3.NewJSONSchemaFromBool(true),
	}}
	a := analyzeExpr(t, `.[]`, in)
	if a.Verdict != VerdictUnverifiable {
		t.Fatalf("verdict = %s, want unverifiable", a.Verdict)
	}
}

func TestRawIterationOverUntypedBranchIsUnverifiable(t *testing.T) {
	in := untypedObject(map[string]*oas3.Schema{"x": IntegerType()}, []string{"x"})
	opts := DefaultOptions()
	opts.Semantics = SchemaSemanticsRaw
	a := analyzeExprWithOptions(t, `.[]`, in, opts)
	if a.Verdict != VerdictUnverifiable {
		t.Fatalf("verdict = %s, want unverifiable", a.Verdict)
	}
}

func TestDistinctConstBranchesRemainInOutput(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"a": {Const: yamlScalar("A", "!!str")},
		"b": {Const: yamlScalar("B", "!!str")},
	}, []string{"a", "b"})

	for _, expr := range []string{`.a, .b`, `.a // .b`, `[.a, .b] | .[]`} {
		t.Run(expr, func(t *testing.T) {
			a := analyzeExpr(t, expr, in)
			if a.Verdict != VerdictProven {
				t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
			}
			if !schemaAdmitsString(a.Output, "A") || !schemaAdmitsString(a.Output, "B") {
				t.Fatalf("output must admit both A and B: %s", schemaTypeSummary(a.Output, 3))
			}
		})
	}
}

func TestDistinctNumericConstBranchesRemainInOutput(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{
		"a": {Const: yamlScalar("7", "!!int")},
		"b": {Const: yamlScalar("9", "!!int")},
	}, []string{"a", "b"})
	a := analyzeExpr(t, `.a, .b`, in)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven", a.Verdict)
	}
	if !schemaAdmitsInteger(a.Output, 7) || !schemaAdmitsInteger(a.Output, 9) {
		t.Fatalf("output must admit both 7 and 9: %s", schemaTypeSummary(a.Output, 3))
	}
}

func TestBooleanTrueCombinatorBranchesAreNotDiscarded(t *testing.T) {
	obj := BuildObject(map[string]*oas3.Schema{"x": StringType()}, []string{"x"})
	for _, combinator := range []string{"anyOf", "oneOf"} {
		t.Run(combinator, func(t *testing.T) {
			in := &oas3.Schema{}
			branches := []*oas3.JSONSchema[oas3.Referenceable]{
				oas3.NewJSONSchemaFromSchema[oas3.Referenceable](obj),
				oas3.NewJSONSchemaFromBool(true),
			}
			if combinator == "anyOf" {
				in.AnyOf = branches
			} else {
				in.OneOf = branches
			}
			a := analyzeExpr(t, `.x`, in)
			if a.Verdict == VerdictProven && !schemaAdmitsInteger(a.Output, 7) {
				t.Fatalf("Proven output excludes integer 7: %s", schemaTypeSummary(a.Output, 3))
			}
			if a.Verdict == VerdictProvenBroken {
				t.Fatal("true combinator branch admits a live object input")
			}
		})
	}
}

func TestIterationOverBooleanTrueItemsProducesUnknownValue(t *testing.T) {
	in := &oas3.Schema{
		Type:  oas3.NewTypeFromString(oas3.SchemaTypeArray),
		Items: oas3.NewJSONSchemaFromBool(true),
	}
	a := analyzeExpr(t, `.[]`, in)
	if a.Verdict != VerdictUnverifiable {
		t.Fatalf("verdict = %s, want unverifiable", a.Verdict)
	}
	if !schemaAdmitsInteger(a.Output, 7) {
		t.Fatalf("items:true output must admit integer 7: %s", schemaTypeSummary(a.Output, 2))
	}
}

func TestBooleanSchemasRespectCombinatorIdentities(t *testing.T) {
	allOfTrue := &oas3.Schema{AllOf: []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
		oas3.NewJSONSchemaFromBool(true),
	}}
	collapsed, err := normalizeSchema(newCollapseContext(), allOfTrue)
	if err != nil {
		t.Fatal(err)
	}
	if getType(collapsed) != "string" {
		t.Fatalf("allOf true must preserve the string constraint: %s", schemaTypeSummary(collapsed, 2))
	}

	allOfFalse := &oas3.Schema{AllOf: []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
		oas3.NewJSONSchemaFromBool(false),
	}}
	collapsed, err = normalizeSchema(newCollapseContext(), allOfFalse)
	if err != nil {
		t.Fatal(err)
	}
	if !isBottomSchema(collapsed) {
		t.Fatalf("allOf false must be Bottom: %s", schemaTypeSummary(collapsed, 2))
	}
}
