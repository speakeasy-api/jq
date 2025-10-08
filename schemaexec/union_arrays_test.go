package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// Test: Union of two arrays with identical object items should preserve properties
func TestUnionArraysWithObjectItems(t *testing.T) {
	opts := DefaultOptions()
	
	// Create two identical object types with 2 properties
	obj := BuildObject(map[string]*oas3.Schema{
		"total": NumberType(),
		"sku":   StringType(),
	}, []string{"total", "sku"})
	
	arr1 := ArrayType(obj)
	arr2 := ArrayType(obj) // Different pointer, same structure
	
	result := Union([]*oas3.Schema{arr1, arr2}, opts)
	
	// Verify result is array
	if getType(result) != "array" {
		t.Errorf("Expected array, got %s", getType(result))
	}
	
	// Verify items exist
	if result.Items == nil || result.Items.Left == nil {
		t.Fatal("Result array has no items")
	}
	
	itemType := getType(result.Items.Left)
	if itemType != "object" {
		t.Errorf("Expected items type 'object', got '%s'", itemType)
	}
	
	// CRITICAL: Verify both properties are preserved
	if result.Items.Left.Properties == nil {
		t.Fatal("Items object has no properties")
	}
	
	hasTotal := false
	hasSku := false
	for k := range result.Items.Left.Properties.All() {
		if k == "total" {
			hasTotal = true
		}
		if k == "sku" {
			hasSku = true
		}
	}
	
	if !hasTotal {
		t.Error("Missing 'total' property in union result!")
	}
	if !hasSku {
		t.Error("Missing 'sku' property in union result!")
	}
}

// Test: Union with same pointer should return original
func TestUnionSamePointer(t *testing.T) {
	opts := DefaultOptions()
	arr := ArrayType(NumberType())
	
	result := Union([]*oas3.Schema{arr, arr}, opts)
	
	// Should dedup and return one of them
	if result != arr {
		t.Log("Note: Union didn't return same pointer (creates new schema)")
	}
	
	// At minimum, should preserve type
	if getType(result) != "array" {
		t.Errorf("Expected array, got %s", getType(result))
	}
}

// Test: tryMergeObjects with array properties
func TestMergeObjectsWithArrayProperties(t *testing.T) {
	opts := DefaultOptions()
	
	itemObj := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
		"b": NumberType(),
	}, []string{"a", "b"})
	
	arr1 := ArrayType(itemObj)
	arr2 := ArrayType(itemObj)
	
	obj1 := BuildObject(map[string]*oas3.Schema{"items": arr1}, []string{"items"})
	obj2 := BuildObject(map[string]*oas3.Schema{"items": arr2}, []string{"items"})
	
	result := tryMergeObjects([]*oas3.Schema{obj1, obj2}, opts)
	
	if result == nil {
		t.Fatal("tryMergeObjects returned nil")
	}
	
	// Check items property
	items, ok := result.Properties.Get("items")
	if !ok || items.Left == nil {
		t.Fatal("Merged object missing 'items' property")
	}
	
	if items.Left.Items == nil || items.Left.Items.Left == nil {
		t.Fatal("Items array has no item schema")
	}
	
	// CRITICAL: Both properties should be preserved
	if items.Left.Items.Left.Properties == nil {
		t.Fatal("Item object has no properties")
	}
	
	hasA := false
	hasB := false
	for k := range items.Left.Items.Left.Properties.All() {
		if k == "a" {
			hasA = true
		}
		if k == "b" {
			hasB = true
		}
	}
	
	if !hasA || !hasB {
		t.Errorf("Missing properties after merge: hasA=%v, hasB=%v", hasA, hasB)
	}
}

// Test tryMergeArrays directly
func TestTryMergeArraysDirect(t *testing.T) {
	opts := DefaultOptions()
	
	obj := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
		"b": NumberType(),
	}, []string{"a", "b"})
	
	t.Logf("Input object has %d properties", countProps(obj))
	for k := range obj.Properties.All() {
		t.Logf("  - %s", k)
	}
	
	arr1 := ArrayType(obj)
	arr2 := ArrayType(obj)
	
	result := tryMergeArrays([]*oas3.Schema{arr1, arr2}, opts)
	
	if result == nil {
		t.Fatal("tryMergeArrays returned nil")
	}
	
	if result.Items == nil || result.Items.Left == nil {
		t.Fatal("Result has no items")
	}
	
	t.Logf("Result items type: %s", getType(result.Items.Left))
	
	if result.Items.Left.Properties != nil {
		t.Logf("Result items has %d properties", countProps(result.Items.Left))
		for k := range result.Items.Left.Properties.All() {
			t.Logf("  - %s", k)
		}
	} else {
		t.Error("Result items has NO properties!")
	}
}

func countProps(s *oas3.Schema) int {
	if s.Properties == nil {
		return 0
	}
	count := 0
	for range s.Properties.All() {
		count++
	}
	return count
}
