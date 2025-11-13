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
	"type":          builtinType,
	"length":        builtinLength,
	"keys":          builtinKeys,
	"keys_unsorted": builtinKeys,
	"values":        builtinValues,
	"has":           builtinHas,

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
	"_equal":     builtinEqual,     // ==
	"_notequal":  builtinNotEqual,  // !=
	"_less":      builtinLess,      // <
	"_greater":   builtinGreater,   // >
	"_lesseq":    builtinLessEq,    // <=
	"_greatereq": builtinGreaterEq, // >=

	// Logical operations - these are inline-expanded by compiler, not builtins!
	// "and", "or", "not" expand to fork/jumpifnot patterns

	// Arithmetic operations (binary)
	// IMPORTANT: _plus and _add both map to builtinAddOp to get full jq "+" semantics.
	"_plus":     builtinAddOp,
	"_minus":    builtinMinus,
	"_multiply": builtinMultiply,
	"_divide":   builtinDivide,
	"_modulo":   builtinModulo,
	"_negate":   builtinNegate,
	// Aliases that jq actually uses
	"_add":      builtinAddOp,
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
	"gsub":           builtinGsub,

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
	"flatten":  builtinFlatten,
	"indices":  builtinIndices,
	"index":    builtinIndex,
	"rindex":   builtinRindex,
	"contains": builtinContains,
	"inside":   builtinInside,

	// Regex
	"test":   builtinTest,
	"_match": builtinMatch,
	"sub":    builtinSub,

	// Indexing
	"_index": builtinIndexOp,

	// Internal
	"_break":     builtinBreak,
	"_allocator": builtinAllocator,
	"_setpath":   builtinSetpath,  // Reuse public version
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

	// DEBUG: Log input object structure
	if env.opts.EnableWarnings {
		propCount := 0
		if input.Properties != nil {
			propCount = input.Properties.Len()
		}
		hasAP := input.AdditionalProperties != nil && input.AdditionalProperties.Left != nil
		var apType string
		if hasAP {
			apType = getType(input.AdditionalProperties.Left)
		}
		inputPtr := fmt.Sprintf("%p", input)
		fmt.Printf("DEBUG builtinToEntries: input ptr=%s, %d properties, additionalProperties=%v (type=%s)\n",
			inputPtr, propCount, hasAP, apType)
	}

	// Create entry object schema: {key: string, value: <union of all values>}
	valueSchema := unionAllObjectValues(input, env.opts)

	// DEBUG: Log what unionAllObjectValues returned
	if env.opts.EnableWarnings {
		fmt.Printf("DEBUG builtinToEntries: unionAllObjectValues returned type=%s, unconstrained=%v\n",
			getType(valueSchema), isUnconstrainedSchema(valueSchema))
	}

	// If unionAllObjectValues returned an unconstrained schema (Top), it means
	// the object had no properties or additionalProperties defined (empty object {}).
	// to_entries on an empty object produces an empty array (no entries).
	if isUnconstrainedSchema(valueSchema) {
		if env.opts.EnableWarnings {
			fmt.Printf("DEBUG builtinToEntries: unconstrained object -> returning EMPTY array (maxItems=0)\n")
		}
		// Return empty array - no entries, so maxItems=0 and Items=nil
		// Call ArrayType(nil) which creates an empty array
		return []*oas3.Schema{ArrayType(nil)}, nil
	}

	if env.opts.EnableWarnings {
		fmt.Printf("DEBUG builtinToEntries: valueSchema is constrained (type=%s), building entry object\n", getType(valueSchema))
	}

	entrySchema := BuildObject(map[string]*oas3.Schema{
		"key":   StringType(),
		"value": valueSchema,
	}, []string{"key", "value"})

	result := ArrayType(entrySchema)
	if env.opts.EnableWarnings {
		resultIsEmpty := result.MaxItems != nil && *result.MaxItems == 0
		resultHasItems := result.Items != nil && result.Items.Left != nil
		var itemType string
		if resultHasItems {
			itemType = getType(result.Items.Left)
		}
		fmt.Printf("DEBUG builtinToEntries: returning array - empty=%v, hasItems=%v, itemType=%s\n",
			resultIsEmpty, resultHasItems, itemType)
	}

	return []*oas3.Schema{result}, nil
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

// builtinSub implements sub(pattern; replacement; flags?) - regex replacement (first match)
func builtinSub(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	if len(args) < 2 {
		return []*oas3.Schema{StringType()}, nil
	}

	// Const folding: if input, pattern, and replacement are all const
	if inputStr, ok := extractConstString(input); ok {
		if patternStr, ok := extractConstString(args[0]); ok {
			if replStr, ok := extractConstString(args[1]); ok {
				re, err := regexp.Compile(patternStr)
				if err != nil {
					// Invalid regex - return string (conservative)
					return []*oas3.Schema{StringType()}, nil
				}
				result := re.ReplaceAllString(inputStr, replStr)
				return []*oas3.Schema{ConstString(result)}, nil
			}
		}
	}

	// Conservative abstract semantics: return string
	return []*oas3.Schema{StringType()}, nil
}

// builtinIndexOp implements _index(index) for array/string indexing with slicing support
func builtinIndexOp(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) == 0 {
		return []*oas3.Schema{Top()}, nil
	}

	indexArg := args[0]

	// Check if this is a slice operation (index is an object with "start" and/or "end")
	if MightBeObject(indexArg) {
		// Slice operation: array[start:end] or string[start:end]
		if MightBeArray(input) {
			var elemType *oas3.Schema
			if input.Items != nil && input.Items.Left != nil {
				elemType = input.Items.Left
			} else {
				elemType = Top()
			}
			return []*oas3.Schema{ArrayType(elemType)}, nil
		}
		if MightBeString(input) {
			return []*oas3.Schema{StringType()}, nil
		}
		return []*oas3.Schema{ConstNull()}, nil
	}

	// Single index operation
	// Array indexing
	if MightBeArray(input) {
		var elemType *oas3.Schema
		if input.Items != nil && input.Items.Left != nil {
			elemType = input.Items.Left
		} else if input.PrefixItems != nil && len(input.PrefixItems) > 0 {
			// Tuple array - try to get specific element if index is const
			if idxVal, ok := extractConstValue(indexArg); ok {
				if idxFloat, ok := idxVal.(float64); ok {
					i := int(idxFloat)
					if i >= 0 && i < len(input.PrefixItems) && input.PrefixItems[i].Left != nil {
						// Found exact element
						return []*oas3.Schema{input.PrefixItems[i].Left}, nil
					}
				}
			}
			// Fall back to union of all tuple elements
			elemSchemas := make([]*oas3.Schema, 0, len(input.PrefixItems))
			for _, item := range input.PrefixItems {
				if item.Left != nil {
					elemSchemas = append(elemSchemas, item.Left)
				}
			}
			if len(elemSchemas) > 0 {
				elemType = Union(elemSchemas, env.opts)
			} else {
				elemType = Top()
			}
		} else {
			elemType = Top()
		}

		// Check if index is provably in-bounds
		mustBePresent := false
		if idxVal, ok := extractConstValue(indexArg); ok {
			if idxFloat, ok := idxVal.(float64); ok {
				idx := int(idxFloat)
				if idx >= 0 {
					// Check minItems constraint
					if input.MinItems != nil && idx < int(*input.MinItems) {
						mustBePresent = true
					}
					// Check prefixItems length (for tuple arrays)
					if len(input.PrefixItems) > 0 && idx < len(input.PrefixItems) {
						mustBePresent = true
					}
				}
			}
		}

		// Return element type only if must be present, otherwise element type | null
		if mustBePresent {
			return []*oas3.Schema{elemType}, nil
		}
		return []*oas3.Schema{Union([]*oas3.Schema{elemType, ConstNull()}, env.opts)}, nil
	}

	// String indexing - returns single character string or null
	if MightBeString(input) {
		// Const folding: if both input and index are const
		if inputStr, ok := extractConstString(input); ok {
			if idxVal, ok := extractConstValue(indexArg); ok {
				if idxFloat, ok := idxVal.(float64); ok {
					i := int(idxFloat)
					if i >= 0 && i < len(inputStr) {
						return []*oas3.Schema{ConstString(string(inputStr[i]))}, nil
					}
					// Out of bounds
					return []*oas3.Schema{ConstNull()}, nil
				}
			}
		}

		// Conservative: return string (single char) | null
		result := StringType()
		// Single character string
		return []*oas3.Schema{Union([]*oas3.Schema{result, ConstNull()}, env.opts)}, nil
	}

	// Neither array nor string
	return []*oas3.Schema{Union([]*oas3.Schema{Top(), ConstNull()}, env.opts)}, nil
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
	if env != nil && env.opts.EnableWarnings {
		fmt.Printf("DEBUG builtinSetpath: CALLED with %d args\n", len(args))
	}

	if len(args) < 2 {
		return []*oas3.Schema{input}, nil
	}

	pathArg := args[0]
	valueArg := args[1]

	// Early dynamic-string key detection for tuple paths: [ non-const string ]
	if env != nil && env.opts.EnableWarnings && MightBeArray(pathArg) {
		hasPrefixItems := pathArg.PrefixItems != nil
		prefixLen := 0
		if hasPrefixItems {
			prefixLen = len(pathArg.PrefixItems)
		}
		fmt.Printf("DEBUG builtinSetpath: pathArg is array, hasPrefixItems=%v, prefixLen=%d\n", hasPrefixItems, prefixLen)
	}

	if MightBeObject(input) && MightBeArray(pathArg) && pathArg.PrefixItems != nil && len(pathArg.PrefixItems) == 1 {
		if seg := pathArg.PrefixItems[0]; seg != nil && seg.Left != nil {
			if env != nil && env.opts.EnableWarnings {
				fmt.Printf("DEBUG builtinSetpath: checking seg.Left type=%s, MightBeString=%v\n", getType(seg.Left), MightBeString(seg.Left))
			}
			if MightBeString(seg.Left) {
				constVal, isConst := extractConstString(seg.Left)
				if env != nil && env.opts.EnableWarnings {
					fmt.Printf("DEBUG builtinSetpath: extractConstString returned '%s', isConst=%v\n", constVal, isConst)
				}
				if !isConst {
					if env != nil && env.opts.EnableWarnings {
						fmt.Printf("DEBUG builtinSetpath: tuple[0] is non-const string -> dynamic key; updating additionalProperties\n")
					}
					return []*oas3.Schema{setDynamicProperty(input, valueArg, env.opts)}, nil
				}
			}
		}
	}

	paths := extractPathsFromSchema(pathArg)
	if env != nil && env.opts.EnableWarnings {
		pathType := getType(pathArg)
		fmt.Printf("DEBUG builtinSetpath: extracted %d paths from pathArg (type=%s)\n", len(paths), pathType)
	}

	if len(paths) == 0 {
		// DEBUG: Log when setpath no-ops due to non-const path
		if env != nil && env.opts.EnableWarnings {
			fmt.Printf("DEBUG builtinSetpath: no const paths extracted, checking for wildcard case\n")
		}

		// HANDLE EMPTY-PATH SENTINEL: Some upstream builders collapse non-const segments to an "empty array" (maxItems=0).
		// Treat this as a dynamic string-key update on objects so reduce .[] as $c ({}; .[$c.name] = $c.value) can proceed.
		if MightBeArray(pathArg) && pathArg.MaxItems != nil && *pathArg.MaxItems == 0 && MightBeObject(input) {
			if env != nil && env.opts.EnableWarnings {
				fmt.Printf("DEBUG builtinSetpath: empty path tuple treated as dynamic string key; updating additionalProperties\n")
				fmt.Printf("DEBUG builtinSetpath: calling setDynamicProperty now...\n")
			}
			result := setDynamicProperty(input, valueArg, env.opts)
			if env != nil && env.opts.EnableWarnings {
				fmt.Printf("DEBUG builtinSetpath: setDynamicProperty returned, hasAP=%v\n",
					result.AdditionalProperties != nil && result.AdditionalProperties.Left != nil)
			}
			return []*oas3.Schema{result}, nil
		}

		// HANDLE WILDCARD CASE: If pathArg is an array with non-const string items,
		// treat it as setting a dynamic object key by updating additionalProperties.
		if MightBeArray(pathArg) && pathArg.Items != nil && pathArg.Items.Left != nil {
			pathItems := pathArg.Items.Left
			// Check if this is a path array with string-typed items
			if MightBeString(pathItems) && MightBeObject(input) {
				if env != nil && env.opts.EnableWarnings {
					fmt.Printf("DEBUG builtinSetpath: detected wildcard string key pattern (non-empty), updating additionalProperties\n")
				}
				return []*oas3.Schema{setDynamicProperty(input, valueArg, env.opts)}, nil
			}
		}

		// No paths and not a wildcard pattern - return input unchanged
		return []*oas3.Schema{input}, nil
	}

	// Set value at path (only handle single path for now)
	// Take a structural snapshot so we can detect no-op static application
	beforeFP := schemaFingerprint(input)
	result := input
	for _, path := range paths {
		result = setPathInSchema(result, path, valueArg, env.opts)
	}

	// Static set done; check if it actually changed anything
	afterFP := schemaFingerprint(result)
	if afterFP == beforeFP && MightBeObject(input) {
		if env != nil && env.opts.EnableWarnings {
			fmt.Printf("DEBUG builtinSetpath: static set had no effect; treating path as dynamic string key and updating additionalProperties\n")
		}
		return []*oas3.Schema{setDynamicProperty(input, valueArg, env.opts)}, nil
	}

	// Heuristic: single-string tuple into a "fresh" object is almost always a dynamic key (e.g., reduce .[] as $c ({}; .[$c.name] = $c.value))
	// Check if INPUT was fresh (before static set), not result
	if MightBeObject(input) && MightBeArray(pathArg) && pathArg.PrefixItems != nil && len(pathArg.PrefixItems) == 1 {
		if seg := pathArg.PrefixItems[0]; seg != nil && seg.Left != nil && MightBeString(seg.Left) {
			inputPropCount := 0
			if input.Properties != nil {
				inputPropCount = input.Properties.Len()
			}
			inputHasAP := input.AdditionalProperties != nil && input.AdditionalProperties.Left != nil

			// Treat a fresh accumulator {} (no properties, no AP before static set) as dynamic-key target
			if inputPropCount == 0 && !inputHasAP {
				if env != nil && env.opts.EnableWarnings {
					fmt.Printf("DEBUG builtinSetpath: widening additionalProperties for single-string path into fresh object; value type=%s\n", getType(valueArg))
				}
				// Preserve accumulator pointer identity for fresh objects
				// This ensures the original accumulator pointer gets AP, not just the cloned result
				widened := setDynamicProperty(input, valueArg, env.opts)
				return []*oas3.Schema{widened}, nil
			}
		}
	}

	return []*oas3.Schema{result}, nil
}

// setDynamicProperty updates an object schema to allow setting properties with dynamic (non-const) keys.
// This is used for patterns like .[$variable] = value where the key isn't known at compile time.
// It updates additionalProperties to union with the new value type.
func setDynamicProperty(obj *oas3.Schema, value *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	fmt.Printf("DEBUG setDynamicProperty: CALLED with value type=%s, obj type=%s\n", getType(value), getType(obj))

	if obj == nil {
		// No input object - create one with additionalProperties
		fmt.Printf("DEBUG setDynamicProperty: creating new object with additionalProperties\n")
		result := &oas3.Schema{
			Type:                 oas3.NewTypeFromString(oas3.SchemaTypeObject),
			AdditionalProperties: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](value),
		}
		return result
	}

	// Prefer in-place update so reduce keeps the same accumulator pointer
	// This ensures the reduce accumulator variable gets the AP update
	result := obj
	objPtr := fmt.Sprintf("%p", obj)
	fmt.Printf("DEBUG setDynamicProperty: modifying IN-PLACE ptr=%s\n", objPtr)

	// Get existing additionalProperties
	var existingAP *oas3.Schema
	if result.AdditionalProperties != nil && result.AdditionalProperties.Left != nil {
		existingAP = result.AdditionalProperties.Left
	}

	// Union existing additionalProperties with new value type
	var newAP *oas3.Schema
	if existingAP != nil {
		fmt.Printf("DEBUG setDynamicProperty: unioning existing AP (type=%s) with new value\n", getType(existingAP))
		newAP = Union([]*oas3.Schema{existingAP, value}, opts)
	} else {
		fmt.Printf("DEBUG setDynamicProperty: no existing AP, using value directly\n")
		newAP = value
	}

	result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newAP)
	fmt.Printf("DEBUG setDynamicProperty: MUTATED IN-PLACE ptr=%s, now has AP type=%s\n", objPtr, getType(newAP))
	return result
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

// extractConstValue extracts a const value from a schema if it has enum with 1 value or a const field.
func extractConstValue(schema *oas3.Schema) (any, bool) {
	if schema == nil {
		return nil, false
	}

	var node *yaml.Node

	// Check for Const field first
	if schema.Const != nil {
		node = schema.Const
	} else if schema.Enum != nil && len(schema.Enum) == 1 {
		node = schema.Enum[0]
	} else {
		return nil, false
	}
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

// ============================================================================
// BUILTINADDOP - JQ "+" WITH PROPER UNION DISTRIBUTION
// ============================================================================

// builtinAddOp implements jq "+" for JSON Schema types with:
// - Union distribution (anyOf/oneOf)
// - Nullable distribution
// - Null identity
// - Proper object merge semantics using allOf
func builtinAddOp(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	var lhs, rhs *oas3.Schema
	if len(args) == 2 {
		// Arity-2: Use args directly (compiler pushes them in order)
		lhs, rhs = args[0], args[1]
	} else if len(args) == 1 {
		// Arity-1: jq uses input as lhs and args[0] as rhs
		lhs, rhs = input, args[0]
	} else {
		// Degenerate: no args
		return []*oas3.Schema{NumberType()}, nil
	}

	opts := SchemaExecOptions{}
	if env != nil {
		opts = env.opts
	}

	result := addSchemasDistributed(lhs, rhs, opts)
	return []*oas3.Schema{result}, nil
}

// AddSchemasForTest exposes "+" semantics for tests without requiring a VM env.
func AddSchemasForTest(lhs, rhs *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	return addSchemasDistributed(lhs, rhs, opts)
}

// addSchemasDistributed performs full union/nullable distribution for "+", unions all pairwise results.
func addSchemasDistributed(lhs, rhs *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if lhs == nil && rhs == nil {
		return Bottom()
	}

	// Expand both operands into alternatives:
	// - anyOf and oneOf branches
	// - Nullable expansion (nullable: true => {non-null, null})
	altsL := explodeAlternatives(lhs)
	altsR := explodeAlternatives(rhs)

	if len(altsL) == 0 && len(altsR) == 0 {
		return Bottom()
	}
	if len(altsL) == 0 {
		altsL = []*oas3.Schema{Bottom()}
	}
	if len(altsR) == 0 {
		altsR = []*oas3.Schema{Bottom()}
	}

	results := make([]*oas3.Schema, 0, len(altsL)*len(altsR))
	for _, a := range altsL {
		for _, b := range altsR {
			p := plusPair(a, b, opts)
			if p != nil {
				results = append(results, p)
			}
		}
	}

	if len(results) == 0 {
		return Bottom()
	}
	return Union(results, opts)
}

// explodeAlternatives flattens anyOf/oneOf and expands nullable into explicit null alternative.
// Returns at least one alternative unless the input is nil, in which case returns empty.
func explodeAlternatives(s *oas3.Schema) []*oas3.Schema {
	if s == nil {
		return nil
	}

	// Gather direct union branches if present
	branches := make([]*oas3.Schema, 0, 4)
	if len(s.AnyOf) > 0 {
		for _, br := range s.AnyOf {
			if br != nil && br.Left != nil {
				branches = append(branches, br.Left)
			}
		}
	} else if len(s.OneOf) > 0 {
		for _, br := range s.OneOf {
			if br != nil && br.Left != nil {
				branches = append(branches, br.Left)
			}
		}
	}

	if len(branches) == 0 {
		// No union wrapper; start with the schema itself
		branches = []*oas3.Schema{s}
	}

	// For each branch, expand nullable into an explicit null alternative
	alts := make([]*oas3.Schema, 0, len(branches)*2)
	for _, br := range branches {
		if br == nil {
			continue
		}
		nullable := br.Nullable != nil && *br.Nullable
		if nullable {
			// non-null variant (clone without Nullable) and explicit null
			nn := cloneSchema(br)
			nn.Nullable = nil
			alts = append(alts, nn, ConstNull())
			continue
		}
		alts = append(alts, br)
	}
	return alts
}

// plusPair applies jq "+" to a single pair of non-union, possibly-const (and non-nullable) schemas.
// Handles null identity and dispatch by type.
// Returns a result schema (Top/Bottom permitted).
func plusPair(a, b *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	// Null identity (handles explicit null branches)
	if isNullSchema(a) && isNullSchema(b) {
		return ConstNull()
	}
	if isNullSchema(a) {
		return b
	}
	if isNullSchema(b) {
		return a
	}

	// Type dispatch
	lt := getType(a)
	rt := getType(b)

	// Numbers (integer | number)
	if (lt == "number" || lt == "integer") && (rt == "number" || rt == "integer") {
		return addNumericSchemas(a, b)
	}

	// Strings
	if lt == "string" && rt == "string" {
		return concatStringSchemas(a, b)
	}

	// Arrays
	if lt == "array" && rt == "array" {
		return concatArraySchemas(a, b, opts)
	}

	// Objects: use allOf merge semantics
	if lt == "object" && rt == "object" {
		return mergeObjectsForPlus(a, b)
	}

	// Mixed/incompatible: jq would error; abstract as Top
	return Top()
}

// mergeObjectsForPlus implements object "+" semantics using allOf:
// - Create allOf [a, b] and collapse it
// - This correctly handles required properties based on what's actually constructed
// - Example: `. + .` preserves original required set
// - Example: `. + {id: "x"}` makes `id` required because it's explicitly constructed
func mergeObjectsForPlus(a, b *oas3.Schema) *oas3.Schema {
	if a == nil && b == nil {
		return Bottom()
	}
	if a == nil {
		return b
	}
	if b == nil {
		return a
	}

	// Use allOf to combine the schemas, then collapse
	// This delegates to the existing MergeObjects logic which handles required correctly
	return MergeObjects(a, b, SchemaExecOptions{})
}

// builtinMinus implements - for two values.
func builtinMinus(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if len(args) < 1 {
		return []*oas3.Schema{NumberType()}, nil
	}

	var lhs, rhs *oas3.Schema
	if len(args) == 2 {
		// Some jq plans encode - as arity-2
		lhs, rhs = pickBinaryOperands(input, args[0], args[1])
	} else {
		// Arity-1: jq uses lhs as input, rhs as arg[0]
		lhs = input
		rhs = args[0]
	}

	// Safety check
	if lhs == nil || rhs == nil {
		return []*oas3.Schema{NumberType()}, nil
	}

	lType := getType(lhs)
	rType := getType(rhs)

	// Array subtraction: remove all occurrences of elements in rhs from lhs
	if lType == "array" && rType == "array" {
		return []*oas3.Schema{subtractArraySchemas(lhs, rhs, env.opts)}, nil
	}

	// Numbers (default arithmetic operation)
	return arithmeticOp(lhs, rhs, func(a, b float64) float64 { return a - b })
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
	// Check if both arrays are empty (MaxItems=0)
	// Empty array + Empty array = Empty array
	aIsEmpty := a != nil && a.MaxItems != nil && *a.MaxItems == 0
	bIsEmpty := b != nil && b.MaxItems != nil && *b.MaxItems == 0

	// DEBUG: Log when concatenating non-empty arrays
	hasNonEmpty := (a != nil && !aIsEmpty) || (b != nil && !bIsEmpty)
	if opts.EnableWarnings && hasNonEmpty {
		aType := "nil"
		if a != nil {
			if aIsEmpty {
				aType = "empty"
			} else if a.Items != nil && a.Items.Left != nil {
				itemType := getType(a.Items.Left)
				if itemType == "object" {
					aType = "array<object>"
				} else {
					aType = fmt.Sprintf("array<%s>", itemType)
				}
			} else {
				aType = "array<any>"
			}
		}
		bType := "nil"
		if b != nil {
			if bIsEmpty {
				bType = "empty"
			} else if b.Items != nil && b.Items.Left != nil {
				itemType := getType(b.Items.Left)
				if itemType == "object" {
					bType = "array<object>"
				} else {
					bType = fmt.Sprintf("array<%s>", itemType)
				}
			} else {
				bType = "array<any>"
			}
		}
		fmt.Printf("DEBUG concatArraySchemas: %s + %s\n", aType, bType)
	}

	if aIsEmpty && bIsEmpty {
		fmt.Printf("DEBUG concatArraySchemas: both empty, returning empty array\n")
		return ArrayType(Bottom()) // Empty array
	}

	items := make([]*oas3.Schema, 0, 8)
	collectArrayItemCandidates := func(arr *oas3.Schema) {
		if arr == nil {
			return
		}
		// Skip empty arrays - they contribute no items
		if arr.MaxItems != nil && *arr.MaxItems == 0 {
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

	result := ArrayType(mergedItems)

	// DEBUG: Log the result with more details
	if opts.EnableWarnings && hasNonEmpty {
		resultIsEmpty := result.MaxItems != nil && *result.MaxItems == 0
		resultHasItems := result.Items != nil && result.Items.Left != nil
		var resultItemType string
		var mergedItemsType string
		if resultHasItems {
			resultItemType = getType(result.Items.Left)
		}
		if mergedItems != nil {
			mergedItemsType = getType(mergedItems)
		} else {
			mergedItemsType = "nil"
		}
		fmt.Printf("DEBUG concatArraySchemas: mergedItems type=%s, result: empty=%v, hasItems=%v, itemType=%s\n",
			mergedItemsType, resultIsEmpty, resultHasItems, resultItemType)
		if resultIsEmpty {
			fmt.Printf("DEBUG concatArraySchemas: WARNING - produced empty array from non-empty inputs!\n")
		}
	}

	return result
}

// subtractArraySchemas subtracts array b from array a (removes all occurrences of b's elements from a).
// In jq, array subtraction removes all elements in the second array from the first.
// Since we work with schemas (not concrete values), we return an array with the same item type as lhs,
// as we cannot determine statically which elements will remain.
func subtractArraySchemas(a, b *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if a == nil {
		return ArrayType(Bottom())
	}

	// For schema analysis: array subtraction returns an array with potentially the same item types as lhs
	// We cannot narrow the type further without concrete values
	var itemType *oas3.Schema
	if a.Items != nil && a.Items.Left != nil {
		itemType = a.Items.Left
	} else if a.PrefixItems != nil && len(a.PrefixItems) > 0 {
		// For tuples, union all item types
		items := make([]*oas3.Schema, 0, len(a.PrefixItems))
		for _, item := range a.PrefixItems {
			if item.Left != nil {
				items = append(items, item.Left)
			}
		}
		if len(items) > 0 {
			itemType = Union(items, opts)
		}
	}

	if itemType == nil {
		itemType = Top()
	}

	// Return array with same item type as lhs (conservative - we can't narrow without concrete values)
	return ArrayType(itemType)
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
