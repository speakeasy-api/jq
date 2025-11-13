package schemaexec

import (
	"fmt"
	"runtime/debug"
	"sort"
	"strconv"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
	"gopkg.in/yaml.v3"
)

// ============================================================================
// PHASE 1: SIMPLIFIED SCHEMA CONSTRUCTORS
// ============================================================================
// These are simplified constructors for Phase 1. Phase 2 will add more
// sophisticated schema operations.

// Top returns a schema that matches any value (union of all types).
func Top() *oas3.Schema {
	schema := &oas3.Schema{}
	// For now, return an empty schema which is permissive
	// In Phase 2, we'll properly set Type to union of all types
	return schema
}

// Bottom returns a schema that matches nothing (the "never" type).
// By convention, we use nil to represent Bottom/Never.
func Bottom() *oas3.Schema {
	return nil
}

// ConstString creates a schema for a specific string literal.
func ConstString(s string) *oas3.Schema {
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: s, Tag: "!!str"}
	schema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{node},
	}
	return schema
}

// ConstNumber creates a schema for a specific number.
func ConstNumber(n float64) *oas3.Schema {
	node := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: strconv.FormatFloat(n, 'g', -1, 64),
		Tag:   "!!float",
	}
	schema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeNumber),
		Enum: []*yaml.Node{node},
	}
	return schema
}

// ConstInteger creates a schema for a specific integer.
func ConstInteger(n int64) *oas3.Schema {
	node := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: strconv.FormatInt(n, 10),
		Tag:   "!!int",
	}
	schema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger),
		Enum: []*yaml.Node{node},
	}
	return schema
}

// ConstBool creates a schema for true or false.
func ConstBool(b bool) *oas3.Schema {
	val := "false"
	if b {
		val = "true"
	}
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: val, Tag: "!!bool"}
	schema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeBoolean),
		Enum: []*yaml.Node{node},
	}
	return schema
}

// ConstNull creates a null schema.
func ConstNull() *oas3.Schema {
	return &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeNull),
	}
}

// accessPropertyUnion performs union-aware property access with jq semantics:
// - For oneOf/anyOf, evaluate each alternative and union the results
// - For plain objects, return property schema ∪ null if the property is optional/may be absent
// - For additionalProperties, return additionalProperties ∪ null
func accessPropertyUnion(obj *oas3.Schema, name string, opts SchemaExecOptions) *oas3.Schema {
	if obj == nil {
		// Unreachable branch must not contribute a null to the union
		return Bottom()
	}
	// If union (oneOf/anyOf), map over alternatives
	alts := make([]*oas3.Schema, 0)
	if len(obj.OneOf) > 0 {
		for _, br := range obj.OneOf {
			if br != nil && br.Left != nil {
				alts = append(alts, br.Left)
			}
		}
	} else if len(obj.AnyOf) > 0 {
		for _, br := range obj.AnyOf {
			if br != nil && br.Left != nil {
				alts = append(alts, br.Left)
			}
		}
	}
	if len(alts) > 0 {
		results := make([]*oas3.Schema, 0, len(alts))
		for _, a := range alts {
			results = append(results, getPropertyWithNull(a, name, opts))
		}
		return Union(results, opts)
	}

	// Plain object
	return getPropertyWithNull(obj, name, opts)
}

// getPropertyWithNull returns property schema ∪ null for optional/maybe-absent properties.
// - If property is declared and required: return its schema
// - If declared optional: union with null
// - If not declared but additionalProperties present: additionalProperties ∪ null
// - Otherwise: null
func getPropertyWithNull(obj *oas3.Schema, name string, opts SchemaExecOptions) *oas3.Schema {
	if obj == nil {
		// Unreachable path: no value produced, do not widen with null
		return Bottom()
	}
	// Only apply to objects; for non-object types, jq property access on them will be widened elsewhere.
	// Consider object shape (properties/additionalProperties) as objects even if 'type' is omitted
	if getType(obj) != "object" && obj.Properties == nil && obj.AdditionalProperties == nil {
		return ConstNull()
	}

	// Declared property?
	if obj.Properties != nil {
		if js, ok := obj.Properties.Get(name); ok && js != nil {
			var prop *oas3.Schema
			if p, ok := derefJSONSchema(newCollapseContext(), js); ok {
				prop = p
			} else {
				prop = Top()
			}
			// Required?
			if isRequired(obj.Required, name) {
				return prop
			}
			return Union([]*oas3.Schema{prop, ConstNull()}, opts)
		}
	}

	// AdditionalProperties?
	if obj.AdditionalProperties != nil {
		if ap, ok := derefJSONSchema(newCollapseContext(), obj.AdditionalProperties); ok {
			return Union([]*oas3.Schema{ap, ConstNull()}, opts)
		}
		// If boolean flag present and false, property cannot exist → definitely null
		if obj.AdditionalProperties.Right != nil && !*obj.AdditionalProperties.Right {
			return ConstNull()
		}
		// additionalProperties: true (or unresolved ref) → could be any type or absent
		return Union([]*oas3.Schema{Top(), ConstNull()}, opts)
	}

	// Not declared and no additionalProperties ⇒ definitely missing
	return ConstNull()
}

func isRequired(req []string, name string) bool {
	for _, r := range req {
		if r == name {
			return true
		}
	}
	return false
}

// StringType creates a basic string schema (unconstrained).
func StringType() *oas3.Schema {
	return &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
	}
}

// NumberType creates a basic number schema (unconstrained).
func NumberType() *oas3.Schema {
	return &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeNumber),
	}
}

// IntegerType creates a basic integer schema (unconstrained).
func IntegerType() *oas3.Schema {
	return &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger),
	}
}

// BoolType creates a basic boolean schema.
func BoolType() *oas3.Schema {
	return &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeBoolean),
	}
}

// NullType creates a null schema.
func NullType() *oas3.Schema {
	return &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeNull),
	}
}

// ArrayType creates a basic array schema with the given items schema.
func ArrayType(items *oas3.Schema) *oas3.Schema {
	// Empty array: items is Bottom (nil)
	// Follow the same convention as buildArrayFromLiteral:
	// Set MaxItems=0 and leave Items unset (nil) to avoid invalid wrapper
	if items == nil {
		emptyArray := &oas3.Schema{
			Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
		}
		maxItems := int64(0)
		emptyArray.MaxItems = &maxItems
		return emptyArray
	}

	// Non-empty array
	schema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
	}
	// Set Items field for non-empty arrays
	// This allows us to distinguish:
	// - ArrayType(StringType()) → Items.Left = StringType() (array of strings)
	// - Unconstrained array → Items = nil (Items field not set at all)
	schema.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](items)
	return schema
}

// ObjectType creates a basic object schema (unconstrained).
func ObjectType() *oas3.Schema {
	return &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeObject),
	}
}

// ============================================================================
// BASIC OPERATIONS
// ============================================================================

// GetProperty extracts the schema for a property from an object schema.
// Simplified version for Phase 1.
func GetProperty(obj *oas3.Schema, key string, opts SchemaExecOptions) *oas3.Schema {
	if obj == nil {
		return Bottom()
	}

	// Check properties
	if obj.Properties != nil {
		if propSchema, ok := obj.Properties.Get(key); ok {
			// Dereference the property schema (handles both inline and $ref)
			if schema, ok := derefJSONSchema(newCollapseContext(), propSchema); ok {
				return schema
			}
			// Unresolved reference - widen conservatively
			return Top()
		}
	}

	// Check additionalProperties
	if obj.AdditionalProperties != nil {
		if schema, ok := derefJSONSchema(newCollapseContext(), obj.AdditionalProperties); ok {
			return schema
		}
		// Unresolved reference in additionalProperties - widen conservatively
		return Top()
	}

	// Not found - return null
	return ConstNull()
}

// Union creates a schema that matches any of the input schemas (anyOf).
// Implements proper flattening, deduplication, and widening when limits exceeded.
func Union(schemas []*oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	// DEBUG: Track AdditionalProperties in input schemas
	if opts.EnableWarnings {
		apCount := 0
		for i, s := range schemas {
			if s != nil && getType(s) == "object" && s.AdditionalProperties != nil && s.AdditionalProperties.Left != nil {
				apCount++
				if i < 3 {
					fmt.Printf("DEBUG Union: schema[%d] (ptr=%p) has AP.Left type=%s\n", i, s, getType(s.AdditionalProperties.Left))
				}
			}
		}
		if apCount > 0 {
			fmt.Printf("DEBUG Union: %d/%d objects have AP.Left set\n", apCount, len(schemas))
		}
	}
	// DEBUG: Track large unions and inspect configs property
	if opts.EnableWarnings && len(schemas) > 100 {
		fmt.Printf("DEBUG Union: processing %d input schemas\n", len(schemas))

		// Count how many have configs property and what it looks like
		objectCount := 0
		configsEmptyCount := 0
		configsNonEmptyCount := 0
		configsMissingCount := 0

		for _, s := range schemas {
			if s != nil && getType(s) == "object" {
				objectCount++
				if s.Properties != nil {
					if configsProp, ok := s.Properties.Get("configs"); ok && configsProp.GetLeft() != nil {
						configs := configsProp.GetLeft()
						if getType(configs) == "array" {
							isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
							if isEmpty {
								configsEmptyCount++
							} else {
								configsNonEmptyCount++
							}
						}
					} else {
						configsMissingCount++
					}
				} else {
					configsMissingCount++
				}
			}
		}

		if objectCount > 0 {
			fmt.Printf("DEBUG Union: of %d objects, configs: %d empty, %d non-empty, %d missing\n",
				objectCount, configsEmptyCount, configsNonEmptyCount, configsMissingCount)
		}
	}
	// DEBUG: Trace Union calls on arrays to understand empty array handling
	if opts.EnableWarnings && len(schemas) > 0 {
		allArrays := true
		hasArray := false
		for _, s := range schemas {
			if s != nil {
				if getType(s) == "array" {
					hasArray = true
				} else {
					allArrays = false
				}
			}
		}
		if hasArray && allArrays {
			fmt.Printf("DEBUG Union: merging %d arrays\n", len(schemas))
			for i, s := range schemas {
				if s == nil {
					fmt.Printf("  [%d] nil schema\n", i)
				} else {
					isEmpty := s.MaxItems != nil && *s.MaxItems == 0
					hasItems := s.Items != nil && s.Items.Left != nil
					var itemType string
					if hasItems {
						itemType = getType(s.Items.Left)
					}
					fmt.Printf("  [%d] array: empty=%v, hasItems=%v, itemType=%s\n", i, isEmpty, hasItems, itemType)
				}
			}
		}
	}

	// First pass: filter out nil/Bottom
	filtered := make([]*oas3.Schema, 0, len(schemas))
	for _, s := range schemas {
		if s != nil {
			filtered = append(filtered, s)
		}
	}

	if len(filtered) == 0 {
		return Bottom()
	}

	// Second pass: if we have multiple schemas and some are empty arrays,
	// filter out the empty arrays since they contribute nothing to the union
	if len(filtered) > 1 {
		hasNonEmptyArray := false
		hasEmptyArray := false
		for _, s := range filtered {
			if getType(s) == "array" {
				if s.MaxItems != nil && *s.MaxItems == 0 {
					hasEmptyArray = true
				} else {
					hasNonEmptyArray = true
				}
			} else {
				// Non-array schema, keep it
				hasNonEmptyArray = true
			}
		}

		// DEBUG: Show filtering decision
		if opts.EnableWarnings && hasEmptyArray {
			fmt.Printf("DEBUG Union: filtering decision - hasEmpty=%v, hasNonEmpty=%v, total=%d schemas\n",
				hasEmptyArray, hasNonEmptyArray, len(filtered))
		}

		// Only filter out empty arrays if there are non-empty alternatives
		if hasNonEmptyArray && hasEmptyArray {
			nonEmpty := make([]*oas3.Schema, 0, len(filtered))
			filteredCount := 0
			for _, s := range filtered {
				// Keep non-arrays and non-empty arrays
				if getType(s) != "array" || s.MaxItems == nil || *s.MaxItems != 0 {
					nonEmpty = append(nonEmpty, s)
				} else {
					filteredCount++
				}
			}
			if opts.EnableWarnings {
				fmt.Printf("DEBUG Union: filtered out %d empty arrays, keeping %d schemas\n", filteredCount, len(nonEmpty))
			}
			filtered = nonEmpty
		}
	}

	if len(filtered) == 0 {
		return Bottom()
	}
	if len(filtered) == 1 {
		return filtered[0]
	}

	// Flatten nested anyOf schemas
	flattened := make([]*oas3.Schema, 0, len(filtered)*2)
	for _, s := range filtered {
		if len(s.AnyOf) > 0 {
			// Extract nested anyOf branches
			for _, branch := range s.AnyOf {
				if branch.Left != nil {
					flattened = append(flattened, branch.Left)
				}
			}
		} else {
			flattened = append(flattened, s)
		}
	}

	// DEBUG: Count configs arrays before dedup
	if opts.EnableWarnings && len(schemas) > 100 {
		emptyConfigsCount := 0
		nonEmptyConfigsCount := 0
		fingerprints := make(map[string]int) // Track unique fingerprints
		for idx, s := range flattened {
			if s != nil && getType(s) == "object" && s.Properties != nil {
				if configsProp, ok := s.Properties.Get("configs"); ok && configsProp.GetLeft() != nil {
					configs := configsProp.GetLeft()
					if getType(configs) == "array" {
						isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
						hasItems := configs.Items != nil && configs.Items.Left != nil

						// Get fingerprint for this configs array
						fp := schemaFingerprint(configs)
						fingerprints[fp]++

						if isEmpty {
							emptyConfigsCount++
							if idx < 2 {
								fmt.Printf("DEBUG Union: flattened[%d] EMPTY configs: fp=%s, hasItems=%v\n",
									idx, fp[:16], hasItems)
							}
						} else {
							nonEmptyConfigsCount++
							if idx < 2 {
								var itemType string
								if hasItems {
									itemType = getType(configs.Items.Left)
								}
								fmt.Printf("DEBUG Union: flattened[%d] NON-EMPTY configs: fp=%s, hasItems=%v, itemType=%s\n",
									idx, fp[:16], hasItems, itemType)
							}
						}
					}
				}
			}
		}
		if emptyConfigsCount > 0 || nonEmptyConfigsCount > 0 {
			fmt.Printf("DEBUG Union: BEFORE dedup - empty configs: %d, non-empty configs: %d, unique fingerprints: %d\n",
				emptyConfigsCount, nonEmptyConfigsCount, len(fingerprints))
		}
	}

	// Deduplicate
	deduped := deduplicateSchemas(flattened)

	// DEBUG: Track deduplication and check configs arrays AFTER dedup
	if opts.EnableWarnings && len(schemas) > 100 {
		fmt.Printf("DEBUG Union: after dedup, have %d schemas (from %d flattened)\n", len(deduped), len(flattened))

		emptyConfigsCount := 0
		nonEmptyConfigsCount := 0
		for idx, s := range deduped {
			if s != nil && getType(s) == "object" && s.Properties != nil {
				if configsProp, ok := s.Properties.Get("configs"); ok && configsProp.GetLeft() != nil {
					configs := configsProp.GetLeft()
					if getType(configs) == "array" {
						isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
						hasItems := configs.Items != nil && configs.Items.Left != nil
						var itemType string
						if hasItems {
							itemType = getType(configs.Items.Left)
						}
						if isEmpty {
							emptyConfigsCount++
							if idx < 3 {
								fmt.Printf("DEBUG Union: deduped[%d] has EMPTY configs (maxItems=0, hasItems=%v, itemType=%s)\n",
									idx, hasItems, itemType)
							}
						} else {
							nonEmptyConfigsCount++
							if idx < 3 {
								fmt.Printf("DEBUG Union: deduped[%d] has NON-EMPTY configs (maxItems=nil, hasItems=%v, itemType=%s)\n",
									idx, hasItems, itemType)
							}
						}
					}
				}
			}
		}
		if emptyConfigsCount > 0 || nonEmptyConfigsCount > 0 {
			fmt.Printf("DEBUG Union: AFTER dedup - empty configs: %d, non-empty configs: %d\n",
				emptyConfigsCount, nonEmptyConfigsCount)
		}
	}

	// EARLY MERGE: preserve shape/AP info before subsumption removes specifics
	// Try merging objects first to preserve AdditionalProperties
	if merged := tryMergeObjects(deduped, opts); merged != nil {
		if opts.EnableWarnings {
			fmt.Printf("DEBUG Union: early object merge succeeded, returning merged object\n")
		}
		return merged
	}

	// Remove subsumed schemas (e.g., {type: number, enum: [0]} ⊆ {type: number})
	collapsed := removeSubsumedSchemas(deduped)

	// DEBUG: Track progression through Union stages
	if opts.EnableWarnings && len(schemas) > 100 {
		fmt.Printf("DEBUG Union: after collapse, have %d schemas (from %d initial)\n", len(collapsed), len(schemas))
	}

	// If only one unique schema after dedup and collapse, return it directly
	if len(collapsed) == 1 {
		// DEBUG: Show what the final collapsed schema looks like
		if opts.EnableWarnings && len(schemas) > 100 {
			finalSchema := collapsed[0]
			fmt.Printf("DEBUG Union: collapsed to single schema of type=%s\n", getType(finalSchema))
			if getType(finalSchema) == "object" && finalSchema.Properties != nil {
				if configsProp, ok := finalSchema.Properties.Get("configs"); ok && configsProp.GetLeft() != nil {
					configs := configsProp.GetLeft()
					if getType(configs) == "array" {
						isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
						hasItems := configs.Items != nil && configs.Items.Left != nil
						var itemType string
						if hasItems {
							itemType = getType(configs.Items.Left)
						}
						fmt.Printf("DEBUG Union: final 'configs' property: empty=%v, hasItems=%v, itemType=%s\n",
							isEmpty, hasItems, itemType)
					}
				}
			}
		}
		return collapsed[0]
	}

	// NULLABLE OPTIMIZATION: Convert [type, null] to {type: T, nullable: true}
	// This simplifies anyOf: [{type: string}, {type: null}] → {type: string, nullable: true}
	if len(collapsed) == 2 {
		var nullSchema *oas3.Schema
		var otherSchema *oas3.Schema

		for _, s := range collapsed {
			if getType(s) == "null" {
				nullSchema = s
			} else if getType(s) != "" {
				otherSchema = s
			}
		}

		// If we have exactly one null and one non-null typed schema, merge to nullable
		if nullSchema != nil && otherSchema != nil {
			// Clone the non-null schema to avoid mutation
			result := cloneSchema(otherSchema)
			nullable := true
			result.Nullable = &nullable
			return result
		}
	}

	// Try to merge compatible arrays into a single array
	if merged := tryMergeArrays(collapsed, opts); merged != nil {
		return merged
	}

	// Try to merge compatible objects into a single object
	if merged := tryMergeObjects(collapsed, opts); merged != nil {
		// Successfully merged - return directly without anyOf wrapper
		return merged
	}

	// Check if we exceed the anyOf limit
	if len(collapsed) > opts.AnyOfLimit {
		if opts.EnableWarnings {
			typeCounts := make(map[string]int)
			for _, s := range collapsed {
				t := getType(s)
				if t == "" {
					t = "untyped"
				}
				typeCounts[t]++
			}
			fmt.Printf("DEBUG Union: exceeding anyOf limit (%d > %d), widening. Types: ", len(collapsed), opts.AnyOfLimit)
			for t, count := range typeCounts {
				fmt.Printf("%s=%d ", t, count)
			}
			fmt.Printf("\n")
		}
		return widenUnion(collapsed, opts)
	}

	// Filter out empty/corrupt schemas before creating anyOf
	validSchemas := make([]*oas3.Schema, 0, len(collapsed))
	for _, s := range collapsed {
		// Skip schemas with no type (corrupt/empty)
		if getType(s) != "" {
			validSchemas = append(validSchemas, s)
		}
	}

	// If only one valid schema remains, return it directly
	if len(validSchemas) == 1 {
		return validSchemas[0]
	}

	if len(validSchemas) == 0 {
		return Top() // All were corrupt
	}

	// Create anyOf
	anyOf := make([]*oas3.JSONSchema[oas3.Referenceable], len(validSchemas))
	for i, s := range validSchemas {
		anyOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](s)
	}

	result := &oas3.Schema{
		AnyOf: anyOf,
	}

	// DEBUG: Trace Union result for objects with 'value' property
	if opts.EnableWarnings && len(anyOf) > 0 {
		hasValueProp := false
		for _, branch := range anyOf {
			if branch.GetLeft() != nil {
				s := branch.GetLeft()
				if getType(s) == "object" && s.Properties != nil {
					if _, ok := s.Properties.Get("value"); ok {
						hasValueProp = true
						break
					}
				}
			}
		}
		if hasValueProp {
			fmt.Printf("DEBUG Union: result is anyOf with %d branches (contains 'value' property)\n", len(anyOf))
		}
	}

	return result
}

// tryMergeObjects attempts to merge multiple object schemas into a single object
// Returns nil if merging is not safe
// tryMergeArrays attempts to merge multiple array schemas into a single array
// Returns nil if merging is not safe
func tryMergeArrays(schemas []*oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if len(schemas) <= 1 {
		return nil
	}

	// All schemas must be arrays
	allArrays := true
	for _, s := range schemas {
		if getType(s) != "array" {
			allArrays = false
			break
		}
	}

	if !allArrays {
		return nil
	}

	// If every array is the exact same pointer, preserve pointer identity.
	// This avoids creating a fresh, untagged array when a canonical one already exists.
	samePtr := true
	first := schemas[0]
	for i := 1; i < len(schemas); i++ {
		if schemas[i] != first {
			samePtr = false
			break
		}
	}
	if samePtr {
		return first
	}

	// Collect all item schemas
	var itemSchemas []*oas3.Schema
	for _, s := range schemas {
		if s.Items != nil && s.Items.Left != nil {
			itemSchemas = append(itemSchemas, s.Items.Left)
		}
	}

	if len(itemSchemas) == 0 {
		// All arrays have unknown/nil items - return an UNCONSTRAINED array, not an empty one
		// ArrayType(Bottom()) would create maxItems=0 which means "provably empty"
		// Instead, we want "array with unknown items" = plain array type with Items unset
		if opts.EnableWarnings {
			fmt.Printf("DEBUG tryMergeArrays: len(itemSchemas)==0, returning unconstrained array (not empty)\n")
		}
		return &oas3.Schema{
			Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
		}
	}

	// Union the item schemas
	mergedItems := Union(itemSchemas, opts)

	// If Union returned Bottom/nil, use Top instead
	if mergedItems == nil {
		mergedItems = Top()
	}

	return ArrayType(mergedItems)
}

func tryMergeObjects(schemas []*oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if len(schemas) <= 1 {
		return nil
	}

	// All schemas must be objects
	for _, s := range schemas {
		if getType(s) != "object" {
			return nil
		}
	}

	// DEBUG: Track tryMergeObjects calls
	if opts.EnableWarnings {
		hasConfigs := false
		for _, s := range schemas {
			if s.Properties != nil {
				if _, ok := s.Properties.Get("configs"); ok {
					hasConfigs = true
					break
				}
			}
		}
		if hasConfigs {
			fmt.Printf("DEBUG tryMergeObjects: merging %d objects with 'configs' property\n", len(schemas))
		}
	}

	// CRITICAL FIX: Instead of requiring identical required sets, compute the intersection
	// A property is required in the merged schema only if it's required in ALL input schemas
	// This correctly models conditional object construction in symbolic execution

	// Calculate required set intersection (property required in ALL schemas)
	var requiredIntersection []string
	if len(schemas) > 0 {
		// Start with first schema's required set
		candidateRequired := make(map[string]bool)
		for _, r := range schemas[0].Required {
			candidateRequired[r] = true
		}

		// Intersect with all other schemas
		for i := 1; i < len(schemas); i++ {
			schemaRequired := make(map[string]bool)
			for _, r := range schemas[i].Required {
				schemaRequired[r] = true
			}

			// Keep only properties that are required in this schema too
			for prop := range candidateRequired {
				if !schemaRequired[prop] {
					delete(candidateRequired, prop)
				}
			}
		}

		// Convert to slice
		for prop := range candidateRequired {
			requiredIntersection = append(requiredIntersection, prop)
		}
		sort.Strings(requiredIntersection)
	}

	// Collect all property names across all branches (property union)
	allProps := make(map[string]bool)
	for _, s := range schemas {
		if s.Properties != nil {
			for propName := range s.Properties.All() {
				allProps[propName] = true
			}
		}
	}

	// Merge each property across branches
	mergedProps := make(map[string]*oas3.Schema)
	for propName := range allProps {
		var propSchemas []*oas3.Schema
		for _, s := range schemas {
			if s.Properties != nil {
				if propSchema, ok := s.Properties.Get(propName); ok {
					if propSchema.GetLeft() != nil {
						propSchemas = append(propSchemas, propSchema.GetLeft())
					}
				}
			}
		}

		// DEBUG: Track what's happening with "configs" property
		if propName == "configs" && opts.EnableWarnings && len(propSchemas) > 0 {
			fmt.Printf("DEBUG tryMergeObjects: merging 'configs' property from %d object schemas\n", len(propSchemas))
			emptyCount := 0
			nonEmptyCount := 0
			for i, ps := range propSchemas {
				if getType(ps) == "array" {
					isEmpty := ps.MaxItems != nil && *ps.MaxItems == 0
					hasItems := ps.Items != nil && ps.Items.Left != nil
					var itemType string
					if hasItems {
						itemType = getType(ps.Items.Left)
					}
					if isEmpty {
						emptyCount++
					} else {
						nonEmptyCount++
					}
					fmt.Printf("  [%d] array: empty=%v, hasItems=%v, itemType=%s\n", i, isEmpty, hasItems, itemType)
				} else {
					fmt.Printf("  [%d] not array: type=%s\n", i, getType(ps))
				}
			}
			fmt.Printf("  Summary: %d empty, %d non-empty arrays\n", emptyCount, nonEmptyCount)
		}

		if len(propSchemas) > 0 {
			// CRITICAL FIX: Filter out unconstrained/unknown schemas before Union to preserve precision
			// When merging {id: {type: string}} with {id: {}}, keep {type: string}
			// Empty schemas {} represent "unknown" and should not eliminate concrete schemas
			filteredSchemas := make([]*oas3.Schema, 0, len(propSchemas))
			hasConcreteSchema := false
			unconstrainedCount := 0
			for _, ps := range propSchemas {
				if !isUnconstrainedSchema(ps) {
					filteredSchemas = append(filteredSchemas, ps)
					hasConcreteSchema = true
				} else {
					unconstrainedCount++
				}
			}
			// If all are unconstrained, keep exactly one
			if !hasConcreteSchema && len(propSchemas) > 0 {
				filteredSchemas = []*oas3.Schema{propSchemas[0]}
			}

			// DEBUG: Log when we filter out unconstrained schemas for "value" property
			if opts.EnableWarnings && propName == "value" && unconstrainedCount > 0 {
				fmt.Printf("DEBUG tryMergeObjects: property=%s had %d schemas (%d unconstrained, %d concrete)\n",
					propName, len(propSchemas), unconstrainedCount, len(filteredSchemas))
				for i, ps := range propSchemas {
					fmt.Printf("  [%d] type=%s, unconstrained=%v\n", i, getType(ps), isUnconstrainedSchema(ps))
				}
			}

			// Recursively union the property schemas
			unionSchema := Union(filteredSchemas, opts)
			// Unwrap single-branch anyOf
			if len(unionSchema.AnyOf) == 1 && unionSchema.AnyOf[0].GetLeft() != nil {
				unionSchema = unionSchema.AnyOf[0].GetLeft()
			}
			mergedProps[propName] = unionSchema

			// DEBUG: Show final result for configs property
			if propName == "configs" && opts.EnableWarnings {
				if getType(unionSchema) == "array" {
					isEmpty := unionSchema.MaxItems != nil && *unionSchema.MaxItems == 0
					hasItems := unionSchema.Items != nil && unionSchema.Items.Left != nil
					var itemType string
					if hasItems {
						itemType = getType(unionSchema.Items.Left)
					}
					fmt.Printf("DEBUG tryMergeObjects: 'configs' final result: empty=%v, hasItems=%v, itemType=%s\n",
						isEmpty, hasItems, itemType)
				} else {
					fmt.Printf("DEBUG tryMergeObjects: 'configs' final result: type=%s (not array!)\n", getType(unionSchema))
				}
			}

			// DEBUG: Log final schema for "value" property
			if opts.EnableWarnings && propName == "value" {
				fmt.Printf("DEBUG tryMergeObjects: property=%s final type=%s, unconstrained=%v\n",
					propName, getType(unionSchema), isUnconstrainedSchema(unionSchema))
			}
		}
	}

	// Build merged object with intersected required set
	result := BuildObject(mergedProps, requiredIntersection)

	// Merge AdditionalProperties across branches
	var apSchemas []*oas3.Schema
	seenTrue := false
	nilCount := 0
	for _, s := range schemas {
		if s.AdditionalProperties != nil {
			if s.AdditionalProperties.Right != nil {
				if *s.AdditionalProperties.Right {
					seenTrue = true
				}
			}
			if s.AdditionalProperties.Left != nil {
				apSchemas = append(apSchemas, s.AdditionalProperties.Left)
			}
		} else {
			nilCount++
		}
	}
	if opts.EnableWarnings && (len(apSchemas) > 0 || seenTrue) {
		fmt.Printf("DEBUG tryMergeObjects: AP merge - %d schemas, %d with AP.Left, seenTrue=%v, nilCount=%d\n",
			len(schemas), len(apSchemas), seenTrue, nilCount)
	}
	switch {
	case seenTrue:
		// true dominates (most permissive)
		result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Top())
	case len(apSchemas) > 0:
		// Union schema APs
		mergedAP := Union(apSchemas, opts)
		if mergedAP == nil {
			mergedAP = Top()
		}
		result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](mergedAP)
		if opts.EnableWarnings {
			fmt.Printf("DEBUG tryMergeObjects: merged AP type=%s schemaCount=%d\n",
				getType(mergedAP), len(apSchemas))
		}
	default:
		// Leave nil (unknown) - includes seenFalse case
	}

	return result
}

// haveSameRequiredSets checks if all schemas have identical required sets
func haveSameRequiredSets(schemas []*oas3.Schema) bool {
	if len(schemas) <= 1 {
		return true
	}

	// Build set from first schema
	first := schemas[0].Required
	firstSet := make(map[string]bool)
	for _, r := range first {
		firstSet[r] = true
	}

	// Compare all others
	for i := 1; i < len(schemas); i++ {
		req := schemas[i].Required
		if len(req) != len(first) {
			return false
		}

		for _, r := range req {
			if !firstSet[r] {
				return false
			}
		}
	}

	return true
}

// deduplicateSchemas removes duplicate schemas from a list.
// Uses enhanced fingerprinting that distinguishes constants and structural shapes.
func deduplicateSchemas(schemas []*oas3.Schema) []*oas3.Schema {
	if len(schemas) <= 1 {
		return schemas
	}

	// Step 1: Pointer-identity dedup (catches shared schema instances)
	seenPtr := make(map[*oas3.Schema]struct{})
	unique := make([]*oas3.Schema, 0, len(schemas))
	for _, s := range schemas {
		if s == nil {
			continue
		}
		if _, ok := seenPtr[s]; ok {
			continue
		}
		seenPtr[s] = struct{}{}
		unique = append(unique, s)
	}

	// Step 2: Special case - merge string const/enum schemas
	// If all schemas are string type with single enum values, combine into one enum
	if canMergeStringEnums(unique) {
		return []*oas3.Schema{mergeStringEnums(unique)}
	}

	// Step 2b: Special case - merge integer const/enum schemas
	if canMergeIntegerEnums(unique) {
		return []*oas3.Schema{mergeIntegerEnums(unique)}
	}

	// Step 3: Structural deduplication using fingerprinting
	unique = deduplicateByFingerprint(unique)

	return unique
}

// deduplicateByFingerprint removes structurally identical schemas using canonical fingerprinting
func deduplicateByFingerprint(schemas []*oas3.Schema) []*oas3.Schema {
	if len(schemas) <= 1 {
		return schemas
	}

	seen := make(map[string]*oas3.Schema)
	result := make([]*oas3.Schema, 0, len(schemas))

	for _, s := range schemas {
		fp := schemaFingerprint(s)
		if _, exists := seen[fp]; !exists {
			seen[fp] = s
			result = append(result, s)
		}
	}

	return result
}

// removeSubsumedSchemas removes schemas that are subsumed by (subsets of) other schemas
// Iterates to fixpoint to handle transitive subsumption
func removeSubsumedSchemas(schemas []*oas3.Schema) []*oas3.Schema {
	if len(schemas) <= 1 {
		return schemas
	}

	// DEBUG: Track what's being removed
	initialCount := len(schemas)
	iteration := 0

	changed := true
	for changed {
		iteration++
		changed = false
		n := len(schemas)
		removed := make([]bool, n)

		for i := 0; i < n; i++ {
			if removed[i] {
				continue
			}
			for j := i + 1; j < n; j++ {
				if removed[j] {
					continue
				}
				a, b := schemas[i], schemas[j]

				// Check if a ⊆ b (a is subsumed by b)
				if isSubschemaOf(a, b) {
					removed[i] = true
					changed = true
					// DEBUG: Show subsumption for objects
					if n == initialCount && iteration == 1 && i < 3 {
						fmt.Printf("DEBUG subsumption iter=%d: schema[%d] (type=%s) subsumed by schema[%d] (type=%s)\n",
							iteration, i, getType(a), j, getType(b))
					}
					break
				}

				// Check if b ⊆ a (b is subsumed by a)
				if isSubschemaOf(b, a) {
					removed[j] = true
					changed = true
					// DEBUG: Show subsumption for objects with configs
					if n == initialCount && getType(b) == "object" && b.Properties != nil {
						if _, ok := b.Properties.Get("configs"); ok {
							fmt.Printf("DEBUG subsumption iter=%d: schema[%d] subsumed by schema[%d]\n", iteration, j, i)
						}
					}
				}
			}
		}

		if changed {
			tmp := make([]*oas3.Schema, 0, n)
			for i := range schemas {
				if !removed[i] {
					tmp = append(tmp, schemas[i])
				}
			}
			schemas = tmp
		}
	}

	return schemas
}

// canMergeStringEnums checks if all schemas are string enums that can be merged
func canMergeStringEnums(schemas []*oas3.Schema) bool {
	if len(schemas) == 0 {
		return false
	}

	for _, s := range schemas {
		if s == nil {
			return false
		}
		// Must be string type
		if getType(s) != "string" {
			return false
		}
		// Must have at least one enum value
		if len(s.Enum) == 0 {
			return false
		}
		// All enum values must be strings
		for _, node := range s.Enum {
			if node == nil || node.Kind != yaml.ScalarNode {
				return false
			}
			// Strings typically have Tag "!!str" or no tag
			if node.Tag != "" && node.Tag != "!!str" {
				return false
			}
		}
		// Must not have other validation constraints that would conflict
		if s.Pattern != nil || s.MinLength != nil || s.MaxLength != nil || s.Format != nil {
			return false
		}
	}
	return true
}

// mergeStringEnums merges multiple string enum schemas into a single enum schema
func mergeStringEnums(schemas []*oas3.Schema) *oas3.Schema {
	// Collect all enum values, deduplicating by canonical form
	seenValues := make(map[string]*yaml.Node)
	var values []*yaml.Node
	nullable := false

	for _, s := range schemas {
		// Track nullable across all branches
		if s.Nullable != nil && *s.Nullable {
			nullable = true
		}

		// Collect all enum values
		if s.Enum != nil {
			for _, node := range s.Enum {
				canonical := canonicalizeYAMLNode(node)
				if _, ok := seenValues[canonical]; !ok {
					seenValues[canonical] = node
					values = append(values, node)
				}
			}
		}
	}

	// Create a schema with all merged enum values
	result := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: values,
	}

	// Preserve nullable if any branch was nullable
	if nullable {
		result.Nullable = &nullable
	}

	return result
}

// canMergeIntegerEnums checks if all schemas are integer enums that can be merged
func canMergeIntegerEnums(schemas []*oas3.Schema) bool {
	if len(schemas) == 0 {
		return false
	}

	for _, s := range schemas {
		if s == nil {
			return false
		}
		// Must be integer type
		if getType(s) != "integer" {
			return false
		}
		// Must have at least one enum value
		if len(s.Enum) == 0 {
			return false
		}
		// All enum values must be integers
		for _, node := range s.Enum {
			if node == nil || node.Kind != yaml.ScalarNode {
				return false
			}
			// Integers should have Tag "!!int" or no tag with numeric value
			if node.Tag != "" && node.Tag != "!!int" {
				return false
			}
		}
		// Must not have other validation constraints that would conflict
		if s.Minimum != nil || s.Maximum != nil || s.MultipleOf != nil {
			return false
		}
	}
	return true
}

// mergeIntegerEnums merges multiple integer enum schemas into a single enum schema
func mergeIntegerEnums(schemas []*oas3.Schema) *oas3.Schema {
	// Collect all enum values, deduplicating by canonical form
	seenValues := make(map[string]*yaml.Node)
	var values []*yaml.Node
	nullable := false

	for _, s := range schemas {
		// Track nullable across all branches
		if s.Nullable != nil && *s.Nullable {
			nullable = true
		}

		// Collect all enum values
		if s.Enum != nil {
			for _, node := range s.Enum {
				canonical := canonicalizeYAMLNode(node)
				if _, ok := seenValues[canonical]; !ok {
					seenValues[canonical] = node
					values = append(values, node)
				}
			}
		}
	}

	// Create a schema with all merged enum values
	result := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger),
		Enum: values,
	}

	// Preserve nullable if any branch was nullable
	if nullable {
		result.Nullable = &nullable
	}

	return result
}

// widenUnion applies widening strategy when union exceeds limits.
func widenUnion(schemas []*oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	switch opts.WideningLevel {
	case 0:
		// No widening - keep all schemas (may exceed limits!)
		anyOf := make([]*oas3.JSONSchema[oas3.Referenceable], len(schemas))
		for i, s := range schemas {
			anyOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](s)
		}
		return &oas3.Schema{AnyOf: anyOf}

	case 1:
		// Conservative: group by type, keep one base schema per type
		byType := make(map[string]*oas3.Schema)

		for _, s := range schemas {
			typ := getType(s)
			if typ == "" {
				typ = "any"
			}

			// Keep first schema of each type, or merge if already seen
			if _, exists := byType[typ]; !exists {
				// Use base type schema (drop facets)
				switch oas3.SchemaType(typ) {
				case oas3.SchemaTypeString:
					byType[typ] = StringType()
				case oas3.SchemaTypeNumber:
					byType[typ] = NumberType()
				case oas3.SchemaTypeInteger:
					byType[typ] = NumberType()
				case oas3.SchemaTypeBoolean:
					byType[typ] = BoolType()
				case oas3.SchemaTypeNull:
					byType[typ] = NullType()
				case oas3.SchemaTypeArray:
					// Keep structure but widen items
					byType[typ] = ArrayType(Top())
				case oas3.SchemaTypeObject:
					// Keep as generic object
					byType[typ] = ObjectType()
				default:
					byType[typ] = Top()
				}
			}
		}

		// Collect widened schemas
		widened := make([]*oas3.Schema, 0, len(byType))
		for _, s := range byType {
			widened = append(widened, s)
		}

		if len(widened) == 1 {
			return widened[0]
		}

		anyOf := make([]*oas3.JSONSchema[oas3.Referenceable], len(widened))
		for i, s := range widened {
			anyOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](s)
		}
		return &oas3.Schema{AnyOf: anyOf}

	case 2:
		// Aggressive: collapse everything to Top
		return Top()

	default:
		// Default to conservative
		return widenUnion(schemas, SchemaExecOptions{
			AnyOfLimit:    opts.AnyOfLimit,
			WideningLevel: 1,
		})
	}
}

// cloneSchema creates a shallow copy of a schema to avoid mutation
func cloneSchema(s *oas3.Schema) *oas3.Schema {
	if s == nil {
		return nil
	}
	// Create shallow copy
	clone := *s
	return &clone
}

// BuildObject creates an object schema from property map.
// Simplified version for Phase 1.
func BuildObject(props map[string]*oas3.Schema, required []string) *oas3.Schema {
	// DEBUG: Trace BuildObject calls with unconstrained 'value' property
	if valProp, ok := props["value"]; ok && isUnconstrainedSchema(valProp) {
		fmt.Printf("DEBUG BuildObject: building object with UNCONSTRAINED value property!\n")
		fmt.Printf("  Stack trace: %s\n", string(debug.Stack()))
	}

	propMap := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	keysInOrder := make([]string, 0, len(props))
	for k := range props {
		keysInOrder = append(keysInOrder, k)
	}
	sort.Strings(keysInOrder)
	for _, k := range keysInOrder {
		v := props[k]
		propMap.Set(k, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](v))
	}
	sort.Strings(required)

	schema := &oas3.Schema{
		Type:       oas3.NewTypeFromString(oas3.SchemaTypeObject),
		Properties: propMap,
		Required:   required,
	}

	return schema
}

// ============================================================================
// HELPERS
// ============================================================================

// getType returns the primary type from a schema.
// Returns empty string if no type or multiple types.
func getType(s *oas3.Schema) string {
	if s == nil {
		return ""
	}

	// Check anyOf - if all branches have same type, return it
	if len(s.AnyOf) > 0 {
		firstType := ""
		allSame := true
		for _, branch := range s.AnyOf {
			if branch.Left != nil {
				branchType := getType(branch.Left)
				if firstType == "" {
					firstType = branchType
				} else if firstType != branchType {
					allSame = false
					break
				}
			}
		}
		if allSame && firstType != "" {
			return firstType
		}
		return "" // Mixed types in anyOf
	}

	types := s.GetType()
	if len(types) == 0 {
		return ""
	}
	if len(types) > 1 {
		return "" // Multiple types
	}
	return string(types[0])
}

// mightBeType checks if a schema could possibly be of the given type.
func mightBeType(s *oas3.Schema, typ oas3.SchemaType) bool {
	if s == nil {
		return false
	}

	// Check explicit type
	types := s.GetType()
	if len(types) > 0 {
		for _, t := range types {
			if t == typ {
				return true
			}
		}
		return false // Has types but not this one
	}

	// No explicit type - could be anything
	if len(types) == 0 && s.AnyOf == nil && s.AllOf == nil && s.OneOf == nil {
		return true
	}

	// Check anyOf branches
	if s.AnyOf != nil {
		for _, branch := range s.AnyOf {
			if branch.Left != nil && mightBeType(branch.Left, typ) {
				return true
			}
		}
		return false
	}

	// Conservative: could be anything
	return true
}

// MightBeObject checks if schema could be an object.
func MightBeObject(s *oas3.Schema) bool {
	return mightBeType(s, oas3.SchemaTypeObject)
}

// MightBeArray checks if schema could be an array.
func MightBeArray(s *oas3.Schema) bool {
	return mightBeType(s, oas3.SchemaTypeArray)
}

// MightBeString checks if schema could be a string.
func MightBeString(s *oas3.Schema) bool {
	return mightBeType(s, oas3.SchemaTypeString)
}

// MightBeNumber checks if schema could be a number.
func MightBeNumber(s *oas3.Schema) bool {
	return mightBeType(s, oas3.SchemaTypeNumber) || mightBeType(s, oas3.SchemaTypeInteger)
}

// ============================================================================
// SET OPERATIONS - ADVANCED
// ============================================================================

// Intersect creates a schema that matches all input schemas (allOf).
// This is used for narrowing and constraint combination.
func Intersect(a, b *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if a == nil || b == nil {
		return Bottom()
	}

	// Check for type incompatibility
	typeA, typeB := getType(a), getType(b)
	if typeA != "" && typeB != "" && typeA != typeB {
		// Incompatible types = Bottom (no valid values)
		return Bottom()
	}

	// If one is Bottom, result is Bottom
	if a == Bottom() || b == Bottom() {
		return Bottom()
	}

	// For same types, we could try to merge constraints intelligently
	// For now, create allOf
	allOf := []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](a),
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](b),
	}

	return &oas3.Schema{
		AllOf: allOf,
	}
}

// RequireType narrows a schema to a specific type.
// Used for type guards like: select(type == "array")
func RequireType(s *oas3.Schema, typ oas3.SchemaType, opts SchemaExecOptions) *oas3.Schema {
	if s == nil {
		return Bottom()
	}

	// Create a schema that requires the specific type
	typeSchema := &oas3.Schema{
		Type: oas3.NewTypeFromString(typ),
	}

	// Intersect with the original schema
	return Intersect(s, typeSchema, opts)
}

// HasProperty refines an object schema to require a property exists.
// Used for guards like: select(has("foo"))
func HasProperty(obj *oas3.Schema, key string, opts SchemaExecOptions) *oas3.Schema {
	if obj == nil {
		return Bottom()
	}

	// If not an object type, this can't succeed
	objType := getType(obj)
	if objType != "" && objType != "object" {
		return Bottom()
	}

	// Create a requirement that the property exists
	// This means adding it to the required list
	requirementSchema := &oas3.Schema{
		Type:     oas3.NewTypeFromString(oas3.SchemaTypeObject),
		Required: []string{key},
	}

	return Intersect(obj, requirementSchema, opts)
}

// MergeObjects combines two object schemas (for the + operator on objects).
func MergeObjects(a, b *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {

	if a == nil {
		return b
	}
	if b == nil {
		return a
	}

	// Merge properties from both objects
	// Properties from b override properties from a
	propMap := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()

	// Add all from a
	if a.Properties != nil {
		for k, v := range a.Properties.All() {
			propMap.Set(k, v)
		}
	}

	// Add/override with b
	if b.Properties != nil {
		for k, v := range b.Properties.All() {
			propMap.Set(k, v)
		}
	}

	// Merge required lists, but only for properties that actually exist in the merged map
	propNames := make(map[string]struct{})
	for k := range propMap.All() {
		propNames[k] = struct{}{}
	}

	reqA := make(map[string]struct{}, len(a.Required))
	for _, r := range a.Required {
		reqA[r] = struct{}{}
	}
	reqB := make(map[string]struct{}, len(b.Required))
	for _, r := range b.Required {
		reqB[r] = struct{}{}
	}

	required := make([]string, 0, len(propNames))
	for k := range propNames {
		if _, ok := reqA[k]; ok {
			required = append(required, k)
			continue
		}
		if _, ok := reqB[k]; ok {
			required = append(required, k)
		}
	}

	// Sort required array for deterministic output
	sort.Strings(required)

	result := &oas3.Schema{
		Type:       oas3.NewTypeFromString(oas3.SchemaTypeObject),
		Properties: propMap,
		Required:   required,
	}

	// Merge AdditionalProperties
	// true dominates (most permissive), then schemas are unioned, false is preserved if both forbid
	switch {
	case a.AdditionalProperties != nil && a.AdditionalProperties.Right != nil && *a.AdditionalProperties.Right:
		// true on a dominates
		result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Top())
	case b.AdditionalProperties != nil && b.AdditionalProperties.Right != nil && *b.AdditionalProperties.Right:
		// true on b dominates
		result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Top())
	default:
		var apParts []*oas3.Schema
		if a.AdditionalProperties != nil && a.AdditionalProperties.Left != nil {
			apParts = append(apParts, a.AdditionalProperties.Left)
		}
		if b.AdditionalProperties != nil && b.AdditionalProperties.Left != nil {
			apParts = append(apParts, b.AdditionalProperties.Left)
		}
		if len(apParts) > 0 {
			result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Union(apParts, opts))
		} else if (a.AdditionalProperties != nil && a.AdditionalProperties.Right != nil && !*a.AdditionalProperties.Right) ||
			(b.AdditionalProperties != nil && b.AdditionalProperties.Right != nil && !*b.AdditionalProperties.Right) {
			// preserve false if any side explicitly forbids
			result.AdditionalProperties = oas3.NewJSONSchemaFromBool(false)
		}
	}

	return result
}

// BuildArray creates an array schema from element schemas.
func BuildArray(items *oas3.Schema, elements []*oas3.Schema) *oas3.Schema {
	// If specific elements provided, use prefixItems
	if len(elements) > 0 {
		prefixItems := make([]*oas3.JSONSchema[oas3.Referenceable], len(elements))
		for i, elem := range elements {
			prefixItems[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](elem)
		}

		schema := &oas3.Schema{
			Type:        oas3.NewTypeFromString(oas3.SchemaTypeArray),
			PrefixItems: prefixItems,
		}

		// If items schema also provided, use it for additional items
		if items != nil {
			schema.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](items)
		}

		return schema
	}

	// Otherwise, just use items schema
	return ArrayType(items)
}

// ============================================================================
// SCHEMA SUBSUMPTION & FINGERPRINTING
// ============================================================================

// schemaFingerprint creates a canonical hash of validation-relevant schema fields
// Used for structural deduplication - ignores annotations like title, description
func schemaFingerprint(s *oas3.Schema) string {
	if s == nil {
		return "null"
	}

	// Build canonical string representation
	var parts []string

	// Type
	types := s.GetType()
	if len(types) > 0 {
		typeStrs := make([]string, len(types))
		for i, t := range types {
			typeStrs[i] = string(t)
		}
		parts = append(parts, "type:"+canonicalizeStringSlice(typeStrs))
	}

	// Enum
	if len(s.Enum) > 0 {
		parts = append(parts, "enum:"+canonicalizeYAMLNodes(s.Enum))
	}

	// Const (represented via Enum in OAS3)
	// Already handled above

	// Number constraints
	if s.Minimum != nil {
		parts = append(parts, "min:"+strconv.FormatFloat(*s.Minimum, 'g', -1, 64))
	}
	if s.Maximum != nil {
		parts = append(parts, "max:"+strconv.FormatFloat(*s.Maximum, 'g', -1, 64))
	}
	// Note: ExclusiveMinimum/Maximum are complex EitherValue types
	// For simplicity in fingerprinting, we skip them as they're rarely used
	if s.MultipleOf != nil {
		parts = append(parts, "mult:"+strconv.FormatFloat(*s.MultipleOf, 'g', -1, 64))
	}

	// String constraints
	if s.MinLength != nil {
		parts = append(parts, "minLen:"+strconv.FormatInt(int64(*s.MinLength), 10))
	}
	if s.MaxLength != nil {
		parts = append(parts, "maxLen:"+strconv.FormatInt(int64(*s.MaxLength), 10))
	}
	if s.Pattern != nil {
		parts = append(parts, "pattern:"+*s.Pattern)
	}

	// Array constraints
	if s.Items != nil && s.Items.Left != nil {
		parts = append(parts, "items:"+schemaFingerprint(s.Items.Left))
	}
	if s.MinItems != nil {
		parts = append(parts, "minItems:"+strconv.FormatInt(int64(*s.MinItems), 10))
	}
	if s.MaxItems != nil {
		parts = append(parts, "maxItems:"+strconv.FormatInt(int64(*s.MaxItems), 10))
	}
	if s.UniqueItems != nil && *s.UniqueItems {
		parts = append(parts, "unique:true")
	}

	// Object constraints
	if s.Properties != nil {
		var propParts []string
		for k, v := range s.Properties.All() {
			if v.Left != nil {
				propParts = append(propParts, k+":"+schemaFingerprint(v.Left))
			}
		}
		if len(propParts) > 0 {
			parts = append(parts, "props:{"+canonicalizeStringSlice(propParts)+"}")
		}
	}
	if len(s.Required) > 0 {
		parts = append(parts, "req:"+canonicalizeStringSlice(s.Required))
	}
	if s.AdditionalProperties != nil {
		if s.AdditionalProperties.Left != nil {
			parts = append(parts, "addProps:"+schemaFingerprint(s.AdditionalProperties.Left))
		} else if s.AdditionalProperties.Right != nil {
			parts = append(parts, "addProps:"+strconv.FormatBool(*s.AdditionalProperties.Right))
		}
	}

	// AnyOf (for nested unions)
	if len(s.AnyOf) > 0 {
		var anyOfParts []string
		for _, branch := range s.AnyOf {
			if branch.Left != nil {
				anyOfParts = append(anyOfParts, schemaFingerprint(branch.Left))
			}
		}
		parts = append(parts, "anyOf:["+canonicalizeStringSlice(anyOfParts)+"]")
	}

	return "{" + canonicalizeStringSlice(parts) + "}"
}

// isSubschemaOf checks if schema A is a subschema of (subsumed by) schema B
// Returns true if every instance that validates against A also validates against B
func isSubschemaOf(a, b *oas3.Schema) bool {
	if a == nil || b == nil {
		return false
	}

	// Handle enum/const cases first (most common in our use case)
	aHasEnum := len(a.Enum) > 0
	bHasEnum := len(b.Enum) > 0

	if aHasEnum {
		if bHasEnum {
			// Both have enums: A ⊆ B if all of A's values are in B
			return enumSubset(a.Enum, b.Enum)
		}
		// A has enum, B doesn't: check if all A's enum values satisfy B's constraints
		// For now, simplified: if B is just a type without constraints, A ⊆ B
		bType := getType(b)
		if bType != "" && b.Minimum == nil && b.Maximum == nil && b.Pattern == nil {
			// B is unconstrained type, A's enum values must match that type
			return enumMatchesType(a.Enum, bType)
		}
		return false
	}

	// A has no enum but B has enum: A cannot be subset of B (A is less constrained)
	if bHasEnum {
		return false
	}

	// Check type compatibility
	aType := getType(a)
	bType := getType(b)

	if aType != "" && bType != "" {
		// integer ⊆ number
		if aType == "integer" && bType == "number" {
			// Check numeric constraints
			return numericConstraintsSubsumed(a, b)
		}

		// Types must match (or B has no type = any)
		if aType != bType {
			return false
		}

		// Same type: check type-specific constraints
		switch oas3.SchemaType(aType) {
		case oas3.SchemaTypeNumber, oas3.SchemaTypeInteger:
			return numericConstraintsSubsumed(a, b)
		case oas3.SchemaTypeString:
			return stringConstraintsSubsumed(a, b)
		case oas3.SchemaTypeArray:
			return arrayConstraintsSubsumed(a, b)
		case oas3.SchemaTypeObject:
			return objectConstraintsSubsumed(a, b)
		}
	} else if aType != "" && bType == "" {
		// B has no type constraint = accepts anything
		// But this was already handled by isTopSchema check above
		return true
	} else if aType == "" && bType != "" {
		// A has no type but B requires specific type
		return false
	}

	// Both have no explicit type - compare constraints generically
	return true
}

// isUnconstrainedSchema checks if a schema is completely unconstrained (empty schema {})
//
// In the symbolic execution lattice, Top represents "unknown/any value" and is created
// when the engine loses precision. It's represented as an empty schema with no type,
// no properties, no constraints - just {}.
//
// This function identifies such schemas so they can be filtered during merging to
// preserve precision. For example, when merging property schemas from multiple paths:
//
//	Path 1: {id: {type: string}}  (concrete)
//	Path 2: {id: {}}              (unknown/lost precision)
//
// We want to keep {type: string}, not the empty schema.
func isUnconstrainedSchema(s *oas3.Schema) bool {
	if s == nil {
		return false
	}

	// Check all possible constraint fields
	// An unconstrained schema has NONE of these set
	hasType := getType(s) != ""
	propCount := 0
	if s.Properties != nil {
		for range s.Properties.All() {
			propCount++
		}
	}
	hasProperties := propCount > 0
	hasEnum := len(s.Enum) > 0
	hasAnyOf := len(s.AnyOf) > 0
	hasAllOf := len(s.AllOf) > 0
	hasOneOf := len(s.OneOf) > 0
	hasConstraints := s.Minimum != nil || s.Maximum != nil || s.Pattern != nil ||
		s.MinLength != nil || s.MaxLength != nil || s.Format != nil
	hasItems := s.Items != nil && s.Items.Left != nil
	hasAdditionalProps := s.AdditionalProperties != nil && s.AdditionalProperties.Left != nil
	hasRequired := len(s.Required) > 0

	// Unconstrained = completely empty (no type, properties, constraints, etc.)
	return !hasType && !hasProperties && !hasEnum && !hasAnyOf && !hasAllOf && !hasOneOf &&
		!hasConstraints && !hasItems && !hasAdditionalProps && !hasRequired
}

// numericConstraintsSubsumed checks if A's numeric constraints are stricter than or equal to B's
func numericConstraintsSubsumed(a, b *oas3.Schema) bool {
	// Check minimum
	if b.Minimum != nil {
		if a.Minimum == nil {
			return false
		}
		if *a.Minimum < *b.Minimum {
			return false
		}
		// Exclusive flags: skip for simplicity (rarely used)
	}

	// Check maximum
	if b.Maximum != nil {
		if a.Maximum == nil {
			return false
		}
		if *a.Maximum > *b.Maximum {
			return false
		}
		// Exclusive flags: skip for simplicity (rarely used)
	}

	// Check multipleOf (simplified: only if both present and A is multiple of B)
	if b.MultipleOf != nil {
		if a.MultipleOf == nil {
			return false
		}
		// A's multipleOf must be a multiple of B's multipleOf
		// For safety with floats, only check if A == B
		if *a.MultipleOf != *b.MultipleOf {
			return false
		}
	}

	return true
}

// stringConstraintsSubsumed checks if A's string constraints are stricter than or equal to B's
func stringConstraintsSubsumed(a, b *oas3.Schema) bool {
	// Check minLength: A.min >= B.min
	if b.MinLength != nil {
		if a.MinLength == nil || *a.MinLength < *b.MinLength {
			return false
		}
	}

	// Check maxLength: A.max <= B.max
	if b.MaxLength != nil {
		if a.MaxLength == nil || *a.MaxLength > *b.MaxLength {
			return false
		}
	}

	// Check pattern: only claim subsumption if patterns are equal
	if b.Pattern != nil {
		if a.Pattern == nil || *a.Pattern != *b.Pattern {
			return false
		}
	}

	return true
}

// arrayConstraintsSubsumed checks if A's array constraints are stricter than or equal to B's
func arrayConstraintsSubsumed(a, b *oas3.Schema) bool {
	fmt.Printf("DEBUG arrayConstraintsSubsumed: CALLED\n")
	// Special case: empty arrays (MaxItems=0) are not subsumed by non-empty arrays
	// An empty array represents a specific constraint (must be empty), not a general array
	aIsEmpty := a.MaxItems != nil && *a.MaxItems == 0
	bIsEmpty := b.MaxItems != nil && *b.MaxItems == 0

	// DEBUG: Log empty array checks
	logArraySubsumption := aIsEmpty || bIsEmpty
	if logArraySubsumption {
		aHasItems := a.Items != nil && a.Items.Left != nil
		bHasItems := b.Items != nil && b.Items.Left != nil
		var aItemType, bItemType string
		if aHasItems {
			aItemType = getType(a.Items.Left)
		}
		if bHasItems {
			bItemType = getType(b.Items.Left)
		}
		fmt.Printf("DEBUG arrayConstraintsSubsumed: aEmpty=%v (items=%v, itemType=%s), bEmpty=%v (items=%v, itemType=%s)\n",
			aIsEmpty, aHasItems, aItemType, bIsEmpty, bHasItems, bItemType)
	}

	if aIsEmpty && !bIsEmpty {
		// A is empty array, B is not - A is NOT subsumed by B
		fmt.Printf("DEBUG arrayConstraintsSubsumed: returning FALSE (A empty, B not)\n")
		return false
	}
	if !aIsEmpty && bIsEmpty {
		// A is non-empty, B is empty - A is NOT subsumed by B
		fmt.Printf("DEBUG arrayConstraintsSubsumed: returning FALSE (A not empty, B empty)\n")
		return false
	}
	// If both empty or both non-empty, continue with normal checks

	// Check minItems: A.min >= B.min
	if b.MinItems != nil {
		if a.MinItems == nil || *a.MinItems < *b.MinItems {
			return false
		}
	}

	// Check maxItems: A.max <= B.max
	if b.MaxItems != nil {
		if a.MaxItems == nil || *a.MaxItems > *b.MaxItems {
			return false
		}
	}

	// Check items: A.items must be subschema of B.items
	aHasItems := a.Items != nil && a.Items.Left != nil
	bHasItems := b.Items != nil && b.Items.Left != nil

	if aHasItems && !bHasItems {
		// A has specific items, B doesn't
		// This means A is MORE constrained, B is LESS constrained
		// So B subsumes A (every array<string> is an array<any>)
		// But we want to keep A (more specific) in Union!
		// So return FALSE to prevent A from being removed
		return false
	}

	if !aHasItems && bHasItems {
		// A has no items (unconstrained), B has specific items
		// A is less specific, so B does NOT subsume A
		// But A might subsume B? No - A ⊆ B means A is stricter
		// This case: A is less strict, so A is NOT subsumed by B
		return false
	}

	if aHasItems && bHasItems {
		// Both have items - recursively check
		if !isSubschemaOf(a.Items.Left, b.Items.Left) {
			return false
		}
	}

	// If neither has items, they're equivalent for items purposes

	// Check uniqueItems
	if b.UniqueItems != nil && *b.UniqueItems {
		// B requires unique items
		if a.UniqueItems == nil || !*a.UniqueItems {
			return false
		}
	}

	return true
}

// objectConstraintsSubsumed checks if A's object constraints are stricter than or equal to B's
func objectConstraintsSubsumed(a, b *oas3.Schema) bool {
	// DEBUG: Track configs property comparisons
	debugConfigs := false
	if a.Properties != nil && b.Properties != nil {
		if _, aHas := a.Properties.Get("configs"); aHas {
			if _, bHas := b.Properties.Get("configs"); bHas {
				debugConfigs = true
				fmt.Printf("DEBUG objectConstraintsSubsumed: comparing objects with 'configs' property\n")
			}
		}
	}

	// Check required: B.required ⊆ A.required (B cannot require more than A)
	for _, req := range b.Required {
		found := false
		for _, aReq := range a.Required {
			if aReq == req {
				found = true
				break
			}
		}
		if !found {
			if debugConfigs {
				fmt.Printf("DEBUG objectConstraintsSubsumed: B requires '%s' but A doesn't -> FALSE\n", req)
			}
			return false
		}
	}

	// Check properties: for each property in A, if also in B, A[prop] ⊆ B[prop]
	if a.Properties != nil {
		for propName, aProp := range a.Properties.All() {
			if aProp.Left != nil {
				if b.Properties != nil {
					if bProp, ok := b.Properties.Get(propName); ok && bProp.Left != nil {
						// Both have this property: check subsumption
						if debugConfigs && propName == "configs" {
							aConfigsType := getType(aProp.Left)
							bConfigsType := getType(bProp.Left)
							aIsEmpty := aProp.Left.MaxItems != nil && *aProp.Left.MaxItems == 0
							bIsEmpty := bProp.Left.MaxItems != nil && *bProp.Left.MaxItems == 0
							fmt.Printf("DEBUG objectConstraintsSubsumed: before check - A(type=%s, empty=%v) vs B(type=%s, empty=%v)\n",
								aConfigsType, aIsEmpty, bConfigsType, bIsEmpty)
						}
						propSubsumed := isSubschemaOf(aProp.Left, bProp.Left)
						if debugConfigs && propName == "configs" {
							fmt.Printf("DEBUG objectConstraintsSubsumed: configs property subsumption check = %v\n", propSubsumed)
						}
						if !propSubsumed {
							if debugConfigs && propName == "configs" {
								fmt.Printf("DEBUG objectConstraintsSubsumed: configs NOT subsumed -> returning FALSE\n")
							}
							return false
						}
					}
				}
				// If B doesn't have this property, check additionalProperties
				// For simplicity, assume B allows it if not explicitly forbidden
			}
		}
	}

	// AdditionalProperties subsumption:
	// Semantics:
	//   - AP == nil          => forbids additional properties
	//   - AP.Right == false  => forbids additional properties
	//   - AP.Right == true   => allows any additional properties (Top)
	//   - AP.Left != nil     => allows additional properties conforming to AP.Left
	//
	// For A ⊆ B:
	//   - If B forbids additional: A must also forbid additional
	//   - If B allows any additional (Right=true): always ok
	//   - If B allows schema S (Left): A must forbid (stricter) OR A allows schema T with T ⊆ S
	aAP := a.AdditionalProperties
	bAP := b.AdditionalProperties

	aForbids := aAP == nil || (aAP.Right != nil && !*aAP.Right)
	aAllowAny := aAP != nil && aAP.Right != nil && *aAP.Right
	aSchema := aAP != nil && aAP.Left != nil

	bForbids := bAP == nil || (bAP.Right != nil && !*bAP.Right)
	bAllowAny := bAP != nil && bAP.Right != nil && *bAP.Right
	bSchema := bAP != nil && bAP.Left != nil

	switch {
	case bAllowAny:
		// B allows anything additional: fine
	case bForbids:
		// B forbids: A must forbid too
		if !aForbids {
			if debugConfigs {
				fmt.Printf("DEBUG objectConstraintsSubsumed: B forbids additional properties but A doesn't -> FALSE\n")
			}
			return false
		}
	case bSchema:
		// B allows schema S: A must forbid OR allow T with T ⊆ S
		if aAllowAny {
			if debugConfigs {
				fmt.Printf("DEBUG objectConstraintsSubsumed: B has AP schema but A allows any -> FALSE\n")
			}
			return false
		}
		if aSchema && !isSubschemaOf(aAP.Left, bAP.Left) {
			if debugConfigs {
				fmt.Printf("DEBUG objectConstraintsSubsumed: A.AP schema not subsumed by B.AP schema -> FALSE\n")
			}
			return false
		}
		// aForbids is fine (stricter than B)
	}

	return true
}

// enumSubset checks if all values in enumA are present in enumB
func enumSubset(enumA, enumB []*yaml.Node) bool {
	if len(enumA) == 0 {
		return true
	}
	if len(enumB) == 0 {
		return false
	}

	// Build set of B's values
	bValues := make(map[string]struct{})
	for _, node := range enumB {
		bValues[canonicalizeYAMLNode(node)] = struct{}{}
	}

	// Check all A's values are in B
	for _, node := range enumA {
		if _, ok := bValues[canonicalizeYAMLNode(node)]; !ok {
			return false
		}
	}

	return true
}

// enumMatchesType checks if all enum values match the given type
func enumMatchesType(enum []*yaml.Node, typ string) bool {
	for _, node := range enum {
		if !nodeMatchesType(node, typ) {
			return false
		}
	}
	return true
}

// nodeMatchesType checks if a YAML node value matches the given JSON Schema type
func nodeMatchesType(node *yaml.Node, typ string) bool {
	if node == nil {
		return typ == "null"
	}

	switch typ {
	case "string":
		return node.Tag == "!!str" || node.Tag == ""
	case "number", "integer":
		return node.Tag == "!!int" || node.Tag == "!!float"
	case "boolean":
		return node.Tag == "!!bool"
	case "null":
		return node.Tag == "!!null" || node.Value == "null"
	default:
		return false
	}
}

// canonicalizeStringSlice sorts and deduplicates a string slice for canonical comparison
func canonicalizeStringSlice(strs []string) string {
	if len(strs) == 0 {
		return ""
	}
	// Make a copy and sort
	sorted := make([]string, len(strs))
	copy(sorted, strs)
	// Simple insertion sort for small slices
	for i := 1; i < len(sorted); i++ {
		key := sorted[i]
		j := i - 1
		for j >= 0 && sorted[j] > key {
			sorted[j+1] = sorted[j]
			j--
		}
		sorted[j+1] = key
	}
	result := ""
	for i, s := range sorted {
		if i > 0 {
			result += ","
		}
		result += s
	}
	return result
}

// canonicalizeYAMLNodes creates a canonical string representation of YAML nodes
func canonicalizeYAMLNodes(nodes []*yaml.Node) string {
	if len(nodes) == 0 {
		return ""
	}
	strs := make([]string, len(nodes))
	for i, node := range nodes {
		strs[i] = canonicalizeYAMLNode(node)
	}
	return canonicalizeStringSlice(strs)
}

// stripNullUnion removes an explicit null alternative from a union and returns the non-null schema.
// Returns nil if the value is definitely null or cannot be refined.
func stripNullUnion(s *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if s == nil {
		return nil
	}
	if getType(s) == "null" {
		return nil
	}

	// anyOf
	if len(s.AnyOf) > 0 {
		nonNulls := make([]*oas3.Schema, 0, len(s.AnyOf))
		for _, br := range s.AnyOf {
			if br == nil || br.Left == nil {
				continue
			}
			if getType(br.Left) == "null" {
				continue
			}
			nonNulls = append(nonNulls, br.Left)
		}
		if len(nonNulls) == 0 {
			return nil
		}
		if len(nonNulls) == 1 {
			return nonNulls[0]
		}
		return Union(nonNulls, opts)
	}

	// oneOf
	if len(s.OneOf) > 0 {
		nonNulls := make([]*oas3.Schema, 0, len(s.OneOf))
		for _, br := range s.OneOf {
			if br == nil || br.Left == nil {
				continue
			}
			if getType(br.Left) == "null" {
				continue
			}
			nonNulls = append(nonNulls, br.Left)
		}
		if len(nonNulls) == 0 {
			return nil
		}
		if len(nonNulls) == 1 {
			return nonNulls[0]
		}
		return Union(nonNulls, opts)
	}

	// Not a union with null; treat as already non-null
	return s
}

// refineVarRefs replaces any scope variable whose schema pointer matches 'old' with 'nw' (in-place).
func refineVarRefs(st *execState, old, nw *oas3.Schema) {
	if st == nil || old == nil || nw == nil {
		return
	}
	reboundCount := 0
	stackReboundCount := 0

	// Rebind scope variables
	for i := range st.scopes {
		frame := st.scopes[i]
		for k, v := range frame {
			if v == old {
				frame[k] = nw
				reboundCount++
				fmt.Printf("DEBUG refineVarRefs: rebound var '%s' from %p to %p\n", k, old, nw)
			}
		}
	}

	// Also rebind stack references
	for i := range st.stack {
		if st.stack[i].Schema == old {
			st.stack[i].Schema = nw
			stackReboundCount++
			fmt.Printf("DEBUG refineVarRefs: rebound stack[%d] from %p to %p\n", i, old, nw)
		} else {
			// DEBUG: Show why this stack entry wasn't rebound
			fmt.Printf("DEBUG refineVarRefs: stack[%d] NOT rebound (stack ptr=%p, old ptr=%p, same=%v)\n",
				i, st.stack[i].Schema, old, st.stack[i].Schema == old)
		}
	}

	if reboundCount == 0 && stackReboundCount == 0 {
		fmt.Printf("DEBUG refineVarRefs: NO variables or stack refs rebound (old ptr=%p not found)\n", old)
	} else {
		fmt.Printf("DEBUG refineVarRefs: rebound %d vars, %d stack refs\n", reboundCount, stackReboundCount)
	}
}
