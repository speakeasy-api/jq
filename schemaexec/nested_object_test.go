package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestNestedObjectConstruction_Minimal tests the minimal failing case
func TestNestedObjectConstruction_Minimal(t *testing.T) {
	tests := []struct {
		name  string
		jq    string
		input *oas3.Schema
	}{
		{
			name:  "T1_nested_object_constants",
			jq:    "{a: {b: 1}}",
			input: Top(),
		},
		{
			name:  "T2_nested_empty_object",
			jq:    "{a: {}}",
			input: Top(),
		},
		{
			name:  "T3_parenthesized_nested_object",
			jq:    "{a: ({})}",
			input: Top(),
		},
		{
			name:  "T4_nested_object_with_field_access",
			jq:    "{a: {b: .x}}",
			input: BuildObject(map[string]*oas3.Schema{}, nil), // Empty object (no .x)
		},
		{
			name: "T5_nested_object_with_nested_field_access",
			jq:   "{location: {city: .address.city}}",
			input: BuildObject(map[string]*oas3.Schema{
				"address": BuildObject(map[string]*oas3.Schema{
					"city": StringType(),
				}, []string{"city"}),
			}, []string{"address"}),
		},
		{
			name: "T6_full_failing_case",
			jq: `{userId: .id, displayName: .name,
                  tier: (if .score >= 90 then "gold" else "silver" end),
                  location: {city: .address.city, zip: .address.postalCode}}`,
			input: BuildObject(map[string]*oas3.Schema{
				"id":    NumberType(), // Use NumberType instead of IntegerType
				"name":  StringType(),
				"score": NumberType(),
				"address": BuildObject(map[string]*oas3.Schema{
					"city":       StringType(),
					"postalCode": StringType(),
				}, []string{"city", "postalCode"}),
			}, []string{"id", "name", "score", "address"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := gojq.Parse(tt.jq)
			if err != nil {
				t.Fatalf("Failed to parse jq %q: %v", tt.jq, err)
			}

			result, err := RunSchema(context.Background(), query, tt.input)
			if err != nil {
				t.Fatalf("RunSchema failed: %v", err)
			}

			if result.Schema == nil {
				t.Fatal("Expected schema, got nil")
			}

			t.Logf("Result schema type: %v", getType(result.Schema))
			if result.Schema.Properties != nil {
				t.Logf("Properties count: %d", result.Schema.Properties.Len())
			}
		})
	}
}
