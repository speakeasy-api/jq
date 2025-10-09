package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestSimpleMapPassthrough tests map without conditionals
func TestSimpleMapPassthrough(t *testing.T) {
	jqExpr := `map({id: .id, title: .title})`

	// Build input schema programmatically
	itemSchema := BuildObject(map[string]*oas3.Schema{
		"id":     StringType(),
		"title":  StringType(),
		"active": BoolType(),
	}, []string{"id", "title", "active"})

	inputSchema := ArrayType(itemSchema)

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Check result
	if getType(result.Schema) != "array" {
		t.Fatalf("Expected array, got %s", getType(result.Schema))
	}

	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Result array has no items")
	}

	itemResult := result.Schema.Items.Left
	if itemResult.Properties == nil {
		t.Fatal("Item schema has no properties")
	}

	// Check id
	idProp, ok := itemResult.Properties.Get("id")
	if !ok || idProp.Left == nil {
		t.Fatal("Missing 'id' property")
	}
	if getType(idProp.Left) != "string" {
		t.Errorf("Property 'id' has type %q, want 'string'", getType(idProp.Left))
	}

	// Check title
	titleProp, ok := itemResult.Properties.Get("title")
	if !ok || titleProp.Left == nil {
		t.Fatal("Missing 'title' property")
	}
	if getType(titleProp.Left) != "string" {
		t.Errorf("Property 'title' has type %q, want 'string'", getType(titleProp.Left))
	}

	t.Log("✅ Both properties correctly preserved their types")
}
