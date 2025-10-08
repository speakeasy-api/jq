package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestDebugCartGrandTotal(t *testing.T) {
	// Just test: .items | map(.price * .quantity) | add
	query, err := gojq.Parse(`.items | map(.price * .quantity) | add`)
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

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	fmt.Printf("Result type: %s\n", getType(result.Schema))
	fmt.Printf("Warnings: %v\n", result.Warnings)
}

func TestDebugCartItems(t *testing.T) {
	// Just test: .items | map({sku, total: (.price * .quantity)})
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

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	fmt.Printf("Result type: %s\n", getType(result.Schema))
	if getType(result.Schema) == "array" && result.Schema.Items != nil && result.Schema.Items.Left != nil {
		fmt.Printf("Items type: %s\n", getType(result.Schema.Items.Left))
		if result.Schema.Items.Left.Properties != nil {
			fmt.Printf("Properties:\n")
			for k, v := range result.Schema.Items.Left.Properties.All() {
				if v.Left != nil {
					fmt.Printf("  %s: %s\n", k, getType(v.Left))
				}
			}
		}
	}
	fmt.Printf("Warnings: %v\n", result.Warnings)
}
