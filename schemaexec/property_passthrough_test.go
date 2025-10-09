package schemaexec

import (
	"context"
	"strings"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// TestPropertyPassthrough tests that properties accessed via .prop notation
// preserve their schema from the input object.
func TestPropertyPassthrough(t *testing.T) {
	tests := []struct {
		name       string
		jqExpr     string
		inputYAML  string
		wantType   string
		wantEnum   []string
		expectFail bool
	}{
		{
			name:   "simple property access",
			jqExpr: ".id",
			inputYAML: `type: object
properties:
  id:
    type: string
  name:
    type: string`,
			wantType: "string",
		},
		{
			name:   "property in object construction",
			jqExpr: "{id: .id, name: .name}",
			inputYAML: `type: object
properties:
  id:
    type: string
  name:
    type: string
  age:
    type: integer`,
			wantType: "object",
		},
		{
			name:   "property through array map",
			jqExpr: "map({id: .id, title: .title})",
			inputYAML: `type: array
items:
  type: object
  properties:
    id:
      type: string
    title:
      type: string
    active:
      type: boolean`,
			wantType: "array",
		},
		{
			name:   "nested property access",
			jqExpr: ".data.items | map({id: .id, title: .title})",
			inputYAML: `type: object
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
              type: boolean`,
			wantType: "array",
		},
		{
			name:   "property with computed field",
			jqExpr: "map({id: .id, status: (if .active then \"active\" else \"inactive\" end)})",
			inputYAML: `type: array
items:
  type: object
  properties:
    id:
      type: string
    active:
      type: boolean`,
			wantType: "array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Parse input schema
			var inputSchema oas3.Schema
			if err := yaml.Unmarshal([]byte(tt.inputYAML), &inputSchema); err != nil {
				t.Fatalf("Failed to parse input schema: %v", err)
			}

			// Execute JQ symbolically
			query, err := gojq.Parse(tt.jqExpr)
			if err != nil {
				t.Fatalf("Failed to parse JQ: %v", err)
			}

			result, err := RunSchema(context.Background(), query, &inputSchema)
			if err != nil {
				if tt.expectFail {
					t.Logf("Expected failure: %v", err)
					return
				}
				t.Fatalf("ExecuteJQ failed: %v", err)
			}

			if tt.expectFail {
				t.Fatal("Expected failure but got success")
			}

			// Check result type
			resultType := getType(result.Schema)
			if resultType != tt.wantType {
				t.Errorf("Result type = %q, want %q", resultType, tt.wantType)
			}

			// Serialize result to inspect
			resultYAML, err := yaml.Marshal(result.Schema)
			if err != nil {
				t.Fatalf("Failed to marshal result: %v", err)
			}
			t.Logf("Result schema:\n%s", string(resultYAML))

			// Specific checks based on test case
			switch tt.name {
			case "simple property access":
				if resultType != "string" {
					t.Errorf("Expected string type for .id access, got %q", resultType)
				}

			case "property in object construction":
				// Check that id and name properties exist and have correct types
				if result.Schema.Properties == nil {
					t.Fatal("Result schema has no properties")
				}

				idProp, ok := result.Schema.Properties.Get("id")
				if !ok || idProp.Left == nil {
					t.Error("Missing 'id' property in result")
				} else if getType(idProp.Left) != "string" {
					t.Errorf("Property 'id' has type %q, want 'string'", getType(idProp.Left))
				}

				nameProp, ok := result.Schema.Properties.Get("name")
				if !ok || nameProp.Left == nil {
					t.Error("Missing 'name' property in result")
				} else if getType(nameProp.Left) != "string" {
					t.Errorf("Property 'name' has type %q, want 'string'", getType(nameProp.Left))
				}

			case "property through array map", "nested property access":
				// Check array items have correct property types
				if result.Schema.Items == nil || result.Schema.Items.Left == nil {
					t.Fatal("Result array has no items schema")
				}

				itemSchema := result.Schema.Items.Left
				if itemSchema.Properties == nil {
					t.Fatal("Array item schema has no properties")
				}

				idProp, ok := itemSchema.Properties.Get("id")
				if !ok || idProp.Left == nil {
					t.Error("Missing 'id' property in array item")
				} else if typ := getType(idProp.Left); typ != "string" && typ != "" {
					// BUG: Currently returns empty schema {}
					t.Errorf("BUG DETECTED: Property 'id' has type %q (empty schema), want 'string'", typ)
					t.Logf("id property schema: %+v", idProp.Left)
				}

				titleProp, ok := itemSchema.Properties.Get("title")
				if !ok || titleProp.Left == nil {
					t.Error("Missing 'title' property in array item")
				} else if typ := getType(titleProp.Left); typ != "string" && typ != "" {
					// BUG: Currently returns empty schema {}
					t.Errorf("BUG DETECTED: Property 'title' has type %q (empty schema), want 'string'", typ)
					t.Logf("title property schema: %+v", titleProp.Left)
				}

			case "property with computed field":
				// Check that id is string and status is enum
				if result.Schema.Items == nil || result.Schema.Items.Left == nil {
					t.Fatal("Result array has no items schema")
				}

				itemSchema := result.Schema.Items.Left
				if itemSchema.Properties == nil {
					t.Fatal("Array item schema has no properties")
				}

				idProp, ok := itemSchema.Properties.Get("id")
				if !ok || idProp.Left == nil {
					t.Error("Missing 'id' property")
				} else if typ := getType(idProp.Left); typ != "string" && typ != "" {
					t.Errorf("BUG DETECTED: Property 'id' has type %q (empty schema), want 'string'", typ)
				}

				statusProp, ok := itemSchema.Properties.Get("status")
				if !ok || statusProp.Left == nil {
					t.Error("Missing 'status' property")
				} else {
					statusType := getType(statusProp.Left)
					if statusType != "string" {
						t.Errorf("Property 'status' has type %q, want 'string'", statusType)
					}
					// Check enum values
					if statusProp.Left.Enum == nil || len(statusProp.Left.Enum) != 2 {
						t.Errorf("Property 'status' should have enum with 2 values, got %d", len(statusProp.Left.Enum))
					}
				}
			}

			// Print warnings if any
			if len(result.Warnings) > 0 {
				t.Logf("Warnings: %v", result.Warnings)
			}
		})
	}
}

// TestPropertyPassthroughInMap specifically tests the bug where properties
// lose their type information when passed through map operations.
func TestPropertyPassthroughInMap(t *testing.T) {
	jqExpr := `(.items // []) | map({id: .id, title: .title, status: (if .active then "active" else "inactive" end)})`

	// Build input schema programmatically
	itemSchema := BuildObject(map[string]*oas3.Schema{
		"id":     StringType(),
		"title":  StringType(),
		"active": BoolType(),
	}, []string{"id", "title", "active"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"items": ArrayType(itemSchema),
	}, []string{"items"})

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse JQ: %v", err)
	}

	result, err := RunSchema(context.Background(), query, inputSchema)
	if err != nil {
		t.Fatalf("ExecuteJQ failed: %v", err)
	}

	// Marshal to YAML for inspection
	resultYAML, marshalErr := yaml.Marshal(result.Schema)
	if marshalErr != nil {
		t.Fatalf("Failed to marshal result: %v", marshalErr)
	}
	t.Logf("Result schema:\n%s", string(resultYAML))

	// Check that result is array
	if getType(result.Schema) != "array" {
		t.Fatalf("Result type = %q, want 'array'", getType(result.Schema))
	}

	// Check array items
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Result array has no items schema")
	}

	resultItemSchema := result.Schema.Items.Left
	if resultItemSchema.Properties == nil {
		t.Fatal("Array item schema has no properties")
	}

	// Check id property
	idProp, ok := resultItemSchema.Properties.Get("id")
	if !ok {
		t.Fatal("Missing 'id' property in result")
	}
	if idProp.Left == nil {
		t.Fatal("Property 'id' has nil schema")
	}

	idType := getType(idProp.Left)
	if idType == "" {
		t.Error("BUG CONFIRMED: Property 'id' has empty type (empty schema {})")
		t.Logf("id property: %+v", idProp.Left)

		// Check if it's actually an empty schema (Top)
		idYAML, _ := yaml.Marshal(idProp.Left)
		if strings.TrimSpace(string(idYAML)) == "{}" {
			t.Error("Property 'id' is an empty schema {} - should be {type: string}")
		}
	} else if idType != "string" {
		t.Errorf("Property 'id' has type %q, want 'string'", idType)
	}

	// Check title property
	titleProp, ok := resultItemSchema.Properties.Get("title")
	if !ok {
		t.Fatal("Missing 'title' property in result")
	}
	if titleProp.Left == nil {
		t.Fatal("Property 'title' has nil schema")
	}

	titleType := getType(titleProp.Left)
	if titleType == "" {
		t.Error("BUG CONFIRMED: Property 'title' has empty type (empty schema {})")
		t.Logf("title property: %+v", titleProp.Left)
	} else if titleType != "string" {
		t.Errorf("Property 'title' has type %q, want 'string'", titleType)
	}

	// Check status property (should work correctly)
	statusProp, ok := resultItemSchema.Properties.Get("status")
	if !ok {
		t.Fatal("Missing 'status' property in result")
	}
	if statusProp.Left == nil {
		t.Fatal("Property 'status' has nil schema")
	}

	statusType := getType(statusProp.Left)
	if statusType != "string" {
		t.Errorf("Property 'status' has type %q, want 'string'", statusType)
	}

	// Check status has enum
	if statusProp.Left.Enum == nil || len(statusProp.Left.Enum) == 0 {
		t.Error("Property 'status' should have enum values")
	}

	if len(result.Warnings) > 0 {
		t.Logf("Warnings: %v", result.Warnings)
	}
}
