package schemaexec

import (
	"context"
	"fmt"
	"testing"
	gojq "github.com/speakeasy-api/jq"
)

func TestDebug_PrintBytecode(t *testing.T) {
	query, err := gojq.Parse(`map({value: .})`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	code, err := gojq.Compile(query)
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	codes := code.GetCodes()
	fmt.Printf("\n=== BYTECODE for map({value: .}) ===\n")
	for i, c := range codes {
		fmt.Printf("[%d] %s (value: %v)\n", i, c.OpString(), c.GetValue())
	}
	
	// Now actually execute
	input := ArrayType(StringType())
	result, _ := RunSchema(context.Background(), query, input)
	fmt.Printf("\n=== EXECUTION RESULT ===\n")
	fmt.Printf("Output type: %s\n", getType(result.Schema))
	if result.Schema.Items != nil && result.Schema.Items.Left != nil {
		fmt.Printf("Items type: %s\n", getType(result.Schema.Items.Left))
	}
}
