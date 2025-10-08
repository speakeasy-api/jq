package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestDebugCartItemsDetailed(t *testing.T) {
	query, err := gojq.Parse(`.items | map({sku, total: (.price * .quantity)})`)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	itemSchema := BuildObject(map[string]*oas3.Schema{
		"sku":      StringType(),
		"price":    NumberType(),
		"quantity": NumberType(),
	}, []string{"sku", "price", "quantity"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"items": ArrayType(itemSchema),
	}, []string{"items"})

	// Enable warnings and run
	opts := DefaultOptions()
	opts.EnableWarnings = true
	
	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	fmt.Printf("\n=== RESULT ===\n")
	fmt.Printf("Result type: %s\n", getType(result.Schema))
	if getType(result.Schema) == "array" && result.Schema.Items != nil && result.Schema.Items.Left != nil {
		fmt.Printf("Items type: %s\n", getType(result.Schema.Items.Left))
		if result.Schema.Items.Left.Properties != nil {
			fmt.Printf("Item Properties:\n")
			for k, v := range result.Schema.Items.Left.Properties.All() {
				if v.Left != nil {
					fmt.Printf("  %s: %s\n", k, getType(v.Left))
				}
			}
		}
	}
	
	fmt.Printf("\n=== WARNINGS ===\n")
	for _, w := range result.Warnings {
		fmt.Printf("%s\n", w)
	}
}
