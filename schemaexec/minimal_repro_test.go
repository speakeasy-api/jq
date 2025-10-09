package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestMinimalRepro is the absolute minimal test case that reproduces the bug
func TestMinimalRepro(t *testing.T) {
	// Simplest possible case: map with property passthrough
	jqExpr := `map({x: .x})`

	// Input: array<{x: string}>
	inputSchema := ArrayType(BuildObject(map[string]*oas3.Schema{
		"x": StringType(),
	}, []string{"x"}))

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	opts := DefaultOptions()
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)
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

	itemSchema := result.Schema.Items.Left
	if itemSchema.Properties == nil {
		t.Fatal("Item schema has no properties")
	}

	xProp, ok := itemSchema.Properties.Get("x")
	if !ok || xProp.Left == nil {
		t.Fatal("Missing 'x' property")
	}

	xType := getType(xProp.Left)
	if xType == "" {
		t.Error("BUG: Property 'x' has empty type")
		t.Logf("x property: %+v", xProp.Left)
	} else if xType != "string" {
		t.Errorf("Property 'x' has type %q, want 'string'", xType)
	} else {
		t.Log("✅ Property 'x' correctly has type 'string'")
	}

	if len(result.Warnings) > 0 {
		fmt.Printf("\nWarnings:\n")
		for i, w := range result.Warnings {
			fmt.Printf("%d: %s\n", i+1, w)
		}
	}
}
