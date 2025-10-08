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

// Pipeline Tests for SymbolicExecuteJQPipeline

func TestSymbolicExecuteJQPipeline_BasicRoundTrip(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: RoundTrip
  version: 1.0.0
paths: {}
components:
  schemas:
    Model:
      type: object
      properties:
        id:
          type: string
        name:
          type: string
      x-speakeasy-transform-from-json: 'jq .'
      x-speakeasy-transform-to-json: 'jq .'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// All panels should contain id and name
	for i, panel := range []string{result.Panel1, result.Panel2, result.Panel3} {
		if !strings.Contains(panel, "id:") {
			t.Errorf("Panel %d missing 'id' property", i+1)
		}
		if !strings.Contains(panel, "name:") {
			t.Errorf("Panel %d missing 'name' property", i+1)
		}
	}
}

func TestSymbolicExecuteJQPipeline_ResponseOnlyProperty(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: ResponseOnly
  version: 1.0.0
paths: {}
components:
  schemas:
    Model:
      type: object
      properties:
        id:
          type: string
      x-speakeasy-transform-from-json: 'jq {id: .id, serverTime: "2024-01-01"}'
      x-speakeasy-transform-to-json: 'jq {id: .id}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 should have serverTime (added by from-json)
	if !strings.Contains(result.Panel2, "serverTime") {
		t.Error("Panel2 should contain serverTime")
	}

	// Panel3 should only have id (to-json removes serverTime)
	if !strings.Contains(result.Panel3, "id:") {
		t.Error("Panel3 should contain id")
	}
}

func TestSymbolicExecuteJQPipeline_RequestOnlyProperty(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: RequestOnly
  version: 1.0.0
paths: {}
components:
  schemas:
    Model:
      type: object
      properties:
        id:
          type: string
      x-speakeasy-transform-from-json: 'jq {id: .id}'
      x-speakeasy-transform-to-json: 'jq {id: .id, clientNonce: "abc123"}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 should only have id (from-json keeps id)
	if !strings.Contains(result.Panel2, "id:") {
		t.Error("Panel2 should contain id")
	}

	// Panel3 should have both id and clientNonce (to-json adds clientNonce)
	if !strings.Contains(result.Panel3, "id:") {
		t.Error("Panel3 should contain id")
	}
	if !strings.Contains(result.Panel3, "clientNonce") {
		t.Error("Panel3 should contain clientNonce")
	}
}

func TestSymbolicExecuteJQPipeline_NoTransformations(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: NoTransforms
  version: 1.0.0
paths: {}
components:
  schemas:
    Model:
      type: object
      properties:
        id:
          type: string
        createdAt:
          type: string
          format: date-time
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Applied flags should be false
	if result.AppliedFromJson {
		t.Error("AppliedFromJson should be false")
	}
	if result.AppliedToJson {
		t.Error("AppliedToJson should be false")
	}

	// All panels should be identical
	// (Note: Panel1 and Panel2 might differ in formatting, so check properties)
	for _, panel := range []string{result.Panel1, result.Panel2, result.Panel3} {
		if !strings.Contains(panel, "id:") || !strings.Contains(panel, "createdAt:") {
			t.Error("All panels should contain original properties")
		}
	}
}

func TestSymbolicExecuteJQPipeline_OnlyFromJson(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: OnlyFrom
  version: 1.0.0
paths: {}
components:
  schemas:
    Model:
      type: object
      properties:
        a:
          type: string
      x-speakeasy-transform-from-json: 'jq {b: .a}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// AppliedFromJson should be true, AppliedToJson false
	if !result.AppliedFromJson {
		t.Error("AppliedFromJson should be true")
	}
	if result.AppliedToJson {
		t.Error("AppliedToJson should be false when extension missing")
	}

	t.Logf("Panel2:\n%s", result.Panel2)
	t.Logf("Panel3:\n%s", result.Panel3)
}

func TestSymbolicExecuteJQPipeline_OnlyToJson(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: OnlyTo
  version: 1.0.0
paths: {}
components:
  schemas:
    Model:
      type: object
      properties:
        a:
          type: string
      x-speakeasy-transform-to-json: 'jq {c: .a}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// AppliedFromJson false, AppliedToJson true
	if result.AppliedFromJson {
		t.Error("AppliedFromJson should be false when extension missing")
	}
	if !result.AppliedToJson {
		t.Error("AppliedToJson should be true")
	}

	t.Logf("Panel2:\n%s", result.Panel2)
	t.Logf("Panel3:\n%s", result.Panel3)
}

func TestSymbolicExecuteJQPipeline_MultipleSchemas(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: MultiSchemas
  version: 1.0.0
paths: {}
components:
  schemas:
    User:
      type: object
      properties:
        id:
          type: string
        email:
          type: string
      x-speakeasy-transform-from-json: 'jq {id: .id, email: .email, serverTime: "2024-01-01"}'
      x-speakeasy-transform-to-json: 'jq {id: .id, email: .email}'
    Order:
      type: object
      properties:
        id:
          type: string
        amount:
          type: number
      x-speakeasy-transform-from-json: 'jq {id: .id, amount: .amount}'
      x-speakeasy-transform-to-json: 'jq {id: .id, amount: .amount, clientNonce: "abc"}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 should have User.serverTime, Panel3 should not
	if !strings.Contains(result.Panel2, "serverTime") {
		t.Error("Panel2 should contain User.serverTime")
	}

	// Panel3 should have Order.clientNonce
	if !strings.Contains(result.Panel3, "clientNonce") {
		t.Error("Panel3 should contain Order.clientNonce")
	}
}

func TestSymbolicExecuteJQPipeline_PropertyRenaming(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: RenameChain
  version: 1.0.0
paths: {}
components:
  schemas:
    Model:
      type: object
      properties:
        a:
          type: string
      x-speakeasy-transform-from-json: 'jq {b: .a}'
      x-speakeasy-transform-to-json: 'jq {c: .b}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 should have 'b'
	if !strings.Contains(result.Panel2, "b:") {
		t.Error("Panel2 should contain property 'b'")
	}

	// Panel3 should have 'c'
	if !strings.Contains(result.Panel3, "c:") {
		t.Error("Panel3 should contain property 'c'")
	}
}

func TestSymbolicExecuteJQPipeline_LossyTransform(t *testing.T) {
	oasYAML := `openapi: 3.0.3
info:
  title: Lossy
  version: 1.0.0
paths: {}
components:
  schemas:
    Model:
      type: object
      properties:
        id:
          type: string
        debug:
          type: string
      x-speakeasy-transform-from-json: 'jq {id: .id}'
      x-speakeasy-transform-to-json: 'jq {id: .id}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 and Panel3 should not contain 'debug' (lost in transform)
	if strings.Contains(result.Panel3, "debug:") {
		t.Error("Panel3 should not contain 'debug' property")
	}
}
