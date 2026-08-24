package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func explicitTypeSet(schema *oas3.Schema) map[oas3.SchemaType]bool {
	result := make(map[oas3.SchemaType]bool)
	seen := make(map[*oas3.Schema]bool)
	var visit func(*oas3.Schema)
	visit = func(current *oas3.Schema) {
		if current == nil || seen[current] {
			return
		}
		seen[current] = true
		for _, typ := range current.GetType() {
			result[typ] = true
		}
		if current.Nullable != nil && *current.Nullable {
			result[oas3.SchemaTypeNull] = true
		}
		for _, branches := range [][]*oas3.JSONSchema[oas3.Referenceable]{current.AnyOf, current.OneOf} {
			for _, branch := range branches {
				visit(resolvedLeft(branch))
			}
		}
	}
	visit(schema)
	return result
}

func TestTypeArrayIdentityPreservesExactTypes(t *testing.T) {
	input := &oas3.Schema{Type: oas3.NewTypeFromArray([]oas3.SchemaType{
		oas3.SchemaTypeString,
		oas3.SchemaTypeNull,
	})}
	analysis := analyzeWithOptions(t, `.`, input, DefaultOptions())
	types := explicitTypeSet(analysis.Output)
	if len(types) != 2 || !types[oas3.SchemaTypeString] || !types[oas3.SchemaTypeNull] {
		t.Fatalf("output types = %v, want exactly string and null; output=%s", types, schemaTypeSummary(analysis.Output, 4))
	}
	if len(analysis.Output.AnyOf) != 2 {
		t.Fatalf("output anyOf branches = %d, want 2; output=%s", len(analysis.Output.AnyOf), schemaTypeSummary(analysis.Output, 4))
	}
}

func TestNestedTypeArrayLengthRemainsTyped(t *testing.T) {
	value := &oas3.Schema{Type: oas3.NewTypeFromArray([]oas3.SchemaType{
		oas3.SchemaTypeString,
		oas3.SchemaTypeNull,
	})}
	input := BuildObject(map[string]*oas3.Schema{"x": value}, []string{"x"})
	analysis := analyzeWithOptions(t, `.x | length`, input, DefaultOptions())
	if analysis.Verdict == VerdictProvenBroken || getType(analysis.Output) != "number" {
		t.Fatalf("verdict=%s output=%s, want a numeric length", analysis.Verdict, schemaTypeSummary(analysis.Output, 4))
	}
}

func TestArrayNullTypeArrayIterationIsNotProvenBroken(t *testing.T) {
	input := &oas3.Schema{
		Type: oas3.NewTypeFromArray([]oas3.SchemaType{
			oas3.SchemaTypeArray,
			oas3.SchemaTypeNull,
		}),
		Items: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
	}
	analysis := analyzeWithOptions(t, `.[]`, input, DefaultOptions())
	if analysis.Verdict == VerdictProvenBroken || !MightBeString(analysis.Output) {
		t.Fatalf("verdict=%s output=%s, want possible string elements", analysis.Verdict, schemaTypeSummary(analysis.Output, 4))
	}
}
