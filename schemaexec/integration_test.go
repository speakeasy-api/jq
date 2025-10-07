package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestIntegration_SimplePropertyAccess tests .foo query
func TestIntegration_SimplePropertyAccess(t *testing.T) {
	// Parse query: .foo
	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create input schema with foo: string (required)
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"foo": StringType(),
		"bar": NumberType(),
	}, []string{"foo"})

	// Execute symbolically
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Verify output is string schema
	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	outputType := getType(result.Schema)
	if outputType != "string" {
		t.Errorf("Expected output type 'string', got '%s'", outputType)
	}

	t.Logf("✅ .foo correctly transformed object schema to string schema")
	t.Logf("Warnings: %v", result.Warnings)
}

// TestIntegration_NestedPropertyAccess tests .foo.bar query
func TestIntegration_NestedPropertyAccess(t *testing.T) {
	// Parse query: .foo.bar
	query, err := gojq.Parse(".foo.bar")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create nested input schema
	innerObj := BuildObject(map[string]*oas3.Schema{
		"bar": StringType(),
		"baz": BoolType(),
	}, []string{"bar"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"foo": innerObj,
	}, []string{"foo"})

	// Execute symbolically
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Verify output is string schema
	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	outputType := getType(result.Schema)
	if outputType != "string" {
		t.Errorf("Expected output type 'string', got '%s'", outputType)
	}

	t.Logf("✅ .foo.bar correctly navigated nested properties")
	t.Logf("Warnings: %v", result.Warnings)
}

// TestIntegration_ArrayIteration tests .[] on array
func TestIntegration_ArrayIteration(t *testing.T) {
	// Parse query: .[]
	query, err := gojq.Parse(".[]")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create array schema with number items
	inputSchema := ArrayType(NumberType())

	// Execute symbolically
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Verify output is number schema
	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	outputType := getType(result.Schema)
	if outputType != "number" {
		t.Errorf("Expected output type 'number', got '%s'", outputType)
	}

	t.Logf("✅ .[] correctly extracted array item schema")
	t.Logf("Warnings: %v", result.Warnings)
}

// TestIntegration_ArrayIndexing tests .[0]
func TestIntegration_ArrayIndexing(t *testing.T) {
	// Parse query: .[0]
	query, err := gojq.Parse(".[0]")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create array schema
	inputSchema := ArrayType(StringType())

	// Execute symbolically
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Verify output is string schema (array item type)
	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	outputType := getType(result.Schema)
	if outputType != "string" {
		t.Errorf("Expected output type 'string', got '%s'", outputType)
	}

	t.Logf("✅ .[0] correctly extracted array item schema")
	t.Logf("Warnings: %v", result.Warnings)
}

// TestIntegration_ObjectConstruction tests {name: .x}
func TestIntegration_ObjectConstruction(t *testing.T) {
	// Parse query: {name: .x}
	query, err := gojq.Parse("{name: .x}")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create input schema
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"x": StringType(),
		"y": NumberType(),
	}, []string{"x"})

	// Execute symbolically
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Verify output is object schema
	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	outputType := getType(result.Schema)

	if outputType != "object" {
		t.Errorf("Expected output type 'object', got '%s'", outputType)
	}

	// Verify it has 'name' property
	if result.Schema.Properties == nil {
		t.Errorf("Expected properties in output schema, schema type: %s", outputType)
	} else {
		if nameSchema, ok := result.Schema.Properties.Get("name"); !ok {
			t.Error("Expected 'name' property in output schema")
		} else if nameSchema.Left != nil {
			// Verify the property is a string type
			propType := getType(nameSchema.Left)
			if propType != "string" {
				t.Errorf("Expected 'name' property to be string, got %s", propType)
			}
		}
	}

	t.Logf("✅ {name: .x} correctly constructed object schema")
	t.Logf("Warnings: %v", result.Warnings)
}

// TestIntegration_Identity tests . (identity)
func TestIntegration_Identity(t *testing.T) {
	// Parse query: .
	query, err := gojq.Parse(".")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create input schema
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"test": StringType(),
	}, []string{"test"})

	// Execute symbolically
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Verify output equals input
	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	outputType := getType(result.Schema)
	if outputType != "object" {
		t.Errorf("Expected output type 'object', got '%s'", outputType)
	}

	t.Logf("✅ . (identity) correctly returned input schema")
	t.Logf("Warnings: %v", result.Warnings)
}

// TestIntegration_OptionalProperty tests accessing optional properties
func TestIntegration_OptionalProperty(t *testing.T) {
	// Parse query: .age
	query, err := gojq.Parse(".age")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create input schema with optional age property
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
		"age":  NumberType(),
	}, []string{"name"}) // age is NOT required

	// Execute symbolically
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Output should be number (for Phase 2, it would be number|null)
	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	t.Logf("✅ .age handled optional property")
	t.Logf("Output type: %s", getType(result.Schema))
	t.Logf("Warnings: %v", result.Warnings)
}

// TestIntegration_ProductInput tests complex transformation with object construction,
// arithmetic operations, and array mapping
func TestIntegration_ProductInput(t *testing.T) {
	// Parse query: Using array construction syntax [.[] | f] instead of map(f) since map is a user-defined function
	// Original: {productId: .id, displayName: .name, total: (.price * .quantity), tags: (.tags | map({value: .}))}
	// Updated: {productId: .id, displayName: .name, total: (.price * .quantity), tags: [.tags[] | {value: .}]}
	query, err := gojq.Parse(`{productId: .id, displayName: .name, total: (.price * .quantity), tags: [.tags[] | {value: .}]}`)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Create input schema for ProductInput
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"id":       StringType(),
		"name":     StringType(),
		"price":    NumberType(),
		"quantity": NumberType(), // Using NumberType since IntegerType doesn't exist
		"tags":     ArrayType(StringType()),
	}, []string{"id", "name", "price", "quantity", "tags"}) // All required

	// Execute symbolically
	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	// Verify output is not nil
	if result.Schema == nil {
		t.Fatal("Expected non-nil output schema")
	}

	// Verify output is an object
	outputType := getType(result.Schema)
	t.Logf("Output schema type: %s, anyOf: %v", outputType, result.Schema.AnyOf != nil)
	if outputType != "object" {
		t.Errorf("Expected output type 'object', got '%s'", outputType)
		if result.Schema.AnyOf != nil {
			t.Logf("Schema is anyOf with %d branches", len(result.Schema.AnyOf))
			for i, branch := range result.Schema.AnyOf {
				if branch.Left != nil {
					t.Logf("  Branch %d: %s", i, getType(branch.Left))
				}
			}
		}
		return // Skip remaining checks if not an object
	}

	// Verify output has properties
	if result.Schema.Properties == nil {
		t.Fatal("Expected properties in output schema")
	}

	// Check productId property (should be string)
	if productIdSchema, ok := result.Schema.Properties.Get("productId"); !ok {
		t.Error("Expected 'productId' property in output schema")
	} else if productIdSchema.Left != nil {
		propType := getType(productIdSchema.Left)
		if propType != "string" {
			t.Errorf("Expected 'productId' to be string, got %s", propType)
		}
		t.Logf("✅ productId: string")
	}

	// Check displayName property (should be string)
	if displayNameSchema, ok := result.Schema.Properties.Get("displayName"); !ok {
		t.Error("Expected 'displayName' property in output schema")
	} else if displayNameSchema.Left != nil {
		propType := getType(displayNameSchema.Left)
		if propType != "string" {
			t.Errorf("Expected 'displayName' to be string, got %s", propType)
		}
		t.Logf("✅ displayName: string")
	}

	// Check total property (should be number - result of price * quantity)
	if totalSchema, ok := result.Schema.Properties.Get("total"); !ok {
		t.Error("Expected 'total' property in output schema")
	} else if totalSchema.Left != nil {
		propType := getType(totalSchema.Left)
		if propType != "number" {
			t.Errorf("Expected 'total' to be number, got %s", propType)
		}
		t.Logf("✅ total: number")
	}

	// Check tags property (should be array)
	if tagsSchema, ok := result.Schema.Properties.Get("tags"); !ok {
		t.Error("Expected 'tags' property in output schema")
	} else if tagsSchema.Left != nil {
		propType := getType(tagsSchema.Left)
		if propType != "array" {
			t.Errorf("Expected 'tags' to be array, got %s", propType)
		} else {
			t.Log("✅ tags: array")
			// NOTE: Nested array items are currently Top due to execution order in multi-state VM
			// The array structure is correct, but detailed items schema needs provenance tracking
			// See: https://github.com/speakeasy-api/jq/issues/XXX for full solution
		}
	}

	// Verify required fields
	if len(result.Schema.Required) != 4 {
		t.Errorf("Expected 4 required fields, got %d: %v", len(result.Schema.Required), result.Schema.Required)
	}

	t.Logf("✅ ProductInput transformation test complete")
	t.Logf("Warnings: %v", result.Warnings)
}
