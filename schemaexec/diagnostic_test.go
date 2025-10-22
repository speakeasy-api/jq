package schemaexec

import (
	"context"
	"testing"
	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// Test simple object construction without map or arithmetic
func TestDiagnostic_SimpleObjectConstruction(t *testing.T) {
	query, err := gojq.Parse(`{productId: .id, displayName: .name}`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"id":   StringType(),
		"name": StringType(),
	}, []string{"id", "name"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)
	
	if result.Schema != nil && result.Schema.Properties != nil {
		t.Logf("Properties count: %d", result.Schema.Properties.Len())
		if pid, ok := result.Schema.Properties.Get("productId"); ok && pid.Left != nil {
			t.Logf("✅ Has productId: %s", getType(pid.Left))
		} else {
			t.Error("❌ Missing productId")
		}
		if dn, ok := result.Schema.Properties.Get("displayName"); ok && dn.Left != nil {
			t.Logf("✅ Has displayName: %s", getType(dn.Left))
		} else {
			t.Error("❌ Missing displayName")
		}
	} else {
		t.Error("❌ No properties in output")
	}
}

// Test just arithmetic operation
func TestDiagnostic_ArithmeticOnly(t *testing.T) {
	query, err := gojq.Parse(`.price * .quantity`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"price":    NumberType(),
		"quantity": NumberType(),
	}, []string{"price", "quantity"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)
	
	if getType(result.Schema) == "number" {
		t.Log("✅ Arithmetic works - returns number")
	} else {
		t.Errorf("❌ Expected number, got: %s", getType(result.Schema))
	}
}

// Test just map operation
func TestDiagnostic_MapOnly(t *testing.T) {
	query, err := gojq.Parse(`.tags | map({value: .})`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{"tags"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)

	if getType(result.Schema) == "array" {
		t.Log("✅ Map returns array")
		if result.Schema.Items != nil && result.Schema.Items.Left != nil {
			itemType := getType(result.Schema.Items.Left)
			t.Logf("Array item type: %s", itemType)
			if itemType == "object" {
				t.Log("✅ Map creates objects")
			}
		}
	} else {
		t.Errorf("❌ Expected array, got: %s", getType(result.Schema))
	}
}

// Test array construction syntax (equivalent to map)
func TestDiagnostic_ArrayConstructionInsteadOfMap(t *testing.T) {
	query, err := gojq.Parse(`[.tags[] | {value: .}]`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{"tags"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)

	if getType(result.Schema) == "array" {
		t.Log("✅ Array construction returns array")
		if result.Schema.Items != nil && result.Schema.Items.Left != nil {
			itemType := getType(result.Schema.Items.Left)
			t.Logf("Array item type: %s", itemType)
			if itemType == "object" {
				t.Log("✅ Array construction creates objects")
				// Check for value property
				if result.Schema.Items.Left.Properties != nil {
					if val, ok := result.Schema.Items.Left.Properties.Get("value"); ok && val.Left != nil {
						t.Logf("✅ Object has 'value' property: %s", getType(val.Left))
					}
				}
			}
		}
	} else {
		t.Errorf("❌ Expected array, got: %s", getType(result.Schema))
	}
}

// Test simple array of literal objects
func TestDiagnostic_ArrayOfLiteralObjects(t *testing.T) {
	query, err := gojq.Parse(`[{value: "test"}]`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{}, []string{})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)

	if getType(result.Schema) == "array" {
		t.Log("✅ Returns array")
		if result.Schema.Items != nil && result.Schema.Items.Left != nil {
			itemType := getType(result.Schema.Items.Left)
			t.Logf("Array item type: %s", itemType)
			if itemType == "object" {
				t.Log("✅ Items are objects")
				if result.Schema.Items.Left.Properties != nil {
					if val, ok := result.Schema.Items.Left.Properties.Get("value"); ok && val.Left != nil {
						valType := getType(val.Left)
						t.Logf("✅ Has 'value' property: %s", valType)
						if valType == "string" {
							t.Log("✅ value is string")
						}
					}
				}
			} else {
				t.Errorf("❌ Item type is %s, not object", itemType)
			}
		}
	}
}

// Test array of iterated strings
func TestSimpleArrayIteration(t *testing.T) {
	query, err := gojq.Parse(`[.tags[]]`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{"tags"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)
	t.Logf("Schema anyOf: %v", len(result.Schema.AnyOf) > 0)

	if result.Schema.Items != nil && result.Schema.Items.Left != nil {
		itemType := getType(result.Schema.Items.Left)
		t.Logf("Array item type: %s", itemType)
		t.Logf("Items.Left Type field: %v", result.Schema.Items.Left.Type)
		t.Logf("Items.Left AnyOf: %v", result.Schema.Items.Left.AnyOf != nil)
		if itemType == "string" {
			t.Log("✅ Correctly returns array of strings")
		}
	} else {
		t.Logf("Items: %v, Items.Left: %v", result.Schema.Items, result.Schema.Items)
		// Check if it's a union
		if len(result.Schema.AnyOf) > 0 {
			t.Logf("Schema is anyOf with %d branches", len(result.Schema.AnyOf))
			for i, branch := range result.Schema.AnyOf {
				if branch.Left != nil {
					branchType := getType(branch.Left)
					t.Logf("  Branch %d type: %s", i, branchType)
					if branchType == "array" && branch.Left.Items != nil && branch.Left.Items.Left != nil {
						t.Logf("    Items type: %s", getType(branch.Left.Items.Left))
					}
				}
			}
		}
	}
}

// Test nested array in object
func TestNestedArrayInObject(t *testing.T) {
	query, err := gojq.Parse(`{tags: [.tags[]]}`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{"tags"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)

	if result.Schema.AnyOf != nil {
		t.Logf("anyOf with %d branches", len(result.Schema.AnyOf))
		for i, b := range result.Schema.AnyOf {
			if b.Left != nil {
				t.Logf("  Branch %d: %s", i, getType(b.Left))
			}
		}
	}

	// Check that tags property has array with string items
	if result.Schema.Properties != nil {
		if tagsSchema, ok := result.Schema.Properties.Get("tags"); ok && tagsSchema.Left != nil {
			t.Logf("tags property type: %s", getType(tagsSchema.Left))
			if getType(tagsSchema.Left) == "array" && tagsSchema.Left.Items != nil && tagsSchema.Left.Items.Left != nil {
				itemsType := getType(tagsSchema.Left.Items.Left)
				t.Logf("  tags items type: %s", itemsType)
				if itemsType == "string" {
					t.Log("✅ tags array has string items")
				}
			}
		}
	}
}
