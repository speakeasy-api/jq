package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// TestDeduplicateSchemas_StringEnumMerging verifies string enum merging works
func TestDeduplicateSchemas_StringEnumMerging(t *testing.T) {
	// Two different string constants
	s1 := ConstString("gold")
	s2 := ConstString("silver")
	s3 := ConstString("gold") // Duplicate

	schemas := []*oas3.Schema{s1, s2, s3}
	deduped := deduplicateSchemas(schemas)

	// Should merge into single schema with enum: ["gold", "silver"]
	if len(deduped) != 1 {
		t.Errorf("Expected 1 merged schema, got %d", len(deduped))
	}

	if deduped[0].Enum == nil {
		t.Fatal("Expected merged schema to have enum")
	}

	if len(deduped[0].Enum) != 2 {
		t.Errorf("Expected 2 enum values, got %d", len(deduped[0].Enum))
	}

	// Verify both values are present
	values := make(map[string]bool)
	for _, node := range deduped[0].Enum {
		values[node.Value] = true
	}

	if !values["gold"] || !values["silver"] {
		t.Error("Expected both 'gold' and 'silver' in merged enum")
	}
}

// TestDeduplicateSchemas_PointerIdentity verifies pointer dedup works
func TestDeduplicateSchemas_PointerIdentity(t *testing.T) {
	s1 := NumberType()

	schemas := []*oas3.Schema{s1, s1, s1} // Same pointer
	deduped := deduplicateSchemas(schemas)

	if len(deduped) != 1 {
		t.Errorf("Expected 1 schema (pointer dedup), got %d", len(deduped))
	}
}

// TestDeduplicateSchemas_ObjectsNotMerged verifies we don't incorrectly merge objects
func TestDeduplicateSchemas_ObjectsNotMerged(t *testing.T) {
	obj1 := BuildObject(map[string]*oas3.Schema{
		"tier": ConstString("gold"),
		"id": NumberType(),
	}, []string{"tier", "id"})

	obj2 := BuildObject(map[string]*oas3.Schema{
		"tier": ConstString("silver"),
		"id": NumberType(),
	}, []string{"tier", "id"})

	schemas := []*oas3.Schema{obj1, obj2}
	deduped := deduplicateSchemas(schemas)

	// Should keep both objects (no structural merging for objects)
	if len(deduped) != 2 {
		t.Errorf("Expected 2 objects (not merged), got %d", len(deduped))
	}
}

// TestDeduplicateSchemas_MultiValueEnums verifies merging multi-value enums
func TestDeduplicateSchemas_MultiValueEnums(t *testing.T) {
	// Create schemas with multiple enum values each
	s1 := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "small"},
			{Kind: yaml.ScalarNode, Value: "medium"},
		},
	}

	s2 := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "large"},
			{Kind: yaml.ScalarNode, Value: "xlarge"},
		},
	}

	s3 := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "medium"}, // Overlaps with s1
		},
	}

	schemas := []*oas3.Schema{s1, s2, s3}
	deduped := deduplicateSchemas(schemas)

	// Should merge into single schema with all unique enum values
	if len(deduped) != 1 {
		t.Errorf("Expected 1 merged schema, got %d", len(deduped))
	}

	// Should have 4 unique values (small, medium, large, xlarge)
	if len(deduped[0].Enum) != 4 {
		t.Errorf("Expected 4 unique enum values, got %d", len(deduped[0].Enum))
	}

	// Verify all values are present
	values := make(map[string]bool)
	for _, node := range deduped[0].Enum {
		values[node.Value] = true
	}

	expected := []string{"small", "medium", "large", "xlarge"}
	for _, v := range expected {
		if !values[v] {
			t.Errorf("Expected %q in merged enum", v)
		}
	}
}
