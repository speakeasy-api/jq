package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
)

func TestObjectWithMapArray(t *testing.T) {
	// Object with dynamic array: {result: ([1,2,3] | map(. * 2))}
	query, err := gojq.Parse(`{result: ([1,2,3] | map(. * 2))}`)
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
			if getType(res.Left) == "array" {
				if res.Left.Items != nil && res.Left.Items.Left != nil {
					fmt.Printf("result items: %s\n", getType(res.Left.Items.Left))
				} else {
					fmt.Printf("result items: NONE\n")
				}
			}
		}
	}
	fmt.Printf("Warnings: %v\n", result.Warnings)
}
