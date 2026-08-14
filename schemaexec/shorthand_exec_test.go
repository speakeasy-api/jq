package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestExecuteShorthand(t *testing.T) {
	query, _ := gojq.Parse(`{sku, total: (.price * .quantity)}`)
	
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"sku":      StringType(),
		"price":    NumberType(),
		"quantity": NumberType(),
	}, []string{"sku", "price", "quantity"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Result type: %s", getType(result.Schema))
	if result.Schema.Properties != nil {
		for k, v := range result.Schema.Properties.All() {
			if v.Left != nil {
				t.Logf("  %s: %s", k, getType(v.Left))
			}
		}
	}
	t.Logf("Warnings: %v", result.Warnings)
}
