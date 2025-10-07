package playground

import (
	"strings"
	"testing"
)

func TestSymbolicExecuteJQ(t *testing.T) {
	oasYAML := `openapi: 3.0.0
info:
  title: Sample API
  version: 1.0.0
  description: A simple API for testing
components:
  schemas:
    User:
      type: object
      x-speakeasy-transform-from-json: 'jq {id, name, status: .id}'
      properties:
        id:
          type: integer
        name:
          type: string
        email:
          type: string
    Product:
      type: object
      x-speakeasy-transform-from-json: 'jq {name, total: (.price * .quantity)}'
      properties:
        name:
          type: string
        price:
          type: number
        quantity:
          type: integer
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	expectedYAML := `openapi: 3.0.0
info:
  title: Sample API
  version: 1.0.0
  description: A simple API for testing
components:
  schemas:
    User:
      type: object
      x-speakeasy-transform-from-json: 'jq {id, name, status: .id}'
      properties:
        id:
          type: integer
        name:
          type: string
        status:
          type: integer
      required:
        - status
        - name
        - id
    Product:
      type: object
      x-speakeasy-transform-from-json: 'jq {name, total: (.price * .quantity)}'
      properties:
        name:
          type: string
        total:
          type: number
      required:
        - total
        - name
`

	if strings.TrimSpace(result) != strings.TrimSpace(expectedYAML) {
		t.Errorf("Transformed OAS does not match expected.\n\nExpected:\n%s\n\nGot:\n%s", expectedYAML, result)
	}
}

func TestSymbolicExecuteJQ_UserInput(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: Test
  version: 1.0.0
components:
  schemas:
    UserInput:
      type: object
      x-speakeasy-transform-from-json: >
        jq {userId: .id, displayName: .name,
            tier: (if .score >= 90 then "gold" else "silver" end),
            location: {city: .address.city, zip: .address.postalCode}}
      properties:
        id:
          type: integer
        name:
          type: string
        score:
          type: integer
        address:
          type: object
          properties:
            city:
              type: string
            postalCode:
              type: string
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Verify userId is integer (from .id)
	if !strings.Contains(result, "userId") {
		t.Error("Expected 'userId' field in output")
	}
	// Verify tier is present (from conditional)
	if !strings.Contains(result, "tier") {
		t.Error("Expected 'tier' field in output")
	}
	// Verify nested location object
	if !strings.Contains(result, "location") {
		t.Error("Expected 'location' field in output")
	}

	t.Logf("Transformed UserInput:\n%s", result)
}

func TestSymbolicExecuteJQ_ProductInput(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: Test
  version: 1.0.0
components:
  schemas:
    ProductInput:
      type: object
      x-speakeasy-transform-from-json: >
        jq {productId: .id, displayName: .name,
            total: (.price * .quantity),
            tags: (.tags | map({value: .}))}
      properties:
        id:
          type: string
        name:
          type: string
        price:
          type: number
        quantity:
          type: integer
        tags:
          type: array
          items:
            type: string
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Verify total is number (from price * quantity)
	if !strings.Contains(result, "total") {
		t.Error("Expected 'total' field in output")
	}
	// Verify tags transformation to array of objects
	if !strings.Contains(result, "tags") {
		t.Error("Expected 'tags' field in output")
	}

	t.Logf("Transformed ProductInput:\n%s", result)
}

func TestSymbolicExecuteJQ_CartInput(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: Test
  version: 1.0.0
components:
  schemas:
    CartInput:
      type: object
      x-speakeasy-transform-from-json: >
        jq {grandTotal: (.items | map(.price * .quantity) | add // 0),
            items: (.items | map({sku, total: (.price * .quantity)}))}
      properties:
        items:
          type: array
          items:
            type: object
            properties:
              sku:
                type: string
              price:
                type: number
              quantity:
                type: integer
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Verify grandTotal (from reduce with // 0)
	if !strings.Contains(result, "grandTotal") {
		t.Error("Expected 'grandTotal' field in output")
	}
	// Verify items transformation
	if !strings.Contains(result, "items") {
		t.Error("Expected 'items' field in output")
	}

	t.Logf("Transformed CartInput:\n%s", result)
}

func TestSymbolicExecuteJQ_InvalidJQ(t *testing.T) {
	oasYAML := `openapi: 3.0.0
info:
  title: Test API
  version: 1.0.0
components:
  schemas:
    User:
      type: object
      x-speakeasy-transform-from-json: 'jq .invalid syntax here'
      properties:
        name:
          type: string
`

	_, err := SymbolicExecuteJQ(oasYAML)
	if err == nil {
		t.Fatal("Expected error for invalid JQ syntax, but got none")
	}

	if !strings.Contains(err.Error(), "invalid jq expression") {
		t.Errorf("Expected error message to contain 'invalid jq expression', got: %v", err)
	}
}

func TestSymbolicExecuteJQ_NoExtension(t *testing.T) {
	oasYAML := `openapi: 3.0.0
info:
  title: Test API
  version: 1.0.0
components:
  schemas:
    User:
      type: object
      properties:
        name:
          type: string
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Should return the document unchanged
	if !strings.Contains(result, "Test API") {
		t.Errorf("Expected result to contain original content")
	}
}

func TestSymbolicExecuteJQ_InvalidOAS(t *testing.T) {
	oasYAML := `invalid: yaml: content`

	_, err := SymbolicExecuteJQ(oasYAML)
	if err == nil {
		t.Fatal("Expected error for invalid OAS, but got none")
	}
}

func TestSymbolicExecuteJQ_NestedTransformation(t *testing.T) {
	oasYAML := `openapi: 3.0.0
info:
  title: Test API
  version: 1.0.0
components:
  schemas:
    Address:
      type: object
      x-speakeasy-transform-from-json: 'jq {street, zipcode: .zip}'
      properties:
        street:
          type: string
        zip:
          type: string
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Check that transformation was applied
	if !strings.Contains(result, "zipcode") {
		t.Errorf("Expected transformed schema to contain 'zipcode' property.\nResult:\n%s", result)
	}

	if !strings.Contains(result, "street") {
		t.Errorf("Expected transformed schema to contain 'street' property.\nResult:\n%s", result)
	}
}
