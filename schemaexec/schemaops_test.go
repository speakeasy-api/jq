package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestTop(t *testing.T) {
	schema := Top()
	if schema == nil {
		t.Error("Top() should not return nil")
	}
}

func TestBottom(t *testing.T) {
	schema := Bottom()
	if schema != nil {
		t.Error("Bottom() should return nil")
	}
}

func TestConstString(t *testing.T) {
	schema := ConstString("hello")
	if schema == nil {
		t.Fatal("ConstString should not return nil")
	}

	types := schema.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeString {
		t.Errorf("Expected type string, got %v", types)
	}

	if len(schema.Enum) != 1 {
		t.Errorf("Expected 1 enum value, got %d", len(schema.Enum))
	}
}

func TestConstNumber(t *testing.T) {
	schema := ConstNumber(42.5)
	if schema == nil {
		t.Fatal("ConstNumber should not return nil")
	}

	types := schema.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeNumber {
		t.Errorf("Expected type number, got %v", types)
	}
}

func TestConstBool(t *testing.T) {
	schema := ConstBool(true)
	if schema == nil {
		t.Fatal("ConstBool should not return nil")
	}

	types := schema.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeBoolean {
		t.Errorf("Expected type boolean, got %v", types)
	}
}

func TestConstNull(t *testing.T) {
	schema := ConstNull()
	if schema == nil {
		t.Fatal("ConstNull should not return nil")
	}

	types := schema.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeNull {
		t.Errorf("Expected type null, got %v", types)
	}
}

func TestStringType(t *testing.T) {
	schema := StringType()
	if schema == nil {
		t.Fatal("StringType should not return nil")
	}

	types := schema.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeString {
		t.Errorf("Expected type string, got %v", types)
	}
}

func TestArrayType(t *testing.T) {
	itemsSchema := StringType()
	schema := ArrayType(itemsSchema)
	if schema == nil {
		t.Fatal("ArrayType should not return nil")
	}

	types := schema.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeArray {
		t.Errorf("Expected type array, got %v", types)
	}

	if schema.Items == nil {
		t.Error("Expected items to be set")
	}
}

func TestObjectType(t *testing.T) {
	schema := ObjectType()
	if schema == nil {
		t.Fatal("ObjectType should not return nil")
	}

	types := schema.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeObject {
		t.Errorf("Expected type object, got %v", types)
	}
}

func TestBuildObject(t *testing.T) {
	props := map[string]*oas3.Schema{
		"name": StringType(),
		"age":  NumberType(),
	}
	required := []string{"name"}

	schema := BuildObject(props, required)
	if schema == nil {
		t.Fatal("BuildObject should not return nil")
	}

	types := schema.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeObject {
		t.Errorf("Expected type object, got %v", types)
	}

	if schema.Properties == nil {
		t.Fatal("Expected properties to be set")
	}

	if _, ok := schema.Properties.Get("name"); !ok {
		t.Error("Expected 'name' property to exist")
	}

	if _, ok := schema.Properties.Get("age"); !ok {
		t.Error("Expected 'age' property to exist")
	}

	if len(schema.Required) != 1 || schema.Required[0] != "name" {
		t.Errorf("Expected required=[name], got %v", schema.Required)
	}
}

func TestGetProperty(t *testing.T) {
	obj := BuildObject(map[string]*oas3.Schema{
		"foo": StringType(),
		"bar": NumberType(),
	}, []string{"foo"})

	opts := DefaultOptions()

	// Get required property
	fooSchema := GetProperty(obj, "foo", opts)
	if fooSchema == nil {
		t.Fatal("Expected foo property to exist")
	}

	// Get optional property
	barSchema := GetProperty(obj, "bar", opts)
	if barSchema == nil {
		t.Fatal("Expected bar property to exist")
	}

	// Get non-existent property
	bazSchema := GetProperty(obj, "baz", opts)
	if bazSchema == nil {
		t.Fatal("Expected null schema for non-existent property")
	}
	bazTypes := bazSchema.GetType()
	if len(bazTypes) != 1 || bazTypes[0] != oas3.SchemaTypeNull {
		t.Errorf("Expected null type for non-existent property, got %v", bazTypes)
	}
}

func TestUnion(t *testing.T) {
	opts := DefaultOptions()

	// Union of nothing
	empty := Union([]*oas3.Schema{}, opts)
	if empty != nil {
		t.Error("Union of empty list should be Bottom (nil)")
	}

	// Union of one
	single := Union([]*oas3.Schema{StringType()}, opts)
	if single == nil {
		t.Fatal("Union of single schema should not be nil")
	}
	types := single.GetType()
	if len(types) != 1 || types[0] != oas3.SchemaTypeString {
		t.Errorf("Expected type string, got %v", types)
	}

	// Union of multiple
	multiple := Union([]*oas3.Schema{StringType(), NumberType(), BoolType()}, opts)
	if multiple == nil {
		t.Fatal("Union of multiple schemas should not be nil")
	}

	if multiple.AnyOf == nil || len(multiple.AnyOf) != 3 {
		t.Errorf("Expected anyOf with 3 branches, got %v", multiple.AnyOf)
	}
}
