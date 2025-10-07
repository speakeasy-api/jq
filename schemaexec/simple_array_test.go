package schemaexec

import (
	"context"
	"testing"
	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// Test simplest possible array construction
func TestLiteralArray(t *testing.T) {
	query, err := gojq.Parse(`[1, 2]`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{}, []string{})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)

	if result.Schema.Items != nil && result.Schema.Items.Left != nil {
		t.Logf("Items type: %s", getType(result.Schema.Items.Left))
	}
}
