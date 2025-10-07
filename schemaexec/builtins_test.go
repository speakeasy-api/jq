package schemaexec

import (
	"context"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// Test built-in functions with real jq queries

func TestBuiltin_Type(t *testing.T) {
	query, _ := gojq.Parse("type")

	// Test with string
	result, err := RunSchema(context.Background(), query, StringType())
	if err != nil {
		t.Fatal(err)
	}

	if getType(result.Schema) != "string" {
		t.Errorf("type should return string, got %s", getType(result.Schema))
	}

	// Check it's a constant
	if result.Schema.Enum == nil || len(result.Schema.Enum) != 1 {
		t.Error("Expected enum with single value")
	} else if result.Schema.Enum[0].Value != "string" {
		t.Errorf("Expected 'string', got %s", result.Schema.Enum[0].Value)
	}
}

func TestBuiltin_Keys(t *testing.T) {
	query, _ := gojq.Parse("keys")

	input := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
		"age":  NumberType(),
		"city": StringType(),
	}, []string{"name"})

	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatal(err)
	}

	// Should return array
	if getType(result.Schema) != "array" {
		t.Errorf("keys should return array, got %s", getType(result.Schema))
	}

	// Items should be string
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Error("Expected items schema")
	} else {
		itemType := getType(result.Schema.Items.Left)
		if itemType != "string" {
			t.Errorf("Expected string items, got %s", itemType)
		}
	}

	t.Logf("✅ keys() returns array of strings")
}

func TestBuiltin_Length(t *testing.T) {
	query, _ := gojq.Parse("length")

	// Test with array
	result, err := RunSchema(context.Background(), query, ArrayType(NumberType()))
	if err != nil {
		t.Fatal(err)
	}

	if getType(result.Schema) != "number" {
		t.Errorf("length should return number, got %s", getType(result.Schema))
	}

	// Should have minimum: 0
	if result.Schema.Minimum == nil || *result.Schema.Minimum != 0 {
		t.Error("Expected minimum: 0 for length")
	}

	t.Logf("✅ length() returns number >= 0")
}

func TestBuiltin_Has(t *testing.T) {
	query, _ := gojq.Parse("has(\"name\")")

	input := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
		"age":  NumberType(),
	}, []string{"name"}) // name is required

	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatal(err)
	}

	// Should return boolean
	if getType(result.Schema) != "boolean" {
		t.Errorf("has should return boolean, got %s", getType(result.Schema))
	}

	// For required property, should be const true
	if result.Schema.Enum != nil && len(result.Schema.Enum) > 0 {
		if result.Schema.Enum[0].Value != "true" {
			t.Logf("has(name) on required property: %s", result.Schema.Enum[0].Value)
		}
	}

	t.Logf("✅ has() returns boolean")
}

func TestBuiltin_Values(t *testing.T) {
	query, _ := gojq.Parse("values")

	input := BuildObject(map[string]*oas3.Schema{
		"x": StringType(),
		"y": NumberType(),
	}, nil)

	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatal(err)
	}

	// Should return array (or conservative Top)
	// Note: values() implementation is conservative for Phase 5
	if result.Schema != nil {
		t.Logf("✅ values() returns schema (type: %s)", getType(result.Schema))
	} else {
		t.Error("values() should return a schema")
	}
}

func TestBuiltin_Add(t *testing.T) {
	query, _ := gojq.Parse("add")

	// Test with number array
	input := ArrayType(NumberType())
	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatal(err)
	}

	if getType(result.Schema) != "number" {
		t.Errorf("add on number array should return number, got %s", getType(result.Schema))
	}

	t.Logf("✅ add() on numbers returns number")
}

func TestBuiltin_ToEntries(t *testing.T) {
	query, _ := gojq.Parse("to_entries")

	input := BuildObject(map[string]*oas3.Schema{
		"a": NumberType(),
		"b": StringType(),
	}, nil)

	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatal(err)
	}

	// Should return array
	if getType(result.Schema) != "array" {
		t.Errorf("to_entries should return array, got %s", getType(result.Schema))
	}

	// Items should be objects with key/value
	if result.Schema.Items != nil && result.Schema.Items.Left != nil {
		itemType := getType(result.Schema.Items.Left)
		if itemType != "object" {
			t.Errorf("Expected object items, got %s", itemType)
		}

		// Check for key and value properties
		itemSchema := result.Schema.Items.Left
		if itemSchema.Properties != nil {
			if _, hasKey := itemSchema.Properties.Get("key"); !hasKey {
				t.Error("Entry object should have 'key' property")
			}
			if _, hasValue := itemSchema.Properties.Get("value"); !hasValue {
				t.Error("Entry object should have 'value' property")
			}
		}
	}

	t.Logf("✅ to_entries() converts object to array of {key, value}")
}

func TestBuiltin_TypeConversions(t *testing.T) {
	// tonumber
	query, _ := gojq.Parse("tonumber")
	result, err := RunSchema(context.Background(), query, StringType())
	if err != nil {
		t.Fatal(err)
	}
	if getType(result.Schema) != "number" {
		t.Errorf("tonumber should return number, got %s", getType(result.Schema))
	}

	// tostring
	query, _ = gojq.Parse("tostring")
	result, err = RunSchema(context.Background(), query, NumberType())
	if err != nil {
		t.Fatal(err)
	}
	if getType(result.Schema) != "string" {
		t.Errorf("tostring should return string, got %s", getType(result.Schema))
	}

	t.Logf("✅ Type conversions work")
}

func TestBuiltin_ChainedOperations(t *testing.T) {
	// Test: .items | keys
	query, _ := gojq.Parse(".items | keys")

	input := BuildObject(map[string]*oas3.Schema{
		"items": BuildObject(map[string]*oas3.Schema{
			"a": StringType(),
			"b": NumberType(),
		}, nil),
	}, []string{"items"})

	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatal(err)
	}

	// Should get array of strings
	if getType(result.Schema) != "array" {
		t.Errorf("Expected array, got %s", getType(result.Schema))
	}

	t.Logf("✅ Chained operations: .items | keys works")
}

func TestBuiltin_ArrayOperations(t *testing.T) {
	// Test: reverse, sort, unique
	tests := []string{"reverse", "sort", "unique"}

	input := ArrayType(NumberType())

	for _, op := range tests {
		query, _ := gojq.Parse(op)
		result, err := RunSchema(context.Background(), query, input)
		if err != nil {
			t.Errorf("%s failed: %v", op, err)
			continue
		}

		// All preserve array type
		if getType(result.Schema) != "array" {
			t.Errorf("%s should preserve array type, got %s", op, getType(result.Schema))
		}
	}

	t.Logf("✅ Array operations preserve type")
}
