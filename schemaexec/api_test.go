package schemaexec

import (
	"context"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestRunSchema_BasicUsage(t *testing.T) {
	// Parse a simple jq query
	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create an input schema
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"foo": StringType(),
		"bar": NumberType(),
	}, []string{"foo"})

	// Run symbolic execution
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// For Phase 1, we expect a placeholder result
	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	if result.Schema == nil {
		t.Error("Expected schema in result")
	}

	// Phase 1 should have warnings
	if len(result.Warnings) == 0 {
		t.Log("Warning: Expected Phase 1 warnings about incomplete implementation")
	}
}

func TestExecSchema_WithOptions(t *testing.T) {
	// Compile a query
	query, err := gojq.Parse(".x")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	code, err := gojq.Compile(query)
	if err != nil {
		t.Fatalf("Failed to compile query: %v", err)
	}

	// Create input schema
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"x": NumberType(),
	}, nil)

	// Execute with custom options
	opts := SchemaExecOptions{
		AnyOfLimit:     5,
		EnumLimit:      20,
		MaxDepth:       50,
		StrictMode:     false,
		EnableWarnings: true,
		EnableMemo:     false,
		WideningLevel:  1,
	}

	result, err := ExecSchema(context.Background(), code, inputSchema, opts)
	if err != nil {
		t.Fatalf("ExecSchema failed: %v", err)
	}

	if result == nil {
		t.Fatal("Expected non-nil result")
	}
}

func TestSchemaExecResult_String(t *testing.T) {
	result := &SchemaExecResult{
		Schema:   StringType(),
		Warnings: []string{"warning 1", "warning 2"},
	}

	str := result.String()
	if str == "" {
		t.Error("Expected non-empty string representation")
	}
	t.Logf("Result string: %s", str)
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()

	if opts.AnyOfLimit != 10 {
		t.Errorf("Expected AnyOfLimit=10, got %d", opts.AnyOfLimit)
	}

	if opts.EnumLimit != 50 {
		t.Errorf("Expected EnumLimit=50, got %d", opts.EnumLimit)
	}

	if opts.MaxDepth != 100 {
		t.Errorf("Expected MaxDepth=100, got %d", opts.MaxDepth)
	}

	if opts.StrictMode {
		t.Error("Expected StrictMode=false by default")
	}

	if !opts.EnableWarnings {
		t.Error("Expected EnableWarnings=true by default")
	}

	if !opts.EnableMemo {
		t.Error("Expected EnableMemo=true by default")
	}

	if opts.WideningLevel != 1 {
		t.Errorf("Expected WideningLevel=1, got %d", opts.WideningLevel)
	}
}
