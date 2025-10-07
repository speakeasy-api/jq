package schemaexec

import (
	"context"
	"fmt"

	"github.com/itchyny/gojq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// RunSchema executes a jq query symbolically on an input JSON Schema.
// It parses and compiles the query, then performs symbolic execution
// to compute the output schema.
//
// Example:
//
//	query, _ := gojq.Parse(".foo.bar")
//	inputSchema := &oas3.Schema{...}
//	result, err := schemaexec.RunSchema(context.Background(), query, inputSchema)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	fmt.Printf("Output schema: %+v\n", result.Schema)
func RunSchema(ctx context.Context, query *gojq.Query, input *oas3.Schema, opts ...SchemaExecOptions) (*SchemaExecResult, error) {
	// Use default options if none provided
	opt := DefaultOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}

	// Compile the query to bytecode
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("failed to compile query: %w", err)
	}

	// Execute symbolically
	return ExecSchema(ctx, code, input, opt)
}

// ExecSchema executes compiled jq bytecode symbolically on an input schema.
// This is the core execution function that will be implemented in Phase 2.
//
// For Phase 1, this returns a placeholder implementation that demonstrates
// the interface but doesn't yet perform full symbolic execution.
func ExecSchema(ctx context.Context, code *gojq.Code, input *oas3.Schema, opts SchemaExecOptions) (*SchemaExecResult, error) {
	// TODO: Phase 2 - Implement full schema VM
	//
	// The full implementation will:
	// 1. Create a schema VM environment
	// 2. Initialize the stack with input schema
	// 3. Execute bytecode operations on schemas
	// 4. Collect output schemas and create union
	// 5. Apply normalization
	// 6. Return result with warnings
	//
	// For now, return a placeholder that shows the structure

	return &SchemaExecResult{
		Schema: input, // Placeholder: return input unchanged
		Warnings: []string{
			"Schema VM not yet implemented - returning input schema unchanged",
			"This is Phase 1: basic operations only",
		},
	}, nil
}

// Helper methods that will be used by the VM in Phase 2

// validateSchema performs basic validation on an input schema.
func validateSchema(s *oas3.Schema) error {
	if s == nil {
		return fmt.Errorf("input schema cannot be nil")
	}
	// Additional validation could be added here
	return nil
}

// String returns a string representation of the result for debugging.
func (r *SchemaExecResult) String() string {
	if r == nil {
		return "<nil>"
	}
	warnings := ""
	if len(r.Warnings) > 0 {
		warnings = fmt.Sprintf(" (warnings: %d)", len(r.Warnings))
	}
	return fmt.Sprintf("SchemaExecResult{Schema: %p%s}", r.Schema, warnings)
}
