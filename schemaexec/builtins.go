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

	// Logical operations
	"and": builtinAnd,
	"or":  builtinOr,
	"not": builtinNot,

	// Arithmetic operations (binary)
	"_plus":     builtinPlus,
	"_minus":    builtinMinus,
	"_multiply": builtinMultiply,
	"_divide":   builtinDivide,
	"_modulo":   builtinModulo,
	"_negate":   builtinNegate,
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
	switch node.Kind {
	case yaml.ScalarNode:
		// Try to parse as number
		if schema.GetType()[0] == "number" {
			if f, err := parseFloat(node.Value); err == nil {
				return f, true
			}
		}
		// String
		if schema.GetType()[0] == "string" {
			return node.Value, true
		}
		// Boolean
		if schema.GetType()[0] == "boolean" {
			return node.Value == "true", true
		}
		return node.Value, true
	default:
		return nil, false
	}
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
	if len(args) != 1 {
		return []*oas3.Schema{NumberType()}, nil
	}
	return arithmeticOp(input, args[0], func(a, b float64) float64 { return a + b })
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
