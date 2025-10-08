package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
)

func TestDebugSimpleObjectConstruction(t *testing.T) {
	// Simplest object: {x: 1}
	query, err := gojq.Parse(`{x: 1}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := RunSchema(context.Background(), query, StringType())
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	fmt.Printf("Result type: '%s'\n", getType(result.Schema))
	fmt.Printf("Warnings: %v\n", result.Warnings)
	
	if getType(result.Schema) != "object" {
		t.Errorf("Expected object, got %s", getType(result.Schema))
	}
}
