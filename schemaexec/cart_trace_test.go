package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestTraceCartFull(t *testing.T) {
	// Full query: {grandTotal: (.items | map(.price * .quantity) | add // 0), items: (.items | map({sku, total: (.price * .quantity)}))}
	query, err := gojq.Parse(`{grandTotal: (.items | map(.price * .quantity) | add // 0), items: (.items | map({sku, total: (.price * .quantity)}))}`)
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

	fmt.Printf("\n=== OUTPUT SCHEMA ===\n")
	fmt.Printf("Type: %s\n", getType(result.Schema))
	
	if result.Schema.Properties != nil {
		for k, v := range result.Schema.Properties.All() {
			if v.Left != nil {
				fmt.Printf("\nProperty '%s': %s\n", k, getType(v.Left))
				if getType(v.Left) == "array" {
					if v.Left.Items != nil && v.Left.Items.Left != nil {
						fmt.Printf("  Items: %s\n", getType(v.Left.Items.Left))
						if getType(v.Left.Items.Left) == "object" && v.Left.Items.Left.Properties != nil {
							fmt.Printf("  Item Properties:\n")
							for pk, pv := range v.Left.Items.Left.Properties.All() {
								if pv.Left != nil {
									fmt.Printf("    %s: %s\n", pk, getType(pv.Left))
								}
							}
						}
					} else {
						fmt.Printf("  Items: NONE\n")
					}
				}
			}
		}
	}
	
	fmt.Printf("\n=== ACCUMULATORS ===\n")
	fmt.Printf("Warnings: %v\n", result.Warnings)
}
