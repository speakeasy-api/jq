package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestInspectAccumulators(t *testing.T) {
	query, _ := gojq.Parse(`{items: (.items | map({sku, total: (.price * .quantity)}))}`)
	
	itemSchema := BuildObject(map[string]*oas3.Schema{
		"sku":      StringType(),
		"price":    NumberType(),
		"quantity": NumberType(),
	}, []string{"sku", "price", "quantity"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"items": ArrayType(itemSchema),
	}, []string{"items"})

	// Run with access to internals
	code, _ := gojq.Compile(query)
	opts := DefaultOptions()
	opts.EnableWarnings = false // Reduce noise
	env := newSchemaEnv(context.Background(), opts)
	result, _ := env.execute(code, inputSchema)
	
	// Can't access state.accum from here, so check result
	items, _ := result.Schema.Properties.Get("items")
	
	if items.Left.Items != nil && items.Left.Items.Left != nil {
		itemType := items.Left.Items.Left
		
		fmt.Printf("Items type: %s\n", getType(itemType))
		fmt.Printf("Has anyOf: %v\n", itemType.AnyOf != nil)
		
		if itemType.AnyOf != nil {
			fmt.Printf("anyOf branches: %d\n", len(itemType.AnyOf))
			for i, branch := range itemType.AnyOf {
				if branch.Left != nil {
					fmt.Printf("  Branch %d: type=%s\n", i, getType(branch.Left))
					if branch.Left.Properties != nil {
						propCount := 0
						for k := range branch.Left.Properties.All() {
							propCount++
							fmt.Printf("    - %s\n", k)
						}
						fmt.Printf("    Total: %d properties\n", propCount)
					} else {
						fmt.Printf("    NO properties\n")
					}
				}
			}
		}
		
		if itemType.Properties != nil {
			fmt.Printf("Direct properties:\n")
			for k := range itemType.Properties.All() {
				fmt.Printf("  - %s\n", k)
			}
		}
	}
}
