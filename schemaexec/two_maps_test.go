package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestObjectWithTwoMaps(t *testing.T) {
	// Like CartInput: two different maps of the same input
	query, err := gojq.Parse(`{first: (.items | map(. * 2)), second: (.items | map({value: .}))}`)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"items": ArrayType(NumberType()),
	}, []string{"items"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	fmt.Printf("\n=== OUTPUT ===\n")
	fmt.Printf("Type: %s\n", getType(result.Schema))
	if result.Schema.Properties != nil {
		for propName, propSchema := range result.Schema.Properties.All() {
			if propSchema.Left != nil {
				fmt.Printf("\n'%s': %s\n", propName, getType(propSchema.Left))
				if getType(propSchema.Left) == "array" {
					if propSchema.Left.Items != nil && propSchema.Left.Items.Left != nil {
						fmt.Printf("  items: %s\n", getType(propSchema.Left.Items.Left))
					} else {
						fmt.Printf("  items: NONE\n")
					}
				}
			}
		}
	}
	fmt.Printf("\n=== WARNINGS ===\n%v\n", result.Warnings)
}
