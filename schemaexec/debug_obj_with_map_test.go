package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
)

func TestDebugObjectWithSimpleMap(t *testing.T) {
	// Object with one field from a map
	query, err := gojq.Parse(`{tags: ([1,2,3] | map(. * 2))}`)
	if err != nil {
		t.Fatal(err)
	}

	result, err := RunSchema(context.Background(), query, StringType())
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	fmt.Printf("Result type: '%s'\n", getType(result.Schema))
	if getType(result.Schema) != "object" {
		t.Errorf("Expected object, got %s", getType(result.Schema))
	}
}
