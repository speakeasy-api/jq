package schemaexec

import (
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
	schema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
	}
	if items != nil {
		schema.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](items)
	}
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
			// Check if required
			isRequired := false
			for _, req := range obj.Required {
				if req == key {
					isRequired = true
					break
				}
			}

			// If not required, might be null
			if !isRequired {
				// For Phase 1, just return the property schema
				// Phase 2 will properly union with null
				if propSchema.Left != nil {
					return propSchema.Left
				}
			}

			if propSchema.Left != nil {
				return propSchema.Left
			}
		}
	}

	// Check additionalProperties
	if obj.AdditionalProperties != nil && obj.AdditionalProperties.Left != nil {
		return obj.AdditionalProperties.Left
	}

	// Not found - return null
	return ConstNull()
}

// Union creates a schema that matches any of the input schemas (anyOf).
// Implements proper flattening, deduplication, and widening when limits exceeded.
func Union(schemas []*oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	// Filter out nil/Bottom
	filtered := make([]*oas3.Schema, 0, len(schemas))
	for _, s := range schemas {
		if s != nil {
			filtered = append(filtered, s)
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
		if s.AnyOf != nil && len(s.AnyOf) > 0 {
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

	// Deduplicate
	deduped := deduplicateSchemas(flattened)

	// Try to merge compatible objects into a single object
	if merged := tryMergeObjects(deduped, opts); merged != nil {
		return merged
	}

	// Check if we exceed the anyOf limit
	if len(deduped) > opts.AnyOfLimit {
		return widenUnion(deduped, opts)
	}

	// Create anyOf
	anyOf := make([]*oas3.JSONSchema[oas3.Referenceable], len(deduped))
	for i, s := range deduped {
		anyOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](s)
	}

	return &oas3.Schema{
		AnyOf: anyOf,
	}
}

// tryMergeObjects attempts to merge multiple object schemas into a single object
// Returns nil if merging is not safe
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

	// All must have identical required sets (critical for correctness)
	if !haveSameRequiredSets(schemas) {
		return nil
	}

	// Collect all property names across all branches
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

		if len(propSchemas) > 0 {
			// Recursively union the property schemas
			unionSchema := Union(propSchemas, opts)
			// Unwrap single-branch anyOf
			if unionSchema.AnyOf != nil && len(unionSchema.AnyOf) == 1 && unionSchema.AnyOf[0].GetLeft() != nil {
				unionSchema = unionSchema.AnyOf[0].GetLeft()
			}
			mergedProps[propName] = unionSchema
		}
	}

	// Build merged object
	required := []string{}
	if len(schemas) > 0 && schemas[0].Required != nil {
		required = schemas[0].Required
	}

	return BuildObject(mergedProps, required)
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

	// Otherwise, keep all unique schemas (no structural dedup to avoid ordering issues)
	return unique
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
		if s.Enum == nil || len(s.Enum) == 0 {
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

// BuildObject creates an object schema from property map.
// Simplified version for Phase 1.
func BuildObject(props map[string]*oas3.Schema, required []string) *oas3.Schema {
	propMap := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	for k, v := range props {
		propMap.Set(k, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](v))
	}

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
	if s.AnyOf != nil && len(s.AnyOf) > 0 {
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

	// Merge required lists
	requiredMap := make(map[string]bool)
	for _, r := range a.Required {
		requiredMap[r] = true
	}
	for _, r := range b.Required {
		requiredMap[r] = true
	}

	required := make([]string, 0, len(requiredMap))
	for k := range requiredMap {
		required = append(required, k)
	}

	return &oas3.Schema{
		Type:       oas3.NewTypeFromString(oas3.SchemaTypeObject),
		Properties: propMap,
		Required:   required,
	}
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
