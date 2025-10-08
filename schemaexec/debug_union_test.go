package schemaexec

import (
	"fmt"
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestDebugUnionFlow(t *testing.T) {
	opts := DefaultOptions()
	opts.EnableWarnings = true
	
	// Create object with 2 properties
	obj := BuildObject(map[string]*oas3.Schema{
		"total": NumberType(),
		"sku":   StringType(),
	}, []string{"total", "sku"})
	
	fmt.Printf("Input object type: %s\n", getType(obj))
	if obj.Properties != nil {
		fmt.Printf("Input object properties:\n")
		for k := range obj.Properties.All() {
			fmt.Printf("  - %s\n", k)
		}
	}
	
	// Union with itself
	result := Union([]*oas3.Schema{obj, obj}, opts)
	
	fmt.Printf("\nResult type: %s\n", getType(result))
	if result.Properties != nil {
		fmt.Printf("Result properties:\n")
		for k := range result.Properties.All() {
			fmt.Printf("  - %s\n", k)
		}
	} else {
		fmt.Printf("Result has NO properties!\n")
	}
	
	// Check if anyOf was created
	if result.AnyOf != nil {
		fmt.Printf("Result has anyOf with %d branches\n", len(result.AnyOf))
	}
}
