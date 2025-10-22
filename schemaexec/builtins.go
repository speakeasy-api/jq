package schemaexec

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

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

	// String operations
	"split":          builtinSplit,
	"join":           builtinJoin,
	"startswith":     builtinStartswith,
	"endswith":       builtinEndswith,
	"ltrimstr":       builtinLtrimstr,
	"rtrimstr":       builtinRtrimstr,
	"ascii_downcase": builtinASCIIDowncase,
	"ascii_upcase":   builtinASCIIUpcase,

	// Math operations
	"floor": builtinFloor,
	"ceil":  builtinCeil,
	"round": builtinRound,
	"sqrt":  builtinSqrt,
	"pow":   builtinPow,
	"log":   builtinLog,
	"exp":   builtinExp,

	// Array grouping
	"_group_by":  builtinGroupBy,
	"_sort_by":   builtinSortBy,
	"_unique_by": builtinUniqueBy,
	"_min_by":    builtinMinMaxBy,
	"_max_by":    builtinMinMaxBy,

	// Array manipulation
	"flatten": builtinFlatten,
	"indices": builtinIndices,
	"index":   builtinIndex,
	"rindex":  builtinRindex,
	"contains": builtinContains,
	"inside":   builtinInside,

	// Regex
	"test":   builtinTest,
	"_match": builtinMatch,

	// Internal
	"_break":     builtinBreak,
	"_allocator": builtinAllocator,
	"_setpath":   builtinSetpath, // Reuse public version
	"_delpaths":  builtinDelpaths, // Reuse public version
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
		if valueSchema == nil {
			valueSchema = Bottom()
		}
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
	if getType(keyArg) == "string" && len(keyArg.Enum) > 0 {
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
// STRING OPERATION BUILTINS
// ============================================================================

// builtinSplit splits a string into an array
func builtinSplit(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	if len(args) == 0 {
		return []*oas3.Schema{ArrayType(StringType())}, nil
	}

	// Const folding: if both input and separator are const
	if inputStr, ok := extractConstString(input); ok {
		if sepStr, ok := extractConstString(args[0]); ok {
			parts := strings.Split(inputStr, sepStr)
			// Return const array if small enough
			if len(parts) <= env.opts.EnumLimit {
				return []*oas3.Schema{buildArrayOfConstStrings(parts)}, nil
			}
		}
	}

	// Conservative: return array<string>
	return []*oas3.Schema{ArrayType(StringType())}, nil
}

// builtinJoin joins an array into a string
func builtinJoin(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding: if array is tuple of const strings and sep is provided
	if len(args) > 0 && input.PrefixItems != nil {
		if sepStr, ok := extractConstString(args[0]); ok {
			strs := extractConstStringsFromTuple(input)
			if strs != nil {
				joined := strings.Join(strs, sepStr)
				return []*oas3.Schema{ConstString(joined)}, nil
			}
		}
	}

	// Conservative: return string
	return []*oas3.Schema{StringType()}, nil
}

// builtinStartswith checks if string starts with prefix
func builtinStartswith(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	if len(args) == 0 {
		return []*oas3.Schema{BoolType()}, nil
	}

	// Const folding: if both const
	if inputStr, ok := extractConstString(input); ok {
		if prefixStr, ok := extractConstString(args[0]); ok {
			result := strings.HasPrefix(inputStr, prefixStr)
			return []*oas3.Schema{ConstBool(result)}, nil
		}
	}

	// Conservative: return boolean
	return []*oas3.Schema{BoolType()}, nil
}

// builtinEndswith checks if string ends with suffix
func builtinEndswith(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	if len(args) == 0 {
		return []*oas3.Schema{BoolType()}, nil
	}

	// Const folding
	if inputStr, ok := extractConstString(input); ok {
		if suffixStr, ok := extractConstString(args[0]); ok {
			result := strings.HasSuffix(inputStr, suffixStr)
			return []*oas3.Schema{ConstBool(result)}, nil
		}
	}

	return []*oas3.Schema{BoolType()}, nil
}

// builtinLtrimstr removes prefix from string
func builtinLtrimstr(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	if len(args) == 0 {
		return []*oas3.Schema{input}, nil
	}

	// Const folding
	if inputStr, ok := extractConstString(input); ok {
		if prefixStr, ok := extractConstString(args[0]); ok {
			result := strings.TrimPrefix(inputStr, prefixStr)
			return []*oas3.Schema{ConstString(result)}, nil
		}
	}

	return []*oas3.Schema{StringType()}, nil
}

// builtinRtrimstr removes suffix from string
func builtinRtrimstr(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	if len(args) == 0 {
		return []*oas3.Schema{input}, nil
	}

	// Const folding
	if inputStr, ok := extractConstString(input); ok {
		if suffixStr, ok := extractConstString(args[0]); ok {
			result := strings.TrimSuffix(inputStr, suffixStr)
			return []*oas3.Schema{ConstString(result)}, nil
		}
	}

	return []*oas3.Schema{StringType()}, nil
}

// builtinASCIIDowncase converts string to lowercase
func builtinASCIIDowncase(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		// Conservative: return string type (don't prune path completely)
		return []*oas3.Schema{StringType()}, nil
	}

	// Const folding
	if inputStr, ok := extractConstString(input); ok {
		return []*oas3.Schema{ConstString(strings.ToLower(inputStr))}, nil
	}

	// Enum preservation
	if len(input.Enum) > 0 && len(input.Enum) <= env.opts.EnumLimit {
		newEnum := make([]*yaml.Node, len(input.Enum))
		for i, node := range input.Enum {
			if node.Kind == yaml.ScalarNode {
				newEnum[i] = &yaml.Node{
					Kind:  yaml.ScalarNode,
					Value: strings.ToLower(node.Value),
					Tag:   "!!str",
				}
			} else {
				newEnum[i] = node
			}
		}
		result := StringType()
		result.Enum = newEnum
		return []*oas3.Schema{result}, nil
	}

	return []*oas3.Schema{StringType()}, nil
}

// builtinASCIIUpcase converts string to uppercase
func builtinASCIIUpcase(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		// Conservative: return string type
		return []*oas3.Schema{StringType()}, nil
	}

	// Const folding
	if inputStr, ok := extractConstString(input); ok {
		return []*oas3.Schema{ConstString(strings.ToUpper(inputStr))}, nil
	}

	// Enum preservation
	if len(input.Enum) > 0 && len(input.Enum) <= env.opts.EnumLimit {
		newEnum := make([]*yaml.Node, len(input.Enum))
		for i, node := range input.Enum {
			if node.Kind == yaml.ScalarNode {
				newEnum[i] = &yaml.Node{
					Kind:  yaml.ScalarNode,
					Value: strings.ToUpper(node.Value),
					Tag:   "!!str",
				}
			} else {
				newEnum[i] = node
			}
		}
		result := StringType()
		result.Enum = newEnum
		return []*oas3.Schema{result}, nil
	}

	return []*oas3.Schema{StringType()}, nil
}

// ============================================================================
// MATH OPERATION BUILTINS
// ============================================================================

// builtinFloor rounds down to integer
func builtinFloor(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeNumber(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding
	if val, ok := extractConstValue(input); ok {
		if f, ok := val.(float64); ok {
			return []*oas3.Schema{ConstInteger(int64(math.Floor(f)))}, nil
		}
	}

	return []*oas3.Schema{IntegerType()}, nil
}

// builtinCeil rounds up to integer
func builtinCeil(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeNumber(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding
	if val, ok := extractConstValue(input); ok {
		if f, ok := val.(float64); ok {
			return []*oas3.Schema{ConstInteger(int64(math.Ceil(f)))}, nil
		}
	}

	return []*oas3.Schema{IntegerType()}, nil
}

// builtinRound rounds to nearest integer
func builtinRound(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeNumber(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding
	if val, ok := extractConstValue(input); ok {
		if f, ok := val.(float64); ok {
			return []*oas3.Schema{ConstInteger(int64(math.Round(f)))}, nil
		}
	}

	return []*oas3.Schema{IntegerType()}, nil
}

// builtinSqrt computes square root
func builtinSqrt(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeNumber(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding
	if val, ok := extractConstValue(input); ok {
		if f, ok := val.(float64); ok {
			if f < 0 {
				return []*oas3.Schema{ConstNull()}, nil
			}
			return []*oas3.Schema{ConstNumber(math.Sqrt(f))}, nil
		}
	}

	// Domain analysis: if definitely non-negative, return number
	if input.Minimum != nil && *input.Minimum >= 0 {
		return []*oas3.Schema{NumberType()}, nil
	}

	// Might be negative → number|null
	return []*oas3.Schema{Union([]*oas3.Schema{NumberType(), ConstNull()}, env.opts)}, nil
}

// builtinLog computes natural logarithm
func builtinLog(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeNumber(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding
	if val, ok := extractConstValue(input); ok {
		if f, ok := val.(float64); ok {
			if f <= 0 {
				return []*oas3.Schema{ConstNull()}, nil
			}
			return []*oas3.Schema{ConstNumber(math.Log(f))}, nil
		}
	}

	// Domain analysis: if definitely positive, return number
	if input.Minimum != nil && *input.Minimum > 0 {
		return []*oas3.Schema{NumberType()}, nil
	}

	// Might be ≤0 → number|null
	return []*oas3.Schema{Union([]*oas3.Schema{NumberType(), ConstNull()}, env.opts)}, nil
}

// builtinExp computes e^x
func builtinExp(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeNumber(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding
	if val, ok := extractConstValue(input); ok {
		if f, ok := val.(float64); ok {
			return []*oas3.Schema{ConstNumber(math.Exp(f))}, nil
		}
	}

	return []*oas3.Schema{NumberType()}, nil
}

// builtinPow computes base^exponent
func builtinPow(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeNumber(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	if len(args) == 0 {
		return []*oas3.Schema{NumberType()}, nil
	}

	// Const folding
	if baseVal, ok := extractConstValue(input); ok {
		if expVal, ok := extractConstValue(args[0]); ok {
			if base, ok := baseVal.(float64); ok {
				if exp, ok := expVal.(float64); ok {
					result := math.Pow(base, exp)
					if math.IsNaN(result) || math.IsInf(result, 0) {
						return []*oas3.Schema{ConstNull()}, nil
					}
					return []*oas3.Schema{ConstNumber(result)}, nil
				}
			}
		}
	}

	// Conservative: return number (might be null for invalid domains, but hard to detect)
	return []*oas3.Schema{NumberType()}, nil
}

// ============================================================================
// ARRAY GROUPING BUILTINS
// ============================================================================

// builtinGroupBy groups array elements by key expression
func builtinGroupBy(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Get item type
	itemType := Top()
	if input.Items != nil && input.Items.Left != nil {
		itemType = input.Items.Left
	} else if len(input.PrefixItems) > 0 {
		// For tuples, union all items
		items := make([]*oas3.Schema, 0, len(input.PrefixItems))
		for _, item := range input.PrefixItems {
			if item.Left != nil {
				items = append(items, item.Left)
			}
		}
		itemType = Union(items, env.opts)
	}

	// group_by: array<T> → array<array<T>>
	return []*oas3.Schema{ArrayType(ArrayType(itemType))}, nil
}

// builtinSortBy sorts array by key expression
func builtinSortBy(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Sorting doesn't change schema - return input as-is
	return []*oas3.Schema{input}, nil
}

// builtinUniqueBy removes duplicates by key expression
func builtinUniqueBy(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Dedup doesn't change schema - return input as-is
	return []*oas3.Schema{input}, nil
}

// builtinMinMaxBy returns min/max element by key (reuses min/max logic)
func builtinMinMaxBy(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Same as builtinMinMax - return single item from array
	return builtinMinMax(input, args, env)
}

// ============================================================================
// ARRAY MANIPULATION BUILTINS
// ============================================================================

// builtinFlatten flattens nested arrays
func builtinFlatten(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Determine flatten depth
	depth := -1 // -1 means fully flatten
	if len(args) > 0 {
		if depthVal, ok := extractConstValue(args[0]); ok {
			if d, ok := depthVal.(float64); ok {
				depth = int(d)
			}
		}
	}

	result := flattenSchemaRecursive(input, depth, env.opts)
	return []*oas3.Schema{result}, nil
}

// flattenSchemaRecursive recursively flattens array schema
func flattenSchemaRecursive(schema *oas3.Schema, depth int, opts SchemaExecOptions) *oas3.Schema {
	if depth == 0 || getType(schema) != "array" {
		return schema
	}

	// Collect all item schemas
	items := make([]*oas3.Schema, 0)

	// Add prefixItems
	if schema.PrefixItems != nil {
		for _, item := range schema.PrefixItems {
			if item.Left != nil {
				items = append(items, item.Left)
			}
		}
	}

	// Add items schema
	if schema.Items != nil && schema.Items.Left != nil {
		items = append(items, schema.Items.Left)
	}

	if len(items) == 0 {
		return ArrayType(Bottom())
	}

	// Flatten each item recursively
	flattenedItems := make([]*oas3.Schema, 0)
	for _, item := range items {
		if getType(item) == "array" {
			// Recursively flatten
			flattened := flattenSchemaRecursive(item, depth-1, opts)
			if getType(flattened) == "array" {
				// Extract items from flattened result
				if flattened.Items != nil && flattened.Items.Left != nil {
					flattenedItems = append(flattenedItems, flattened.Items.Left)
				}
				if flattened.PrefixItems != nil {
					for _, pi := range flattened.PrefixItems {
						if pi.Left != nil {
							flattenedItems = append(flattenedItems, pi.Left)
						}
					}
				}
			} else {
				flattenedItems = append(flattenedItems, flattened)
			}
		} else {
			// Already flat (non-array item)
			flattenedItems = append(flattenedItems, item)
		}
	}

	if len(flattenedItems) == 0 {
		return ArrayType(Bottom())
	}

	// Union all flattened items
	unionedType := Union(flattenedItems, opts)
	if unionedType == nil {
		unionedType = Bottom()
	}
	return ArrayType(unionedType)
}

// builtinIndices finds all indices of value in array
func builtinIndices(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) && !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Return array of integers (can't determine which indices match)
	return []*oas3.Schema{ArrayType(IntegerType())}, nil
}

// builtinIndex finds first index of value
func builtinIndex(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) && !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Return integer|null (might find, might not)
	return []*oas3.Schema{Union([]*oas3.Schema{IntegerType(), ConstNull()}, env.opts)}, nil
}

// builtinRindex finds last index of value
func builtinRindex(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeArray(input) && !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Return integer|null
	return []*oas3.Schema{Union([]*oas3.Schema{IntegerType(), ConstNull()}, env.opts)}, nil
}

// builtinContains checks if input contains all elements from argument
func builtinContains(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Conservative: return boolean (can't determine containment symbolically)
	return []*oas3.Schema{BoolType()}, nil
}

// builtinInside checks if input is contained in argument
func builtinInside(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Conservative: return boolean
	return []*oas3.Schema{BoolType()}, nil
}

// ============================================================================
// REGEX OPERATION BUILTINS
// ============================================================================

// builtinTest tests if string matches regex pattern
func builtinTest(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	if len(args) == 0 {
		return []*oas3.Schema{BoolType()}, nil
	}

	// Const folding: if both input and pattern are const
	if inputStr, ok := extractConstString(input); ok {
		if patternStr, ok := extractConstString(args[0]); ok {
			matched, err := regexp.MatchString(patternStr, inputStr)
			if err != nil {
				// Invalid regex pattern - return boolean (unknown)
				return []*oas3.Schema{BoolType()}, nil
			}
			return []*oas3.Schema{ConstBool(matched)}, nil
		}
	}

	// Conservative: can't evaluate regex on symbolic input
	return []*oas3.Schema{BoolType()}, nil
}

// builtinMatch returns match object for regex
func builtinMatch(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Build match object schema
	matchObj := BuildObject(map[string]*oas3.Schema{
		"offset": IntegerType(),
		"length": IntegerType(),
		"string": StringType(),
		"captures": ArrayType(BuildObject(map[string]*oas3.Schema{
			"offset": Union([]*oas3.Schema{IntegerType(), ConstNull()}, env.opts),
			"length": Union([]*oas3.Schema{IntegerType(), ConstNull()}, env.opts),
			"string": Union([]*oas3.Schema{StringType(), ConstNull()}, env.opts),
			"name":   Union([]*oas3.Schema{StringType(), ConstNull()}, env.opts),
		}, []string{})),
	}, []string{"offset", "length", "string", "captures"})

	// Return match object | null (conservative - can't evaluate regex)
	return []*oas3.Schema{Union([]*oas3.Schema{matchObj, ConstNull()}, env.opts)}, nil
}

// ============================================================================
// INTERNAL BUILTINS
// ============================================================================

// builtinBreak signals iteration termination
func builtinBreak(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Return empty to signal backtrack (terminates this execution path)
	// The VM's opCall handler will treat this as no successors
	return []*oas3.Schema{}, nil
}

// builtinAllocator is an internal allocator (no-op for symbolic execution)
func builtinAllocator(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Return empty object (allocator doesn't affect schema)
	return []*oas3.Schema{ObjectType()}, nil
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
// STRING OPERATION HELPERS
// ============================================================================

// extractConstString extracts a const string value from a schema
func extractConstString(schema *oas3.Schema) (string, bool) {
	if val, ok := extractConstValue(schema); ok {
		if s, ok := val.(string); ok {
			return s, true
		}
	}
	return "", false
}

// buildArrayOfConstStrings creates an array schema from const string values
func buildArrayOfConstStrings(strs []string) *oas3.Schema {
	if len(strs) == 0 {
		return ArrayType(StringType())
	}

	prefixItems := make([]*oas3.Schema, len(strs))
	for i, s := range strs {
		prefixItems[i] = ConstString(s)
	}
	return BuildArray(StringType(), prefixItems)
}

// extractConstStringsFromTuple extracts const strings from tuple array
func extractConstStringsFromTuple(schema *oas3.Schema) []string {
	if schema.PrefixItems == nil {
		return nil
	}

	strs := make([]string, 0, len(schema.PrefixItems))
	for _, item := range schema.PrefixItems {
		if item.Left != nil {
			if s, ok := extractConstString(item.Left); ok {
				strs = append(strs, s)
			} else {
				return nil // Non-const item
			}
		}
	}
	return strs
}

// Note: MightBeString and MightBeNumber are defined in schemaops.go

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
	if mergedItems == nil {
		mergedItems = Bottom()
	}
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
