package schemaexec

import (
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// SValue wraps a schema for the schema VM stack.
// This is the value type that flows through the schema virtual machine.
type SValue struct {
	Schema *oas3.Schema
}

// SchemaExecOptions configures symbolic execution behavior.
type SchemaExecOptions struct {
	// Limits to prevent combinatorial explosion
	AnyOfLimit int // Max branches in anyOf before widening (default: 10)
	EnumLimit  int // Max enum values before widening to plain type (default: 50)
	MaxDepth   int // Max recursion depth (default: 100)

	// Behavior flags
	StrictMode     bool // If true, fail on unsupported ops; if false, widen to Top (default: false)
	EnableWarnings bool // If true, collect precision-loss warnings (default: true)
	EnableMemo     bool // If true, enable memoization for performance (default: true)

	// Widening level controls how aggressively we simplify schemas
	// 0 = none (keep all precision)
	// 1 = conservative (keep types, drop facets when limits exceeded)
	// 2 = aggressive (collapse to Top when limits exceeded)
	WideningLevel int // default: 1
}

// SchemaExecResult contains the output schema and diagnostic information.
type SchemaExecResult struct {
	Schema   *oas3.Schema // The resulting schema after transformation
	Warnings []string     // Warnings about precision loss or unsupported operations
}

// DefaultOptions returns the default configuration for schema execution.
func DefaultOptions() SchemaExecOptions {
	return SchemaExecOptions{
		AnyOfLimit:     10,
		EnumLimit:      50,
		MaxDepth:       100,
		StrictMode:     false,
		EnableWarnings: true,
		EnableMemo:     true,
		WideningLevel:  1,
	}
}
