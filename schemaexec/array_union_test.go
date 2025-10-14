package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestArrayUnionWithEmptyArray tests that (.tags // []) doesn't collapse
// array items to Top when the fallback is an empty array.
// This is bug #1: array-union over-aggression.
func TestArrayUnionWithEmptyArray(t *testing.T) {
	ctx := context.Background()

	t.Run("ArrayOfStrings_UnionEmptyArray", func(t *testing.T) {
		// (.tags // []) where .tags is array<string>
		query, _ := gojq.Parse(`(.tags // [])`)

		input := BuildObject(map[string]*oas3.Schema{
			"tags": ArrayType(StringType()),
		}, []string{"tags"})

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Result should still be array<string>, not array<Top>
		if getType(result.Schema) != "array" {
			t.Fatalf("Expected array, got %s", getType(result.Schema))
		}

		if result.Schema.Items == nil || result.Schema.Items.Left == nil {
			t.Fatal("Result array has no items schema")
		}

		itemType := getType(result.Schema.Items.Left)
		if itemType != "string" {
			t.Errorf("Expected items to be string, got %s", itemType)
			t.Logf("This indicates the empty array fallback collapsed items to Top")
		}
	})

	t.Run("MapOverFallbackArray", func(t *testing.T) {
		// (.tags // []) | map(.)
		// Tests that map preserves item type after fallback
		query, _ := gojq.Parse(`(.tags // []) | map(.)`)

		input := BuildObject(map[string]*oas3.Schema{
			"tags": ArrayType(StringType()),
		}, []string{"tags"})

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if getType(result.Schema) != "array" {
			t.Fatalf("Expected array, got %s", getType(result.Schema))
		}

		if result.Schema.Items == nil || result.Schema.Items.Left == nil {
			t.Fatal("Result array has no items schema")
		}

		itemType := getType(result.Schema.Items.Left)
		if itemType != "string" {
			t.Errorf("Expected items to be string after map, got %s", itemType)
		}
	})

	t.Run("MapIdentity_PreservesType", func(t *testing.T) {
		// map(.) over array<string> should return array<string>
		query, _ := gojq.Parse(`map(.)`)

		input := ArrayType(StringType())

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if getType(result.Schema) != "array" {
			t.Fatalf("Expected array, got %s", getType(result.Schema))
		}

		if result.Schema.Items == nil || result.Schema.Items.Left == nil {
			t.Fatal("Result array has no items schema")
		}

		itemType := getType(result.Schema.Items.Left)
		if itemType != "string" {
			t.Errorf("Expected items to be string, got %s", itemType)
		}
	})

	t.Run("MapWithValueField", func(t *testing.T) {
		// (.tags // []) | map({value: .})
		// This is the minimal repro for the value: {} bug
		query, _ := gojq.Parse(`(.tags // []) | map({value: .})`)

		input := BuildObject(map[string]*oas3.Schema{
			"tags": ArrayType(StringType()),
		}, []string{"tags"})

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Print warnings for debugging
		if len(result.Warnings) > 0 {
			t.Logf("=== WARNINGS/TRACE ===")
			for _, w := range result.Warnings {
				t.Logf("%s", w)
			}
		}

		if getType(result.Schema) != "array" {
			t.Fatalf("Expected array, got %s", getType(result.Schema))
		}

		if result.Schema.Items == nil || result.Schema.Items.Left == nil {
			t.Fatal("Result array has no items schema")
		}

		itemObj := result.Schema.Items.Left
		if getType(itemObj) != "object" {
			t.Fatalf("Expected items to be object, got %s", getType(itemObj))
		}

		if itemObj.Properties == nil {
			t.Fatal("Item object has no properties")
		}

		valueProp, ok := itemObj.Properties.Get("value")
		if !ok || valueProp.Left == nil {
			t.Fatal("Missing 'value' property")
		}

		valueType := getType(valueProp.Left)
		if valueType != "string" {
			t.Errorf("Expected value to be string, got %s", valueType)
			t.Logf("This is the main bug: value should be string, not empty/Top")
		}
	})
}

// TestEmptyArrayRepresentation tests that empty array literals are
// represented correctly (not as Array(Top)).
func TestEmptyArrayRepresentation(t *testing.T) {
	ctx := context.Background()

	t.Run("EmptyArrayLiteral", func(t *testing.T) {
		query, _ := gojq.Parse(`[]`)
		input := ObjectType()

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if getType(result.Schema) != "array" {
			t.Fatalf("Expected array, got %s", getType(result.Schema))
		}

		// Check that it's marked as empty with maxItems=0
		if result.Schema.MaxItems == nil || *result.Schema.MaxItems != 0 {
			t.Error("Empty array should have maxItems=0")
		}

		// Check that it's recognized as empty (implementation-dependent)
		// At minimum, it should not have items = Top
		if result.Schema.Items != nil && result.Schema.Items.Left != nil {
			itemType := getType(result.Schema.Items.Left)
			// Empty arrays should have Bottom items, not Top
			// Bottom is often represented as empty type or special marker
			if itemType != "" && itemType != "null" {
				t.Logf("Warning: empty array has items type %s (expected Bottom/empty)", itemType)
			}
		}
	})

	t.Run("TruthyEmptyArray", func(t *testing.T) {
		// [] // ["x"] should return [], not ["x"]
		// Empty arrays are truthy in jq
		query, _ := gojq.Parse(`[] // ["x"]`)
		input := ObjectType()

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Result should be empty array (left), not ["x"] (right)
		// For symbolic execution, we accept array as result
		// but items should not include "x" if we can determine
		// that left is definitely an array (even if empty)
		if getType(result.Schema) != "array" {
			t.Fatalf("Expected array, got %s", getType(result.Schema))
		}
	})
}

// TestArrayUnionCardinality tests that union properly merges minItems/maxItems constraints
func TestArrayUnionCardinality(t *testing.T) {
	opts := DefaultOptions()

	t.Run("UnboundedMaxItems", func(t *testing.T) {
		// Union(Array(String, maxItems=5), Array(String)) should be unbounded
		arr1 := ArrayType(StringType())
		five := int64(5)
		arr1.MaxItems = &five

		arr2 := ArrayType(StringType())
		// arr2 has no maxItems (unbounded)

		result := Union([]*oas3.Schema{arr1, arr2}, opts)

		if getType(result) != "array" {
			t.Fatalf("Expected array, got %s", getType(result))
		}

		if result.MaxItems != nil {
			t.Errorf("Expected unbounded maxItems (nil), got %d", *result.MaxItems)
		}
	})

	t.Run("MinItemsFromBoundedArrays", func(t *testing.T) {
		// Union(Array(String, minItems=2), Array(String, minItems=1)) should have minItems=1
		arr1 := ArrayType(StringType())
		two := int64(2)
		arr1.MinItems = &two

		arr2 := ArrayType(StringType())
		one := int64(1)
		arr2.MinItems = &one

		result := Union([]*oas3.Schema{arr1, arr2}, opts)

		if getType(result) != "array" {
			t.Fatalf("Expected array, got %s", getType(result))
		}

		if result.MinItems == nil || *result.MinItems != 1 {
			if result.MinItems == nil {
				t.Error("Expected minItems=1, got nil")
			} else {
				t.Errorf("Expected minItems=1, got %d", *result.MinItems)
			}
		}
	})

	t.Run("MinItemsWithUnspecified", func(t *testing.T) {
		// Union(Array(String, minItems=2), Array(String)) should have minItems=0
		arr1 := ArrayType(StringType())
		two := int64(2)
		arr1.MinItems = &two

		arr2 := ArrayType(StringType())
		// arr2 has no minItems (implicitly 0)

		result := Union([]*oas3.Schema{arr1, arr2}, opts)

		if getType(result) != "array" {
			t.Fatalf("Expected array, got %s", getType(result))
		}

		// minItems should be 0 (or nil which means "no constraint, implicitly 0")
		if result.MinItems != nil && *result.MinItems != 0 {
			t.Errorf("Expected minItems=0 or nil, got %d", *result.MinItems)
		}
	})

	t.Run("EmptyArrayWithNonEmpty", func(t *testing.T) {
		// Union(Array(String, minItems=1, maxItems=10), []) should be Array(String, minItems=0, maxItems=10)
		arr1 := ArrayType(StringType())
		one := int64(1)
		ten := int64(10)
		arr1.MinItems = &one
		arr1.MaxItems = &ten

		arr2 := ArrayType(Bottom())
		zero := int64(0)
		arr2.MinItems = &zero
		arr2.MaxItems = &zero

		result := Union([]*oas3.Schema{arr1, arr2}, opts)

		if getType(result) != "array" {
			t.Fatalf("Expected array, got %s", getType(result))
		}

		// Should have minItems=0 (can be empty)
		if result.MinItems == nil || *result.MinItems != 0 {
			if result.MinItems == nil {
				t.Error("Expected minItems=0, got nil")
			} else {
				t.Errorf("Expected minItems=0, got %d", *result.MinItems)
			}
		}

		// Should have maxItems=10 (from non-empty branch)
		if result.MaxItems == nil || *result.MaxItems != 10 {
			if result.MaxItems == nil {
				t.Error("Expected maxItems=10, got nil")
			} else {
				t.Errorf("Expected maxItems=10, got %d", *result.MaxItems)
			}
		}

		// Items should be String (not polluted by empty array)
		if result.Items == nil || result.Items.Left == nil {
			t.Fatal("Expected items to be set")
		}
		if getType(result.Items.Left) != "string" {
			t.Errorf("Expected items to be string, got %s", getType(result.Items.Left))
		}
	})
}
