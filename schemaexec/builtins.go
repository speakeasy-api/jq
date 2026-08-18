package schemaexec

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
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
	"tonumber":   builtinToNumber,
	"tostring":   builtinToString,
	"toarray":    builtinToArray,
	"explode":    builtinExplode,
	"implode":    builtinImplode,
	"tojson":     builtinToJSON,
	"fromjson":   builtinFromJSON,
	"format":     builtinFormatString,
	"_tohtml":    builtinFormatString,
	"_touri":     builtinFormatString,
	"_tourid":    builtinFormatString,
	"_tocsv":     builtinFormatString,
	"_totsv":     builtinFormatString,
	"_tosh":      builtinFormatString,
	"_tobase64":  builtinToBase64,
	"_tobase64d": builtinFromBase64,

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
		// Single type - return const string ("null" joins for nullable)
		if input.Nullable != nil && *input.Nullable {
			return []*oas3.Schema{ConstString(string(types[0])), ConstString("null")}, nil
		}
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

// builtinKeys returns an array of string keys (or indices for arrays).
func builtinKeys(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	result := distributeBuiltinInput(input, env, func(branch *oas3.Schema) *oas3.Schema {
		return keysBranch(branch, env)
	})
	return []*oas3.Schema{result}, nil
}

func keysBranch(input *oas3.Schema, env *schemaEnv) *oas3.Schema {
	// jq: keys on an ARRAY yields the index list [0, 1, ...].
	if getType(input) == "array" {
		idx := IntegerType()
		zero := 0.0
		idx.Minimum = &zero
		return ArrayType(idx)
	}
	if getType(input) == "" {
		return env.NewTopWithCause("keys receiver type is unknown")
	}
	if getType(input) != "object" {
		return Bottom()
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

	// patternProperties admit arbitrary matching keys: the declared-key enum
	// would under-enumerate.
	if input.PatternProperties != nil && input.PatternProperties.Len() > 0 {
		itemSchema = StringType()
	}

	// If additionalProperties allowed (schema or boolean true), or the object
	// is open (absent AP under raw semantics), arbitrary keys are possible.
	if input.AdditionalProperties != nil {
		if input.AdditionalProperties.Left != nil ||
			(input.AdditionalProperties.Right != nil && *input.AdditionalProperties.Right) {
			itemSchema = StringType()
		}
	} else if env.opts.Semantics == SchemaSemanticsRaw {
		itemSchema = StringType()
	}

	return ArrayType(itemSchema)
}

// builtinValues returns an array of all values.
func builtinValues(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	result := distributeBuiltinInput(input, env, func(branch *oas3.Schema) *oas3.Schema {
		switch getType(branch) {
		case "object":
			return ArrayType(unionAllObjectValues(branch, env.opts))
		case "array":
			return branch
		case "":
			return env.NewTopWithCause("values receiver type is unknown")
		default:
			return Bottom()
		}
	})
	return []*oas3.Schema{result}, nil
}

// builtinHas checks if object has a property.
func builtinHas(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	result := distributeBuiltinInput(input, env, func(branch *oas3.Schema) *oas3.Schema {
		return hasBranch(branch, args, env)
	})
	return []*oas3.Schema{result}, nil
}

func hasBranch(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) *oas3.Schema {
	if len(args) == 0 {
		return BoolType()
	}
	if getType(input) == "" {
		return env.NewTopWithCause("has receiver type is unknown")
	}
	if getType(input) == "array" {
		if getType(args[0]) == "integer" || getType(args[0]) == "number" || isTopSchema(args[0]) {
			return BoolType()
		}
		return Bottom()
	}
	if getType(input) != "object" {
		return Bottom()
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
						return ConstBool(true)
					}
				}
				// Exists but optional - could be true or false
				return BoolType()
			}
		}

		// patternProperties: a matching pattern admits the key; an
		// unparseable pattern prevents proving absence.
		if input.PatternProperties != nil && input.PatternProperties.Len() > 0 {
			for pattern := range input.PatternProperties.All() {
				re, err := regexp.Compile(pattern)
				if err != nil || re.MatchString(keyStr) {
					return BoolType()
				}
			}
		}

		// Check additionalProperties: a schema-valued AP or explicit
		// additionalProperties: true means the key may exist.
		if input.AdditionalProperties != nil {
			if input.AdditionalProperties.Left != nil ||
				(input.AdditionalProperties.Right != nil && *input.AdditionalProperties.Right) {
				return BoolType()
			}
			// additionalProperties: false — definitely doesn't exist
			return ConstBool(false)
		}

		// Absent additionalProperties: closed world under Speakeasy
		// semantics (key definitely absent), open under raw semantics.
		if env.opts.Semantics == SchemaSemanticsRaw {
			return BoolType()
		}
		return ConstBool(false)
	}

	// Unknown key - could be true or false
	return BoolType()
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

func builtinExplode(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return []*oas3.Schema{ArrayType(IntegerType())}, nil
}

func builtinImplode(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return []*oas3.Schema{StringType()}, nil
}

func builtinToJSON(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if value, ok := extractConstValue(input); ok {
		if encoded, err := json.Marshal(value); err == nil {
			return []*oas3.Schema{ConstString(string(encoded))}, nil
		}
	}
	return []*oas3.Schema{StringType()}, nil
}

func builtinFromJSON(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return []*oas3.Schema{Top()}, nil
}

func builtinFormatString(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return []*oas3.Schema{StringType()}, nil
}

func builtinToBase64(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if value, ok := extractConstString(input); ok {
		return []*oas3.Schema{ConstString(base64.StdEncoding.EncodeToString([]byte(value)))}, nil
	}
	return []*oas3.Schema{StringType()}, nil
}

func builtinFromBase64(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if value, ok := extractConstString(input); ok {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err == nil {
			return []*oas3.Schema{ConstString(string(decoded))}, nil
		}
	}
	return []*oas3.Schema{StringType()}, nil
}

// builtinToArray converts to array.
func builtinToArray(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	result := distributeBuiltinInput(input, env, func(branch *oas3.Schema) *oas3.Schema {
		if getType(branch) == "array" {
			return branch
		}
		return ArrayType(branch)
	})
	return []*oas3.Schema{result}, nil
}

func distributeBuiltinInput(input *oas3.Schema, env *schemaEnv, leaf func(*oas3.Schema) *oas3.Schema) *oas3.Schema {
	return distributeBuiltinInputSeen(input, env, leaf, make(map[*oas3.Schema]bool))
}

func distributeBuiltinInputSeen(input *oas3.Schema, env *schemaEnv, leaf func(*oas3.Schema) *oas3.Schema, seen map[*oas3.Schema]bool) *oas3.Schema {
	if input == nil {
		return Bottom()
	}
	if seen[input] {
		return env.NewTopWithCause("builtin receiver has a recursive union")
	}
	if branches, ok := disjunctiveBranches(input); ok {
		seen[input] = true
		results := make([]*oas3.Schema, 0, len(branches))
		for _, branch := range branches {
			results = append(results, distributeBuiltinInputSeen(branch, env, leaf, seen))
		}
		delete(seen, input)
		return Union(results, env.opts)
	}
	if input.Nullable != nil && *input.Nullable {
		nonNull := cloneSchema(input)
		nonNull.Nullable = nil
		return Union([]*oas3.Schema{
			distributeBuiltinInputSeen(nonNull, env, leaf, seen),
			leaf(ConstNull()),
		}, env.opts)
	}
	return leaf(input)
}

func distributeArrayBuiltin(input *oas3.Schema, env *schemaEnv, transform func(*oas3.Schema) *oas3.Schema) []*oas3.Schema {
	result := distributeBuiltinInput(input, env, func(branch *oas3.Schema) *oas3.Schema {
		if isTopSchema(branch) {
			return env.NewTopWithCause("array builtin receiver type is unknown")
		}
		typ := getType(branch)
		if getTypeExplicit(branch) == "" && typ != "array" {
			return env.NewTopWithCause("array builtin receiver type is unknown")
		}
		if typ == "" {
			return env.NewTopWithCause("array builtin receiver type is unknown")
		}
		if typ != "array" {
			return Bottom()
		}
		return transform(branch)
	})
	return []*oas3.Schema{result}
}

// ============================================================================
// ARRAY BUILTINS
// ============================================================================

// builtinAdd sums array elements (for numbers) or concatenates.
func builtinAdd(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		return addArraySchema(array, env)
	}), nil
}

func addArraySchema(input *oas3.Schema, env *schemaEnv) *oas3.Schema {
	// jq: add over an EMPTY array yields null. Unless minItems proves the
	// array non-empty, null is a possible output.
	maybeEmpty := input.MinItems == nil || *input.MinItems == 0
	withEmptyNull := func(result *oas3.Schema) *oas3.Schema {
		if maybeEmpty {
			return Union([]*oas3.Schema{result, ConstNull()}, env.opts)
		}
		return result
	}

	items := arrayElementUnion(input, env.opts)
	itemType := getType(items)

	// add on number array -> number
	if itemType == "number" || itemType == "integer" {
		return withEmptyNull(NumberType())
	}

	// add on string array -> string
	if itemType == "string" {
		return withEmptyNull(StringType())
	}

	// add on array array -> array (concatenation)
	if itemType == "array" {
		return withEmptyNull(items)
	}

	// add on object array -> object (merge)
	if itemType == "object" {
		return withEmptyNull(items)
	}

	// Unknown item type: jq's add can produce null (empty array), a number,
	// a string, an array, or an object depending on the items — narrowing to
	// number would discard valid outputs. Widen.
	return Top()
}

// builtinReverse reverses an array.
func builtinReverse(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		return eraseArrayPositions(array, env.opts)
	}), nil
}

// builtinSort sorts an array.
func builtinSort(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		return eraseArrayPositions(array, env.opts)
	}), nil
}

// builtinUnique removes duplicates.
func builtinUnique(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		result := eraseArrayPositions(array, env.opts)
		result.MinItems = nil
		return result
	}), nil
}

// builtinMinMax returns min or max element.
func builtinMinMax(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		item := arrayElementUnion(array, env.opts)
		if item == nil {
			return ConstNull()
		}
		if array.MinItems == nil || *array.MinItems == 0 {
			return Union([]*oas3.Schema{item, ConstNull()}, env.opts)
		}
		return item
	}), nil
}

// ============================================================================
// OBJECT BUILTINS
// ============================================================================

// builtinToEntries converts {a:1, b:2} to [{key:"a", value:1}, {key:"b", value:2}]
func builtinToEntries(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	result := distributeBuiltinInput(input, env, func(branch *oas3.Schema) *oas3.Schema {
		return toEntriesBranch(branch, env)
	})
	return []*oas3.Schema{result}, nil
}

func toEntriesBranch(input *oas3.Schema, env *schemaEnv) *oas3.Schema {
	// jq: to_entries on an ARRAY yields [{key: index, value: item}, ...].
	if getType(input) == "array" {
		idx := IntegerType()
		zero := 0.0
		idx.Minimum = &zero
		itemVal := arrayElementUnion(input, env.opts)
		if itemVal == nil {
			return ArrayType(Bottom())
		}
		entry := BuildObject(map[string]*oas3.Schema{
			"key":   idx,
			"value": itemVal,
		}, []string{"key", "value"})
		return ArrayType(entry)
	}
	if getType(input) == "" {
		return env.NewTopWithCause("to_entries receiver type is unknown")
	}
	if getType(input) != "object" {
		return Bottom()
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
		env.logger.Debugf("builtinToEntries: input ptr=%s, %d properties, additionalProperties=%v (type=%s)",
			inputPtr, propCount, hasAP, apType)
	}

	// Create entry object schema: {key: string, value: <union of all values>}
	valueSchema := unionAllObjectValues(input, env.opts)

	// DEBUG: Log what unionAllObjectValues returned
	if env.opts.EnableWarnings {
		env.logger.Debugf("builtinToEntries: unionAllObjectValues returned type=%s, unconstrained=%v",
			getType(valueSchema), isUnconstrainedSchema(valueSchema))
	}

	// Distinguish "provably NO entries" from "entries of UNKNOWN value type":
	// unionAllObjectValues returns Top for both an open object (AP true /
	// raw-mode absent AP) and genuinely unknown values, so the emptiness
	// decision must come from object closure, not from the value union.
	noDeclared := (input.Properties == nil || input.Properties.Len() == 0) &&
		(input.PatternProperties == nil || input.PatternProperties.Len() == 0)
	apForbids := input.AdditionalProperties == nil ||
		(input.AdditionalProperties.Right != nil && !*input.AdditionalProperties.Right)
	openWorld := env.opts.Semantics == SchemaSemanticsRaw && input.AdditionalProperties == nil
	if noDeclared && apForbids && !openWorld {
		if env.opts.EnableWarnings {
			env.logger.Debugf("builtinToEntries: provably empty object -> empty array")
		}
		return ArrayType(nil)
	}

	if env.opts.EnableWarnings {
		env.logger.Debugf("builtinToEntries: valueSchema is constrained (type=%s), building entry object", getType(valueSchema))
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
		env.logger.Debugf("builtinToEntries: returning array - empty=%v, hasItems=%v, itemType=%s",
			resultIsEmpty, resultHasItems, itemType)
	}

	return result
}

// builtinFromEntries converts [{key:"a", value:1}] to {a:1}
func builtinFromEntries(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		return fromEntriesArray(array, env.opts)
	}), nil
}

func fromEntriesArray(input *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if input.MaxItems != nil && *input.MaxItems == 0 {
		return ObjectType()
	}

	type entryCandidate struct {
		schema   *oas3.Schema
		required bool
	}
	entries := make([]entryCandidate, 0, len(input.PrefixItems)+1)
	for i, wrapper := range input.PrefixItems {
		entry, ok := derefJSONSchema(collapseContextForOptions(opts), wrapper)
		if !ok {
			entry = Top()
		}
		entries = append(entries, entryCandidate{
			schema:   entry,
			required: input.MinItems != nil && int64(i) < *input.MinItems,
		})
	}
	if item, ok := derefJSONSchema(collapseContextForOptions(opts), input.Items); ok && item != nil {
		entries = append(entries, entryCandidate{schema: item})
	}
	if len(entries) == 0 {
		return OpenObjectType(Top())
	}

	props := make(map[string]*oas3.Schema)
	required := make(map[string]bool)
	unknownValues := make([]*oas3.Schema, 0)
	for _, candidate := range entries {
		key, value, ok := entryKeyValue(candidate.schema, opts)
		if !ok {
			unknownValues = append(unknownValues, Top())
			continue
		}
		if key == "" {
			unknownValues = append(unknownValues, value)
			continue
		}
		if old := props[key]; old != nil {
			props[key] = Union([]*oas3.Schema{old, value}, opts)
		} else {
			props[key] = value
		}
		if candidate.required {
			required[key] = true
		}
	}
	requiredKeys := make([]string, 0, len(required))
	for key := range required {
		requiredKeys = append(requiredKeys, key)
	}
	sort.Strings(requiredKeys)
	result := BuildObject(props, requiredKeys)
	if len(unknownValues) > 0 {
		result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Union(unknownValues, opts))
	}
	return result
}

func entryKeyValue(entry *oas3.Schema, opts SchemaExecOptions) (string, *oas3.Schema, bool) {
	if entry == nil || isTopSchema(entry) {
		return "", Top(), false
	}
	if getType(entry) != "object" || entry.Properties == nil {
		return "", nil, false
	}
	var keySchema, valueSchema *oas3.Schema
	for _, name := range []string{"key", "Key", "name", "Name"} {
		if wrapper, ok := entry.Properties.Get(name); ok {
			keySchema, _ = derefJSONSchema(collapseContextForOptions(opts), wrapper)
			if keySchema != nil {
				break
			}
		}
	}
	for _, name := range []string{"value", "Value"} {
		if wrapper, ok := entry.Properties.Get(name); ok {
			valueSchema, _ = derefJSONSchema(collapseContextForOptions(opts), wrapper)
			if valueSchema != nil {
				break
			}
		}
	}
	if valueSchema == nil {
		valueSchema = Top()
	}
	key, constant := extractConstString(keySchema)
	if !constant {
		return "", valueSchema, true
	}
	return key, valueSchema, true
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
	return []*oas3.Schema{OpenObjectType(Top())}, nil
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
		return []*oas3.Schema{ConstString(asciiDowncase(inputStr))}, nil
	}

	// Enum preservation
	if len(input.Enum) > 0 && len(input.Enum) <= env.opts.EnumLimit {
		newEnum := make([]*yaml.Node, len(input.Enum))
		for i, node := range input.Enum {
			if node.Kind == yaml.ScalarNode {
				newEnum[i] = &yaml.Node{
					Kind:  yaml.ScalarNode,
					Value: asciiDowncase(node.Value),
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
		return []*oas3.Schema{ConstString(asciiUpcase(inputStr))}, nil
	}

	// Enum preservation
	if len(input.Enum) > 0 && len(input.Enum) <= env.opts.EnumLimit {
		newEnum := make([]*yaml.Node, len(input.Enum))
		for i, node := range input.Enum {
			if node.Kind == yaml.ScalarNode {
				newEnum[i] = &yaml.Node{
					Kind:  yaml.ScalarNode,
					Value: asciiUpcase(node.Value),
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

func asciiDowncase(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

func asciiUpcase(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}, s)
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
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		itemType := arrayElementUnion(array, env.opts)
		if itemType == nil {
			itemType = Top()
		}
		return ArrayType(ArrayType(itemType))
	}), nil
}

// builtinSortBy sorts array by key expression
func builtinSortBy(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		return eraseArrayPositions(array, env.opts)
	}), nil
}

// builtinUniqueBy removes duplicates by key expression
func builtinUniqueBy(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		result := eraseArrayPositions(array, env.opts)
		result.MinItems = nil
		return result
	}), nil
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
	// Determine flatten depth
	depth := -1 // -1 means fully flatten
	if len(args) > 0 {
		if depthVal, ok := extractConstValue(args[0]); ok {
			if d, ok := depthVal.(float64); ok {
				depth = int(d)
			}
		}
	}

	return distributeArrayBuiltin(input, env, func(array *oas3.Schema) *oas3.Schema {
		return flattenSchemaRecursive(array, depth, env.opts)
	}), nil
}

// flattenSchemaRecursive recursively flattens array schema
func flattenSchemaRecursive(schema *oas3.Schema, depth int, opts SchemaExecOptions) *oas3.Schema {
	if depth == 0 || getType(schema) != "array" {
		return schema
	}

	if schema.MaxItems != nil && *schema.MaxItems == 0 {
		return ArrayType(Bottom())
	}

	// Collect all item schemas
	items := make([]*oas3.Schema, 0)

	// Add prefixItems
	if schema.PrefixItems != nil {
		for _, item := range schema.PrefixItems {
			if left := resolvedLeft(item); left != nil {
				items = append(items, left)
			}
		}
	}

	// Add items schema
	if schema.Items != nil {
		if item, ok := derefJSONSchema(collapseContextForOptions(opts), schema.Items); !ok {
			items = append(items, Top())
		} else if item != nil {
			items = append(items, item)
		}
	} else if schema.MaxItems == nil || *schema.MaxItems > int64(len(schema.PrefixItems)) {
		items = append(items, Top())
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
				if left := resolvedLeft(flattened.Items); left != nil {
					flattenedItems = append(flattenedItems, left)
				}
				if flattened.PrefixItems != nil {
					for _, pi := range flattened.PrefixItems {
						if left := resolvedLeft(pi); left != nil {
							flattenedItems = append(flattenedItems, left)
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

	global := false
	if len(args) >= 3 {
		flags, ok := extractConstString(args[2])
		if !ok || (flags != "" && flags != "g") {
			return []*oas3.Schema{StringType()}, nil
		}
		global = flags == "g"
	}

	// Const folding: if input, pattern, and replacement are all const.
	if inputStr, ok := extractConstString(input); ok {
		if patternStr, ok := extractConstString(args[0]); ok {
			if replStr, ok := extractConstString(args[1]); ok {
				re, err := regexp.Compile(patternStr)
				if err != nil {
					// Invalid regex - return string (conservative)
					return []*oas3.Schema{StringType()}, nil
				}
				var result string
				if global {
					result = replaceAllLiteral(re, inputStr, replStr)
				} else if loc := re.FindStringIndex(inputStr); loc != nil {
					result = inputStr[:loc[0]] + replStr + inputStr[loc[1]:]
				} else {
					result = inputStr
				}
				return []*oas3.Schema{ConstString(result)}, nil
			}
		}
	}

	// Conservative abstract semantics: return string
	return []*oas3.Schema{StringType()}, nil
}

func replaceAllLiteral(re *regexp.Regexp, input, replacement string) string {
	return re.ReplaceAllStringFunc(input, func(string) string { return replacement })
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
		} else if len(input.PrefixItems) > 0 {
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
		return []*oas3.Schema{env.NewTopWithCause("getpath: missing path argument")}, nil
	}

	pathArg := args[0]
	paths := extractPathsFromSchema(pathArg)

	if len(paths) == 0 {
		// Path argument shape not modeled — absence cannot be proven.
		return []*oas3.Schema{env.NewTopWithCause("getpath: path argument not modeled; result not proven")}, nil
	}

	// For single path, navigate and return schema at that location
	// For multiple paths (union), return union of all results
	results := make([]*oas3.Schema, len(paths))
	for i, path := range paths {
		results[i] = navigatePathInSchema(input, path, env.opts)
	}

	result := results[0]
	if len(results) > 1 {
		result = Union(results, env.opts)
	}
	// Path navigation does not yet model optional properties and non-object
	// receivers faithfully; a Bottom/null-only conclusion here is not
	// trustworthy enough for hard verdicts. Downgrade to Unverifiable.
	if isBottomSchema(result) || getType(result) == "null" {
		return []*oas3.Schema{env.NewTopWithCause("getpath: path navigation not fully modeled; absence not proven")}, nil
	}
	return []*oas3.Schema{result}, nil
}

// builtinSetpath implements setpath(path; value) - set value at path.
// Wraps the core implementation with a soundness guard: setpath creates
// containers on null receivers and extends objects/arrays, behaviors the core
// does not fully model. A Bottom/null-only conclusion is therefore not
// trustworthy for hard verdicts and is downgraded to Unverifiable.
func builtinSetpath(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	results, err := builtinSetpathInner(input, args, env)
	if err != nil {
		return results, err
	}
	for i, r := range results {
		if isBottomSchema(r) || getType(r) == "null" {
			results[i] = env.NewTopWithCause("setpath: receiver/path shape not fully modeled; result not proven")
		}
	}
	return results, nil
}

func builtinSetpathInner(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if env.opts.EnableWarnings {
		env.logger.Debugf("builtinSetpath: CALLED with %d args", len(args))
	}

	if len(args) < 2 {
		return []*oas3.Schema{input}, nil
	}

	pathArg := args[0]
	valueArg := args[1]

	// Early dynamic-string key detection for tuple paths: [ non-const string ]
	if env.opts.EnableWarnings && MightBeArray(pathArg) {
		hasPrefixItems := pathArg.PrefixItems != nil
		prefixLen := 0
		if hasPrefixItems {
			prefixLen = len(pathArg.PrefixItems)
		}
		env.logger.Debugf("builtinSetpath: pathArg is array, hasPrefixItems=%v, prefixLen=%d", hasPrefixItems, prefixLen)
	}

	if MightBeObject(input) && MightBeArray(pathArg) && pathArg.PrefixItems != nil && len(pathArg.PrefixItems) == 1 {
		if seg := pathArg.PrefixItems[0]; seg != nil && seg.Left != nil {
			if env.opts.EnableWarnings {
				env.logger.Debugf("builtinSetpath: checking seg.Left type=%s, MightBeString=%v", getType(seg.Left), MightBeString(seg.Left))
			}
			if MightBeString(seg.Left) {
				constVal, isConst := extractConstString(seg.Left)
				if env.opts.EnableWarnings {
					env.logger.Debugf("builtinSetpath: extractConstString returned '%s', isConst=%v", constVal, isConst)
				}
				if !isConst {
					if env.opts.EnableWarnings {
						env.logger.Debugf("builtinSetpath: tuple[0] is non-const string -> dynamic key; updating additionalProperties")
					}
					return []*oas3.Schema{setDynamicProperty(input, valueArg, env.opts)}, nil
				}
			}
		}
	}

	paths := extractPathsFromSchema(pathArg)
	if env.opts.EnableWarnings {
		pathType := getType(pathArg)
		env.logger.Debugf("builtinSetpath: extracted %d paths from pathArg (type=%s)", len(paths), pathType)
	}

	if len(paths) == 0 {
		// DEBUG: Log when setpath no-ops due to non-const path
		if env.opts.EnableWarnings {
			env.logger.Debugf("builtinSetpath: no const paths extracted, checking for wildcard case")
		}

		// A homogeneous integer path loses positional length information in the
		// accumulator representation. Preserve the receiver's array shape but
		// widen its items rather than pretending setpath was a no-op.
		if MightBeArray(input) && MightBeArray(pathArg) {
			if pathItems := resolvedLeft(pathArg.Items); pathItems != nil && MightBeNumber(pathItems) {
				unknownWrite := env.NewTopWithCause("setpath: numeric path positions are not fully modeled")
				return []*oas3.Schema{widenArrayAfterWrite(input, unknownWrite, nil, env.opts)}, nil
			}
		}

		// HANDLE EMPTY-PATH SENTINEL: Some upstream builders collapse non-const segments to an "empty array" (maxItems=0).
		// Treat this as a dynamic string-key update on objects so reduce .[] as $c ({}; .[$c.name] = $c.value) can proceed.
		if MightBeArray(pathArg) && pathArg.MaxItems != nil && *pathArg.MaxItems == 0 && MightBeObject(input) {
			if env.opts.EnableWarnings {
				env.logger.Debugf("builtinSetpath: empty path tuple treated as dynamic string key; updating additionalProperties")
				env.logger.Debugf("builtinSetpath: calling setDynamicProperty now...")
			}
			result := setDynamicProperty(input, valueArg, env.opts)
			if env.opts.EnableWarnings {
				env.logger.Debugf("builtinSetpath: setDynamicProperty returned, hasAP=%v",
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
				if env.opts.EnableWarnings {
					env.logger.Debugf("builtinSetpath: detected wildcard string key pattern (non-empty), updating additionalProperties")
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
		if env.opts.EnableWarnings {
			env.logger.Debugf("builtinSetpath: static set had no effect; treating path as dynamic string key and updating additionalProperties")
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
				if env.opts.EnableWarnings {
					env.logger.Debugf("builtinSetpath: widening additionalProperties for single-string path into fresh object; value type=%s", getType(valueArg))
				}
				// Fresh reducer accumulators keep their executor-owned identity.
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
	if obj == nil {
		// No input object - create one with additionalProperties
		result := &oas3.Schema{
			Type:                 oas3.NewTypeFromString(oas3.SchemaTypeObject),
			AdditionalProperties: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](value),
		}
		return result
	}

	result := cloneSchema(obj)

	// Get existing additionalProperties
	var existingAP *oas3.Schema
	if result.AdditionalProperties != nil && result.AdditionalProperties.Left != nil {
		existingAP = result.AdditionalProperties.Left
	}

	// Union existing additionalProperties with new value type
	var newAP *oas3.Schema
	if existingAP != nil {
		newAP = Union([]*oas3.Schema{existingAP, value}, opts)
	} else {
		newAP = value
	}

	result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newAP)
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
	if env.opts.Semantics == SchemaSemanticsRaw {
		if rawBuiltinOperandNeedsWidening(input) {
			return []*oas3.Schema{env.NewTopWithCause("raw semantics: builtin applied to untyped schema")}, nil
		}
		for _, arg := range args {
			if rawBuiltinOperandNeedsWidening(arg) {
				return []*oas3.Schema{env.NewTopWithCause("raw semantics: builtin applied to untyped schema")}, nil
			}
		}
	}

	return fn(input, args, env)
}

func rawBuiltinOperandNeedsWidening(schema *oas3.Schema) bool {
	return schema != nil && getTypeExplicit(schema) == "" && impliedTypeOf(schema) != ""
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
	} else if len(schema.Enum) == 1 {
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

	// Gather direct union branches if present ($refs followed via resolvedLeft)
	branches := make([]*oas3.Schema, 0, 4)
	if len(s.AnyOf) > 0 {
		for _, br := range s.AnyOf {
			if left := resolvedLeft(br); left != nil {
				branches = append(branches, left)
			}
		}
	} else if len(s.OneOf) > 0 {
		for _, br := range s.OneOf {
			if left := resolvedLeft(br); left != nil {
				branches = append(branches, left)
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
		return mergeObjectsForPlus(a, b, opts)
	}

	// Mixed/incompatible: jq would error; abstract as Top
	return Top()
}

// mergeObjectsForPlus implements object "+" semantics using allOf:
// - Create allOf [a, b] and collapse it
// - This correctly handles required properties based on what's actually constructed
// - Example: `. + .` preserves original required set
// - Example: `. + {id: "x"}` makes `id` required because it's explicitly constructed
func mergeObjectsForPlus(a, b *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
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
	return MergeObjects(a, b, opts)
}

// builtinMinus implements - for two values.
func builtinMinus(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	lhs, rhs, ok := binaryOperands(input, args)
	if !ok {
		return []*oas3.Schema{env.NewTopWithCause("minus operands could not be modeled")}, nil
	}
	return []*oas3.Schema{distributeBinarySchemas(lhs, rhs, env, func(a, b *oas3.Schema) *oas3.Schema {
		switch {
		case isNumericType(getType(a)) && isNumericType(getType(b)):
			return numericBinarySchema(a, b, func(x, y float64) float64 { return x - y })
		case getType(a) == "array" && getType(b) == "array":
			return subtractArraySchemas(a, b, env.opts)
		case getType(a) == "" || getType(b) == "":
			return env.NewTopWithCause("minus operand type is unknown")
		default:
			return Bottom()
		}
	})}, nil
}

// builtinMultiply implements * for two values.
func builtinMultiply(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	lhs, rhs, ok := binaryOperands(input, args)
	if !ok {
		return []*oas3.Schema{env.NewTopWithCause("multiply operands could not be modeled")}, nil
	}
	seen := make(map[schemaPair]*oas3.Schema)
	return []*oas3.Schema{distributeBinarySchemas(lhs, rhs, env, func(a, b *oas3.Schema) *oas3.Schema {
		return multiplySchemaPair(a, b, env, seen)
	})}, nil
}

// builtinDivide implements / for two values.
func builtinDivide(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	lhs, rhs, ok := binaryOperands(input, args)
	if !ok {
		return []*oas3.Schema{env.NewTopWithCause("divide operands could not be modeled")}, nil
	}
	return []*oas3.Schema{distributeBinarySchemas(lhs, rhs, env, func(a, b *oas3.Schema) *oas3.Schema {
		switch {
		case isNumericType(getType(a)) && isNumericType(getType(b)):
			if divisor, ok := constNumber(b); ok && divisor == 0 {
				return Bottom()
			}
			return numericBinarySchema(a, b, func(x, y float64) float64 { return x / y })
		case getType(a) == "string" && getType(b) == "string":
			return splitStringSchema(a, b)
		case getType(a) == "" || getType(b) == "":
			return env.NewTopWithCause("divide operand type is unknown")
		default:
			return Bottom()
		}
	})}, nil
}

// builtinModulo implements % for two values.
func builtinModulo(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	lhs, rhs, ok := binaryOperands(input, args)
	if !ok {
		return []*oas3.Schema{env.NewTopWithCause("modulo operands could not be modeled")}, nil
	}
	return []*oas3.Schema{distributeBinarySchemas(lhs, rhs, env, func(a, b *oas3.Schema) *oas3.Schema {
		if getType(a) == "" || getType(b) == "" {
			return env.NewTopWithCause("modulo operand type is unknown")
		}
		if !isNumericType(getType(a)) || !isNumericType(getType(b)) {
			return Bottom()
		}
		if divisor, ok := constNumber(b); ok && divisor == 0 {
			return Bottom()
		}
		return numericBinarySchema(a, b, math.Mod)
	})}, nil
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

func binaryOperands(input *oas3.Schema, args []*oas3.Schema) (*oas3.Schema, *oas3.Schema, bool) {
	switch len(args) {
	case 1:
		return input, args[0], input != nil && args[0] != nil
	case 2:
		return args[0], args[1], args[0] != nil && args[1] != nil
	default:
		return nil, nil, false
	}
}

func distributeBinarySchemas(lhs, rhs *oas3.Schema, env *schemaEnv, pair func(*oas3.Schema, *oas3.Schema) *oas3.Schema) *oas3.Schema {
	left, right := explodeAlternatives(lhs), explodeAlternatives(rhs)
	results := make([]*oas3.Schema, 0, len(left)*len(right))
	for _, a := range left {
		for _, b := range right {
			if result := pair(a, b); result != nil {
				results = append(results, result)
			}
		}
	}
	if len(results) == 0 {
		return Bottom()
	}
	return Union(results, env.opts)
}

func isNumericType(typ string) bool {
	return typ == "integer" || typ == "number"
}

func constNumber(schema *oas3.Schema) (float64, bool) {
	value, ok := extractConstValue(schema)
	if !ok {
		return 0, false
	}
	number, ok := value.(float64)
	return number, ok
}

func numericBinarySchema(lhs, rhs *oas3.Schema, operation func(float64, float64) float64) *oas3.Schema {
	left, leftOK := constNumber(lhs)
	right, rightOK := constNumber(rhs)
	if leftOK && rightOK {
		return ConstNumber(operation(left, right))
	}
	return NumberType()
}

type schemaPair struct {
	left  *oas3.Schema
	right *oas3.Schema
}

func multiplySchemaPair(lhs, rhs *oas3.Schema, env *schemaEnv, seen map[schemaPair]*oas3.Schema) *oas3.Schema {
	leftType, rightType := getType(lhs), getType(rhs)
	switch {
	case isNumericType(leftType) && isNumericType(rightType):
		return numericBinarySchema(lhs, rhs, func(a, b float64) float64 { return a * b })
	case leftType == "string" && isNumericType(rightType):
		return repeatStringSchema(lhs, rhs, env.opts)
	case leftType == "object" && rightType == "object":
		return mergeObjectsForMultiply(lhs, rhs, env, seen)
	case leftType == "" || rightType == "":
		return env.NewTopWithCause("multiply operand type is unknown")
	default:
		return Bottom()
	}
}

func repeatStringSchema(value, count *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	number, countOK := constNumber(count)
	if !countOK {
		return Union([]*oas3.Schema{StringType(), ConstNull()}, opts)
	}
	if number != math.Trunc(number) {
		return Bottom()
	}
	if number < 0 {
		return ConstNull()
	}
	text, textOK := extractConstString(value)
	if !textOK {
		return StringType()
	}
	// Constant folding must not turn authored jq into an allocation attack.
	if number > 10_000 || float64(len(text))*number > 1_000_000 {
		return StringType()
	}
	return ConstString(strings.Repeat(text, int(number)))
}

func splitStringSchema(value, separator *oas3.Schema) *oas3.Schema {
	text, textOK := extractConstString(value)
	sep, sepOK := extractConstString(separator)
	if !textOK || !sepOK {
		return ArrayType(StringType())
	}
	parts := strings.Split(text, sep)
	elements := make([]*oas3.Schema, len(parts))
	for i, part := range parts {
		elements[i] = ConstString(part)
	}
	result := BuildArray(nil, elements)
	length := int64(len(elements))
	result.MinItems = &length
	result.MaxItems = &length
	result.Items = oas3.NewJSONSchemaFromBool(false)
	return result
}

func mergeObjectsForMultiply(lhs, rhs *oas3.Schema, env *schemaEnv, seen map[schemaPair]*oas3.Schema) *oas3.Schema {
	pair := schemaPair{left: lhs, right: rhs}
	if result, ok := seen[pair]; ok {
		return result
	}
	result := MergeObjects(lhs, rhs, env.opts)
	seen[pair] = result
	if result.Properties == nil || lhs.Properties == nil || rhs.Properties == nil {
		return result
	}
	result = cloneSchema(result)
	result.Properties = cloneSchemaMap(result.Properties)
	seen[pair] = result
	for key, leftProperty := range lhs.Properties.All() {
		rightProperty, ok := rhs.Properties.Get(key)
		if !ok {
			continue
		}
		leftValue, rightValue := resolvedLeft(leftProperty), resolvedLeft(rightProperty)
		if leftValue == nil || rightValue == nil {
			result.Properties.Set(key, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](
				env.NewTopWithCause("recursive object merge property could not be resolved")))
			continue
		}
		merged := recursiveMultiplyProperty(leftValue, rightValue, env, seen)
		result.Properties.Set(key, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](merged))
	}
	return result
}

func recursiveMultiplyProperty(lhs, rhs *oas3.Schema, env *schemaEnv, seen map[schemaPair]*oas3.Schema) *oas3.Schema {
	left, right := explodeAlternatives(lhs), explodeAlternatives(rhs)
	results := make([]*oas3.Schema, 0, len(left)*len(right))
	for _, a := range left {
		for _, b := range right {
			switch {
			case getType(a) == "object" && getType(b) == "object":
				results = append(results, mergeObjectsForMultiply(a, b, env, seen))
			case getType(a) == "" || getType(b) == "":
				results = append(results, env.NewTopWithCause("recursive object merge property type is unknown"))
			default:
				// jq recursively merges only object/object conflicts; otherwise
				// the right-hand property replaces the left-hand value.
				results = append(results, b)
			}
		}
	}
	return Union(results, env.opts)
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
		opts.debugf("concatArraySchemas: %s + %s", aType, bType)
	}

	if aIsEmpty && bIsEmpty {
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
		opts.debugf("concatArraySchemas: mergedItems type=%s, result: empty=%v, hasItems=%v, itemType=%s",
			mergedItemsType, resultIsEmpty, resultHasItems, resultItemType)
		if resultIsEmpty {
			opts.debugf("concatArraySchemas: WARNING - produced empty array from non-empty inputs!")
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
	} else if len(a.PrefixItems) > 0 {
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
