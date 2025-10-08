package schemaexec

import (
	"context"
	"fmt"
	"testing"
	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestDebug_MapObjectConstruction(t *testing.T) {
	query, err := gojq.Parse(`.tags | map({value: .})`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{"tags"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	fmt.Printf("\n=== RESULT ANALYSIS ===\n")
	fmt.Printf("Output type: '%s'\n", getType(result.Schema))
	fmt.Printf("Warnings: %v\n", result.Warnings)

	if result.Schema != nil && result.Schema.Items != nil && result.Schema.Items.Left != nil {
		fmt.Printf("Items.Left type: '%s'\n", getType(result.Schema.Items.Left))
		fmt.Printf("Items.Left.Type field: %v\n", result.Schema.Items.Left.Type)
		fmt.Printf("Items.Left.Properties: %v\n", result.Schema.Items.Left.Properties != nil)

		if result.Schema.Items.Left.Properties != nil {
			fmt.Printf("Properties count: %d\n", result.Schema.Items.Left.Properties.Len())
			if val, ok := result.Schema.Items.Left.Properties.Get("value"); ok && val.Left != nil {
				fmt.Printf("  value property type: '%s'\n", getType(val.Left))
			}
		}
	}
}
