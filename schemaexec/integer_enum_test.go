package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// Test that integer literals in JQ conditionals produce integer enums, not float enums
func TestIntegerEnumFromConditional(t *testing.T) {
	// JQ: if .tier == "gold" then 95 else 50 end
	// Input schema: tier is string with enum ["gold", "silver"]
	// Expected output: integer with enum [50, 95]

	jqExpr := `if .tier == "gold" then 95 else 50 end`
	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse JQ: %v", err)
	}

	// Create input schema with string enum
	tierSchema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{
			{Kind: yaml.ScalarNode, Value: "gold"},
			{Kind: yaml.ScalarNode, Value: "silver"},
		},
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tier": tierSchema,
	}, []string{"tier"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Check that result is integer, not number
	resultType := getType(result.Schema)
	if resultType != "integer" {
		t.Errorf("Expected type 'integer', got '%s'", resultType)
	}

	// Check that enum contains integers [50, 95], not floats
	if result.Schema.Enum == nil {
		t.Fatal("Expected enum to be set")
	}

	if len(result.Schema.Enum) != 2 {
		t.Errorf("Expected 2 enum values, got %d: %v", len(result.Schema.Enum), result.Schema.Enum)
	}

	// Verify enum values are integers (not floats)
	hasInt50 := false
	hasInt95 := false
	for _, node := range result.Schema.Enum {
		if node.Tag == "!!int" || (node.Tag == "" && node.Kind == yaml.ScalarNode) {
			if node.Value == "50" {
				hasInt50 = true
			} else if node.Value == "95" {
				hasInt95 = true
			}
		} else if node.Tag == "!!float" {
			// This is the bug - should be !!int
			t.Errorf("Enum value has !!float tag, should be !!int: %v", node.Value)
		}
	}

	if !hasInt50 {
		t.Error("Expected enum to contain integer 50")
	}
	if !hasInt95 {
		t.Error("Expected enum to contain integer 95")
	}

	t.Logf("Result schema type: %s", resultType)
	t.Logf("Result enum nodes: %+v", result.Schema.Enum)
}

// Test that Union can merge integer enums without creating anyOf
func TestUnionMergesIntegerEnums(t *testing.T) {
	// Two schemas with integer enums that should merge into a single enum
	schema1 := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger),
		Enum: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!int", Value: "50"},
		},
	}

	schema2 := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger),
		Enum: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!int", Value: "95"},
		},
	}

	opts := SchemaExecOptions{
		AnyOfLimit: 10,
	}

	merged := Union([]*oas3.Schema{schema1, schema2}, opts)

	// Should merge to single integer schema with combined enum
	mergedType := getType(merged)
	if mergedType != "integer" {
		t.Errorf("Expected merged type 'integer', got '%s'", mergedType)
	}

	// Should have combined enum [50, 95]
	if merged.Enum == nil {
		t.Fatal("Expected merged schema to have enum")
	}

	if len(merged.Enum) != 2 {
		t.Errorf("Expected 2 enum values in merged schema, got %d: %v", len(merged.Enum), merged.Enum)
	}

	// Should not be anyOf
	if len(merged.AnyOf) > 0 {
		t.Errorf("Expected single schema, got anyOf with %d variants", len(merged.AnyOf))
		for i, variant := range merged.AnyOf {
			if variant.Left != nil {
				t.Logf("  Variant %d: type=%s, enum=%v", i, getType(variant.Left), variant.Left.Enum)
			}
		}
	}

	t.Logf("Merged schema type: %s, enum: %v", mergedType, merged.Enum)
}
