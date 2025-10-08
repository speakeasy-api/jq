package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestCartInputDetailed(t *testing.T) {
	query, err := gojq.Parse(`{grandTotal: (.items | map(.price * .quantity) | add // 0), items: (.items | map({sku, total: (.price * .quantity)}))}`)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}

	// Check grandTotal
	fmt.Println("=== grandTotal ===")
	if grandTotal, ok := result.Schema.Properties.Get("grandTotal"); ok && grandTotal.Left != nil {
		fmt.Printf("Type: %s\n", getType(grandTotal.Left))
		fmt.Printf("Has anyOf: %v\n", grandTotal.Left.AnyOf != nil)
		if grandTotal.Left.AnyOf != nil {
			fmt.Printf("AnyOf branches: %d\n", len(grandTotal.Left.AnyOf))
			for i, branch := range grandTotal.Left.AnyOf {
				if branch.Left != nil {
					fmt.Printf("  Branch %d: type=%s, enum=%v\n", i, getType(branch.Left), branch.Left.Enum)
				}
			}
		}
		fmt.Printf("Enum: %v\n", grandTotal.Left.Enum)
	}

	// Check items
	fmt.Println("\n=== items ===")
	if items, ok := result.Schema.Properties.Get("items"); ok && items.Left != nil {
		fmt.Printf("Type: %s\n", getType(items.Left))

		if items.Left.Items != nil && items.Left.Items.Left != nil {
			fmt.Println("\n=== items.Items (array element schema) ===")
			itemsSchema := items.Left.Items.Left
			fmt.Printf("Type: %s\n", getType(itemsSchema))
			fmt.Printf("Has anyOf: %v\n", itemsSchema.AnyOf != nil)

			if itemsSchema.AnyOf != nil {
				fmt.Printf("AnyOf branches: %d\n", len(itemsSchema.AnyOf))
				for i, branch := range itemsSchema.AnyOf {
					if branch.Left != nil {
						fmt.Printf("  Branch %d: type=%s\n", i, getType(branch.Left))
						if branch.Left.Properties != nil {
							propCount := 0
							for k := range branch.Left.Properties.All() {
								propCount++
								fmt.Printf("    Property: %s\n", k)
							}
							fmt.Printf("    Total properties: %d\n", propCount)
						}
					}
				}
			}

			fmt.Printf("Direct type field: %v\n", itemsSchema.Type)
			fmt.Printf("Properties: %v\n", itemsSchema.Properties)

			if itemsSchema.Properties != nil {
				fmt.Println("\nProperties in items schema:")
				for k, v := range itemsSchema.Properties.All() {
					if v.Left != nil {
						fmt.Printf("  %s: %s\n", k, getType(v.Left))
					}
				}
			}
		}
	}

	fmt.Println("\n=== Warnings ===")
	for _, w := range result.Warnings {
		fmt.Println(w)
	}
}
