package schemaexec

import (
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// SValue wraps a schema for the schema VM stack.
// This is the value type that flows through the schema virtual machine.
type SValue struct {
	Schema *oas3.Schema
}

// SchemaSemantics selects how the executor interprets schemas that do not
// fully specify their shape.
type SchemaSemantics int

const (
	// SchemaSemanticsSpeakeasy (the default) targets Speakeasy-processed
	// OpenAPI documents and mirrors the structural inference Speakeasy's
	// SDK/CLI generators apply:
	//   - untyped schemas get an implied type from structure (enum→string,
	//     const→its scalar type, properties/additionalProperties→object,
	//     items→array);
	//   - an object property that is not declared and has no
	//     additionalProperties is treated as ABSENT (closed world): access
	//     yields null. "Valid input" means a value as modeled by the
	//     generator, not an arbitrary API payload.
	SchemaSemanticsSpeakeasy SchemaSemantics = iota

	// SchemaSemanticsRaw keeps raw JSON Schema semantics at navigation:
	//   - untyped schemas are NOT implied to a single type when dispatching
	//     property access/iteration; they conservatively widen to Top;
	//   - an object property that is not declared and has no
	//     additionalProperties is treated as OPEN (additionalProperties
	//     defaults to true in JSON Schema): access yields unknown ∪ null,
	//     so nothing is ever "provably missing" without an explicit
	//     additionalProperties: false.
	// Note: auxiliary type-compatibility guards inside builtins may still
	// consult structural inference for precision; schemas reaching them have
	// normally already been widened by raw-mode dispatch.
	SchemaSemanticsRaw
)

// SchemaExecOptions configures symbolic execution behavior. Callers should
// start from DefaultOptions when changing individual fields. Public entry
// points fill zero-valued numeric limits from DefaultOptions, but boolean
// fields whose defaults are true (EnableWarnings, EnableMemo, and
// LogSchemaDeltas) remain false in a zero-value struct.
type SchemaExecOptions struct {
	// Semantics selects the schema interpretation mode (see SchemaSemantics).
	// The zero value is SchemaSemanticsSpeakeasy.
	Semantics SchemaSemantics

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
	LogLevel             string // Log level: "", "error", "warn", "info", "debug". Default "": no output — the library is silent on stdout/stderr unless a level is set.
	LogMaxEnumValues     int    // Max enum values to show in logs (default: 5)
	LogMaxProps          int    // Max object properties to show in logs (default: 5)
	LogStackPreviewDepth int    // Max stack depth to preview in logs (default: 3)
	LogSchemaDeltas      bool   // If true, include schema deltas in debug logs (default: true)

	// logger is the resolved Logger for this execution. It is attached by
	// newSchemaEnv (from LogLevel) so that package-level schema operations
	// receiving options can emit diagnostics through the Logger interface.
	// When nil (options not created by an execution), diagnostics are dropped:
	// the library must be silent on stdout/stderr by default.
	logger Logger
}

// normalizeOptions fills numeric safety limits that cannot use zero during an
// execution. Boolean fields are deliberately left unchanged so an explicit
// false remains meaningful.
func normalizeOptions(opts SchemaExecOptions) SchemaExecOptions {
	defaults := DefaultOptions()
	if opts.AnyOfLimit == 0 {
		opts.AnyOfLimit = defaults.AnyOfLimit
	}
	if opts.EnumLimit == 0 {
		opts.EnumLimit = defaults.EnumLimit
	}
	if opts.MaxDepth == 0 {
		opts.MaxDepth = defaults.MaxDepth
	}
	if opts.LogMaxEnumValues == 0 {
		opts.LogMaxEnumValues = defaults.LogMaxEnumValues
	}
	if opts.LogMaxProps == 0 {
		opts.LogMaxProps = defaults.LogMaxProps
	}
	if opts.LogStackPreviewDepth == 0 {
		opts.LogStackPreviewDepth = defaults.LogStackPreviewDepth
	}
	return opts
}

// debugf routes diagnostic output from options-carrying helpers through the
// configured logger. No-op when no logger is attached.
func (o SchemaExecOptions) debugf(format string, args ...any) {
	if o.logger != nil {
		o.logger.Debugf(format, args...)
	}
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
		LogLevel:       "", // silent by default; set "warn"/"debug" to log

		LogMaxEnumValues:     5,
		LogMaxProps:          5,
		LogStackPreviewDepth: 3,
		LogSchemaDeltas:      true,
	}
}
