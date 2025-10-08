package schemaexec

import (
	"context"
	"fmt"
	"testing"
	gojq "github.com/speakeasy-api/jq"
)

func TestDebug_FullTrace(t *testing.T) {
	query, _ := gojq.Parse(`map({value: .})`)
	code, _ := gojq.Compile(query)
	input := ArrayType(StringType())
	
	opts := DefaultOptions()
	opts.EnableWarnings = true
	
	result, err := ExecSchema(context.Background(), code, input, opts)
	if err != nil {
		t.Fatalf("Failed: %v", err)
	}
	
	fmt.Printf("\nWarnings:\n")
	for _, w := range result.Warnings {
		fmt.Printf("  - %s\n", w)
	}
	fmt.Printf("\nOutput type: %s\n", getType(result.Schema))
	if result.Schema.Items != nil && result.Schema.Items.Left != nil {
		fmt.Printf("Items type: %s\n", getType(result.Schema.Items.Left))
	}
}
