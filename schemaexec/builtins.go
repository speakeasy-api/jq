package schemaexec

import (
	"fmt"
	"strconv"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// builtinFunc is a function that transforms a schema (and optional args).
// It returns the output schema(s) - can return multiple for functions that branch.
type builtinFunc func(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error)

// builtinRegistry maps function names to their schema transformation implementations.
var builtinRegistry = map[string]builtinFunc{
	// Introspection
	"type":   builtinType,
	"length": builtinLength,
	"keys":   builtinKeys,
	"keys_unsorted": builtinKeys,
	"values": builtinValues,
	"has":    builtinHas,

	// Type conversions
	"tonumber": builtinToNumber,
	"tostring": builtinToString,
	"toarray":  builtinToArray,

	// Array operations
	"add":     builtinAdd,
	"reverse": builtinReverse,
	"sort":    builtinSort,
	"unique":  builtinUnique,
	"min":     builtinMinMax,
	"max":     builtinMinMax,

	// Object operations
	"to_entries":   builtinToEntries,
	"from_entries": builtinFromEntries,
	"with_entries": builtinWithEntries,

	// Selection/filtering - these are inline-expanded by compiler
	// NOT builtins - they expand to fork/backtrack patterns
	"select": nil, // Special: compiler macro
	"map":    nil, // Special: compiler macro

	// Comparison operations (for predicates in select, etc.)
	"_equal":   builtinEqual, // ==
	"_notequal": builtinNotEqual, // !=
	"_less":    builtinLess, // <
	"_greater": builtinGreater, // >
	"_lesseq":  builtinLessEq, // <=
	"_greatereq": builtinGreaterEq, // >=

	// Logical operations - these are inline-expanded by compiler, not builtins!
	// "and", "or", "not" expand to fork/jumpifnot patterns

	// Arithmetic operations (binary)
	"_plus":     builtinPlus,
	"_minus":    builtinMinus,
	"_multiply": builtinMultiply,
	"_divide":   builtinDivide,
	"_modulo":   builtinModulo,
	"_negate":   builtinNegate,
	// Aliases that jq actually uses
	"_add":      builtinPlus,
	"_subtract": builtinMinus,

	// Path operations
	"delpaths": builtinDelpaths,
	"getpath":  builtinGetpath,
	"setpath":  builtinSetpath,
}

// ============================================================================
// INTROSPECTION BUILTINS
// ============================================================================

// builtinType returns the type of the input as a string schema.
func builtinType(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if input == nil {
		return []*oas3.Schema{ConstString("null")}, nil
	}

	// Get all possible types from the schema
	types := input.GetType()

	if len(types) == 0 {
		// No type specified - could be anything
		// Return union of all type strings
		typeStrings := []string{"null", "boolean", "number", "string", "array", "object"}
		schemas := make([]*oas3.Schema, len(typeStrings))
		for i, t := range typeStrings {
			schemas[i] = ConstString(t)
		}
		return schemas, nil
	}

	if len(types) == 1 {
		// Single type - return const string
		return []*oas3.Schema{ConstString(string(types[0]))}, nil
	}

	// Multiple types - return union of type strings
	schemas := make([]*oas3.Schema, len(types))
	for i, t := range types {
		schemas[i] = ConstString(string(t))
	}
	return schemas, nil
}

// builtinLength returns a number schema (length is always a number).
func builtinLength(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Length always returns a non-negative number
	schema := NumberType()
	// Could add minimum: 0 constraint
	zero := 0.0
	schema.Minimum = &zero

	return []*oas3.Schema{schema}, nil
}

// builtinKeys returns an array of string keys.
func builtinKeys(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeObject(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Collect known keys
	keys := make([]string, 0)
	if input.Properties != nil {
		for k := range input.Properties.All() {
			keys = append(keys, k)
		}
	}

	var itemSchema *oas3.Schema
	if len(keys) > 0 && len(keys) <= env.opts.EnumLimit {
		// Create string enum of known keys
		enumNodes := make([]*yaml.Node, len(keys))
		for i, k := range keys {
			enumNodes[i] = &yaml.Node{
				Kind:  yaml.ScalarNode,
				Value: k,
				Tag:   "!!str",
			}
		}
		itemSchema = &oas3.Schema{
			Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
			Enum: enumNodes,
		}
	} else if len(keys) > 0 {
		// Too many keys - just string type
		itemSchema = StringType()
	} else {
		// Unknown keys
		itemSchema = StringType()
	}

	// If additionalProperties allowed, widen to any string
	if input.AdditionalProperties != nil && input.AdditionalProperties.Left != nil {
		itemSchema = StringType()
	}

	return []*oas3.Schema{ArrayType(itemSchema)}, nil
}

// builtinValues returns an array of all values.
func builtinValues(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeObject(input) && !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// For objects: return array of value schemas
	if MightBeObject(input) {
		valueSchema := unionAllObjectValues(input, env.opts)
		return []*oas3.Schema{ArrayType(valueSchema)}, nil
	}

	// For arrays: same as input
	return []*oas3.Schema{input}, nil
}

// builtinHas checks if object has a property.
func builtinHas(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) == 0 {
		return []*oas3.Schema{BoolType()}, nil
	}

	// Check if the key is a constant string
	keyArg := args[0]
	if getType(keyArg) == "string" && keyArg.Enum != nil && len(keyArg.Enum) > 0 {
		keyStr := keyArg.Enum[0].Value

		// Check if property exists in schema
		if input.Properties != nil {
			if _, ok := input.Properties.Get(keyStr); ok {
				// Check if required
				for _, req := range input.Required {
					if req == keyStr {
						return []*oas3.Schema{ConstBool(true)}, nil
					}
				}
				// Exists but optional - could be true or false
				return []*oas3.Schema{BoolType()}, nil
			}
		}

		// Check additionalProperties
		if input.AdditionalProperties != nil && input.AdditionalProperties.Left != nil {
			return []*oas3.Schema{BoolType()}, nil
		}

		// Definitely doesn't exist
		return []*oas3.Schema{ConstBool(false)}, nil
	}

	// Unknown key - could be true or false
	return []*oas3.Schema{BoolType()}, nil
}

// ============================================================================
// TYPE CONVERSION BUILTINS
// ============================================================================

// builtinToNumber converts to number.
func builtinToNumber(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return []*oas3.Schema{NumberType()}, nil
}

// builtinToString converts to string.
func builtinToString(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return []*oas3.Schema{StringType()}, nil
}

// builtinToArray converts to array.
func builtinToArray(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if MightBeArray(input) {
		return []*oas3.Schema{input}, nil
	}
	// Wrap in array
	return []*oas3.Schema{ArrayType(input)}, nil
}

// ============================================================================
// ARRAY BUILTINS
// ============================================================================

// builtinAdd sums array elements (for numbers) or concatenates.
func builtinAdd(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Get item type
	var itemType string
	if input.Items != nil && input.Items.Left != nil {
		itemType = getType(input.Items.Left)
	}

	// add on number array -> number
	if itemType == "number" || itemType == "integer" {
		return []*oas3.Schema{NumberType()}, nil
	}

	// add on string array -> string
	if itemType == "string" {
		return []*oas3.Schema{StringType()}, nil
	}

	// add on array array -> array (concatenation)
	if itemType == "array" && input.Items != nil && input.Items.Left != nil {
		return []*oas3.Schema{input.Items.Left}, nil
	}

	// add on object array -> object (merge)
	if itemType == "object" && input.Items != nil && input.Items.Left != nil {
		return []*oas3.Schema{input.Items.Left}, nil
	}

	// If items are empty/unknown but we're in a numeric context (e.g., map arithmetic),
	// conservatively return number (most common case for add)
	if itemType == "" {
		return []*oas3.Schema{NumberType()}, nil
	}

	// Unknown - return Top
	return []*oas3.Schema{Top()}, nil
}

// builtinReverse reverses an array.
func builtinReverse(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}
	// Reverse doesn't change schema
	return []*oas3.Schema{input}, nil
}

// builtinSort sorts an array.
func builtinSort(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}
	// Sort doesn't change schema
	return []*oas3.Schema{input}, nil
}

// builtinUnique removes duplicates.
func builtinUnique(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}
	// Unique doesn't change schema type
	return []*oas3.Schema{input}, nil
}

// builtinMinMax returns min or max element.
func builtinMinMax(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Return item type
	if input.Items != nil && input.Items.Left != nil {
		return []*oas3.Schema{input.Items.Left}, nil
	}

	return []*oas3.Schema{Top()}, nil
}

// ============================================================================
// OBJECT BUILTINS
// ============================================================================

// builtinToEntries converts {a:1, b:2} to [{key:"a", value:1}, {key:"b", value:2}]
func builtinToEntries(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeObject(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Create entry object schema: {key: string, value: <union of all values>}
	valueSchema := unionAllObjectValues(input, env.opts)

	entrySchema := BuildObject(map[string]*oas3.Schema{
		"key":   StringType(),
		"value": valueSchema,
	}, []string{"key", "value"})

	return []*oas3.Schema{ArrayType(entrySchema)}, nil
}

// builtinFromEntries converts [{key:"a", value:1}] to {a:1}
func builtinFromEntries(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Get item schema
	var itemSchema *oas3.Schema
	if input.Items != nil && input.Items.Left != nil {
		itemSchema = input.Items.Left
	} else {
		return []*oas3.Schema{ObjectType()}, nil
	}

	// Extract value type from items
	var valueSchema *oas3.Schema
	if itemSchema.Properties != nil {
		if valProp, ok := itemSchema.Properties.Get("value"); ok {
			if valProp.Left != nil {
				valueSchema = valProp.Left
			}
		}
	}

	if valueSchema == nil {
		valueSchema = Top()
	}

	// Result is object with unknown keys and the value type
	result := ObjectType()
	result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](valueSchema)

	return []*oas3.Schema{result}, nil
}

// builtinWithEntries is a helper for object transformations.
// with_entries(f) is equivalent to: to_entries | map(f) | from_entries
func builtinWithEntries(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// For now, conservative: preserve object structure but widen values
	if !MightBeObject(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// TODO: Apply transformation to entry objects
	// For now, return object with Top values
	result := ObjectType()
	result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Top())

	return []*oas3.Schema{result}, nil
}

// ============================================================================
// PATH OPERATION BUILTINS
// ============================================================================

// builtinDelpaths implements delpaths(paths) - delete multiple paths from input
// Used by del() which compiles to delpaths
func builtinDelpaths(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if env.opts.EnableWarnings {
		env.addWarning("delpaths called: input type=%s, args count=%d", getType(input), len(args))
	}

	if len(args) == 0 {
		// No paths to delete
		if env.opts.EnableWarnings {
			env.addWarning("delpaths: no args, returning input unchanged")
		}
		return []*oas3.Schema{input}, nil
	}

	pathsArg := args[0]
	if env.opts.EnableWarnings {
		env.addWarning("delpaths: pathsArg type=%s", getType(pathsArg))
		if pathsArg.PrefixItems != nil {
			env.addWarning("delpaths: pathsArg has %d prefixItems", len(pathsArg.PrefixItems))
		}
		if pathsArg.Items != nil && pathsArg.Items.Left != nil {
			env.addWarning("delpaths: pathsArg.Items type=%s", getType(pathsArg.Items.Left))
			if pathsArg.Items.Left.PrefixItems != nil {
				env.addWarning("delpaths: pathsArg.Items has %d prefixItems", len(pathsArg.Items.Left.PrefixItems))
			}
		}
	}

	// Extract paths from the schema (array of path arrays)
	paths := extractPathsFromSchema(pathsArg)

	if env.opts.EnableWarnings {
		env.addWarning("delpaths: extracted %d paths", len(paths))
		for i, path := range paths {
			env.addWarning("delpaths: path[%d] has %d segments", i, len(path))
		}
	}

	if len(paths) == 0 {
		// No paths to delete
		if env.opts.EnableWarnings {
			env.addWarning("delpaths: no paths extracted, returning input unchanged")
		}
		return []*oas3.Schema{input}, nil
	}

	// Apply deletion for each path
	result := input
	for _, path := range paths {
		result = deletePathFromSchema(result, path, env.opts)
	}

	if env.opts.EnableWarnings {
		env.addWarning("delpaths: result type=%s", getType(result))
	}

	return []*oas3.Schema{result}, nil
}

// builtinGetpath implements getpath(path) - get value at path
func builtinGetpath(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) == 0 {
		return []*oas3.Schema{Bottom()}, nil
	}

	pathArg := args[0]
	paths := extractPathsFromSchema(pathArg)

	if len(paths) == 0 {
		return []*oas3.Schema{Bottom()}, nil
	}

	// For single path, navigate and return schema at that location
	// For multiple paths (union), return union of all results
	results := make([]*oas3.Schema, len(paths))
	for i, path := range paths {
		results[i] = navigatePathInSchema(input, path, env.opts)
	}

	if len(results) == 1 {
		return []*oas3.Schema{results[0]}, nil
	}
	return []*oas3.Schema{Union(results, env.opts)}, nil
}

// builtinSetpath implements setpath(path; value) - set value at path
func builtinSetpath(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) < 2 {
		return []*oas3.Schema{input}, nil
	}

	pathArg := args[0]
	valueArg := args[1]

	paths := extractPathsFromSchema(pathArg)
	if len(paths) == 0 {
		return []*oas3.Schema{input}, nil
	}

	// Set value at path (only handle single path for now)
	result := input
	for _, path := range paths {
		result = setPathInSchema(result, path, valueArg, env.opts)
	}

	return []*oas3.Schema{result}, nil
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

// callBuiltin invokes a builtin function on a schema.
func (env *schemaEnv) callBuiltin(name string, input *oas3.Schema, args []*oas3.Schema) ([]*oas3.Schema, error) {
	fn, exists := builtinRegistry[name]
	if !exists || fn == nil {
		// Unknown or special builtin
		return nil, fmt.Errorf("builtin %s not implemented", name)
	}

	return fn(input, args, env)
}

// isBuiltin checks if a name is a known builtin.
func isBuiltin(name string) bool {
	_, exists := builtinRegistry[name]
	return exists
}

// ============================================================================
// COMPARISON BUILTINS (for predicates in select, etc.)
// ============================================================================

// builtinEqual implements == comparison.
func builtinEqual(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{BoolType()}, nil
	}
	return compareSchemas(input, args[0], func(cmp int) bool { return cmp == 0 })
}

// builtinNotEqual implements != comparison.
func builtinNotEqual(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{BoolType()}, nil
	}
	return compareSchemas(input, args[0], func(cmp int) bool { return cmp != 0 })
}

// builtinLess implements < comparison.
func builtinLess(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{BoolType()}, nil
	}
	return compareSchemas(input, args[0], func(cmp int) bool { return cmp < 0 })
}

// builtinGreater implements > comparison.
func builtinGreater(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{BoolType()}, nil
	}
	return compareSchemas(input, args[0], func(cmp int) bool { return cmp > 0 })
}

// builtinLessEq implements <= comparison.
func builtinLessEq(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{BoolType()}, nil
	}
	return compareSchemas(input, args[0], func(cmp int) bool { return cmp <= 0 })
}

// builtinGreaterEq implements >= comparison.
func builtinGreaterEq(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{BoolType()}, nil
	}
	return compareSchemas(input, args[0], func(cmp int) bool { return cmp >= 0 })
}

// compareSchemas compares two schemas and returns a boolean schema.
// If both are const values, compares them and returns ConstBool.
// Otherwise returns BoolType().
func compareSchemas(lhs, rhs *oas3.Schema, pred func(int) bool) ([]*oas3.Schema, error) {
	// Try to extract const values
	lhsVal, lhsIsConst := extractConstValue(lhs)
	rhsVal, rhsIsConst := extractConstValue(rhs)

	if lhsIsConst && rhsIsConst {
		// Both are const - can compute result
		cmp := compareValues(lhsVal, rhsVal)
		return []*oas3.Schema{ConstBool(pred(cmp))}, nil
	}

	// Can't determine statically - return generic boolean
	return []*oas3.Schema{BoolType()}, nil
}

// extractConstValue extracts a const value from a schema if it has enum with 1 value.
func extractConstValue(schema *oas3.Schema) (any, bool) {
	if schema == nil || schema.Enum == nil || len(schema.Enum) != 1 {
		return nil, false
	}

	node := schema.Enum[0]
	if node.Kind != yaml.ScalarNode {
		return nil, false
	}

	// Get type safely
	types := schema.GetType()
	var t string
	if len(types) > 0 {
		t = string(types[0])
	}

	// Try type-specific parsing
	switch t {
	case "number", "integer":
		if f, err := parseFloat(node.Value); err == nil {
			return f, true
		}
	case "string":
		return node.Value, true
	case "boolean":
		return node.Value == "true", true
	}

	// Fallback to YAML tag if type is missing or unrecognized
	switch node.Tag {
	case "!!bool":
		return node.Value == "true", true
	case "!!int", "!!float":
		if f, err := parseFloat(node.Value); err == nil {
			return f, true
		}
	case "!!str":
		return node.Value, true
	}

	// Default: return string value
	return node.Value, true
}

// compareValues compares two values similar to jq's Compare function.
// Returns -1 if l < r, 0 if l == r, 1 if l > r.
func compareValues(l, r any) int {
	// Handle nils
	if l == nil && r == nil {
		return 0
	}
	if l == nil {
		return -1
	}
	if r == nil {
		return 1
	}

	// Type-based comparison
	switch lv := l.(type) {
	case float64:
		if rv, ok := r.(float64); ok {
			if lv < rv {
				return -1
			} else if lv > rv {
				return 1
			}
			return 0
		}
	case string:
		if rv, ok := r.(string); ok {
			if lv < rv {
				return -1
			} else if lv > rv {
				return 1
			}
			return 0
		}
	case bool:
		if rv, ok := r.(bool); ok {
			if !lv && rv {
				return -1
			} else if lv && !rv {
				return 1
			}
			return 0
		}
	}

	// Different types or unsupported - return 0 (conservative)
	return 0
}

// parseFloat parses a string to float64.
func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

// ============================================================================
// LOGICAL BUILTINS
// ============================================================================

// builtinAnd implements logical AND.
func builtinAnd(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{BoolType()}, nil
	}

	// Check if definitely true/false
	inputTruthy := isTruthy(input)
	argTruthy := isTruthy(args[0])

	if inputTruthy != nil && argTruthy != nil {
		// Both definite
		return []*oas3.Schema{ConstBool(*inputTruthy && *argTruthy)}, nil
	}
	if inputTruthy != nil && !*inputTruthy {
		return []*oas3.Schema{ConstBool(false)}, nil // false AND x = false
	}
	if argTruthy != nil && !*argTruthy {
		return []*oas3.Schema{ConstBool(false)}, nil // x AND false = false
	}

	return []*oas3.Schema{BoolType()}, nil
}

// builtinOr implements logical OR.
func builtinOr(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{BoolType()}, nil
	}

	inputTruthy := isTruthy(input)
	argTruthy := isTruthy(args[0])

	if inputTruthy != nil && argTruthy != nil {
		return []*oas3.Schema{ConstBool(*inputTruthy || *argTruthy)}, nil
	}
	if inputTruthy != nil && *inputTruthy {
		return []*oas3.Schema{ConstBool(true)}, nil // true OR x = true
	}
	if argTruthy != nil && *argTruthy {
		return []*oas3.Schema{ConstBool(true)}, nil // x OR true = true
	}

	return []*oas3.Schema{BoolType()}, nil
}

// builtinNot implements logical NOT.
func builtinNot(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	truthy := isTruthy(input)
	if truthy != nil {
		return []*oas3.Schema{ConstBool(!*truthy)}, nil
	}
	return []*oas3.Schema{BoolType()}, nil
}

// isTruthy determines if a schema is definitely truthy/falsy.
// Returns nil if uncertain.
func isTruthy(schema *oas3.Schema) *bool {
	if schema == nil {
		f := false
		return &f // null is falsy
	}

	// Check for const boolean
	if val, ok := extractConstValue(schema); ok {
		if b, ok := val.(bool); ok {
			return &b
		}
	}

	// Check for const false/null
	typ := getType(schema)
	if typ == "boolean" && schema.Enum != nil && len(schema.Enum) == 1 {
		if schema.Enum[0].Value == "false" {
			f := false
			return &f
		}
		if schema.Enum[0].Value == "true" {
			t := true
			return &t
		}
	}
	if typ == "null" {
		f := false
		return &f
	}

	return nil // Unknown
}

// ============================================================================
// ARITHMETIC BUILTINS
// ============================================================================

// builtinPlus implements + for two values.
func builtinPlus(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) < 1 {
		return []*oas3.Schema{NumberType()}, nil
	}

	var lhs, rhs *oas3.Schema
	if len(args) == 2 {
		// Some jq plans encode + as arity-2; normalize against possible outer input object bleed-through.
		lhs, rhs = pickBinaryOperands(input, args[0], args[1])
	} else {
		// Arity-1: jq uses lhs as input, rhs as arg[0]
		lhs = input
		rhs = args[0]
	}

	// Null identity
	if isNullSchema(lhs) {
		return []*oas3.Schema{rhs}, nil
	}
	if isNullSchema(rhs) {
		return []*oas3.Schema{lhs}, nil
	}

	// Safety check
	if lhs == nil || rhs == nil {
		return []*oas3.Schema{NumberType()}, nil
	}

	lType := getType(lhs)
	rType := getType(rhs)

	// Numbers
	if (lType == "number" || lType == "integer") && (rType == "number" || rType == "integer") {
		return []*oas3.Schema{addNumericSchemas(lhs, rhs)}, nil
	}

	// Arrays
	if lType == "array" && rType == "array" {
		return []*oas3.Schema{concatArraySchemas(lhs, rhs, env.opts)}, nil
	}

	// Strings
	if lType == "string" && rType == "string" {
		return []*oas3.Schema{concatStringSchemas(lhs, rhs)}, nil
	}

	// Objects
	if lType == "object" && rType == "object" {
		return []*oas3.Schema{MergeObjects(lhs, rhs, env.opts)}, nil
	}

	// Mixed/unknown or empty types: fallback to number (conservative for arithmetic)
	if lType == "" || rType == "" {
		return []*oas3.Schema{NumberType()}, nil
	}

	// Mixed types: return Top
	return []*oas3.Schema{Top()}, nil
}

// builtinMinus implements - for two values.
func builtinMinus(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{NumberType()}, nil
	}
	return arithmeticOp(input, args[0], func(a, b float64) float64 { return a - b })
}

// builtinMultiply implements * for two values.
func builtinMultiply(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{NumberType()}, nil
	}
	return arithmeticOp(input, args[0], func(a, b float64) float64 { return a * b })
}

// builtinDivide implements / for two values.
func builtinDivide(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{NumberType()}, nil
	}
	return arithmeticOp(input, args[0], func(a, b float64) float64 { return a / b })
}

// builtinModulo implements % for two values.
func builtinModulo(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) != 1 {
		return []*oas3.Schema{NumberType()}, nil
	}
	return arithmeticOp(input, args[0], func(a, b float64) float64 {
		// Integer modulo for integers
		return float64(int(a) % int(b))
	})
}

// builtinNegate implements unary - (negation).
func builtinNegate(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Extract const if possible
	if val, ok := extractConstValue(input); ok {
		if f, ok := val.(float64); ok {
			return []*oas3.Schema{ConstNumber(-f)}, nil
		}
	}
	return []*oas3.Schema{NumberType()}, nil
}

// pickBinaryOperands chooses (lhs, rhs) robustly across jq calling conventions.
// Object-first: preserve explicit args order for object+object cases.
func pickBinaryOperands(input, a0, a1 *oas3.Schema) (*oas3.Schema, *oas3.Schema) {
	// Count how many are objects
	numObjects := 0
	if getType(input) == "object" {
		numObjects++
	}
	if getType(a0) == "object" {
		numObjects++
	}
	if getType(a1) == "object" {
		numObjects++
	}

	// If we have 3 objects, the outer input is likely a1
	// Prefer a0 (first operand) + input (second operand)
	if numObjects == 3 {
		return a0, input
	}

	// If a0 and input are both objects (common for object literals where a1 is null)
	if getType(a0) == "object" && getType(input) == "object" {
		return a0, input
	}

	// If both args are objects (and input is not), use args as-is
	if getType(a0) == "object" && getType(a1) == "object" {
		return a0, a1
	}

	cands := []*oas3.Schema{a0, a1, input}

	// Prefer two non-object operands (avoid outer input object bleed-through)
	nonObjIdx := make([]int, 0, 3)
	for i, s := range cands {
		if s == nil {
			continue
		}
		if getType(s) != "object" {
			nonObjIdx = append(nonObjIdx, i)
		}
	}
	if len(nonObjIdx) >= 2 {
		i := nonObjIdx[len(nonObjIdx)-2]
		j := nonObjIdx[len(nonObjIdx)-1]
		return cands[i], cands[j]
	}

	// Fallback: use args as-is
	return a0, a1
}

// arithmeticOp performs arithmetic on two schemas.
func arithmeticOp(lhs, rhs *oas3.Schema, op func(float64, float64) float64) ([]*oas3.Schema, error) {
	lhsVal, lhsIsConst := extractConstValue(lhs)
	rhsVal, rhsIsConst := extractConstValue(rhs)

	if lhsIsConst && rhsIsConst {
		lf, lok := lhsVal.(float64)
		rf, rok := rhsVal.(float64)
		if lok && rok {
			return []*oas3.Schema{ConstNumber(op(lf, rf))}, nil
		}
	}

	return []*oas3.Schema{NumberType()}, nil
}

// isNullSchema returns true if schema is explicitly null type.
func isNullSchema(s *oas3.Schema) bool {
	return getType(s) == "null"
}

// addNumericSchemas handles integer/number addition with const folding and result type.
func addNumericSchemas(lhs, rhs *oas3.Schema) *oas3.Schema {
	// Const-fold when possible
	if lv, lok := extractConstValue(lhs); lok {
		if rv, rok := extractConstValue(rhs); rok {
			if lf, okL := lv.(float64); okL {
				if rf, okR := rv.(float64); okR {
					// If both are integer-typed, preserve integer when sum is integral
					if getType(lhs) == "integer" && getType(rhs) == "integer" {
						sum := lf + rf
						if sum == float64(int64(sum)) {
							return ConstInteger(int64(sum))
						}
					}
					return ConstNumber(lf + rf)
				}
			}
		}
	}

	// Type-only result
	if getType(lhs) == "integer" && getType(rhs) == "integer" {
		return IntegerType()
	}
	return NumberType()
}

// concatArraySchemas concatenates arrays by unioning all possible element schemas.
func concatArraySchemas(a, b *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	items := make([]*oas3.Schema, 0, 8)
	collectArrayItemCandidates := func(arr *oas3.Schema) {
		if arr == nil {
			return
		}
		if arr.PrefixItems != nil {
			for _, pi := range arr.PrefixItems {
				if pi.Left != nil {
					items = append(items, pi.Left)
				}
			}
		}
		if arr.Items != nil && arr.Items.Left != nil {
			items = append(items, arr.Items.Left)
		}
	}
	collectArrayItemCandidates(a)
	collectArrayItemCandidates(b)

	var mergedItems *oas3.Schema
	if len(items) == 0 {
		mergedItems = Top()
	} else {
		mergedItems = Union(items, opts)
		if mergedItems == nil {
			mergedItems = Top()
		}
	}
	// Widen to homogeneous array of merged items (drop tuple info for safe concat)
	return ArrayType(mergedItems)
}

// concatStringSchemas concatenates strings with const folding.
func concatStringSchemas(a, b *oas3.Schema) *oas3.Schema {
	if av, okA := extractConstValue(a); okA {
		if bv, okB := extractConstValue(b); okB {
			as, okAs := av.(string)
			bs, okBs := bv.(string)
			if okAs && okBs {
				return ConstString(as + bs)
			}
		}
	}
	return StringType()
}
