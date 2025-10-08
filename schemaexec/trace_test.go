package schemaexec

import (
	"context"
	"fmt"
	"testing"
	gojq "github.com/speakeasy-api/jq"
)

func TestTrace_SimpleVsObjectMap(t *testing.T) {
	// First test: map(.) - this works
	query1, _ := gojq.Parse(`map(.)`)
	input1 := ArrayType(StringType())
	result1, _ := RunSchema(context.Background(), query1, input1)
	fmt.Printf("\n=== map(.) ===\n")
	fmt.Printf("Result type: %s\n", getType(result1.Schema))
	if result1.Schema.Items != nil && result1.Schema.Items.Left != nil {
		fmt.Printf("Items type: %s\n", getType(result1.Schema.Items.Left))
	}

	// Second test: map({value: .}) - this fails
	query2, _ := gojq.Parse(`map({value: .})`)
	input2 := ArrayType(StringType())
	result2, _ := RunSchema(context.Background(), query2, input2)
	fmt.Printf("\n=== map({value: .}) ===\n")
	fmt.Printf("Result type: %s\n", getType(result2.Schema))
	if result2.Schema.Items != nil && result2.Schema.Items.Left != nil {
		fmt.Printf("Items type: %s\n", getType(result2.Schema.Items.Left))
		fmt.Printf("Items is object: %v\n", getType(result2.Schema.Items.Left) == "object")
		if result2.Schema.Items.Left.Properties != nil {
			fmt.Printf("Has properties: %v\n", result2.Schema.Items.Left.Properties.Len())
		}
	}
}
