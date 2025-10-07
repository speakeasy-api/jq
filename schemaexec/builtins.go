package schemaexec

import (
	"fmt"

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

	// Selection/filtering - these need special handling
	// Implemented in opcall handler
	"select": nil, // Special: handled in opcall
	"map":    nil, // Special: handled in opcall
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
