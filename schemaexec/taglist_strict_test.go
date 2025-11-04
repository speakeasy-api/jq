package schemaexec

import (
	"context"
	"encoding/json"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestTagList_StrictMode_ActualFailure reproduces the actual failure from the web playground
// The issue is that we're trying to access .value on string items in the array
func TestTagList_StrictMode_ActualFailure(t *testing.T) {
	// The input schema has tags as array of strings (via $ref to Example)
	// Example is just a string type
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{})

	// The transform tries to map over the array and access properties on each string
	// This is the problem: you can't access .value on a string!
	transformQuery := `{
		tags: (.tags // []) | map({
			value: .,
			slug: (. | ascii_downcase | gsub("[^a-z0-9]+"; "-") | gsub("(^-|-$)"; "")),
			length: (. | length)
		}),
		primarySlug: (
			(.tags[0] // null)
			| if . == null then null
				else (. | ascii_downcase | gsub("[^a-z0-9]+"; "-") | gsub("(^-|-$)"; ""))
				end
		)
	}`

	query, err := gojq.Parse(transformQuery)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Test with strict mode enabled - this should show the issue
	opts := DefaultOptions()
	opts.StrictMode = true
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Logf("RunSchema failed with strict mode (expected): %v", err)
		// This is expected - we're accessing properties on strings
	} else {
		t.Logf("RunSchema succeeded unexpectedly")
	}

	if result != nil && result.Schema != nil {
		// Check if there are any Top schemas in the result
		tagsProp := GetProperty(result.Schema, "tags", opts)
		if tagsProp != nil && tagsProp.Items != nil {
			if schema, ok := derefJSONSchema(tagsProp.Items); ok {
				t.Logf("Tags items schema type: %v", getType(schema))

				// Check for properties on the items
				if schema.Properties != nil {
					for k, v := range schema.Properties.All() {
						if propSchema, ok := derefJSONSchema(v); ok {
							propType := getType(propSchema)
							t.Logf("  Property %s: type=%v", k, propType)

							// Check if this is Top
							if isTop(propSchema) {
								t.Errorf("Found Top schema at $.properties.tags.items.properties.%s", k)
								t.Logf("This indicates we're accessing a property on a non-object type")
							}
						}
					}
				}
			}
		}
	}

	// Print warnings
	if result != nil && len(result.Warnings) > 0 {
		t.Logf("Warnings (%d):", len(result.Warnings))
		for i, w := range result.Warnings {
			t.Logf("  %d: %s", i+1, w)
		}
	}
}

// TestTagList_TraceExecution traces the execution to see where Top is introduced
func TestTagList_TraceExecution(t *testing.T) {
	// Simple case: map over array of strings and try to access properties
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{})

	// Simplified transform that just does map({value: .})
	transformQuery := `.tags | map({value: .})`

	query, err := gojq.Parse(transformQuery)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	opts := DefaultOptions()
	opts.StrictMode = true
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)

	t.Logf("=== Trace Execution ===")
	t.Logf("Input: .tags = array of strings")
	t.Logf("Transform: .tags | map({value: .})")
	t.Logf("Expected: array of objects with value property")
	t.Logf("")

	if err != nil {
		t.Logf("Error: %v", err)
	}

	if result != nil {
		if result.Schema != nil {
			t.Logf("Result schema type: %v", getType(result.Schema))

			if result.Schema.Items != nil {
				if itemSchema, ok := derefJSONSchema(result.Schema.Items); ok {
					t.Logf("Items schema type: %v", getType(itemSchema))

					// Check properties
					if itemSchema.Properties != nil {
						for k, v := range itemSchema.Properties.All() {
							if propSchema, ok := derefJSONSchema(v); ok {
								propType := getType(propSchema)
								t.Logf("  Property '%s': type=%v, isTop=%v", k, propType, isTop(propSchema))

								// Try to print the schema as JSON for debugging
								if bytes, err := json.MarshalIndent(propSchema, "    ", "  "); err == nil {
									t.Logf("    Schema JSON: %s", string(bytes))
								}
							}
						}
					}
				}
			}
		}

		if len(result.Warnings) > 0 {
			t.Logf("\nWarnings:")
			for _, w := range result.Warnings {
				t.Logf("  - %s", w)
			}
		}
	}
}

// isTop checks if a schema is the Top type (any/unconstrained)
func isTop(s *oas3.Schema) bool {
	if s == nil {
		return false
	}
	// Top is typically represented as no type constraint and no properties
	return s.Type == nil && s.Properties == nil && s.AnyOf == nil && s.AllOf == nil && s.OneOf == nil
}
