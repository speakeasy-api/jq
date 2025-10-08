package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
)

func TestSimpleObjectWithArray(t *testing.T) {
	// Simpler: {result: [1, 2, 3]}
	query, err := gojq.Parse(`{result: [1, 2, 3]}`)
	if err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	result, err := RunSchema(context.Background(), query, StringType())
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	fmt.Printf("Type: %s\n", getType(result.Schema))
	if result.Schema.Properties != nil {
		if res, ok := result.Schema.Properties.Get("result"); ok && res.Left != nil {
			fmt.Printf("result type: %s\n", getType(res.Left))
			if getType(res.Left) == "array" && res.Left.Items != nil && res.Left.Items.Left != nil {
				fmt.Printf("result items: %s\n", getType(res.Left.Items.Left))
			}
		}
	}
}
