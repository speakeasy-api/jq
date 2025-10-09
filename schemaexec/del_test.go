package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestDel_SimpleProperty tests deleting a top-level property
func TestDel_SimpleProperty(t *testing.T) {
	// JQ: del(.a)
	// Input: {a: integer, b: string} with required [a, b]
	// Expected: {b: string} with required [b]
	query, err := gojq.Parse("del(.a)")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
		"b": StringType(),
	}, []string{"a", "b"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Should only have property 'b'
	if result.Schema.Properties == nil {
		t.Fatal("Expected properties")
	}

	if _, ok := result.Schema.Properties.Get("a"); ok {
		t.Error("Property 'a' should be deleted")
	}

	if _, ok := result.Schema.Properties.Get("b"); !ok {
		t.Error("Property 'b' should still exist")
	}

	// Required should only have 'b'
	if len(result.Schema.Required) != 1 || result.Schema.Required[0] != "b" {
		t.Errorf("Expected required ['b'], got: %v", result.Schema.Required)
	}
}

// TestDel_NonExistentProperty tests deleting a property that doesn't exist
func TestDel_NonExistentProperty(t *testing.T) {
	// JQ: del(.missing)
	// Input: {b: string}
	// Expected: unchanged
	query, err := gojq.Parse("del(.missing)")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"b": StringType(),
	}, []string{"b"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Should still have property 'b'
	if _, ok := result.Schema.Properties.Get("b"); !ok {
		t.Error("Property 'b' should still exist")
	}

	// Required unchanged
	if len(result.Schema.Required) != 1 || result.Schema.Required[0] != "b" {
		t.Errorf("Expected required ['b'], got: %v", result.Schema.Required)
	}
}

// TestDel_OptionalProperty tests deleting an optional property
func TestDel_OptionalProperty(t *testing.T) {
	// JQ: del(.a)
	// Input: {a: integer (optional), b: string (required)}
	// Expected: {b: string} with required [b]
	query, err := gojq.Parse("del(.a)")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
		"b": StringType(),
	}, []string{"b"}) // only 'b' is required

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Should only have property 'b'
	if _, ok := result.Schema.Properties.Get("a"); ok {
		t.Error("Property 'a' should be deleted")
	}

	if _, ok := result.Schema.Properties.Get("b"); !ok {
		t.Error("Property 'b' should still exist")
	}
}

// TestDel_MultipleProperties tests deleting multiple properties
func TestDel_MultipleProperties(t *testing.T) {
	// JQ: del(.a, .b)
	// Input: {a: int, b: str, c: number} with required [a, b, c]
	// Expected: {c: number} with required [c]
	query, err := gojq.Parse("del(.a, .b)")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
		"b": StringType(),
		"c": NumberType(),
	}, []string{"a", "b", "c"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Should only have property 'c'
	if _, ok := result.Schema.Properties.Get("a"); ok {
		t.Error("Property 'a' should be deleted")
	}
	if _, ok := result.Schema.Properties.Get("b"); ok {
		t.Error("Property 'b' should be deleted")
	}
	if _, ok := result.Schema.Properties.Get("c"); !ok {
		t.Error("Property 'c' should still exist")
	}

	// Required should only have 'c'
	if len(result.Schema.Required) != 1 || result.Schema.Required[0] != "c" {
		t.Errorf("Expected required ['c'], got: %v", result.Schema.Required)
	}
}

// TestDel_WithPriorAssignment tests the pattern: (.id = X) | del(.old)
func TestDel_WithPriorAssignment(t *testing.T) {
	// JQ: . + {id: "extracted"} | del(.data)
	// Input: {data: {nested: object}}
	// Expected: {id: string} (data removed)
	query, err := gojq.Parse(`. + {id: "extracted"} | del(.data)`)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"data": BuildObject(map[string]*oas3.Schema{
			"nested": StringType(),
		}, []string{}),
	}, []string{"data"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Should have 'id' but not 'data'
	if _, ok := result.Schema.Properties.Get("id"); !ok {
		t.Error("Property 'id' should exist (added by +)")
	}

	if _, ok := result.Schema.Properties.Get("data"); ok {
		t.Error("Property 'data' should be deleted")
	}
}
