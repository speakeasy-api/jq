package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// Tests for Phase 3 operations

func TestIntersect(t *testing.T) {
	opts := DefaultOptions()

	// Intersect string with string
	result := Intersect(StringType(), StringType(), opts)
	if result == nil {
		t.Fatal("Intersect of compatible types should not be Bottom")
	}

	// Intersect string with number (incompatible)
	result = Intersect(StringType(), NumberType(), opts)
	if result != nil {
		t.Error("Intersect of incompatible types should be Bottom (nil)")
	}

	// Intersect with Bottom
	result = Intersect(StringType(), Bottom(), opts)
	if result != nil {
		t.Error("Intersect with Bottom should be Bottom")
	}

	// Result should have allOf
	result = Intersect(StringType(), StringType(), opts)
	if result.AllOf == nil || len(result.AllOf) != 2 {
		t.Errorf("Expected allOf with 2 schemas, got %v", result.AllOf)
	}
}

func TestRequireType(t *testing.T) {
	opts := DefaultOptions()

	// Require specific type
	result := RequireType(Top(), oas3.SchemaTypeString, opts)
	if result == nil {
		t.Fatal("RequireType should not return Bottom for valid operation")
	}

	// Should have allOf constraining the type
	if result.AllOf == nil {
		t.Error("Expected allOf in result")
	}

	// Require incompatible type
	result = RequireType(StringType(), oas3.SchemaTypeNumber, opts)
	if result != nil {
		t.Error("Requiring incompatible type should give Bottom")
	}
}

func TestHasProperty(t *testing.T) {
	opts := DefaultOptions()

	obj := BuildObject(map[string]*oas3.Schema{
		"foo": StringType(),
	}, nil) // foo not required initially

	// Require foo to exist
	result := HasProperty(obj, "foo", opts)
	if result == nil {
		t.Fatal("HasProperty should not return Bottom")
	}

	// Should have allOf
	if result.AllOf == nil {
		t.Error("Expected allOf in result")
	}

	// HasProperty on non-object
	result = HasProperty(StringType(), "foo", opts)
	if result != nil {
		t.Error("HasProperty on non-object should be Bottom")
	}
}

func TestMergeObjects(t *testing.T) {
	opts := DefaultOptions()

	obj1 := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
		"b": NumberType(),
	}, []string{"a"})

	obj2 := BuildObject(map[string]*oas3.Schema{
		"b": BoolType(), // Override b
		"c": ArrayType(Top()),
	}, []string{"c"})

	result := MergeObjects(obj1, obj2, opts)

	if result == nil {
		t.Fatal("MergeObjects should not return nil")
	}

	if result.Properties == nil {
		t.Fatal("Expected properties in merged object")
	}

	// Should have all three properties
	if result.Properties.Len() != 3 {
		t.Errorf("Expected 3 properties (a, b, c), got %d", result.Properties.Len())
	}

	// b should be from obj2 (override)
	if bSchema, ok := result.Properties.Get("b"); ok {
		if bSchema.Left != nil {
			bType := getType(bSchema.Left)
			if bType != "boolean" {
				t.Errorf("Expected b to be boolean (from obj2), got %s", bType)
			}
		}
	}

	// Required should include both a and c
	if len(result.Required) != 2 {
		t.Errorf("Expected 2 required properties, got %d: %v", len(result.Required), result.Required)
	}
}

func TestBuildArray(t *testing.T) {
	// Build homogeneous array
	arr := BuildArray(NumberType(), nil)
	if arr == nil {
		t.Fatal("BuildArray should not return nil")
	}

	if getType(arr) != "array" {
		t.Errorf("Expected array type, got %s", getType(arr))
	}

	if arr.Items == nil {
		t.Error("Expected items to be set")
	}

	// Build tuple array
	elements := []*oas3.Schema{StringType(), NumberType(), BoolType()}
	tuple := BuildArray(nil, elements)

	if tuple == nil {
		t.Fatal("BuildArray with elements should not return nil")
	}

	if tuple.PrefixItems == nil || len(tuple.PrefixItems) != 3 {
		t.Errorf("Expected 3 prefixItems, got %v", tuple.PrefixItems)
	}

	// Build tuple with additional items
	tupleWithItems := BuildArray(StringType(), elements)

	if tupleWithItems.PrefixItems == nil || len(tupleWithItems.PrefixItems) != 3 {
		t.Error("Expected prefixItems")
	}

	if tupleWithItems.Items == nil {
		t.Error("Expected items schema for additional elements")
	}
}
