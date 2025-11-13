package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
)

// TestFingerprintBasicTypes tests fingerprinting of basic types
func TestFingerprintBasicTypes(t *testing.T) {
	fp := NewFingerprinter()

	// Same types should have same fingerprint
	s1 := StringType()
	s2 := StringType()
	fp1 := fp.FingerprintSchema(s1)
	fp2 := fp.FingerprintSchema(s2)
	if fp1 != fp2 {
		t.Errorf("Expected identical string types to have same fingerprint:\n  fp1=%s\n  fp2=%s", fp1, fp2)
	}

	// Different types should have different fingerprints
	s3 := NumberType()
	fp3 := fp.FingerprintSchema(s3)
	if fp1 == fp3 {
		t.Error("Expected string and number types to have different fingerprints")
	}

	// Integer vs Number should differ
	s4 := IntegerType()
	fp4 := fp.FingerprintSchema(s4)
	if fp3 == fp4 {
		t.Error("Expected number and integer types to have different fingerprints")
	}
}

// TestFingerprintEnums tests that enum values are included in fingerprint
func TestFingerprintEnums(t *testing.T) {
	fp := NewFingerprinter()

	// Same enum values should have same fingerprint
	gold1 := ConstString("gold")
	gold2 := ConstString("gold")
	fp1 := fp.FingerprintSchema(gold1)
	fp2 := fp.FingerprintSchema(gold2)
	if fp1 != fp2 {
		t.Errorf("Expected identical enum values to have same fingerprint:\n  fp1=%s\n  fp2=%s", fp1, fp2)
	}

	// Different enum values should have different fingerprints
	silver := ConstString("silver")
	fp3 := fp.FingerprintSchema(silver)
	if fp1 == fp3 {
		t.Error("Expected different enum values to have different fingerprints")
	}
}

// TestFingerprintNestedProperties tests that nested property schemas are included
func TestFingerprintNestedProperties(t *testing.T) {
	fp := NewFingerprinter()

	// Two objects with same structure but different enum values in properties
	obj1 := BuildObject(map[string]*oas3.Schema{
		"tier": ConstString("gold"),
	}, []string{"tier"})

	obj2 := BuildObject(map[string]*oas3.Schema{
		"tier": ConstString("silver"),
	}, []string{"tier"})

	fp1 := fp.FingerprintSchema(obj1)
	fp2 := fp.FingerprintSchema(obj2)

	if fp1 == fp2 {
		t.Error("Expected objects with different property enum values to have different fingerprints (gold vs silver)")
	}

	// Identical objects should have same fingerprint
	obj3 := BuildObject(map[string]*oas3.Schema{
		"tier": ConstString("gold"),
	}, []string{"tier"})
	fp3 := fp.FingerprintSchema(obj3)

	if fp1 != fp3 {
		t.Errorf("Expected identical objects to have same fingerprint:\n  fp1=%s\n  fp3=%s", fp1, fp3)
	}
}

// TestFingerprintPropertyOrder tests that property order doesn't affect fingerprint
func TestFingerprintPropertyOrder(t *testing.T) {
	fp := NewFingerprinter()

	// Properties in different order
	obj1 := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
		"b": NumberType(),
		"c": BoolType(),
	}, []string{"a", "b", "c"})

	obj2 := BuildObject(map[string]*oas3.Schema{
		"c": BoolType(),
		"a": StringType(),
		"b": NumberType(),
	}, []string{"c", "a", "b"})

	fp1 := fp.FingerprintSchema(obj1)
	fp2 := fp.FingerprintSchema(obj2)

	if fp1 != fp2 {
		t.Error("Expected objects with same properties in different order to have same fingerprint")
	}
}

// TestFingerprintAllOfCollapsing tests that allOf schemas need explicit collapsing
func TestFingerprintAllOfCollapsing(t *testing.T) {
	fp := NewFingerprinter()

	// Schema with allOf
	base := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeObject),
	}
	sub1 := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
	}, []string{"a"})
	sub2 := BuildObject(map[string]*oas3.Schema{
		"b": NumberType(),
	}, []string{"b"})

	allOfSchema := base
	allOfSchema.AllOf = []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](sub1),
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](sub2),
	}

	// Manually merged schema (semantically equivalent but structurally different)
	merged := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
		"b": NumberType(),
	}, []string{"a", "b"})

	fp1 := fp.FingerprintSchema(allOfSchema)
	fp2 := fp.FingerprintSchema(merged)

	// These should have DIFFERENT fingerprints because they are structurally different
	// Fingerprinting is structural, not semantic
	if fp1 == fp2 {
		t.Error("Expected allOf schema and manually merged schema to have DIFFERENT fingerprints (structural, not semantic)")
	}

	// However, if we collapse the allOf schema first, they should match
	collapsed, err := collapseAllOf(allOfSchema)
	if err != nil {
		t.Fatalf("Failed to collapse allOf: %v", err)
	}
	fp3 := fp.FingerprintSchema(collapsed)

	if fp3 != fp2 {
		t.Error("Expected collapsed allOf schema and manually merged schema to have same fingerprint")
	}
}

// TestFingerprintAnyOfBranchOrder tests that anyOf branch order doesn't affect fingerprint
func TestFingerprintAnyOfBranchOrder(t *testing.T) {
	fp := NewFingerprinter()

	// anyOf with branches in one order
	anyOf1 := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](NumberType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](BoolType()),
		},
	}

	// anyOf with branches in different order
	anyOf2 := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](BoolType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](NumberType()),
		},
	}

	fp1 := fp.FingerprintSchema(anyOf1)
	fp2 := fp.FingerprintSchema(anyOf2)

	if fp1 != fp2 {
		t.Error("Expected anyOf with same branches in different order to have same fingerprint")
	}
}

// TestFingerprintAnyOfDeduplication tests that duplicate branches are deduplicated
func TestFingerprintAnyOfDeduplication(t *testing.T) {
	fp := NewFingerprinter()

	// anyOf with duplicate branches
	anyOf1 := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](NumberType()),
		},
	}

	// anyOf without duplicates
	anyOf2 := &oas3.Schema{
		AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](NumberType()),
		},
	}

	fp1 := fp.FingerprintSchema(anyOf1)
	fp2 := fp.FingerprintSchema(anyOf2)

	if fp1 != fp2 {
		t.Error("Expected anyOf with duplicate branches to have same fingerprint as deduplicated version")
	}
}

// TestFingerprintCircularReference tests handling of circular schema references
func TestFingerprintCircularReference(t *testing.T) {
	// Now that collapse functions are cycle-aware, we can use default options with collapsing
	fp := NewFingerprinter()

	// Create a circular reference: linked list node with "next" pointing to itself
	node := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeObject),
	}

	// Create properties map with circular reference
	node.Properties = createPropertiesMap(map[string]*oas3.Schema{
		"value": StringType(),
		"next":  node, // Circular reference
	})
	node.Required = []string{"value"}

	// Should not panic or infinite loop
	fp1 := fp.FingerprintSchema(node)
	if fp1 == "" {
		t.Error("Expected non-empty fingerprint for circular schema")
	}

	// Two structurally identical circular schemas should have same fingerprint
	node2 := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeObject),
	}
	node2.Properties = createPropertiesMap(map[string]*oas3.Schema{
		"value": StringType(),
		"next":  node2,
	})
	node2.Required = []string{"value"}

	fp2 := fp.FingerprintSchema(node2)
	if fp1 != fp2 {
		t.Error("Expected structurally identical circular schemas to have same fingerprint")
	}
}

// TestFingerprintArrays tests fingerprinting of array schemas
func TestFingerprintArrays(t *testing.T) {
	fp := NewFingerprinter()

	// Arrays with same item type
	arr1 := ArrayType(StringType())
	arr2 := ArrayType(StringType())
	fp1 := fp.FingerprintSchema(arr1)
	fp2 := fp.FingerprintSchema(arr2)

	if fp1 != fp2 {
		t.Error("Expected arrays with same item type to have same fingerprint")
	}

	// Arrays with different item types
	arr3 := ArrayType(NumberType())
	fp3 := fp.FingerprintSchema(arr3)

	if fp1 == fp3 {
		t.Error("Expected arrays with different item types to have different fingerprints")
	}
}

// TestFingerprintTuples tests fingerprinting of tuple schemas (prefixItems)
func TestFingerprintTuples(t *testing.T) {
	fp := NewFingerprinter()

	// Tuples with same structure
	tuple1 := BuildArray(Top(), []*oas3.Schema{
		StringType(),
		NumberType(),
		BoolType(),
	})
	tuple2 := BuildArray(Top(), []*oas3.Schema{
		StringType(),
		NumberType(),
		BoolType(),
	})

	fp1 := fp.FingerprintSchema(tuple1)
	fp2 := fp.FingerprintSchema(tuple2)

	if fp1 != fp2 {
		t.Error("Expected tuples with same structure to have same fingerprint")
	}

	// Tuples with different structure
	tuple3 := BuildArray(Top(), []*oas3.Schema{
		StringType(),
		BoolType(), // Different order
		NumberType(),
	})
	fp3 := fp.FingerprintSchema(tuple3)

	if fp1 == fp3 {
		t.Error("Expected tuples with different structure to have different fingerprints")
	}
}

// TestFingerprintTopBottom tests special handling of Top and Bottom
func TestFingerprintTopBottom(t *testing.T) {
	fp := NewFingerprinter()

	// Bottom (nil)
	fp1 := fp.FingerprintSchema(nil)
	if fp1 == "" {
		t.Error("Expected non-empty fingerprint for Bottom")
	}

	// Top
	top := Top()
	fp2 := fp.FingerprintSchema(top)
	if fp2 == "" {
		t.Error("Expected non-empty fingerprint for Top")
	}

	// Top and Bottom should differ
	if fp1 == fp2 {
		t.Error("Expected Top and Bottom to have different fingerprints")
	}

	// Multiple Tops should have same fingerprint
	top2 := Top()
	fp3 := fp.FingerprintSchema(top2)
	if fp2 != fp3 {
		t.Error("Expected all Top schemas to have same fingerprint")
	}
}

// TestFingerprintCaching tests that caching works correctly
func TestFingerprintCaching(t *testing.T) {
	fp := NewFingerprinter()

	schema := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
		"b": NumberType(),
	}, []string{"a", "b"})

	// First call
	fp1 := fp.FingerprintSchema(schema)

	// Second call should use cache (should return same result)
	fp2 := fp.FingerprintSchema(schema)

	if fp1 != fp2 {
		t.Error("Expected cached fingerprint to match original")
	}

	// Reset cache
	fp.Reset()

	// After reset, should still compute same fingerprint
	fp3 := fp.FingerprintSchema(schema)
	if fp1 != fp3 {
		t.Error("Expected fingerprint after cache reset to match original")
	}
}

// TestFingerprintExclusions tests that exclusion set skips persistent cache
func TestFingerprintExclusions(t *testing.T) {
	fp := NewFingerprinter()

	schema := ArrayType(StringType())

	// Normal fingerprint (cached)
	fp1 := fp.FingerprintSchema(schema)

	// With exclusion (skips cache)
	excl := map[*oas3.Schema]struct{}{
		schema: {},
	}
	fp2 := fp.FingerprintSchemaWithExclusions(schema, excl)

	// Should still compute same fingerprint
	if fp1 != fp2 {
		t.Error("Expected excluded schema to have same fingerprint")
	}
}

// TestFingerprintNumericConstraints tests that numeric constraints are included
func TestFingerprintNumericConstraints(t *testing.T) {
	fp := NewFingerprinter()

	// Number without constraints
	n1 := NumberType()
	fp1 := fp.FingerprintSchema(n1)

	// Number with minimum
	n2 := NumberType()
	min := float64(0)
	n2.Minimum = &min
	fp2 := fp.FingerprintSchema(n2)

	if fp1 == fp2 {
		t.Error("Expected number with minimum constraint to have different fingerprint")
	}

	// Number with maximum
	n3 := NumberType()
	max := float64(100)
	n3.Maximum = &max
	fp3 := fp.FingerprintSchema(n3)

	if fp1 == fp3 {
		t.Error("Expected number with maximum constraint to have different fingerprint")
	}
	if fp2 == fp3 {
		t.Error("Expected numbers with different constraints to have different fingerprints")
	}
}

// TestFingerprintStringConstraints tests that string constraints are included
func TestFingerprintStringConstraints(t *testing.T) {
	fp := NewFingerprinter()

	// String without constraints
	s1 := StringType()
	fp1 := fp.FingerprintSchema(s1)

	// String with pattern
	s2 := StringType()
	pattern := "^[a-z]+$"
	s2.Pattern = &pattern
	fp2 := fp.FingerprintSchema(s2)

	if fp1 == fp2 {
		t.Error("Expected string with pattern constraint to have different fingerprint")
	}

	// String with minLength
	s3 := StringType()
	minLen := int64(1)
	s3.MinLength = &minLen
	fp3 := fp.FingerprintSchema(s3)

	if fp1 == fp3 {
		t.Error("Expected string with minLength constraint to have different fingerprint")
	}
}

// TestFingerprintNullable tests that nullable flag is included
func TestFingerprintNullable(t *testing.T) {
	fp := NewFingerprinter()

	// Non-nullable string
	s1 := StringType()
	fp1 := fp.FingerprintSchema(s1)

	// Nullable string
	s2 := StringType()
	nullable := true
	s2.Nullable = &nullable
	fp2 := fp.FingerprintSchema(s2)

	if fp1 == fp2 {
		t.Error("Expected nullable string to have different fingerprint than non-nullable")
	}
}

// TestFingerprintComplexNested tests deeply nested structures
func TestFingerprintComplexNested(t *testing.T) {
	fp := NewFingerprinter()

	// Deeply nested object
	inner := BuildObject(map[string]*oas3.Schema{
		"value": ConstString("test"),
	}, []string{"value"})

	middle := BuildObject(map[string]*oas3.Schema{
		"inner": inner,
		"count": ConstInteger(42),
	}, []string{"inner", "count"})

	outer := BuildObject(map[string]*oas3.Schema{
		"middle": middle,
		"items":  ArrayType(NumberType()),
	}, []string{"middle", "items"})

	fp1 := fp.FingerprintSchema(outer)

	// Rebuild the same structure
	inner2 := BuildObject(map[string]*oas3.Schema{
		"value": ConstString("test"),
	}, []string{"value"})

	middle2 := BuildObject(map[string]*oas3.Schema{
		"inner": inner2,
		"count": ConstInteger(42),
	}, []string{"inner", "count"})

	outer2 := BuildObject(map[string]*oas3.Schema{
		"middle": middle2,
		"items":  ArrayType(NumberType()),
	}, []string{"middle", "items"})

	fp2 := fp.FingerprintSchema(outer2)

	if fp1 != fp2 {
		t.Error("Expected deeply nested identical structures to have same fingerprint")
	}

	// Change a deep value
	inner3 := BuildObject(map[string]*oas3.Schema{
		"value": ConstString("different"), // Changed value
	}, []string{"value"})

	middle3 := BuildObject(map[string]*oas3.Schema{
		"inner": inner3,
		"count": ConstInteger(42),
	}, []string{"inner", "count"})

	outer3 := BuildObject(map[string]*oas3.Schema{
		"middle": middle3,
		"items":  ArrayType(NumberType()),
	}, []string{"middle", "items"})

	fp3 := fp.FingerprintSchema(outer3)

	if fp1 == fp3 {
		t.Error("Expected deeply nested structures with different values to have different fingerprints")
	}
}

// Helper function to create properties map
func createPropertiesMap(props map[string]*oas3.Schema) *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]] {
	sm := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	for k, v := range props {
		sm.Set(k, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](v))
	}
	return sm
}
