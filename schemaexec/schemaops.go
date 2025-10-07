package schemaexec

import (
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
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: string(rune(n)), Tag: "!!float"}
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
// Simplified version for Phase 1.
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

	// Create anyOf
	anyOf := make([]*oas3.JSONSchema[oas3.Referenceable], len(filtered))
	for i, s := range filtered {
		anyOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](s)
	}

	return &oas3.Schema{
		AnyOf: anyOf,
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
func getType(s *oas3.Schema) string {
	if s == nil {
		return ""
	}
	types := s.GetType()
	if len(types) == 0 {
		return ""
	}
	return string(types[0])
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
