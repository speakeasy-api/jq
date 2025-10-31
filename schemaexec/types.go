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

	// Logging configuration
	LogLevel             string // Log level: "error", "warn", "info", "debug" (default: "warn")
	LogMaxEnumValues     int    // Max enum values to show in logs (default: 5)
	LogMaxProps          int    // Max object properties to show in logs (default: 5)
	LogStackPreviewDepth int    // Max stack depth to preview in logs (default: 3)
	LogSchemaDeltas      bool   // If true, include schema deltas in debug logs (default: true)

	// Lazy $ref resolution (optional)
	// ResolveRef should return the JSONSchema node targeted by the given ref string
	// (e.g., "#/components/schemas/Foo"). If unset, refs are not resolved.
	ResolveRef     func(ref string) (*oas3.JSONSchema[oas3.Referenceable], bool)
	MaxRefDepth    int  // Maximum dereference depth (default: 16)
	CopyOnDeref    bool // Clone resolved schemas to avoid mutating shared component defs (default: true)
	EnableRefCache bool // Cache dereferenced clones per-ref-node within a single execution (default: true)
}

// SchemaExecResult contains the output schema and diagnostic information.
type SchemaExecResult struct {
	Schema   *oas3.Schema // The resulting schema after transformation
	Warnings []string     // Warnings about precision loss or unsupported operations
}

// DefaultOptions returns the default configuration for schema execution.
func DefaultOptions() SchemaExecOptions {
	return SchemaExecOptions{
		AnyOfLimit:           10,
		EnumLimit:            50,
		MaxDepth:             100,
		StrictMode:           false,
		EnableWarnings:       true,
		EnableMemo:           true,
		WideningLevel:        1,
		LogLevel:             "warn",
		LogMaxEnumValues:     5,
		LogMaxProps:          5,
		LogStackPreviewDepth: 3,
		LogSchemaDeltas:      true,

		// Lazy $ref resolution defaults
		ResolveRef:     nil, // supplied by caller (e.g., playground) when available
		MaxRefDepth:    16,
		CopyOnDeref:    true,
		EnableRefCache: true,
	}
}
