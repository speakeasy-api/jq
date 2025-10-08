package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestDumpCartInput(t *testing.T) {
	query, _ := gojq.Parse(`{grandTotal: (.items | map(.price * .quantity) | add // 0), items: (.items | map({sku, total: (.price * .quantity)}))}`)
	
	itemSchema := BuildObject(map[string]*oas3.Schema{
		"sku":      StringType(),
		"price":    NumberType(),
		"quantity": NumberType(),
	}, []string{"sku", "price", "quantity"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"items": ArrayType(itemSchema),
	}, []string{"items"})

	result, _ := RunSchema(context.Background(), query, inputSchema)
	
	items, _ := result.Schema.Properties.Get("items")
	
	fmt.Printf("items.Left type: %s\n", getType(items.Left))
	fmt.Printf("items.Left.Items: %v\n", items.Left.Items)
	
	if items.Left.Items != nil {
		fmt.Printf("items.Left.Items.Left: %v\n", items.Left.Items.Left)
		if items.Left.Items.Left != nil {
			fmt.Printf("items.Left.Items.Left type: %s\n", getType(items.Left.Items.Left))
			fmt.Printf("items.Left.Items.Left.Type: %v\n", items.Left.Items.Left.Type)
			fmt.Printf("items.Left.Items.Left.Properties: %v\n", items.Left.Items.Left.Properties)
			
			if items.Left.Items.Left.AnyOf != nil {
				fmt.Printf("Has anyOf with %d branches\n", len(items.Left.Items.Left.AnyOf))
				for i, branch := range items.Left.Items.Left.AnyOf {
					if branch.Left != nil {
						fmt.Printf("  Branch %d type: %s\n", i, getType(branch.Left))
					}
				}
			}
		}
	}
}
