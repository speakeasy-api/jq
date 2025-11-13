package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestMapPreservesValueType(t *testing.T) {
	entryObject := BuildObject(map[string]*oas3.Schema{
		"key":   StringType(),
		"value": StringType(),
	}, []string{"key", "value"})
	inputSchema := ArrayType(entryObject)

	jqExpr := `map({name: .key, value: .value})`

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse jq: %v", err)
	}

	opts := DefaultOptions()
	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	itemSchema := result.Schema.Items.Left
	valueProp, _ := itemSchema.Properties.Get("value")
	valueType := getType(valueProp.Left)

	if valueType != "string" {
		t.Errorf("❌ value type is '%s', expected 'string'", valueType)
		t.FailNow()
	}

	fmt.Printf("✅ map preserves value type\n")
}
