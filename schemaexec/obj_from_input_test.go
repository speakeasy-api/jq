package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestObjectFromInputProperty(t *testing.T) {
	// Object built from input property: {result: (.items | map(. * 2))}
	query, err := gojq.Parse(`{result: (.items | map(. * 2))}`)
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

	fmt.Printf("Type: %s\n", getType(result.Schema))
	if result.Schema.Properties != nil {
		if res, ok := result.Schema.Properties.Get("result"); ok && res.Left != nil {
			fmt.Printf("result type: %s\n", getType(res.Left))
			if getType(res.Left) == "array" {
				if res.Left.Items != nil && res.Left.Items.Left != nil {
					fmt.Printf("result items: %s\n", getType(res.Left.Items.Left))
				} else {
					fmt.Printf("result items: NONE (ptr=%p)\n", res.Left)
				}
			}
		}
	}
	fmt.Printf("Warnings: %v\n", result.Warnings)
}
