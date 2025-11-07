package schemaexec

import (
	"context"
	"strings"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
)

// Helper to create schema with allOf
func BuildAllOf(subschemas ...*oas3.Schema) *oas3.Schema {
	allOf := make([]*oas3.JSONSchema[oas3.Referenceable], len(subschemas))
	for i, s := range subschemas {
		allOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](s)
	}
	return &oas3.Schema{
		AllOf: allOf,
	}
}

// ============================================================================
// SINGLE ALLOF TESTS - Should act as identity
// ============================================================================

// TestAllOf_SingleSubschema_UserExample tests the user's provided example
// A single allOf with one object subschema should act the same as without allOf
func TestAllOf_SingleSubschema_UserExample(t *testing.T) {
	query, err := gojq.Parse(".data | map({key: .name, value: .value}) | from_entries")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Schema with single allOf in items
	input := BuildObject(map[string]*oas3.Schema{
		"data": BuildArray(BuildAllOf(
			BuildObject(map[string]*oas3.Schema{
				"name":  StringType(),
				"value": StringType(),
			}, nil),
		), nil),
	}, []string{"data"})

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	// Should produce object with dynamic keys (Top for additionalProperties)
	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "object" {
		t.Errorf("expected object type, got %s", resultType)
	}
}

// TestAllOf_SingleSubschema_Identity tests that single allOf is removed
func TestAllOf_SingleSubschema_Identity(t *testing.T) {
	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Schema with single allOf at property level
	singleSchema := BuildObject(map[string]*oas3.Schema{
		"bar": StringType(),
	}, nil)

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(singleSchema),
	}, nil)

	opts := DefaultOptions()
	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	// Should access .bar successfully
	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	// The result should be an object (the collapsed allOf)
	resultType := getType(result.Schema)
	if resultType != "object" {
		t.Errorf("expected object type, got %s", resultType)
	}
}

// TestAllOf_EmptyAllOf tests that empty allOf doesn't crash
func TestAllOf_EmptyAllOf(t *testing.T) {
	query, err := gojq.Parse(".")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Schema with empty allOf
	input := &oas3.Schema{
		AllOf: []*oas3.JSONSchema[oas3.Referenceable]{},
	}

	opts := DefaultOptions()
	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}
}

// ============================================================================
// OBJECT PROPERTY MERGING TESTS
// ============================================================================

// TestAllOf_DisjointProperties tests merging schemas with disjoint properties
func TestAllOf_DisjointProperties(t *testing.T) {
	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Two subschemas with disjoint properties should merge
	schema1 := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
	}, nil)

	schema2 := BuildObject(map[string]*oas3.Schema{
		"age": IntegerType(),
	}, nil)

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	// The merged schema should have both properties
	if result.Schema.Properties == nil {
		t.Fatal("result schema has no properties")
	}

	if result.Schema.Properties.Len() != 2 {
		t.Errorf("expected 2 properties after merge, got %d", result.Schema.Properties.Len())
	}
}

// TestAllOf_OverlappingProperties_Compatible tests merging same property with compatible types
func TestAllOf_OverlappingProperties_Compatible(t *testing.T) {
	t.Skip("TODO: Implement property-level type intersection merging")

	query, err := gojq.Parse(".foo.name")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Both define 'name' as string - should merge successfully
	schema1 := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
	}, nil)

	schema2 := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
	}, nil)

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "string" {
		t.Errorf("expected string type, got %s", resultType)
	}
}

// TestAllOf_OverlappingProperties_Incompatible tests error on incompatible property types
func TestAllOf_OverlappingProperties_Incompatible(t *testing.T) {
	t.Skip("TODO: Implement conflict detection for incompatible property types")

	query, err := gojq.Parse(".foo.name")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// 'name' is string in one, number in other - should error
	schema1 := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
	}, nil)

	schema2 := BuildObject(map[string]*oas3.Schema{
		"name": IntegerType(),
	}, nil)

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	_, err = RunSchema(context.Background(), query, input, opts)
	if err == nil {
		t.Fatal("expected error for incompatible property types")
	}

	if !strings.Contains(err.Error(), "incompatible") && !strings.Contains(err.Error(), "conflict") {
		t.Errorf("expected error about incompatible types, got: %v", err)
	}
}

// ============================================================================
// REQUIRED PROPERTIES MERGING TESTS
// ============================================================================

// TestAllOf_RequiredMerge tests that required arrays are merged (union)
func TestAllOf_RequiredMerge(t *testing.T) {
	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Different required fields should be merged
	schema1 := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
		"age":  IntegerType(),
	}, []string{"name"})

	schema2 := BuildObject(map[string]*oas3.Schema{
		"email": StringType(),
	}, []string{"email"})

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	// Check that required fields are merged
	if len(result.Schema.Required) < 2 {
		t.Errorf("expected at least 2 required fields after merge, got %d: %v",
			len(result.Schema.Required), result.Schema.Required)
	}
}

// TestAllOf_RequiredDuplicates tests that duplicate required fields are deduplicated
func TestAllOf_RequiredDuplicates(t *testing.T) {
	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Both require 'name' - should deduplicate
	schema1 := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
	}, []string{"name"})

	schema2 := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
		"age":  IntegerType(),
	}, []string{"name"})

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	// Count occurrences of 'name' in required
	nameCount := 0
	for _, req := range result.Schema.Required {
		if req == "name" {
			nameCount++
		}
	}

	if nameCount > 1 {
		t.Errorf("expected 'name' to appear once in required, got %d occurrences", nameCount)
	}
}

// ============================================================================
// NESTED SCHEMA MERGING TESTS
// ============================================================================

// TestAllOf_NestedAllOf tests that nested allOf is recursively collapsed
func TestAllOf_NestedAllOf(t *testing.T) {
	query, err := gojq.Parse(".foo.bar.baz")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Nested structure: allOf contains property with another allOf
	innerSchema := BuildObject(map[string]*oas3.Schema{
		"baz": StringType(),
	}, nil)

	middleSchema := BuildObject(map[string]*oas3.Schema{
		"bar": BuildAllOf(innerSchema),
	}, nil)

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(middleSchema),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "string" {
		t.Errorf("expected string type for nested access, got %s", resultType)
	}
}

// TestAllOf_ArrayItemsWithAllOf tests allOf in array items (like user's example)
func TestAllOf_ArrayItemsWithAllOf(t *testing.T) {
	query, err := gojq.Parse(".items[0].name")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Array with allOf in items
	itemSchema := BuildAllOf(
		BuildObject(map[string]*oas3.Schema{
			"name": StringType(),
			"id":   IntegerType(),
		}, nil),
	)

	input := BuildObject(map[string]*oas3.Schema{
		"items": BuildArray(itemSchema, nil),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "string" {
		t.Errorf("expected string type, got %s", resultType)
	}
}

// TestAllOf_DeepNestedMerge tests multiple levels of property nesting with allOf
func TestAllOf_DeepNestedMerge(t *testing.T) {
	query, err := gojq.Parse(".outer.middle.inner.value")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Deep nesting with allOf at multiple levels
	innerObj := BuildObject(map[string]*oas3.Schema{
		"value": StringType(),
	}, nil)

	middleSchema1 := BuildObject(map[string]*oas3.Schema{
		"inner": innerObj,
	}, nil)

	middleSchema2 := BuildObject(map[string]*oas3.Schema{
		"extra": IntegerType(),
	}, nil)

	outerSchema := BuildObject(map[string]*oas3.Schema{
		"middle": BuildAllOf(middleSchema1, middleSchema2),
	}, nil)

	input := BuildObject(map[string]*oas3.Schema{
		"outer": outerSchema,
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "string" {
		t.Errorf("expected string type, got %s", resultType)
	}
}

// ============================================================================
// TYPE CONSTRAINT MERGING TESTS
// ============================================================================

// TestAllOf_TypeIntersection_Compatible tests merging same types
func TestAllOf_TypeIntersection_Compatible(t *testing.T) {
	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Both are objects - should merge fine
	props1 := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	props1.Set("a", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()))
	schema1 := &oas3.Schema{
		Type:       oas3.NewTypeFromString(oas3.SchemaTypeObject),
		Properties: props1,
	}

	props2 := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	props2.Set("b", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](IntegerType()))
	schema2 := &oas3.Schema{
		Type:       oas3.NewTypeFromString(oas3.SchemaTypeObject),
		Properties: props2,
	}

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "object" {
		t.Errorf("expected object type, got %s", resultType)
	}
}

// TestAllOf_TypeIntersection_Incompatible tests error on incompatible types
func TestAllOf_TypeIntersection_Incompatible(t *testing.T) {
	t.Skip("TODO: Implement type conflict detection (object vs array)")

	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// One is object, one is array - should error
	schema1 := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeObject),
	}

	schema2 := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
	}

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	_, err = RunSchema(context.Background(), query, input, opts)
	if err == nil {
		t.Fatal("expected error for incompatible types (object vs array)")
	}

	if !strings.Contains(err.Error(), "incompatible") && !strings.Contains(err.Error(), "type") {
		t.Errorf("expected error about incompatible types, got: %v", err)
	}
}

// ============================================================================
// ENUM AND CONST MERGING TESTS
// ============================================================================

// TestAllOf_EnumIntersection tests that enum values are intersected
func TestAllOf_EnumIntersection(t *testing.T) {
	t.Skip("TODO: Implement enum intersection with proper value types")
}

// TestAllOf_EnumEmpty tests error when enum intersection is empty
func TestAllOf_EnumEmpty(t *testing.T) {
	t.Skip("TODO: Implement enum intersection with empty check and proper value types")
}

// TestAllOf_ConstMatching tests that matching const values merge
func TestAllOf_ConstMatching(t *testing.T) {
	t.Skip("TODO: Implement const merging with proper value types")
}

// TestAllOf_ConstMismatch tests error on mismatched const values
func TestAllOf_ConstMismatch(t *testing.T) {
	t.Skip("TODO: Implement const conflict detection with proper value types")
}

// ============================================================================
// NUMERIC/STRING CONSTRAINT MERGING TESTS
// ============================================================================

// TestAllOf_StringLengthIntersection tests that minLength/maxLength are intersected
func TestAllOf_StringLengthIntersection(t *testing.T) {
	t.Skip("TODO: Implement string constraint merging")

	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	minLen1 := int64(5)
	maxLen1 := int64(20)
	minLen2 := int64(10)
	maxLen2 := int64(15)

	// Intersection should be [10, 15]
	schema1 := &oas3.Schema{
		Type:      oas3.NewTypeFromString(oas3.SchemaTypeString),
		MinLength: &minLen1,
		MaxLength: &maxLen1,
	}

	schema2 := &oas3.Schema{
		Type:      oas3.NewTypeFromString(oas3.SchemaTypeString),
		MinLength: &minLen2,
		MaxLength: &maxLen2,
	}

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	if result.Schema.MinLength == nil || *result.Schema.MinLength != 10 {
		t.Errorf("expected minLength 10, got %v", result.Schema.MinLength)
	}

	if result.Schema.MaxLength == nil || *result.Schema.MaxLength != 15 {
		t.Errorf("expected maxLength 15, got %v", result.Schema.MaxLength)
	}
}

// TestAllOf_StringLengthConflict tests error when string length constraints conflict
func TestAllOf_StringLengthConflict(t *testing.T) {
	t.Skip("TODO: Implement string constraint conflict detection")

	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	minLen := int64(20)
	maxLen := int64(10)

	// minLength > maxLength after intersection - should error
	schema1 := &oas3.Schema{
		Type:      oas3.NewTypeFromString(oas3.SchemaTypeString),
		MinLength: &minLen,
	}

	schema2 := &oas3.Schema{
		Type:      oas3.NewTypeFromString(oas3.SchemaTypeString),
		MaxLength: &maxLen,
	}

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	_, err = RunSchema(context.Background(), query, input, opts)
	if err == nil {
		t.Fatal("expected error for invalid string length constraint")
	}
}

// TestAllOf_NumericRangeIntersection tests that min/max for numbers are intersected
func TestAllOf_NumericRangeIntersection(t *testing.T) {
	t.Skip("TODO: Implement numeric constraint merging")

	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	min1 := float64(0)
	max1 := float64(100)
	min2 := float64(50)
	max2 := float64(75)

	// Intersection should be [50, 75]
	schema1 := &oas3.Schema{
		Type:    oas3.NewTypeFromString(oas3.SchemaTypeNumber),
		Minimum: &min1,
		Maximum: &max1,
	}

	schema2 := &oas3.Schema{
		Type:    oas3.NewTypeFromString(oas3.SchemaTypeNumber),
		Minimum: &min2,
		Maximum: &max2,
	}

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	if result.Schema.Minimum == nil || *result.Schema.Minimum != 50 {
		t.Errorf("expected minimum 50, got %v", result.Schema.Minimum)
	}

	if result.Schema.Maximum == nil || *result.Schema.Maximum != 75 {
		t.Errorf("expected maximum 75, got %v", result.Schema.Maximum)
	}
}

// ============================================================================
// ADDITIONAL PROPERTIES MERGING TESTS
// ============================================================================

// TestAllOf_AdditionalPropertiesBoolean tests that false dominates true
func TestAllOf_AdditionalPropertiesBoolean(t *testing.T) {
	t.Skip("TODO: Implement additionalProperties merging with proper schema structure")
}

// ============================================================================
// ARRAY CONSTRAINT MERGING TESTS
// ============================================================================

// TestAllOf_ArrayItemsMerge tests that array items schemas are merged
func TestAllOf_ArrayItemsMerge(t *testing.T) {
	query, err := gojq.Parse(".foo[0].name")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	// Two array schemas with different item properties
	itemSchema1 := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
	}, nil)

	itemSchema2 := BuildObject(map[string]*oas3.Schema{
		"id": IntegerType(),
	}, nil)

	schema1 := BuildArray(itemSchema1, nil)
	schema2 := BuildArray(itemSchema2, nil)

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	opts.StrictMode = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "string" {
		t.Errorf("expected string type for .name, got %s", resultType)
	}
}

// TestAllOf_ArrayMinMaxItems tests that array size constraints are intersected
func TestAllOf_ArrayMinMaxItems(t *testing.T) {
	t.Skip("TODO: Implement array size constraint merging")

	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	minItems1 := int64(1)
	maxItems1 := int64(10)
	minItems2 := int64(5)
	maxItems2 := int64(8)

	// Intersection should be [5, 8]
	schema1 := &oas3.Schema{
		Type:     oas3.NewTypeFromString(oas3.SchemaTypeArray),
		MinItems: &minItems1,
		MaxItems: &maxItems1,
	}

	schema2 := &oas3.Schema{
		Type:     oas3.NewTypeFromString(oas3.SchemaTypeArray),
		MinItems: &minItems2,
		MaxItems: &maxItems2,
	}

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("result schema is nil")
	}

	if result.Schema.MinItems == nil || *result.Schema.MinItems != 5 {
		t.Errorf("expected minItems 5, got %v", result.Schema.MinItems)
	}

	if result.Schema.MaxItems == nil || *result.Schema.MaxItems != 8 {
		t.Errorf("expected maxItems 8, got %v", result.Schema.MaxItems)
	}
}

// TestAllOf_UniqueItems tests that uniqueItems true dominates
func TestAllOf_UniqueItems(t *testing.T) {
	t.Skip("TODO: Implement uniqueItems merging")

	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("failed to parse query: %v", err)
	}

	trueVal := true
	falseVal := false

	// true is stricter for uniqueItems
	schema1 := &oas3.Schema{
		Type:        oas3.NewTypeFromString(oas3.SchemaTypeArray),
		UniqueItems: &falseVal,
	}

	schema2 := &oas3.Schema{
		Type:        oas3.NewTypeFromString(oas3.SchemaTypeArray),
		UniqueItems: &trueVal,
	}

	input := BuildObject(map[string]*oas3.Schema{
		"foo": BuildAllOf(schema1, schema2),
	}, nil)

	opts := DefaultOptions()
	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("execution failed: %v", err)
	}

	if result.Schema == nil || result.Schema.UniqueItems == nil {
		t.Fatal("expected uniqueItems in result")
	}

	if !*result.Schema.UniqueItems {
		t.Error("expected uniqueItems to be true (stricter wins)")
	}
}
