package schemaexec

import (
	"fmt"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
	"gopkg.in/yaml.v3"
)

// extractPathsFromSchema converts a path array schema to concrete path segments
// Input: array schema where items are arrays of (string | integer)
// Output: list of path segment arrays
func extractPathsFromSchema(pathSchema *oas3.Schema) [][]PathSegment {
	if pathSchema == nil {
		return nil
	}

	schemaType := getType(pathSchema)

	if schemaType != "array" {
		return nil
	}

	// Handle case where path is a single path (array with prefixItems)
	if len(pathSchema.PrefixItems) > 0 {
		// Single path represented as tuple
		path := extractSinglePath(pathSchema)
		if path != nil {
			return [][]PathSegment{path}
		}
	}

	// Handle array-of-paths (accumulator of multiple paths)
	// Items.Left should be an array schema representing one path tuple
	if pathSchema.Items != nil && pathSchema.Items.Left != nil {
		itemSchema := pathSchema.Items.Left
		itemType := getType(itemSchema)

		if itemType == "array" {
			// Check if this is a union of multiple path tuples (AnyOf)
			if len(itemSchema.AnyOf) > 0 {
				// Multiple paths - extract each from AnyOf
				paths := make([][]PathSegment, 0, len(itemSchema.AnyOf))
				for _, anyOfSchema := range itemSchema.AnyOf {
					if anyOfSchema.Left != nil && getType(anyOfSchema.Left) == "array" {
						if path := extractSinglePath(anyOfSchema.Left); path != nil {
							paths = append(paths, path)
						}
					}
				}
				if len(paths) > 0 {
					return paths
				}
			}

			// Single path tuple
			path := extractSinglePath(itemSchema)
			if path != nil {
				return [][]PathSegment{path}
			}
		}
	}

	return nil
}

// extractSinglePath extracts path segments from a single path array schema
// For unionized paths (e.g., del(.a, .b)), prefixItems[0] might have anyOf
// In that case, we need to extract multiple paths
func extractSinglePath(pathSchema *oas3.Schema) []PathSegment {
	if pathSchema.PrefixItems == nil {
		return nil
	}

	// For now, just extract the first path
	// TODO: Handle unionized paths where prefixItems[0] has anyOf
	segments := make([]PathSegment, 0, len(pathSchema.PrefixItems))
	for _, itemSchema := range pathSchema.PrefixItems {
		if itemSchema.Left == nil {
			continue
		}

		seg := extractSegmentFromSchema(itemSchema.Left)
		segments = append(segments, seg)
	}

	return segments
}

// extractSegmentFromSchema converts a schema to a path segment
func extractSegmentFromSchema(schema *oas3.Schema) PathSegment {
	// Check for const string (property name)
	if getType(schema) == "string" && len(schema.Enum) > 0 {
		if schema.Enum[0].Kind == yaml.ScalarNode {
			return PathSegment{
				Key:        schema.Enum[0].Value,
				IsSymbolic: false,
			}
		}
	}

	// Check for const integer (array index)
	if getType(schema) == "integer" && len(schema.Enum) > 0 {
		if schema.Enum[0].Kind == yaml.ScalarNode {
			// Parse integer value
			var idx int64
			if err := schema.Enum[0].Decode(&idx); err == nil {
				return PathSegment{
					Key:        int(idx),
					IsSymbolic: false,
				}
			}
		}
	}

	// Non-const integer means wildcard (symbolic index)
	if getType(schema) == "integer" {
		return PathSegment{
			Key:        PathWildcard{},
			IsSymbolic: true,
		}
	}

	// Fallback: treat as symbolic
	return PathSegment{
		Key:        PathWildcard{},
		IsSymbolic: true,
	}
}

// deletePathFromSchema deletes a single path from the schema
func deletePathFromSchema(schema *oas3.Schema, path []PathSegment, opts SchemaExecOptions) *oas3.Schema {
	if schema == nil || len(path) == 0 {
		return schema
	}

	seg := path[0]

	// Last segment: perform deletion
	if len(path) == 1 {
		return deleteSegment(schema, seg, opts)
	}

	// Recursive: navigate to parent and delete child
	return navigateAndModify(schema, seg, path[1:], opts, func(child *oas3.Schema) *oas3.Schema {
		return deletePathFromSchema(child, path[1:], opts)
	})
}

// deleteSegment deletes a single segment (property or array element)
func deleteSegment(schema *oas3.Schema, seg PathSegment, opts SchemaExecOptions) *oas3.Schema {
	if seg.IsSymbolic {
		// Wildcard deletion: for arrays, clear all elements
		if getType(schema) == "array" {
			// Return empty array schema
			return ArrayType(Bottom())
		}
		// For objects, can't delete "all properties" - return unchanged
		return schema
	}

	// Property deletion
	if key, ok := seg.Key.(string); ok {
		return deleteProperty(schema, key)
	}

	// Array index deletion
	if idx, ok := seg.Key.(int); ok {
		return deleteArrayIndex(schema, idx)
	}

	return schema
}

// deleteProperty removes a property from an object schema
func deleteProperty(schema *oas3.Schema, propName string) *oas3.Schema {
	if schema == nil || getType(schema) != "object" {
		return schema
	}

	// Clone schema
	result := *schema

	// Remove from properties
	if schema.Properties != nil {
		newProps := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for k, v := range schema.Properties.All() {
			if k != propName {
				newProps.Set(k, v)
			}
		}
		result.Properties = newProps
	}

	// Remove from required
	if schema.Required != nil {
		newRequired := make([]string, 0, len(schema.Required))
		for _, req := range schema.Required {
			if req != propName {
				newRequired = append(newRequired, req)
			}
		}
		result.Required = newRequired
	}

	return &result
}

// deleteArrayIndex removes an element from an array schema
func deleteArrayIndex(schema *oas3.Schema, index int) *oas3.Schema {
	if schema == nil || getType(schema) != "array" {
		return schema
	}

	// For symbolic execution, deleting a specific index is complex
	// For now, keep schema unchanged (conservative)
	// TODO: Handle tuple schemas with prefixItems
	return schema
}

// navigatePathInSchema navigates to a path and returns the schema at that location
func navigatePathInSchema(schema *oas3.Schema, path []PathSegment, opts SchemaExecOptions) *oas3.Schema {
	if schema == nil || len(path) == 0 {
		return schema
	}

	seg := path[0]

	// Navigate one step
	var next *oas3.Schema
	if seg.IsSymbolic {
		// Wildcard: return union of all possible values
		if getType(schema) == "array" && schema.Items != nil && schema.Items.Left != nil {
			next = schema.Items.Left
		} else if getType(schema) == "object" {
			next = unionAllObjectValues(schema, opts)
		} else {
			return Bottom()
		}
	} else if key, ok := seg.Key.(string); ok {
		// Property access
		next = GetProperty(schema, key, opts)
	} else if idx, ok := seg.Key.(int); ok {
		// Array index
		next = getArrayElement(schema, idx, opts)
	} else {
		return Bottom()
	}

	// Continue navigation
	if len(path) == 1 {
		return next
	}
	return navigatePathInSchema(next, path[1:], opts)
}

// setPathInSchema sets a value at a path in the schema
func setPathInSchema(schema *oas3.Schema, path []PathSegment, value *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if schema == nil || len(path) == 0 {
		return value
	}

	seg := path[0]

	// Last segment: set value
	if len(path) == 1 {
		return setSegment(schema, seg, value, opts)
	}

	// Recursive: navigate and set at child
	return navigateAndModify(schema, seg, path[1:], opts, func(child *oas3.Schema) *oas3.Schema {
		return setPathInSchema(child, path[1:], value, opts)
	})
}

// setSegment sets a value at a single segment
func setSegment(schema *oas3.Schema, seg PathSegment, value *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	fmt.Printf("DEBUG setSegment: IsSymbolic=%v, Key type=%T, Key=%v\n", seg.IsSymbolic, seg.Key, seg.Key)

	if seg.IsSymbolic {
		// Dynamic object key: widen additionalProperties with the value
		if getType(schema) == "object" {
			fmt.Printf("DEBUG setSegment: symbolic segment on object, setting additionalProperties\n")
			return setDynamicProperty(schema, value, opts)
		}
		// For arrays, still unclear how to set "all elements" symbolically; keep conservative behavior
		fmt.Printf("DEBUG setSegment: symbolic segment on non-object (type=%s), returning unchanged\n", getType(schema))
		return schema
	}

	// Set property
	if key, ok := seg.Key.(string); ok {
		return setProperty(schema, key, value)
	}

	// Set array element (complex for symbolic execution)
	fmt.Printf("DEBUG setSegment: non-string key, returning unchanged\n")
	return schema
}

// setProperty sets or updates a property in an object schema
func setProperty(schema *oas3.Schema, propName string, value *oas3.Schema) *oas3.Schema {
	fmt.Printf("DEBUG setProperty: setting property '%s' (value type=%s) on schema type=%s\n",
		propName, getType(value), getType(schema))

	if schema == nil {
		// Create new object with property
		fmt.Printf("DEBUG setProperty: schema is nil, creating new object\n")
		return BuildObject(map[string]*oas3.Schema{propName: value}, []string{propName})
	}

	if getType(schema) != "object" {
		// Not an object - can't set property
		fmt.Printf("DEBUG setProperty: schema is not an object (type=%s), returning unchanged\n", getType(schema))
		return schema
	}

	// Clone and update
	result := *schema
	if schema.Properties == nil {
		result.Properties = sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	} else {
		// Clone properties
		newProps := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for k, v := range schema.Properties.All() {
			newProps.Set(k, v)
		}
		result.Properties = newProps
	}

	// Set property
	result.Properties.Set(propName, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](value))

	// Add to required if not already present
	found := false
	for _, req := range result.Required {
		if req == propName {
			found = true
			break
		}
	}
	if !found {
		result.Required = append(result.Required, propName)
	}

	return &result
}

// navigateAndModify navigates to a segment and applies a modification function
func navigateAndModify(schema *oas3.Schema, seg PathSegment, remainingPath []PathSegment, opts SchemaExecOptions, modifyFn func(*oas3.Schema) *oas3.Schema) *oas3.Schema {
	if seg.IsSymbolic {
		// Wildcard: apply to all elements
		if getType(schema) == "array" && schema.Items != nil && schema.Items.Left != nil {
			// Modify array item schema
			result := *schema
			newItems := modifyFn(schema.Items.Left)
			result.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newItems)
			return &result
		}
		// For objects, would need to modify all properties (complex)
		return schema
	}

	// Property navigation
	if key, ok := seg.Key.(string); ok {
		if getType(schema) != "object" {
			return schema
		}

		result := *schema
		if schema.Properties == nil {
			return schema
		}

		// Clone properties
		newProps := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for k, v := range schema.Properties.All() {
			if k == key && v.Left != nil {
				// Modify this property
				modified := modifyFn(v.Left)
				newProps.Set(k, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](modified))
			} else {
				newProps.Set(k, v)
			}
		}
		result.Properties = newProps
		return &result
	}

	// Array index navigation (complex for symbolic execution)
	return schema
}
