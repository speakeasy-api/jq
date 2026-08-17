package schemaexec

import (
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
	"gopkg.in/yaml.v3"
)

const allElementsPathFormat = "jq-path-all-elements"

func allElementsPathSchema() *oas3.Schema {
	result := IntegerType()
	marker := allElementsPathFormat
	result.Format = &marker
	return result
}

func isAllElementsSegment(seg PathSegment) bool {
	_, ok := seg.Key.(PathAllElements)
	return seg.IsSymbolic && ok
}

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
	if itemSchema := resolvedLeft(pathSchema.Items); itemSchema != nil {
		itemType := getType(itemSchema)

		if itemType == "array" {
			// Check if this is a union of multiple path tuples (AnyOf)
			if len(itemSchema.AnyOf) > 0 {
				// Multiple paths - extract each from AnyOf
				paths := make([][]PathSegment, 0, len(itemSchema.AnyOf))
				for _, anyOfSchema := range itemSchema.AnyOf {
					if left := resolvedLeft(anyOfSchema); left != nil && getType(left) == "array" {
						if path := extractSinglePath(left); path != nil {
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
		left := resolvedLeft(itemSchema)
		if left == nil {
			continue
		}

		seg := extractSegmentFromSchema(left)
		segments = append(segments, seg)
	}

	return segments
}

// extractSegmentFromSchema converts a schema to a path segment
func extractSegmentFromSchema(schema *oas3.Schema) PathSegment {
	if getType(schema) == "integer" && schema.Format != nil && *schema.Format == allElementsPathFormat {
		return PathSegment{Key: PathAllElements{}, IsSymbolic: true}
	}

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
		if getType(schema) == "array" {
			if isAllElementsSegment(seg) {
				return ArrayType(Bottom())
			}
			// Deleting an unknown index may shift every tuple position and may
			// reduce the length by one, but does not necessarily empty the array.
			result := eraseArrayPositions(schema, opts)
			result.MinItems = nil
			return result
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
		// Both all-elements and unknown-index reads may observe any element.
		if getType(schema) == "array" {
			next = arrayElementUnion(schema, opts)
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
	if seg.IsSymbolic {
		// Dynamic object key: widen additionalProperties with the value
		if getType(schema) == "object" {
			return setDynamicProperty(schema, value, opts)
		}
		if getType(schema) == "array" {
			if isAllElementsSegment(seg) {
				return replaceAllArrayElements(schema, value, opts)
			}
			return widenArrayAfterWrite(schema, value, nil, opts)
		}
		return schema
	}

	// Set property
	if key, ok := seg.Key.(string); ok {
		return setProperty(schema, key, value)
	}

	if idx, ok := seg.Key.(int); ok && getType(schema) == "array" {
		return widenArrayAfterWrite(schema, value, &idx, opts)
	}

	return schema
}

func replaceAllArrayElements(schema, value *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if schema == nil || getType(schema) != "array" {
		return schema
	}
	result := eraseArrayPositions(schema, opts)
	if value == nil {
		return result
	}
	result.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](value)
	return result
}

// widenArrayAfterWrite drops tuple positions invalidated by an indexed write.
// The homogeneous item type includes prior positions, the written value, and
// null padding that setpath may insert when extending past the end.
func widenArrayAfterWrite(schema, value *oas3.Schema, index *int, opts SchemaExecOptions) *oas3.Schema {
	if schema == nil || getType(schema) != "array" {
		return schema
	}
	result := eraseArrayPositions(schema, opts)
	items := arrayElementUnion(schema, opts)
	result.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Union([]*oas3.Schema{
		items,
		value,
		ConstNull(),
	}, opts))
	result.MaxItems = nil
	if index != nil && *index >= 0 {
		writtenLength := int64(*index + 1)
		if result.MinItems == nil || *result.MinItems < writtenLength {
			result.MinItems = &writtenLength
		}
	}
	return result
}

// setProperty sets or updates a property in an object schema
func setProperty(schema *oas3.Schema, propName string, value *oas3.Schema) *oas3.Schema {
	if schema == nil {
		// Create new object with property
		return BuildObject(map[string]*oas3.Schema{propName: value}, []string{propName})
	}

	if getType(schema) != "object" {
		// Not an object - can't set property
		return schema
	}

	// Clone and update
	result := *schema
	result.Required = append([]string(nil), schema.Required...)
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
		if getType(schema) == "array" {
			newItems := modifyFn(arrayElementUnion(schema, opts))
			if isAllElementsSegment(seg) {
				return replaceAllArrayElements(schema, newItems, opts)
			}
			return widenArrayAfterWrite(schema, newItems, nil, opts)
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

	if idx, ok := seg.Key.(int); ok && getType(schema) == "array" {
		child := getArrayElement(schema, idx, opts)
		modified := modifyFn(child)
		return widenArrayAfterWrite(schema, modified, &idx, opts)
	}

	return schema
}
