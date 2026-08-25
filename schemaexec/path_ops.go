package schemaexec

import (
	"regexp"
	"sort"
	"strings"

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

func stripInternalSchemaMarkers(schema *oas3.Schema) *oas3.Schema {
	if !hasInternalSchemaMarker(schema, make(map[*oas3.Schema]bool)) {
		return schema
	}
	return stripInternalSchemaMarkersSeen(schema, make(map[*oas3.Schema]*oas3.Schema))
}

func hasInternalSchemaMarker(schema *oas3.Schema, seen map[*oas3.Schema]bool) bool {
	if schema == nil || seen[schema] {
		return false
	}
	seen[schema] = true
	if schema.Format != nil && *schema.Format == allElementsPathFormat {
		return true
	}
	hasWrapper := func(wrapper *oas3.JSONSchema[oas3.Referenceable]) bool {
		return hasInternalSchemaMarker(resolvedLeft(wrapper), seen)
	}
	for _, wrappers := range [][]*oas3.JSONSchema[oas3.Referenceable]{schema.PrefixItems, schema.AllOf, schema.AnyOf, schema.OneOf} {
		for _, wrapper := range wrappers {
			if hasWrapper(wrapper) {
				return true
			}
		}
	}
	for _, wrapper := range []*oas3.JSONSchema[oas3.Referenceable]{
		schema.Items, schema.Contains, schema.AdditionalProperties, schema.PropertyNames,
		schema.UnevaluatedItems, schema.UnevaluatedProperties, schema.ContentSchema,
		schema.Not, schema.If, schema.Then, schema.Else,
	} {
		if hasWrapper(wrapper) {
			return true
		}
	}
	for _, values := range []*sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]]{
		schema.Properties, schema.PatternProperties, schema.DependentSchemas, schema.Defs,
	} {
		if values == nil {
			continue
		}
		for _, wrapper := range values.All() {
			if hasWrapper(wrapper) {
				return true
			}
		}
	}
	return false
}

func stripInternalSchemaMarkersSeen(schema *oas3.Schema, memo map[*oas3.Schema]*oas3.Schema) *oas3.Schema {
	if schema == nil {
		return nil
	}
	if result, ok := memo[schema]; ok {
		return result
	}
	result := cloneSchema(schema)
	memo[schema] = result
	if result.Format != nil && *result.Format == allElementsPathFormat {
		result.Format = nil
	}

	stripWrapper := func(wrapper *oas3.JSONSchema[oas3.Referenceable]) *oas3.JSONSchema[oas3.Referenceable] {
		if wrapper == nil {
			return wrapper
		}
		if value, ok := resolvedBooleanSchema(wrapper); ok {
			return oas3.NewJSONSchemaFromBool(value)
		}
		left := resolvedLeft(wrapper)
		if left == nil {
			return wrapper
		}
		return oas3.NewJSONSchemaFromSchema[oas3.Referenceable](stripInternalSchemaMarkersSeen(left, memo))
	}
	stripSlice := func(wrappers []*oas3.JSONSchema[oas3.Referenceable]) []*oas3.JSONSchema[oas3.Referenceable] {
		if wrappers == nil {
			return nil
		}
		cleaned := make([]*oas3.JSONSchema[oas3.Referenceable], len(wrappers))
		for i, wrapper := range wrappers {
			cleaned[i] = stripWrapper(wrapper)
		}
		return cleaned
	}
	stripMap := func(values *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]]) *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]] {
		if values == nil {
			return nil
		}
		cleaned := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for key, wrapper := range values.All() {
			cleaned.Set(key, stripWrapper(wrapper))
		}
		return cleaned
	}

	result.Properties = stripMap(schema.Properties)
	result.PatternProperties = stripMap(schema.PatternProperties)
	result.DependentSchemas = stripMap(schema.DependentSchemas)
	result.Defs = stripMap(schema.Defs)
	result.Items = stripWrapper(schema.Items)
	result.PrefixItems = stripSlice(schema.PrefixItems)
	result.Contains = stripWrapper(schema.Contains)
	result.AdditionalProperties = stripWrapper(schema.AdditionalProperties)
	result.PropertyNames = stripWrapper(schema.PropertyNames)
	result.UnevaluatedItems = stripWrapper(schema.UnevaluatedItems)
	result.UnevaluatedProperties = stripWrapper(schema.UnevaluatedProperties)
	result.ContentSchema = stripWrapper(schema.ContentSchema)
	result.AllOf = stripSlice(schema.AllOf)
	result.AnyOf = stripSlice(schema.AnyOf)
	result.OneOf = stripSlice(schema.OneOf)
	result.Not = stripWrapper(schema.Not)
	result.If = stripWrapper(schema.If)
	result.Then = stripWrapper(schema.Then)
	result.Else = stripWrapper(schema.Else)
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

	// An exact empty tuple ({type: array, maxItems: 0} with no items and no
	// prefixItems) is the EMPTY path, not "no paths": jq's setpath([]; v)
	// replaces the whole input with v and getpath([]) is identity. Both are
	// realized by returning one zero-length path (setPathInSchema and
	// navigatePathInSchema already map an empty path to the value/input).
	if pathSchema.MaxItems != nil && *pathSchema.MaxItems == 0 &&
		pathSchema.Items == nil && len(pathSchema.PrefixItems) == 0 {
		return [][]PathSegment{{}}
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

// weakDeletePathFromSchema over-approximates a delete that only POSSIBLY
// happens: the result must admit both the untouched and the deleted concrete
// value. Rather than building Union(input, deleted) — which keeps two anyOf
// branches alive per update round and grows exponentially through reduce
// fixpoints — it weakens in place: member schemas survive, the targeted key
// stops being required, and count/value facets the delete could invalidate
// are dropped. The in-place form is a superset of both outcomes, hence sound.
func weakDeletePathFromSchema(schema *oas3.Schema, path []PathSegment, opts SchemaExecOptions) *oas3.Schema {
	if schema == nil || len(path) == 0 {
		return schema
	}
	if len(path) == 1 {
		return weakDeleteSegment(schema, path[0], opts)
	}
	// Intermediate navigation installs the weakened child, which is a
	// superset of the untouched child, so the strong set stays sound.
	return navigateAndModify(schema, path[0], path[1:], opts, false, func(child *oas3.Schema) *oas3.Schema {
		return weakDeletePathFromSchema(child, path[1:], opts)
	})
}

func weakDeleteSegment(schema *oas3.Schema, seg PathSegment, opts SchemaExecOptions) *oas3.Schema {
	if seg.IsSymbolic {
		// The symbolic delete is already weak: values survive, required and
		// lower bounds are dropped.
		return deleteSegment(schema, seg, opts)
	}
	if key, ok := seg.Key.(string); ok && getType(schema) == "object" {
		result := cloneSchema(schema)
		required := make([]string, 0, len(schema.Required))
		for _, name := range schema.Required {
			if name != key {
				required = append(required, name)
			}
		}
		result.Required = required
		result.MinProperties = nil
		result.Const = nil
		result.Enum = nil
		result.DependentSchemas = nil
		return result
	}
	if _, ok := seg.Key.(int); ok && getType(schema) == "array" {
		result := arrayWriteSurvivors(eraseArrayPositions(schema, opts))
		result.MinItems = nil
		return result
	}
	return schema
}

// maxExpandedDeletePaths bounds the cartesian expansion of enum-headed path
// tuples; beyond it the caller falls back to weakDeleteUnknown.
const maxExpandedDeletePaths = 64

// expandDeletePaths recovers delete paths from a paths-array schema,
// splitting them into DEFINITE paths (every segment is a single constant, so
// the tuple names exactly one collected path) and POSSIBLE variants expanded
// from multi-value enum segments. Disjunctive merging can flatten several
// collected tuples into one tuple whose head is a string enum; each enum
// value was some collected path's key, but no single value is definitely the
// deleted one, so expanded variants must only ever be applied weakly.
// complete=false means the argument's paths could not be fully represented.
func expandDeletePaths(pathsArg *oas3.Schema) (definite, possible [][]PathSegment, complete bool) {
	if pathsArg == nil || getType(pathsArg) != "array" {
		return nil, nil, false
	}
	complete = true
	addTuple := func(tuple *oas3.Schema) {
		paths, multi, ok := expandPathTuple(tuple)
		if !ok {
			complete = false
			return
		}
		if multi {
			possible = append(possible, paths...)
		} else {
			definite = append(definite, paths...)
		}
	}

	if len(pathsArg.PrefixItems) > 0 {
		addTuple(pathsArg)
		return definite, possible, complete
	}
	if itemSchema := resolvedLeft(pathsArg.Items); itemSchema != nil && getType(itemSchema) == "array" {
		if len(itemSchema.AnyOf) > 0 {
			for _, wrapper := range itemSchema.AnyOf {
				if left := resolvedLeft(wrapper); left != nil && getType(left) == "array" {
					addTuple(left)
				} else {
					complete = false
				}
			}
			return definite, possible, complete
		}
		addTuple(itemSchema)
		return definite, possible, complete
	}
	return nil, nil, false
}

func expandPathTuple(tuple *oas3.Schema) (paths [][]PathSegment, multi bool, ok bool) {
	if tuple == nil || tuple.PrefixItems == nil {
		return nil, false, false
	}
	paths = [][]PathSegment{{}}
	for _, wrapper := range tuple.PrefixItems {
		left := resolvedLeft(wrapper)
		if left == nil {
			continue
		}
		candidates, candidatesOK := pathSegmentCandidates(left)
		if !candidatesOK {
			return nil, false, false
		}
		if len(candidates) > 1 {
			multi = true
		}
		if len(paths)*len(candidates) > maxExpandedDeletePaths {
			return nil, false, false
		}
		next := make([][]PathSegment, 0, len(paths)*len(candidates))
		for _, prefix := range paths {
			for _, candidate := range candidates {
				extended := append(append([]PathSegment(nil), prefix...), candidate)
				next = append(next, extended)
			}
		}
		paths = next
	}
	return paths, multi, true
}

// pathSegmentCandidates expands one path-tuple position into its candidate
// segments. ok=false means a multi-value enum member could not be decoded
// (nil, non-scalar, or failed decoding); falling back to Enum[0] there would
// silently drop candidates, so the caller must treat the whole tuple as
// unextractable and take the weak unknown-delete fallback.
func pathSegmentCandidates(schema *oas3.Schema) ([]PathSegment, bool) {
	isAllElements := getType(schema) == "integer" && schema.Format != nil && *schema.Format == allElementsPathFormat
	if !isAllElements && len(schema.Enum) > 1 {
		switch getType(schema) {
		case "string":
			out := make([]PathSegment, 0, len(schema.Enum))
			for _, node := range schema.Enum {
				if node == nil || node.Kind != yaml.ScalarNode {
					return nil, false
				}
				out = append(out, PathSegment{Key: node.Value})
			}
			return out, true
		case "integer":
			out := make([]PathSegment, 0, len(schema.Enum))
			for _, node := range schema.Enum {
				var idx int64
				if node == nil || node.Kind != yaml.ScalarNode || node.Decode(&idx) != nil {
					return nil, false
				}
				out = append(out, PathSegment{Key: int(idx)})
			}
			return out, true
		}
	}
	return []PathSegment{extractSegmentFromSchema(schema)}, true
}

// sortDeletePathsDescending orders paths in descending jq path order — the
// order jq applies deletes in (from the end), so an earlier delete never
// shifts the indices a later delete targets.
func sortDeletePathsDescending(paths [][]PathSegment) {
	sort.SliceStable(paths, func(i, j int) bool {
		return compareDeletePaths(paths[i], paths[j]) > 0
	})
}

func compareDeletePaths(a, b []PathSegment) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := compareDeleteSegments(a[i], b[i]); c != 0 {
			return c
		}
	}
	// Equal prefix: the longer path sorts greater so the descending order
	// deletes it first — the shorter (ancestor) path deletes last.
	switch {
	case len(a) > len(b):
		return 1
	case len(a) < len(b):
		return -1
	}
	return 0
}

func compareDeleteSegments(a, b PathSegment) int {
	aIdx, aInt := a.Key.(int)
	bIdx, bInt := b.Key.(int)
	aKey, aStr := a.Key.(string)
	bKey, bStr := b.Key.(string)
	switch {
	case aInt && bInt:
		switch {
		case aIdx > bIdx:
			return 1
		case aIdx < bIdx:
			return -1
		}
		return 0
	case aStr && bStr:
		return strings.Compare(aKey, bKey)
	case aInt && bStr:
		// jq orders numbers before strings.
		return -1
	case aStr && bInt:
		return 1
	}
	// Symbolic segments have no concrete order; treat as equal so the stable
	// sort keeps their collected order.
	return 0
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
	return navigateAndModify(schema, seg, path[1:], opts, false, func(child *oas3.Schema) *oas3.Schema {
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
			// reduce the length by one, but does not necessarily empty the
			// array. Element-value facets do not survive the removal.
			result := arrayWriteSurvivors(eraseArrayPositions(schema, opts))
			result.MinItems = nil
			return result
		}
		if getType(schema) == "object" {
			// Deleting an unknown key: surviving members keep their schemas,
			// but no key is guaranteed present anymore, the key count may
			// shrink, and value-dependent facets described the old shape.
			return &oas3.Schema{
				Type:                 oas3.NewTypeFromString(oas3.SchemaTypeObject),
				Properties:           schema.Properties,
				PatternProperties:    schema.PatternProperties,
				AdditionalProperties: schema.AdditionalProperties,
				MaxProperties:        schema.MaxProperties,
				Nullable:             schema.Nullable,
			}
		}
		return schema
	}

	// Property deletion
	if key, ok := seg.Key.(string); ok {
		return deleteProperty(schema, key)
	}

	// Array index deletion
	if idx, ok := seg.Key.(int); ok {
		return deleteArrayIndex(schema, idx, opts)
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

	// The removal invalidates container-level value facets and lowers the
	// guaranteed key count by one.
	result.Const = nil
	result.Enum = nil
	result.DependentSchemas = nil
	if schema.MinProperties != nil {
		value := *schema.MinProperties - 1
		if value < 0 {
			value = 0
		}
		result.MinProperties = &value
	}

	return &result
}

// deleteArrayIndex removes an element from an array schema
func deleteArrayIndex(schema *oas3.Schema, index int, opts SchemaExecOptions) *oas3.Schema {
	if schema == nil || getType(schema) != "array" {
		return schema
	}

	normalized := index
	if normalized < 0 && schema.MinItems != nil && schema.MaxItems != nil && *schema.MinItems == *schema.MaxItems {
		normalized = int(*schema.MaxItems) + normalized
	}
	if normalized < 0 {
		result := eraseArrayPositions(schema, opts)
		decrementArrayBounds(result, index)
		return result
	}

	if schema.MinItems != nil && schema.MaxItems != nil && *schema.MinItems == *schema.MaxItems &&
		normalized < len(schema.PrefixItems) {
		result := cloneSchema(schema)
		result.PrefixItems = append([]*oas3.JSONSchema[oas3.Referenceable](nil), schema.PrefixItems[:normalized]...)
		result.PrefixItems = append(result.PrefixItems, schema.PrefixItems[normalized+1:]...)
		decrementArrayBounds(result, normalized)
		return result
	}

	result := eraseArrayPositions(schema, opts)
	decrementArrayBounds(result, normalized)
	return result
}

func decrementArrayBounds(schema *oas3.Schema, index int) {
	if schema == nil {
		return
	}
	if index < 0 {
		if schema.MinItems != nil && *schema.MinItems > 0 {
			value := *schema.MinItems - 1
			schema.MinItems = &value
		}
		if schema.MaxItems != nil && *schema.MaxItems > 0 {
			value := *schema.MaxItems - 1
			schema.MaxItems = &value
		}
		return
	}
	if schema.MinItems != nil && int64(index) < *schema.MinItems {
		value := *schema.MinItems - 1
		schema.MinItems = &value
	}
	if schema.MaxItems != nil && int64(index) < *schema.MaxItems {
		value := *schema.MaxItems - 1
		schema.MaxItems = &value
	}
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
	return navigateAndModify(schema, seg, path[1:], opts, true, func(child *oas3.Schema) *oas3.Schema {
		return setPathInSchema(child, path[1:], value, opts)
	})
}

// setSegment sets a value at a single segment
func setSegment(schema *oas3.Schema, seg PathSegment, value *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	if seg.IsSymbolic {
		if getType(schema) == "object" {
			if isAllElementsSegment(seg) {
				// .[] writes through every value: each one is definitely
				// replaced, so no union with the previous value schemas.
				return mapObjectValues(schema, opts, func(*oas3.Schema) *oas3.Schema {
					return value
				})
			}
			// Dynamic object key: widen additionalProperties with the value
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
	result := arrayWriteSurvivors(eraseArrayPositions(schema, opts))
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
	result := arrayWriteSurvivors(eraseArrayPositions(schema, opts))
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
func navigateAndModify(schema *oas3.Schema, seg PathSegment, remainingPath []PathSegment, opts SchemaExecOptions, createMissing bool, modifyFn func(*oas3.Schema) *oas3.Schema) *oas3.Schema {
	if seg.IsSymbolic {
		if getType(schema) == "array" {
			newItems := modifyFn(arrayElementUnion(schema, opts))
			if isAllElementsSegment(seg) {
				return replaceAllArrayElements(schema, newItems, opts)
			}
			return widenArrayAfterWrite(schema, newItems, nil, opts)
		}
		if getType(schema) == "object" {
			if isAllElementsSegment(seg) {
				// .[] traverses every existing value, so each value schema is
				// definitely rewritten by the remaining path's modification.
				return mapObjectValues(schema, opts, modifyFn)
			}
			candidates := []*oas3.Schema{modifyFn(missingPathContainer(remainingPath))}
			if existing := dynamicObjectValueUnion(schema, opts); existing != nil {
				candidates = append(candidates, modifyFn(existing))
			}
			return setDynamicProperty(schema, Union(candidates, opts), opts)
		}
		return schema
	}

	// Property navigation
	if key, ok := seg.Key.(string); ok {
		if getType(schema) != "object" {
			return schema
		}

		result := *schema

		// Clone properties
		newProps := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		found := false
		childModified := false
		if schema.Properties != nil {
			for k, v := range schema.Properties.All() {
				if k == key {
					found = true
				}
				if k == key && resolvedLeft(v) != nil {
					// Modify this property
					original := resolvedLeft(v)
					modified := modifyFn(original)
					childModified = childModified || modified != original
					newProps.Set(k, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](modified))
				} else {
					newProps.Set(k, v)
				}
			}
		}
		if createMissing && !found {
			child := missingPathContainer(remainingPath)
			newProps.Set(key, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](modifyFn(child)))
			result.Required = append([]string(nil), schema.Required...)
			if !isRequired(result.Required, key) {
				result.Required = append(result.Required, key)
			}
		} else if createMissing && found && !isRequired(result.Required, key) {
			result.Required = append(append([]string(nil), schema.Required...), key)
		} else if !createMissing && !found {
			// Delete traversal into a key covered only by a dynamic-key facet
			// (patternProperties/additionalProperties): silently returning the
			// input would skip the delete for that member. Weakly update the
			// covering facet instead.
			childModified = modifyCoveringDynamicFacet(&result, key, opts, modifyFn) || childModified
		}
		if childModified {
			// Interior modification invalidates container-level value facets.
			result.Const = nil
			result.Enum = nil
			result.DependentSchemas = nil
		}
		result.Properties = newProps
		return &result
	}

	if idx, ok := seg.Key.(int); ok && getType(schema) == "array" {
		child := getArrayElement(schema, idx, opts)
		modified := modifyFn(child)
		// Delete traversal (createMissing=false) must not bump MinItems to
		// idx+1: deletes never pad, so a concrete array shorter than idx+1
		// passes through unchanged and must stay admitted.
		index := &idx
		if !createMissing {
			index = nil
		}
		return widenArrayAfterWrite(schema, modified, index, opts)
	}

	return schema
}

// modifyCoveringDynamicFacet applies modifyFn to the value schema of the
// dynamic-key facet covering key (the first matching patternProperties entry,
// else a schema-valued additionalProperties) and installs Union(old, modified)
// back into that facet. The union keeps the update weak: other dynamic keys
// matched by the same facet keep their old shape. Reports whether a facet was
// updated. result is a shallow copy of the input, so the facet maps it points
// at are shared and must be replaced, never mutated in place.
func modifyCoveringDynamicFacet(result *oas3.Schema, key string, opts SchemaExecOptions, modifyFn func(*oas3.Schema) *oas3.Schema) bool {
	install := func(old *oas3.Schema) (*oas3.JSONSchema[oas3.Referenceable], bool) {
		modified := modifyFn(old)
		if modified == old {
			return nil, false
		}
		return oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Union([]*oas3.Schema{old, modified}, opts)), true
	}
	if result.PatternProperties != nil {
		for pattern, wrapper := range result.PatternProperties.All() {
			re, err := regexp.Compile(pattern)
			if err != nil || !re.MatchString(key) {
				continue
			}
			old := resolvedLeft(wrapper)
			if old == nil {
				return false
			}
			updated, ok := install(old)
			if !ok {
				return false
			}
			newPatterns := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
			for k, v := range result.PatternProperties.All() {
				if k == pattern {
					newPatterns.Set(k, updated)
				} else {
					newPatterns.Set(k, v)
				}
			}
			result.PatternProperties = newPatterns
			return true
		}
	}
	if old := resolvedLeft(result.AdditionalProperties); old != nil {
		if updated, ok := install(old); ok {
			result.AdditionalProperties = updated
			return true
		}
	}
	return false
}

// weakDeleteUnknown over-approximates a delpaths whose path set could not be
// extracted but may be non-empty: some member at some depth may have been
// removed. Member schemas survive, but no key stays required, count and
// length lower bounds are dropped, tuple positions may shift, and
// value-dependent container facets (const/enum, contains, uniqueItems) no
// longer hold. Returning the input unchanged here would be unsound: the
// deleted concrete instance would violate the retained required/min facets.
func weakDeleteUnknown(schema *oas3.Schema, opts SchemaExecOptions, seen map[*oas3.Schema]*oas3.Schema) *oas3.Schema {
	if schema == nil {
		return nil
	}
	if cached, ok := seen[schema]; ok {
		return cached
	}
	var result *oas3.Schema
	if getType(schema) == "array" {
		result = cloneSchema(eraseArrayPositions(schema, opts))
	} else {
		result = cloneSchema(schema)
	}
	seen[schema] = result

	mapWrapper := func(wrapper *oas3.JSONSchema[oas3.Referenceable]) *oas3.JSONSchema[oas3.Referenceable] {
		if wrapper == nil {
			return nil
		}
		value, possible := schemaFacetValue(wrapper, opts)
		if !possible {
			return wrapper
		}
		if isTopSchema(value) {
			return wrapper
		}
		return oas3.NewJSONSchemaFromSchema[oas3.Referenceable](weakDeleteUnknown(value, opts, seen))
	}
	mapWrapperMap := func(values *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]]) *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]] {
		if values == nil {
			return nil
		}
		mapped := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for key, wrapper := range values.All() {
			mapped.Set(key, mapWrapper(wrapper))
		}
		return mapped
	}
	mapWrappers := func(wrappers []*oas3.JSONSchema[oas3.Referenceable]) []*oas3.JSONSchema[oas3.Referenceable] {
		if wrappers == nil {
			return nil
		}
		mapped := make([]*oas3.JSONSchema[oas3.Referenceable], len(wrappers))
		for i, wrapper := range wrappers {
			mapped[i] = mapWrapper(wrapper)
		}
		return mapped
	}

	result.Const = nil
	result.Enum = nil
	switch getType(schema) {
	case "object":
		result.Required = nil
		result.MinProperties = nil
		result.DependentSchemas = nil
		result.Properties = mapWrapperMap(schema.Properties)
		result.PatternProperties = mapWrapperMap(schema.PatternProperties)
		result.AdditionalProperties = mapWrapper(schema.AdditionalProperties)
	case "array":
		result.MinItems = nil
		result.UniqueItems = nil
		result.Contains = nil
		result.MinContains = nil
		result.MaxContains = nil
		result.Items = mapWrapper(result.Items)
	}
	result.AnyOf = mapWrappers(schema.AnyOf)
	result.OneOf = mapWrappers(schema.OneOf)
	// A delete at unknown depth can invalidate conditional/negation facets
	// (they describe the pre-delete value), so they cannot be carried over.
	result.Not = nil
	result.If = nil
	result.Then = nil
	result.Else = nil
	return result
}

// arrayWriteSurvivors rebuilds an array schema keeping only the facets an
// element write leaves valid: element type and length bounds. Everything else
// (const/enum, uniqueItems, contains, combinators) depends on the concrete
// element values, which the write just changed, so nothing not listed here
// may survive into the post-write schema.
func arrayWriteSurvivors(arr *oas3.Schema) *oas3.Schema {
	return &oas3.Schema{
		Type:     oas3.NewTypeFromString(oas3.SchemaTypeArray),
		Items:    arr.Items,
		MinItems: arr.MinItems,
		MaxItems: arr.MaxItems,
		Nullable: arr.Nullable,
	}
}

// mapObjectValues rewrites every object value schema through fn, modeling a
// definite all-elements write (.[] = v, .[].x |= f): declared, pattern, and
// additional property values are each replaced, never unioned with their old
// selves. Impossible (boolean false) facets stay impossible.
func mapObjectValues(schema *oas3.Schema, opts SchemaExecOptions, fn func(*oas3.Schema) *oas3.Schema) *oas3.Schema {
	if schema == nil || getType(schema) != "object" {
		return schema
	}
	// The post-write schema keeps only what a value rewrite leaves intact:
	// the key set (required, counts, propertyNames) and the member schemas
	// this function rewrites below. Value-dependent facets (const/enum,
	// dependentSchemas, combinators) described the old values and do not
	// survive.
	result := &oas3.Schema{
		Type:          oas3.NewTypeFromString(oas3.SchemaTypeObject),
		Required:      schema.Required,
		MinProperties: schema.MinProperties,
		MaxProperties: schema.MaxProperties,
		PropertyNames: schema.PropertyNames,
		Nullable:      schema.Nullable,
	}
	apply := func(wrapper *oas3.JSONSchema[oas3.Referenceable]) *oas3.JSONSchema[oas3.Referenceable] {
		value, possible := schemaFacetValue(wrapper, opts)
		if !possible {
			return wrapper
		}
		mapped := fn(value)
		if mapped == nil {
			return oas3.NewJSONSchemaFromBool(false)
		}
		return oas3.NewJSONSchemaFromSchema[oas3.Referenceable](mapped)
	}
	mapAll := func(values *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]]) *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]] {
		if values == nil {
			return nil
		}
		mapped := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for key, wrapper := range values.All() {
			mapped.Set(key, apply(wrapper))
		}
		return mapped
	}
	result.Properties = mapAll(schema.Properties)
	result.PatternProperties = mapAll(schema.PatternProperties)
	if schema.AdditionalProperties != nil {
		result.AdditionalProperties = apply(schema.AdditionalProperties)
	} else if opts.Semantics == SchemaSemanticsRaw {
		// Raw semantics: absent AP admits arbitrary extras, and the write
		// rewrites their values too.
		if mapped := fn(Top()); mapped != nil && !isTopSchema(mapped) {
			result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](mapped)
		}
	}
	return result
}

func dynamicObjectValueUnion(schema *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	values := make([]*oas3.Schema, 0)
	if schema.Properties != nil {
		for _, wrapper := range schema.Properties.All() {
			if value, possible := schemaFacetValue(wrapper, opts); possible {
				values = append(values, value)
			}
		}
	}
	if schema.PatternProperties != nil {
		for _, wrapper := range schema.PatternProperties.All() {
			if value, possible := schemaFacetValue(wrapper, opts); possible {
				values = append(values, value)
			}
		}
	}
	if schema.AdditionalProperties != nil {
		if value, possible := schemaFacetValue(schema.AdditionalProperties, opts); possible {
			values = append(values, value)
		}
	} else if opts.Semantics == SchemaSemanticsRaw {
		values = append(values, Top())
	}
	return Union(values, opts)
}

func missingPathContainer(remainingPath []PathSegment) *oas3.Schema {
	if len(remainingPath) == 0 {
		return Top()
	}
	next := remainingPath[0]
	if _, ok := next.Key.(string); ok && !next.IsSymbolic {
		return ObjectType()
	}
	if _, ok := next.Key.(int); ok && !next.IsSymbolic {
		zero := int64(0)
		result := ArrayType(Bottom())
		result.MaxItems = &zero
		return result
	}
	return ArrayType(Top())
}
