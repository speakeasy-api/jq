package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestAddition_IntegerPlusInteger tests that integer + integer = integer
func TestAddition_IntegerPlusInteger(t *testing.T) {
	// JQ: .a + 1
	// Input: {a: integer}
	// Expected: integer
	query, err := gojq.Parse(".a + 1")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{"a"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "integer" {
		t.Errorf("Expected type 'integer', got '%s'", resultType)
	}
}

// TestAddition_NumberPlusNumber tests that number + number = number
func TestAddition_NumberPlusNumber(t *testing.T) {
	// JQ: .a + .b
	// Input: {a: number, b: number}
	// Expected: number
	query, err := gojq.Parse(".a + .b")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": NumberType(),
		"b": NumberType(),
	}, []string{"a", "b"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "number" {
		t.Errorf("Expected type 'number', got '%s'", resultType)
	}
}

// TestAddition_IntegerPlusNumber tests type widening (integer + number = number)
func TestAddition_IntegerPlusNumber(t *testing.T) {
	// JQ: .a + .b
	// Input: {a: integer, b: number}
	// Expected: number (widened)
	query, err := gojq.Parse(".a + .b")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
		"b": NumberType(),
	}, []string{"a", "b"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "number" {
		t.Errorf("Expected type 'number' (widened from integer+number), got '%s'", resultType)
	}
}

// TestAddition_ArrayPlusArray_SameItemType tests array concatenation with same item type
func TestAddition_ArrayPlusArray_SameItemType(t *testing.T) {
	// JQ: .a + .b
	// Input: {a: array<integer>, b: array<integer>}
	// Expected: array<integer>
	query, err := gojq.Parse(".a + .b")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	arraySchema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
		Items: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](
			&oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
		),
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": arraySchema,
		"b": arraySchema,
	}, []string{"a", "b"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "array" {
		t.Errorf("Expected type 'array', got '%s'", resultType)
	}

	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Expected items schema")
	}

	itemType := getType(result.Schema.Items.Left)
	if itemType != "integer" {
		t.Errorf("Expected items type 'integer', got '%s'", itemType)
	}
}

// TestAddition_ArrayPlusArray_DifferentItemTypes tests array concat with type union
func TestAddition_ArrayPlusArray_DifferentItemTypes(t *testing.T) {
	// JQ: .a + .b
	// Input: {a: array<integer>, b: array<string>}
	// Expected: array<integer|string>
	query, err := gojq.Parse(".a + .b")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	intArraySchema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
		Items: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](
			&oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
		),
	}

	strArraySchema := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
		Items: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](
			StringType(),
		),
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": intArraySchema,
		"b": strArraySchema,
	}, []string{"a", "b"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "array" {
		t.Errorf("Expected type 'array', got '%s'", resultType)
	}

	// Items should be a union of integer and string
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Expected items schema")
	}

	// Check for anyOf or type union
	itemSchema := result.Schema.Items.Left
	hasIntegerType := false
	hasStringType := false

	// Check if it's an anyOf
	if itemSchema.AnyOf != nil && len(itemSchema.AnyOf) > 0 {
		for _, variant := range itemSchema.AnyOf {
			if variant.Left != nil {
				vType := getType(variant.Left)
				if vType == "integer" {
					hasIntegerType = true
				}
				if vType == "string" {
					hasStringType = true
				}
			}
		}
		if !hasIntegerType || !hasStringType {
			t.Errorf("Expected anyOf with both integer and string types, got integer=%v, string=%v", hasIntegerType, hasStringType)
		}
	} else {
		// Check direct type
		itemType := getType(itemSchema)
		t.Logf("Item type (non-anyOf): %s", itemType)
		// If implementation uses type array or keeps both, check that
	}

	t.Logf("Array items schema type: %s, anyOf count: %d", getType(itemSchema), len(itemSchema.AnyOf))
}

// TestAddition_StringPlusString tests string concatenation
func TestAddition_StringPlusString(t *testing.T) {
	// JQ: .a + .b
	// Input: {a: string, b: string}
	// Expected: string
	query, err := gojq.Parse(".a + .b")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": StringType(),
		"b": StringType(),
	}, []string{"a", "b"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "string" {
		t.Errorf("Expected type 'string', got '%s'", resultType)
	}
}

// TestAddition_ObjectPlusObject_DisjointProperties tests object merge with no conflicts
func TestAddition_ObjectPlusObject_DisjointProperties(t *testing.T) {
	// JQ: .x + .y
	// Input: {x: {a: int}, y: {b: int}}
	// Expected: {a: int, b: int}
	query, err := gojq.Parse(".x + .y")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	objX := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{"a"})

	objY := BuildObject(map[string]*oas3.Schema{
		"b": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{"b"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"x": objX,
		"y": objY,
	}, []string{"x", "y"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "object" {
		t.Errorf("Expected type 'object', got '%s'", resultType)
	}

	// Check that both properties a and b are present
	if result.Schema.Properties == nil {
		t.Fatal("Expected properties in merged object")
	}

	if _, ok := result.Schema.Properties.Get("a"); !ok {
		t.Error("Expected property 'a' in merged object")
	}

	if _, ok := result.Schema.Properties.Get("b"); !ok {
		t.Error("Expected property 'b' in merged object")
	}

	// Check required includes both
	hasA := false
	hasB := false
	for _, req := range result.Schema.Required {
		if req == "a" {
			hasA = true
		}
		if req == "b" {
			hasB = true
		}
	}

	if !hasA || !hasB {
		t.Errorf("Expected both 'a' and 'b' in required, got: %v", result.Schema.Required)
	}
}

// TestAddition_ObjectPlusObject_OverlappingProperties tests that right wins on conflicts
func TestAddition_ObjectPlusObject_OverlappingProperties(t *testing.T) {
	// JQ: .left + .right
	// Input: {left: {a: integer}, right: {a: number}}
	// Expected: {a: number} (right wins)
	query, err := gojq.Parse(".left + .right")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	objLeft := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{"a"})

	objRight := BuildObject(map[string]*oas3.Schema{
		"a": NumberType(),
	}, []string{"a"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"left":  objLeft,
		"right": objRight,
	}, []string{"left", "right"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Check that property 'a' has type 'number' (from right side)
	if result.Schema.Properties == nil {
		t.Fatal("Expected properties")
	}

	propA, ok := result.Schema.Properties.Get("a")
	if !ok {
		t.Fatal("Expected property 'a'")
	}

	if propA.Left == nil {
		t.Fatal("Expected property 'a' schema")
	}

	propType := getType(propA.Left)
	if propType != "number" {
		t.Errorf("Expected property 'a' type 'number' (right wins), got '%s'", propType)
	}
}

// TestAddition_ValuePlusNull tests that value + null = value (null is identity)
func TestAddition_ValuePlusNull(t *testing.T) {
	testCases := []struct {
		name       string
		schema     *oas3.Schema
		expectType string
	}{
		{"integer", &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)}, "integer"},
		{"number", NumberType(), "number"},
		{"string", StringType(), "string"},
		{"array", &oas3.Schema{
			Type:  oas3.NewTypeFromString(oas3.SchemaTypeArray),
			Items: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](&oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)}),
		}, "array"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// JQ: .a + null
			query, err := gojq.Parse(".a + null")
			if err != nil {
				t.Fatalf("Failed to parse query: %v", err)
			}

			inputSchema := BuildObject(map[string]*oas3.Schema{
				"a": tc.schema,
			}, []string{"a"})

			result, err := RunSchema(context.Background(), query, inputSchema)
			if err != nil {
				t.Fatalf("RunSchema failed: %v", err)
			}

			if result.Schema == nil {
				t.Fatal("Result schema is nil")
			}

			resultType := getType(result.Schema)
			if resultType != tc.expectType {
				t.Errorf("Expected type '%s', got '%s'", tc.expectType, resultType)
			}
		})
	}
}

// TestAddition_NullPlusValue tests that null + value = value (null is identity on LHS)
func TestAddition_NullPlusValue(t *testing.T) {
	// JQ: null + .a
	// Input: {a: integer}
	// Expected: integer
	query, err := gojq.Parse("null + .a")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{"a"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "integer" {
		t.Errorf("Expected type 'integer', got '%s'", resultType)
	}
}

// TestAddition_OptionalPlusLiteral tests that missing property (treated as null) + literal works
func TestAddition_OptionalPlusLiteral(t *testing.T) {
	// JQ: .a + 1
	// Input: {a: integer} with a NOT required (optional)
	// Expected: integer (null + 1 = 1, or int + 1 = int)
	query, err := gojq.Parse(".a + 1")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{}) // a is NOT required

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "integer" {
		t.Errorf("Expected type 'integer', got '%s'", resultType)
	}
}

// TestAddition_ChainAddition tests chaining multiple additions
func TestAddition_ChainAddition(t *testing.T) {
	// JQ: .o1 + .o2 + .o3
	// Input: {o1: {a: int}, o2: {b: int}, o3: {c: int}}
	// Expected: {a: int, b: int, c: int}
	query, err := gojq.Parse(".o1 + .o2 + .o3")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	obj1 := BuildObject(map[string]*oas3.Schema{
		"a": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{"a"})

	obj2 := BuildObject(map[string]*oas3.Schema{
		"b": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{"b"})

	obj3 := BuildObject(map[string]*oas3.Schema{
		"c": {Type: oas3.NewTypeFromString(oas3.SchemaTypeInteger)},
	}, []string{"c"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"o1": obj1,
		"o2": obj2,
		"o3": obj3,
	}, []string{"o1", "o2", "o3"})

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Check all three properties are present
	if result.Schema.Properties == nil {
		t.Fatal("Expected properties in merged object")
	}

	for _, propName := range []string{"a", "b", "c"} {
		if _, ok := result.Schema.Properties.Get(propName); !ok {
			t.Errorf("Expected property '%s' in merged object", propName)
		}
	}

	// Check required includes all three
	requiredSet := make(map[string]bool)
	for _, req := range result.Schema.Required {
		requiredSet[req] = true
	}

	for _, propName := range []string{"a", "b", "c"} {
		if !requiredSet[propName] {
			t.Errorf("Expected '%s' in required, got: %v", propName, result.Schema.Required)
		}
	}
}

// TestAddition_ObjectLiteralChain tests object literal addition with conflicts
func TestAddition_ObjectLiteralChain(t *testing.T) {
	// JQ: {a: 1} + {b: 2} + {c: 3} + {a: 42}
	// Input: null
	// Expected: {a: 42, b: 2, c: 3} (rightmost 'a' wins)
	query, err := gojq.Parse("{a: 1} + {b: 2} + {c: 3} + {a: 42}")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	inputSchema := ConstNull()

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	resultType := getType(result.Schema)
	if resultType != "object" {
		t.Errorf("Expected type 'object', got '%s'", resultType)
	}

	// Check properties: a, b, c
	if result.Schema.Properties == nil {
		t.Fatal("Expected properties")
	}

	// Property 'a' should be integer with enum [42] (rightmost wins)
	if propA, ok := result.Schema.Properties.Get("a"); ok && propA.Left != nil {
		aType := getType(propA.Left)
		if aType != "integer" {
			t.Errorf("Expected property 'a' type 'integer', got '%s'", aType)
		}
		// Check that it's the value 42 (rightmost), not 1
		if propA.Left.Enum != nil && len(propA.Left.Enum) == 1 {
			if propA.Left.Enum[0].Value != "42" {
				t.Errorf("Expected property 'a' enum value '42', got '%s'", propA.Left.Enum[0].Value)
			}
		}
	} else {
		t.Error("Expected property 'a'")
	}

	// Properties b and c should exist
	if _, ok := result.Schema.Properties.Get("b"); !ok {
		t.Error("Expected property 'b'")
	}
	if _, ok := result.Schema.Properties.Get("c"); !ok {
		t.Error("Expected property 'c'")
	}
}
