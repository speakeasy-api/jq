package schemaexec

import (
	"context"
	"fmt"

	gojq "github.com/speakeasy-api/jq"
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
// This is the core execution function - Phase 2 implementation.
func ExecSchema(ctx context.Context, code *gojq.Code, input *oas3.Schema, opts SchemaExecOptions) (*SchemaExecResult, error) {
	// Validate input
	if err := validateSchema(input); err != nil {
		return nil, fmt.Errorf("invalid input schema: %w", err)
	}

	// Create schema VM environment
	env := newSchemaEnv(ctx, opts)

	// Execute bytecode on the input schema
	return env.execute(code, input)
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
