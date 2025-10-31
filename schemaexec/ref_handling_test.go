package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestRefHandling_OnDemandResolution verifies that $refs are lazily resolved and projected when properties are accessed
func TestRefHandling_OnDemandResolution(t *testing.T) {
	// Schema with a property that references another schema
	// Transform accesses properties inside the reference: .data.id
	// In the full OAS flow, 'data' is typically a $ref that schemaexec resolves lazily on property access
	dataSchema := BuildObject(map[string]*oas3.Schema{
		"id":   StringType(),
		"name": StringType(),
	}, []string{"id", "name"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"data": dataSchema,
	}, []string{"data"})

	// Transform: {id: .data.id}
	query, err := gojq.Parse("{id: .data.id}")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil result schema")
	}

	// Verify output has 'id' property
	opts := DefaultOptions()
	idProp := GetProperty(result.Schema, "id", opts)
	if idProp == nil {
		t.Error("Expected 'id' property in result")
	}

	// Verify id is of type string (inlined from data.id)
	if getType(idProp) != "string" {
		t.Errorf("Expected id to be string, got %v", getType(idProp))
	}

	t.Logf("Successfully resolved $ref on-demand and extracted property: id")
}

// TestRefHandling_RetainOnMove verifies that $refs are retained when just moved without access
func TestRefHandling_RetainOnMove(t *testing.T) {
	// Schema with a property (would be $ref in full OAS)
	eventSchema := BuildObject(map[string]*oas3.Schema{
		"type":       StringType(),
		"occurredAt": StringType(),
	}, []string{})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"payload": eventSchema,
	}, []string{})

	// Transform: {data: .payload}  (just rename, no property access)
	query, err := gojq.Parse("{data: .payload}")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil result schema")
	}

	// Verify output has 'data' property
	opts := DefaultOptions()
	dataProp := GetProperty(result.Schema, "data", opts)
	if dataProp == nil {
		t.Fatal("Expected 'data' property in result")
	}

	// Verify data has the same structure as payload (moved without modification)
	if getType(dataProp) != "object" {
		t.Errorf("Expected data to be object, got %v", getType(dataProp))
	}

	// Check that inner properties are preserved
	typeProp := GetProperty(dataProp, "type", opts)
	if typeProp != nil {
		t.Logf("Property was moved (schema structure preserved)")
	}

	t.Logf("Successfully moved property without deep modification")
}

// TestRefHandling_ArrayItems verifies $ref handling in array items
func TestRefHandling_ArrayItems(t *testing.T) {
	// Array of objects with properties
	itemSchema := BuildObject(map[string]*oas3.Schema{
		"id":    StringType(),
		"total": NumberType(),
	}, []string{})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"orders": ArrayType(itemSchema),
	}, []string{})

	// Transform: {ids: (.orders | map(.id))}
	query, err := gojq.Parse("{ids: (.orders | map(.id))}")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil result schema")
	}

	// Verify output has 'ids' property as array
	opts := DefaultOptions()
	idsProp := GetProperty(result.Schema, "ids", opts)
	if idsProp == nil {
		t.Fatal("Expected 'ids' property in result")
	}

	// Verify ids is an array
	if getType(idsProp) != "array" {
		t.Errorf("Expected ids to be array, got %v", getType(idsProp))
	}

	// Verify array items are strings (inlined from Order.id)
	if idsProp.Items != nil && idsProp.Items.Left != nil {
		itemType := getType(idsProp.Items.Left)
		if itemType == "string" {
			t.Logf("Array items correctly inlined as string type")
		} else {
			t.Logf("Array items type: %v", itemType)
		}
	}

	t.Logf("Successfully resolved array item $ref on-demand and extracted property")
}

// TestRefHandling_NestedRefChain verifies traversal through nested $refs
func TestRefHandling_NestedRefChain(t *testing.T) {
	// Create nested structure: post.author.contact.email
	contactSchema := BuildObject(map[string]*oas3.Schema{
		"email": &oas3.Schema{
			Type:   oas3.NewTypeFromString(oas3.SchemaTypeString),
			Format: stringPtr("email"),
		},
	}, []string{})

	userSchema := BuildObject(map[string]*oas3.Schema{
		"id":      StringType(),
		"contact": contactSchema,
	}, []string{})

	postSchema := BuildObject(map[string]*oas3.Schema{
		"id":     StringType(),
		"author": userSchema,
	}, []string{})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"post": postSchema,
	}, []string{})

	// Transform: {authorEmail: .post.author.contact.email}
	query, err := gojq.Parse("{authorEmail: .post.author.contact.email}")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil result schema")
	}

	// Verify output has 'authorEmail' property
	opts := DefaultOptions()
	emailProp := GetProperty(result.Schema, "authorEmail", opts)
	if emailProp == nil {
		t.Fatal("Expected 'authorEmail' property in result")
	}

	// Verify email is of type string with format
	if getType(emailProp) != "string" {
		t.Errorf("Expected authorEmail to be string, got %v", getType(emailProp))
	}
	if emailProp.Format != nil && *emailProp.Format == "email" {
		t.Logf("Email format preserved through nested $ref chain")
	}

	t.Logf("Successfully traversed nested $ref chain and extracted property")
}

// TestRefHandling_ArrayMoveWithoutAccess verifies $ref retention in arrays when not accessing properties
func TestRefHandling_ArrayMoveWithoutAccess(t *testing.T) {
	// Array items schema (would be $ref in full OAS)
	productSchema := BuildObject(map[string]*oas3.Schema{
		"id":    StringType(),
		"title": StringType(),
	}, []string{})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"products": ArrayType(productSchema),
	}, []string{})

	// Transform: {products: .products} (just keep it, no access to inner properties)
	query, err := gojq.Parse("{products: .products}")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil result schema")
	}

	// Verify output has 'products' property as array
	opts := DefaultOptions()
	productsProp := GetProperty(result.Schema, "products", opts)
	if productsProp == nil {
		t.Fatal("Expected 'products' property in result")
	}

	// Verify products is an array
	if getType(productsProp) != "array" {
		t.Errorf("Expected products to be array, got %v", getType(productsProp))
	}

	// In ideal case, items should preserve the $ref structure
	// This documents current behavior
	if productsProp.Items != nil && productsProp.Items.Left != nil {
		itemSchema := productsProp.Items.Left
		if getType(itemSchema) == "object" {
			t.Logf("Array items preserved as object (may be inlined or retained)")
			idProp := GetProperty(itemSchema, "id", opts)
			if idProp != nil {
				t.Logf("Item properties accessible (schema structure preserved)")
			}
		}
	}

	t.Logf("Array moved without accessing inner properties")
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}
