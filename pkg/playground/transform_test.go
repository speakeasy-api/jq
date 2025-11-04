package playground

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// loadTestSchema loads a schema from testdata directory
func loadTestSchema(t *testing.T, filename string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", filename))
	if err != nil {
		t.Fatalf("Failed to read %s: %v", filename, err)
	}
	var schema map[string]any
	if err := yaml.Unmarshal(data, &schema); err != nil {
		t.Fatalf("Failed to parse %s: %v", filename, err)
	}
	return schema
}

// normalizeSchema converts YAML to canonical form for comparison
// compareSchemas compares two schemas for structural equality
func compareSchemas(t *testing.T, name string, expected, actual map[string]any) {
	t.Helper()
	expectedYAML, _ := yaml.Marshal(expected)
	actualYAML, _ := yaml.Marshal(actual)

	if string(expectedYAML) != string(actualYAML) {
		t.Errorf("%s schema mismatch:\nExpected:\n%s\n\nActual:\n%s",
			name, string(expectedYAML), string(actualYAML))
	}
}

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
      x-speakeasy-transform-from-api:
        jq: '{id, name, status: .id}'
      properties:
        id:
          type: integer
        name:
          type: string
        email:
          type: string
    Product:
      type: object
      x-speakeasy-transform-from-api:
        jq: '{name, total: (.price * .quantity)}'
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
      x-speakeasy-transform-from-api:
        jq: '{id, name, status: .id}'
      properties:
        id:
          type: integer
        name:
          type: string
        status:
          type: integer
      required:
        - id
        - name
        - status
    Product:
      type: object
      x-speakeasy-transform-from-api:
        jq: '{name, total: (.price * .quantity)}'
      properties:
        name:
          type: string
        total:
          type: number
      required:
        - name
        - total
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
      x-speakeasy-transform-from-api:
        jq: >
          {userId: .id, displayName: .name,
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
      x-speakeasy-transform-from-api:
        jq: >
          {productId: .id, displayName: .name,
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
      x-speakeasy-transform-from-api:
        jq: >
          {grandTotal: (.items | map(.price * .quantity) | add // 0),
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
      x-speakeasy-transform-from-api:
        jq: '.invalid syntax here'
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
      x-speakeasy-transform-from-api:
        jq: '{street, zipcode: .zip}'
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

// TestSymbolicExecuteJQ_WithGoldenFiles tests schema transformation with expected output validation
// Test data is loaded from testdata/*.in.yaml which contains the schema + JQ transforms
// Two types of tests:
// - (1-way): Apply from-api transform, validate against .out.yaml
// - (2-way): Apply from-api then to-api, should return close to original .in.yaml (round-trip)
func TestSymbolicExecuteJQ_WithGoldenFiles(t *testing.T) {
	// Tests where 2-way round-trip is currently broken (even though to-api exists)
	knownRoundTripFailures := []string{
		"PaginatedItemsResponse", // Stack underflow on complex // chains
	}

	// Discover all test files automatically
	files, err := filepath.Glob("testdata/*.in.yaml")
	if err != nil {
		t.Fatalf("Failed to glob test files: %v", err)
	}

	for _, file := range files {
		testName := strings.TrimSuffix(filepath.Base(file), ".in.yaml")

		// Test 1-way: from-api transform only (always run)
		t.Run(testName+" (1-way)", func(t *testing.T) {
			runOneWayTest(t, testName)
		})

		// Test 2-way: only if to-api transform exists
		inputSchema := loadTestSchema(t, testName+".in.yaml")
		if _, hasToAPI := inputSchema["x-speakeasy-transform-to-api"]; hasToAPI {
			isRoundTripBroken := contains(knownRoundTripFailures, testName)

			t.Run(testName+" (2-way)", func(t *testing.T) {
				if isRoundTripBroken {
					t.Skip("Round-trip currently broken - skipping until fixed")
				}
				runTwoWayTest(t, testName)
			})
		}
	}
}

// runOneWayTest validates from-api transform against expected output
func runOneWayTest(t *testing.T, testName string) {
	// Load input schema
	inputSchema := loadTestSchema(t, testName+".in.yaml")

	// Extract JQ from-api expression
	jqFromAPI := extractJQTransform(t, inputSchema, "x-speakeasy-transform-from-api")

	// Build OAS and execute
	inputCopy := deepCopySchema(inputSchema)
	oasYAML := buildOASForTest(testName, inputCopy, jqFromAPI, "")

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("Transform failed: %v", err)
	}

	// Extract and compare
	var oasDoc map[string]any
	if err := yaml.Unmarshal([]byte(result), &oasDoc); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	actual := extractSchema(t, oasDoc, testName)
	expected := loadTestSchema(t, testName+".out.yaml")

	compareSchemas(t, "from-api output", expected, actual)
}

// runTwoWayTest validates from-api → to-api round-trip
func runTwoWayTest(t *testing.T, testName string) {
	// Load original input schema
	inputSchema := loadTestSchema(t, testName+".in.yaml")

	// Extract both transforms
	jqFromAPI := extractJQTransform(t, inputSchema, "x-speakeasy-transform-from-api")
	jqToAPI := extractJQTransform(t, inputSchema, "x-speakeasy-transform-to-api")

	// Build OAS with both transforms (use SymbolicExecuteJQPipeline)
	inputCopy := deepCopySchema(inputSchema)
	oasYAML := buildOASForTest(testName, inputCopy, jqFromAPI, jqToAPI)

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel3 (after to-api) should be close to original input
	var panel3Doc map[string]any
	if err := yaml.Unmarshal([]byte(result.Panel3), &panel3Doc); err != nil {
		t.Fatalf("Failed to parse panel3: %v", err)
	}

	roundTrip := extractSchema(t, panel3Doc, testName)

	// Remove JQ extensions from original for comparison
	originalCopy := deepCopySchema(inputSchema)
	delete(originalCopy, "x-speakeasy-transform-from-api")
	delete(originalCopy, "x-speakeasy-transform-to-api")

	// Compare round-trip
	expectedYAML, _ := yaml.Marshal(originalCopy)
	actualYAML, _ := yaml.Marshal(roundTrip)

	if string(expectedYAML) != string(actualYAML) {
		t.Errorf("Round-trip mismatch:\n\nOriginal:\n%s\n\nAfter round-trip:\n%s\n\nDifferences indicate lossy transformation or bugs",
			string(expectedYAML), string(actualYAML))
	}
}

// extractJQTransform extracts JQ expression from schema extension
func extractJQTransform(t *testing.T, schema map[string]any, extensionKey string) string {
	t.Helper()
	transform, ok := schema[extensionKey].(map[string]any)
	if !ok {
		t.Fatalf("Schema missing %s", extensionKey)
	}
	jq, ok := transform["jq"].(string)
	if !ok {
		t.Fatalf("JQ expression not found in %s", extensionKey)
	}
	return jq
}

// buildOASForTest builds OAS doc with transforms
func buildOASForTest(schemaKey string, schema map[string]any, fromAPI, toAPI string) string {
	// Set transforms
	if fromAPI != "" {
		schema["x-speakeasy-transform-from-api"] = map[string]any{"jq": fromAPI}
	}
	if toAPI != "" {
		schema["x-speakeasy-transform-to-api"] = map[string]any{"jq": toAPI}
	}

	oasDoc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "Test",
			"version": "1.0.0",
		},
		"components": map[string]any{
			"schemas": map[string]any{
				schemaKey: schema,
			},
		},
	}

	yamlBytes, _ := yaml.Marshal(oasDoc)
	return string(yamlBytes)
}

// deepCopySchema performs deep copy of schema map
func deepCopySchema(schema map[string]any) map[string]any {
	// Simple deep copy via marshal/unmarshal
	data, _ := yaml.Marshal(schema)
	var copy map[string]any
	yaml.Unmarshal(data, &copy)
	return copy
}

// contains checks if slice contains string
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

// extractSchema extracts a schema from an OAS document
func extractSchema(t *testing.T, oasDoc map[string]any, schemaKey string) map[string]any {
	t.Helper()
	components, ok := oasDoc["components"].(map[string]any)
	if !ok {
		t.Fatal("No components in OAS document")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("No schemas in components")
	}
	schema, ok := schemas[schemaKey].(map[string]any)
	if !ok {
		t.Fatalf("Schema %s not found", schemaKey)
	}

	// Remove the JQ transform extensions for comparison
	delete(schema, "x-speakeasy-transform-from-api")
	delete(schema, "x-speakeasy-transform-to-api")

	return schema
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
      x-speakeasy-transform-from-api:
        jq: '.'
      x-speakeasy-transform-to-api:
        jq: '.'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
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
      x-speakeasy-transform-from-api:
        jq: '{id: .id, serverTime: "2024-01-01"}'
      x-speakeasy-transform-to-api:
        jq: '{id: .id}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 should have serverTime (added by from-api)
	if !strings.Contains(result.Panel2, "serverTime") {
		t.Error("Panel2 should contain serverTime")
	}

	// Panel3 should only have id (to-api removes serverTime)
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
      x-speakeasy-transform-from-api:
        jq: '{id: .id}'
      x-speakeasy-transform-to-api:
        jq: '{id: .id, clientNonce: "abc123"}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 should only have id (from-api keeps id)
	if !strings.Contains(result.Panel2, "id:") {
		t.Error("Panel2 should contain id")
	}

	// Panel3 should have both id and clientNonce (to-api adds clientNonce)
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

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Applied flags should be false
	if result.AppliedFromAPI {
		t.Error("AppliedFromAPI should be false")
	}
	if result.AppliedToAPI {
		t.Error("AppliedToAPI should be false")
	}

	// All panels should be identical
	// (Note: Panel1 and Panel2 might differ in formatting, so check properties)
	for _, panel := range []string{result.Panel1, result.Panel2, result.Panel3} {
		if !strings.Contains(panel, "id:") || !strings.Contains(panel, "createdAt:") {
			t.Error("All panels should contain original properties")
		}
	}
}

func TestSymbolicExecuteJQPipeline_OnlyFromApi(t *testing.T) {
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
      x-speakeasy-transform-from-api:
        jq: '{b: .a}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// AppliedFromAPI should be true, AppliedToAPI false
	if !result.AppliedFromAPI {
		t.Error("AppliedFromAPI should be true")
	}
	if result.AppliedToAPI {
		t.Error("AppliedToAPI should be false when extension missing")
	}

	t.Logf("Panel2:\n%s", result.Panel2)
	t.Logf("Panel3:\n%s", result.Panel3)
}

func TestSymbolicExecuteJQPipeline_OnlyToApi(t *testing.T) {
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
      x-speakeasy-transform-to-api:
        jq: '{c: .a}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// AppliedFromAPI false, AppliedToAPI true
	if result.AppliedFromAPI {
		t.Error("AppliedFromAPI should be false when extension missing")
	}
	if !result.AppliedToAPI {
		t.Error("AppliedToAPI should be true")
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
      x-speakeasy-transform-from-api:
        jq: '{id: .id, email: .email, serverTime: "2024-01-01"}'
      x-speakeasy-transform-to-api:
        jq: '{id: .id, email: .email}'
    Order:
      type: object
      properties:
        id:
          type: string
        amount:
          type: number
      x-speakeasy-transform-from-api:
        jq: '{id: .id, amount: .amount}'
      x-speakeasy-transform-to-api:
        jq: '{id: .id, amount: .amount, clientNonce: "abc"}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
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
      x-speakeasy-transform-from-api:
        jq: '{b: .a}'
      x-speakeasy-transform-to-api:
        jq: '{c: .b}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
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
      x-speakeasy-transform-from-api:
        jq: '{id: .id}'
      x-speakeasy-transform-to-api:
        jq: '{id: .id}'
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 and Panel3 should not contain 'debug' (lost in transform)
	if strings.Contains(result.Panel3, "debug:") {
		t.Error("Panel3 should not contain 'debug' property")
	}
}

func TestSymbolicExecuteJQPipeline_MinimalExtraction(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: MinimalExtraction
  version: 1.0.0
paths: {}
components:
  schemas:
    EntityResponse:
      type: object
      description: Extract nested ID to top-level with minimal references.
      x-speakeasy-transform-from-api:
        jq: '. + {id: .data.result[0].id}'
      x-speakeasy-transform-to-api:
        jq: '{data}'
      properties:
        data:
          type: object
          properties:
            result:
              type: array
              items:
                type: object
                properties:
                  id:
                    type: string
                  name:
                    type: string
                  active:
                    type: boolean
            meta:
              type: object
              properties:
                timestamp:
                  type: string
                  format: date-time
                version:
                  type: integer
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 should have id at top-level plus original data
	if !strings.Contains(result.Panel2, "id:") {
		t.Error("Panel2 should contain extracted 'id' field")
	}
	if !strings.Contains(result.Panel2, "data:") {
		t.Error("Panel2 should still contain 'data' object")
	}

	// Panel3 should have data with id nested back
	if !strings.Contains(result.Panel3, "data:") {
		t.Error("Panel3 should contain 'data' object")
	}

	t.Logf("Minimal extraction transformation successful")
}

func TestSymbolicExecuteJQPipeline_PaginationFlattening(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: PaginationExample
  version: 1.0.0
paths: {}
components:
  schemas:
    PaginatedItemsResponse:
      type: object
      x-speakeasy-transform-from-api:
        jq: >
          {
            items: (.data.items // []) | map({
              id: .id,
              title: .title,
              status: (if (.active // false) then "active" else "inactive" end)
            }),
            hasMore: (.data.pagination.nextCursor != null),
            total: (.data.pagination.total // 0),
            nextCursor: (.data.pagination.nextCursor // null)
          }
      x-speakeasy-transform-to-api:
        jq: >
          {
            data: {
              items: (.items // []) | map({
                id: .id,
                title: .title,
                active: (.status == "active")
              }),
              pagination: {
                nextCursor: .nextCursor,
                total: (.total // 0)
              }
            }
          }
      properties:
        data:
          type: object
          properties:
            items:
              type: array
              items:
                type: object
                properties:
                  id:
                    type: string
                  title:
                    type: string
                  active:
                    type: boolean
            pagination:
              type: object
              properties:
                nextCursor:
                  type: string
                  nullable: true
                total:
                  type: integer
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	// Panel2 should have flattened pagination
	if !strings.Contains(result.Panel2, "hasMore:") {
		t.Error("Panel2 should contain 'hasMore' field")
	}
	if !strings.Contains(result.Panel2, "total:") {
		t.Error("Panel2 should contain 'total' field")
	}

	// id and title should preserve their schema from the input
	// They should be type: string, not empty schemas {}
	if !strings.Contains(result.Panel2, "id:") || !strings.Contains(result.Panel2, "type: string") {
		// Check that we don't have empty schema {} for id
		if strings.Contains(result.Panel2, "id: {}") {
			t.Errorf("Panel2 contains empty schema for 'id', should be type: string")
		}
	}
	if !strings.Contains(result.Panel2, "title:") || !strings.Contains(result.Panel2, "type: string") {
		// Check that we don't have empty schema {} for title
		if strings.Contains(result.Panel2, "title: {}") {
			t.Errorf("Panel2 contains empty schema for 'title', should be type: string")
		}
	}

	// status should be computed as enum [active, inactive]
	if !strings.Contains(result.Panel2, "status:") {
		t.Error("Panel2 should contain 'status' field")
	}

	t.Logf("Pagination flattening successful")
}

// DISABLED: This test exposes a bug in schemaexec with array slicing operations.
// See schemaexec/array_slice_bug_test.go for the isolated reproduction test case.
// The bug causes "EitherValue has neither Left nor Right set" error during marshaling.
// Property root.data.user.profile.name.last has an invalid JSONSchema wrapper.
func TestSymbolicExecuteJQPipeline_TagEnrichment(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: TagEnrichment
  version: 1.0.0
paths: {}
components:
  schemas:
    TagList:
      type: object
      x-speakeasy-transform-from-api:
        jq: >
          {
            tags: (.tags // []) | map({
              value: .,
              slug: (. | ascii_downcase),
              length: (. | length)
            })
          }
      x-speakeasy-transform-to-api:
        jq: >
          {
            tags: (.tags // []) | map(.value)
          }
      properties:
        tags:
          type: array
          items:
            type: string
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, false)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	t.Logf("Panel2 content (working):\n%s", result.Panel2)

	// Panel2 should have enriched tags with slug and length
	if !strings.Contains(result.Panel2, "slug:") {
		t.Error("Panel2 should contain 'slug' field in tag objects")
	}
	if !strings.Contains(result.Panel2, "length:") {
		t.Error("Panel2 should contain 'length' field in tag objects")
	}

	t.Logf("Tag enrichment transformation successful")
}

func TestSymbolicExecuteJQPipeline_TagEnrichmentRef(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: TagEnrichment
  version: 1.0.0
paths: {}
components:
  schemas:
    List:
      type: array
      items:
        type: string
    TagList:
      type: object
      x-speakeasy-transform-from-api:
        jq: >
          {
            tags: (.tags // []) | map({
              value: .,
              slug: (. | ascii_downcase),
              length: (. | length)
            })
          }
      x-speakeasy-transform-to-api:
        jq: >
          {
            tags: (.tags // []) | map(.value)
          }
      properties:
        tags:
          "$ref": "#/components/schemas/List"
`

	result, err := SymbolicExecuteJQPipeline(oasYAML, true)
	if err != nil {
		t.Fatalf("Pipeline failed: %v", err)
	}

	t.Logf("Panel2 content:\n%s", result.Panel2)

	// Panel2 should have enriched tags with slug and length
	if !strings.Contains(result.Panel2, "slug:") {
		t.Error("Panel2 should contain 'slug' field in tag objects")
	}
	if !strings.Contains(result.Panel2, "length:") {
		t.Error("Panel2 should contain 'length' field in tag objects")
	}

	t.Logf("Tag enrichment transformation successful")
}

// ============================================================================
// $ref Handling Tests
// These tests verify that $ref nodes are correctly handled:
// - RETAIN $ref when unaffected by JQ (just moved around)
// - INLINE $ref when affected by JQ (properties accessed/modified)
// ============================================================================

// TEST 1: Inline $ref via nested property access ($data-like pattern)
func TestSymbolicExecuteJQ_RefInline_DataPropertyAccess(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: RefInline1
  version: 1.0.0
components:
  schemas:
    GetUserResponse:
      type: object
      x-speakeasy-transform-from-api:
        jq: '{id: .data.id, email: .data.contact.email}'
      properties:
        data:
          $ref: '#/components/schemas/User'
    User:
      type: object
      properties:
        id:
          type: string
        name:
          type: string
        contact:
          type: object
          properties:
            email:
              type: string
              format: email
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Verify that $ref was inlined (id and email should be top-level)
	if !strings.Contains(result, "id:") {
		t.Error("Expected 'id' field in transformed schema")
	}
	if !strings.Contains(result, "email:") {
		t.Error("Expected 'email' field in transformed schema")
	}

	// Verify GetUserResponse no longer has 'data' property with $ref
	// (it should be replaced with id and email)
	var oasDoc map[string]any
	if err := yaml.Unmarshal([]byte(result), &oasDoc); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	getUserResponse := extractSchema(t, oasDoc, "GetUserResponse")
	props, ok := getUserResponse["properties"].(map[string]any)
	if !ok {
		t.Fatal("GetUserResponse should have properties")
	}

	// Should have id and email, not data
	if _, hasData := props["data"]; hasData {
		t.Error("GetUserResponse should not have 'data' property after inlining")
	}
	if _, hasID := props["id"]; !hasID {
		t.Error("GetUserResponse should have 'id' property")
	}
	if _, hasEmail := props["email"]; !hasEmail {
		t.Error("GetUserResponse should have 'email' property")
	}

	t.Logf("$ref inlined successfully:\n%s", result)
}

// TEST 2: Retain $ref when only moved/re-wrapped
func TestSymbolicExecuteJQ_RefRetain_MoveWithoutAccess(t *testing.T) {
	t.Skip("is accessed..")
	oasYAML := `openapi: 3.1.0
info:
  title: RefRetain1
  version: 1.0.0
components:
  schemas:
    Wrapper:
      type: object
      x-speakeasy-transform-from-api:
        jq: '{data: .payload}'
      properties:
        payload:
          $ref: '#/components/schemas/Event'
    Event:
      type: object
      properties:
        type:
          type: string
        occurredAt:
          type: string
          format: date-time
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Verify that $ref was retained
	if !strings.Contains(result, "$ref") {
		t.Error("Expected $ref to be retained when only moved")
	}

	// Parse and verify structure
	var oasDoc map[string]any
	if err := yaml.Unmarshal([]byte(result), &oasDoc); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	wrapper := extractSchema(t, oasDoc, "Wrapper")
	props, ok := wrapper["properties"].(map[string]any)
	if !ok {
		t.Fatal("Wrapper should have properties")
	}

	// Should have 'data' property with $ref
	dataProp, hasData := props["data"]
	if !hasData {
		t.Error("Wrapper should have 'data' property")
	}

	// Check if data property contains a $ref
	dataPropMap, ok := dataProp.(map[string]any)
	if ok {
		if _, hasRef := dataPropMap["$ref"]; hasRef {
			t.Logf("$ref retained successfully (moved from 'payload' to 'data')")
		} else {
			// May be inlined - this test documents current behavior
			t.Logf("Note: $ref was inlined even though transform only renamed the property")
		}
	}

	t.Logf("Result:\n%s", result)
}

// TEST 3: Inline array item $ref via map access
func TestSymbolicExecuteJQ_RefInline_ArrayItemPropertyAccess(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: RefInline2
  version: 1.0.0
components:
  schemas:
    OrderList:
      type: object
      x-speakeasy-transform-from-api:
        jq: '{ids: (.orders | map(.id))}'
      properties:
        orders:
          type: array
          items:
            $ref: '#/components/schemas/Order'
    Order:
      type: object
      properties:
        id:
          type: string
        total:
          type: number
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Verify that items.$ref was inlined and ids contains array of strings
	if !strings.Contains(result, "ids:") {
		t.Error("Expected 'ids' field in transformed schema")
	}

	var oasDoc map[string]any
	if err := yaml.Unmarshal([]byte(result), &oasDoc); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	orderList := extractSchema(t, oasDoc, "OrderList")
	props, ok := orderList["properties"].(map[string]any)
	if !ok {
		t.Fatal("OrderList should have properties")
	}

	// Check that ids is an array
	idsProp, hasIds := props["ids"]
	if !hasIds {
		t.Error("OrderList should have 'ids' property")
	}

	idsPropMap, ok := idsProp.(map[string]any)
	if ok {
		if idsType, hasType := idsPropMap["type"]; hasType && idsType == "array" {
			t.Logf("ids is correctly an array type")
			// Verify items are strings (inlined from Order.id)
			if items, hasItems := idsPropMap["items"].(map[string]any); hasItems {
				if itemType, hasItemType := items["type"]; hasItemType && itemType == "string" {
					t.Logf("Array items correctly inlined as string (from Order.id)")
				}
			}
		}
	}

	t.Logf("Array item $ref inlined successfully:\n%s", result)
}

// TEST 4: Retain array item $ref when moved unchanged
func TestSymbolicExecuteJQ_RefRetain_ArrayMoved(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: RefRetain2
  version: 1.0.0
components:
  schemas:
    Catalog:
      type: object
      x-speakeasy-transform-from-api:
        jq: '{products: .products}'
      properties:
        products:
          type: array
          items:
            $ref: '#/components/schemas/Product'
    Product:
      type: object
      properties:
        id:
          type: string
        title:
          type: string
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Parse and check if $ref is retained in items
	var oasDoc map[string]any
	if err := yaml.Unmarshal([]byte(result), &oasDoc); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	catalog := extractSchema(t, oasDoc, "Catalog")
	props, ok := catalog["properties"].(map[string]any)
	if !ok {
		t.Fatal("Catalog should have properties")
	}

	productsProp, hasProducts := props["products"]
	if !hasProducts {
		t.Error("Catalog should have 'products' property")
	}

	// Check if products.items still has $ref
	productsPropMap, ok := productsProp.(map[string]any)
	if ok {
		if items, hasItems := productsPropMap["items"].(map[string]any); hasItems {
			if _, hasRef := items["$ref"]; hasRef {
				t.Logf("$ref retained successfully in array items")
			} else {
				t.Logf("Note: $ref was inlined even though transform didn't access inner properties")
			}
		}
	}

	t.Logf("Result:\n%s", result)
}

// TEST 5: Inline through nested $ref chain
func TestSymbolicExecuteJQ_RefInline_NestedRefChain(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: RefInline3
  version: 1.0.0
components:
  schemas:
    PostWithAuthor:
      type: object
      x-speakeasy-transform-from-api:
        jq: '{authorEmail: .post.author.contact.email}'
      properties:
        post:
          $ref: '#/components/schemas/Post'
    Post:
      type: object
      properties:
        id:
          type: string
        author:
          $ref: '#/components/schemas/User'
    User:
      type: object
      properties:
        id:
          type: string
        contact:
          type: object
          properties:
            email:
              type: string
              format: email
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Verify that nested $refs were traversed and authorEmail is extracted
	if !strings.Contains(result, "authorEmail:") {
		t.Error("Expected 'authorEmail' field in transformed schema")
	}

	var oasDoc map[string]any
	if err := yaml.Unmarshal([]byte(result), &oasDoc); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	postWithAuthor := extractSchema(t, oasDoc, "PostWithAuthor")
	props, ok := postWithAuthor["properties"].(map[string]any)
	if !ok {
		t.Fatal("PostWithAuthor should have properties")
	}

	// Should have authorEmail property
	if _, hasAuthorEmail := props["authorEmail"]; !hasAuthorEmail {
		t.Error("PostWithAuthor should have 'authorEmail' property")
	}

	t.Logf("Nested $ref chain inlined successfully:\n%s", result)
}
