package schemaexec

import (
	"context"
	"fmt"

	"github.com/itchyny/gojq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// schemaEnv is the execution environment for symbolic execution over schemas.
// It's analogous to the concrete env in execute.go but operates on schemas.
type schemaEnv struct {
	ctx       context.Context
	opts      SchemaExecOptions
	codes     []codeOp // Simplified view of bytecode
	stack     *schemaStack
	pc        int
	warnings  []string
	variables map[string]*oas3.Schema // Variable storage for load/store
}

// codeOp represents a bytecode operation for schema execution.
// We extract just the opcode name and value from gojq's internal code structure.
type codeOp struct {
	op    string // Opcode name (from code.String())
	value any    // Opcode value
}

// newSchemaEnv creates a new schema execution environment.
func newSchemaEnv(ctx context.Context, opts SchemaExecOptions) *schemaEnv {
	return &schemaEnv{
		ctx:       ctx,
		opts:      opts,
		stack:     newSchemaStack(),
		warnings:  make([]string, 0),
		variables: make(map[string]*oas3.Schema),
	}
}

// execute runs the bytecode on the input schema and returns the result.
func (env *schemaEnv) execute(c *gojq.Code, input *oas3.Schema) (*SchemaExecResult, error) {
	// Get bytecode from Code
	rawCodes := c.GetCodes()

	// Convert to our simplified code representation
	env.codes = make([]codeOp, len(rawCodes))
	for i, rc := range rawCodes {
		env.codes[i] = codeOp{
			op:    opcodeToString(rc),
			value: getCodeValue(rc),
		}
	}

	// Push input schema onto stack
	env.stack.pushSchema(input)

	// Execute bytecode
	for env.pc < len(env.codes) {
		// Check context cancellation
		select {
		case <-env.ctx.Done():
			return nil, env.ctx.Err()
		default:
		}

		code := env.codes[env.pc]

		// Dispatch opcode
		if err := env.executeOp(&code); err != nil {
			return nil, fmt.Errorf("error at pc=%d op=%v: %w", env.pc, code.op, err)
		}

		env.pc++
	}

	// Collect all outputs from stack
	outputs := make([]*oas3.Schema, 0)
	for !env.stack.empty() {
		outputs = append(outputs, env.stack.popSchema())
	}

	// Union all outputs
	var result *oas3.Schema
	if len(outputs) == 0 {
		result = Bottom()
	} else if len(outputs) == 1 {
		result = outputs[0]
	} else {
		result = Union(outputs, env.opts)
	}

	return &SchemaExecResult{
		Schema:   result,
		Warnings: env.warnings,
	}, nil
}

// executeOp executes a single bytecode operation.
func (env *schemaEnv) executeOp(c *codeOp) error {
	switch c.op {
	case "nop":
		// No operation
		return nil

	case "push":
		return env.execPush(c)

	case "pop":
		return env.execPop(c)

	case "const":
		return env.execConst(c)

	case "index":
		return env.execIndex(c)

	case "indexarray":
		return env.execIndexArray(c)

	case "iter":
		return env.execIter(c)

	case "object":
		return env.execObject(c)

	case "ret":
		// Return from function - for now, just continue
		// Full implementation needs scope handling
		return nil

	case "scope":
		// Variable scope - for Phase 2, we don't track variables yet
		// Just no-op for now
		return nil

	case "store":
		// Store variable - save to variables map
		if !env.stack.empty() {
			val := env.stack.popSchema()
			// The value contains [scopeID, varIndex]
			// For simplicity, use string key
			key := fmt.Sprintf("%v", c.value)
			env.variables[key] = val
		}
		return nil

	case "load":
		// Load variable from variables map
		key := fmt.Sprintf("%v", c.value)
		if val, ok := env.variables[key]; ok {
			env.stack.pushSchema(val)
		} else {
			// Variable not found - push Top
			env.stack.pushSchema(Top())
			env.addWarning("variable %s not found, using Top", key)
		}
		return nil

	case "dup":
		// Duplicate top of stack
		if env.stack.empty() {
			return fmt.Errorf("stack underflow on dup")
		}
		top := env.stack.topSchema()
		env.stack.pushSchema(top)
		return nil

	// Unsupported opcodes - handle gracefully
	default:
		if env.opts.StrictMode {
			return fmt.Errorf("unsupported opcode: %v", c.op)
		}

		// Permissive mode: widen to Top
		env.addWarning("unsupported opcode %v, widening to Top", c.op)
		if !env.stack.empty() {
			env.stack.popSchema()
		}
		env.stack.pushSchema(Top())
		return nil
	}
}

// ============================================================================
// OPCODE HANDLERS
// ============================================================================

// execPush handles oppush - push a constant value as a schema.
func (env *schemaEnv) execPush(c *codeOp) error {
	v := c.value

	var schema *oas3.Schema
	switch val := v.(type) {
	case string:
		schema = ConstString(val)
	case float64:
		schema = ConstNumber(val)
	case int:
		schema = ConstNumber(float64(val))
	case bool:
		schema = ConstBool(val)
	case nil:
		schema = ConstNull()
	case map[string]any:
		// Object literal - for now, create generic object
		// TODO Phase 3: Build proper object schema from literal
		schema = ObjectType()
		env.addWarning("object literal not fully supported, using generic object type")
	case []any:
		// Array literal - for now, create generic array
		// TODO Phase 3: Build proper array schema from literal
		schema = ArrayType(Top())
		env.addWarning("array literal not fully supported, using generic array type")
	default:
		schema = Top()
		env.addWarning("unknown constant type: %T", v)
	}

	env.stack.pushSchema(schema)
	return nil
}

// execPop handles oppop - remove top value from stack.
func (env *schemaEnv) execPop(c *codeOp) error {
	if env.stack.empty() {
		return fmt.Errorf("stack underflow on pop")
	}
	env.stack.popSchema()
	return nil
}

// execConst handles opconst - replace top of stack with constant.
func (env *schemaEnv) execConst(c *codeOp) error {
	if env.stack.empty() {
		return fmt.Errorf("stack underflow on const")
	}

	env.stack.popSchema()

	// Push the constant value
	v := c.value
	var schema *oas3.Schema
	switch val := v.(type) {
	case string:
		schema = ConstString(val)
	case float64:
		schema = ConstNumber(val)
	case int:
		schema = ConstNumber(float64(val))
	case bool:
		schema = ConstBool(val)
	case nil:
		schema = ConstNull()
	default:
		schema = Top()
	}

	env.stack.pushSchema(schema)
	return nil
}

// execIndex handles opindex - index into object or array with a constant key.
func (env *schemaEnv) execIndex(c *codeOp) error {
	if env.stack.empty() {
		return fmt.Errorf("stack underflow on index")
	}

	base := env.stack.popSchema()
	indexKey := c.value // The key is stored in the code

	var result *oas3.Schema

	// Determine what we're indexing
	baseType := getType(base)

	switch baseType {
	case "object":
		// Object property access
		if key, ok := indexKey.(string); ok {
			result = GetProperty(base, key, env.opts)
		} else {
			result = Top()
			env.addWarning("non-string object index")
		}

	case "array":
		// Array indexing - for now, return items schema
		if base.Items != nil && base.Items.Left != nil {
			result = base.Items.Left
		} else {
			result = Top()
		}

	case "":
		// Unknown type - could be object or array
		// Try both and union
		if key, ok := indexKey.(string); ok {
			objResult := GetProperty(base, key, env.opts)
			arrResult := Top() // Conservative for array case
			result = Union([]*oas3.Schema{objResult, arrResult}, env.opts)
		} else {
			result = Top()
		}

	default:
		// Indexing other types returns null in jq
		result = ConstNull()
	}

	env.stack.pushSchema(result)
	return nil
}

// execIndexArray handles opindexarray - index that requires array type.
func (env *schemaEnv) execIndexArray(c *codeOp) error {
	// Similar to execIndex but enforces array type
	return env.execIndex(c)
}

// execIter handles opiter - iterate array or object values.
func (env *schemaEnv) execIter(c *codeOp) error {
	if env.stack.empty() {
		return fmt.Errorf("stack underflow on iter")
	}

	val := env.stack.popSchema()
	baseType := getType(val)

	var itemSchema *oas3.Schema

	switch baseType {
	case "array":
		// Iterate array items
		if val.Items != nil && val.Items.Left != nil {
			itemSchema = val.Items.Left
		} else {
			itemSchema = Top()
		}

	case "object":
		// Iterate object values - union of all property schemas
		// For now, return Top (conservative)
		// TODO Phase 3: Implement proper unionAllObjectValues
		itemSchema = Top()
		env.addWarning("object iteration uses Top (conservative)")

	case "":
		// Unknown type - could be either
		itemSchema = Top()

	default:
		// Non-iterable returns nothing
		itemSchema = Bottom()
	}

	env.stack.pushSchema(itemSchema)
	return nil
}

// execObject handles opobject - construct object from key-value pairs.
func (env *schemaEnv) execObject(c *codeOp) error {
	n := c.value.(int) // Number of key-value pairs

	props := make(map[string]*oas3.Schema)
	required := make([]string, 0, n)

	for i := 0; i < n; i++ {
		if env.stack.len() < 2 {
			return fmt.Errorf("stack underflow on object construction (need %d pairs, stack has %d)", n, env.stack.len()/2)
		}

		val := env.stack.popSchema()
		key := env.stack.popSchema()

		// Check if key is constant string
		if getType(key) == "string" && key.Enum != nil && len(key.Enum) > 0 {
			// Extract constant string from enum
			keyNode := key.Enum[0]
			if keyNode.Kind == yaml.ScalarNode {
				keyStr := keyNode.Value
				props[keyStr] = val
				// In jq object construction, keys are always present
				required = append(required, keyStr)
			}
		} else {
			// Dynamic key - not fully supported yet
			env.addWarning("dynamic object key not fully supported")
		}
	}
	obj := BuildObject(props, required)
	env.stack.pushSchema(obj)
	return nil
}

// ============================================================================
// HELPERS
// ============================================================================

// addWarning adds a warning message to the execution result.
func (env *schemaEnv) addWarning(format string, args ...any) {
	if env.opts.EnableWarnings {
		env.warnings = append(env.warnings, fmt.Sprintf(format, args...))
	}
}

// opcodeToString extracts the opcode name from gojq's code.
func opcodeToString(c any) string {
	// Access via the public OpString method we added to code
	if code, ok := c.(interface{ OpString() string }); ok {
		return code.OpString()
	}
	return fmt.Sprintf("%v", c)
}

// getCodeValue extracts the value from gojq's code.
func getCodeValue(c any) any {
	// Access via the public GetValue method we added to code
	if code, ok := c.(interface{ GetValue() any }); ok {
		return code.GetValue()
	}
	return nil
}
