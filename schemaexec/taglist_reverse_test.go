package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestTagList_ReverseTransform tests the x-speakeasy-transform-to-api direction
// This is the transform that takes the enriched TagList and converts it back to API format
func TestTagList_ReverseTransform(t *testing.T) {
	// The INPUT for the reverse transform is the enriched TagList
	// tags is an array of objects with {value, slug, length}
	enrichedTagSchema := BuildObject(map[string]*oas3.Schema{
		"value":  StringType(),
		"slug":   StringType(),
		"length": IntegerType(),
	}, []string{})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags":        ArrayType(enrichedTagSchema),
		"primarySlug": StringType(),
	}, []string{})

	// The reverse transform: map over tags and extract .value
	// This should produce an array of strings
	reverseTransformQuery := `{
		tags: (.tags // []) | map(.value)
	}`

	query, err := gojq.Parse(reverseTransformQuery)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Test with strict mode
	opts := DefaultOptions()
	opts.StrictMode = true
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("RunSchema failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected non-nil result schema")
	}

	// The output should have tags as array of strings
	tagsProp := GetProperty(result.Schema, "tags", opts)
	if tagsProp == nil {
		t.Fatal("Expected 'tags' property in result")
	}

	if getType(tagsProp) != "array" {
		t.Errorf("Expected tags to be array, got %v", getType(tagsProp))
	}

	// Check the items type
	if tagsProp.Items != nil {
		if itemSchema, ok := derefJSONSchema(newCollapseContext(), tagsProp.Items); ok {
			itemType := getType(itemSchema)
			t.Logf("Tags items type: %v", itemType)

			if itemType != "string" {
				t.Errorf("Expected tags items to be strings, got %v", itemType)
			}

			// Check if there are any properties on the items (there shouldn't be for strings)
			if itemSchema.Properties != nil {
				hasProps := false
				for range itemSchema.Properties.All() {
					hasProps = true
					break
				}
				if hasProps {
					t.Error("String items should not have properties")
				for k, v := range itemSchema.Properties.All() {
					if propSchema, ok := derefJSONSchema(newCollapseContext(), v); ok {
						propType := getType(propSchema)
						t.Logf("  Unexpected property %s: type=%v, isTop=%v", k, propType, isTop(propSchema))

						if isTop(propSchema) {
							t.Errorf("Found Top schema at $.properties.tags.items.properties.%s", k)
						}
					}
				}
				}
			}
		}
	}

	if len(result.Warnings) > 0 {
		t.Logf("Warnings (%d):", len(result.Warnings))
		for i, w := range result.Warnings {
			t.Logf("  %d: %s", i+1, w)
		}
	}
}

// TestTagList_BothDirections tests both from-api and to-api transforms
func TestTagList_BothDirections(t *testing.T) {
	// Start with API schema: tags is array of strings
	apiSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{})

	t.Log("=== Step 1: from-api transform (enrich) ===")

	// from-api: enrich the tags
	fromAPIQuery := `{
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

	fromQuery, err := gojq.Parse(fromAPIQuery)
	if err != nil {
		t.Fatalf("Failed to parse from-api query: %v", err)
	}

	opts := DefaultOptions()
	opts.StrictMode = true

	fromResult, err := RunSchema(context.Background(), fromQuery, apiSchema, opts)
	if err != nil {
		t.Fatalf("from-api transform failed: %v", err)
	}

	// Verify enriched schema
	enrichedTags := GetProperty(fromResult.Schema, "tags", opts)
	if enrichedTags == nil {
		t.Fatal("Expected 'tags' in enriched schema")
	}

	t.Logf("Enriched tags type: %v", getType(enrichedTags))
	if enrichedTags.Items != nil {
		if itemSchema, ok := derefJSONSchema(newCollapseContext(), enrichedTags.Items); ok {
			t.Logf("Enriched tags items type: %v", getType(itemSchema))
			if itemSchema.Properties != nil {
				propCount := 0
				for range itemSchema.Properties.All() {
					propCount++
				}
				t.Logf("Enriched tags items has %d properties", propCount)
			}
		}
	}

	t.Log("\n=== Step 2: to-api transform (extract value) ===")

	// to-api: extract value from enriched tags
	toAPIQuery := `{
		tags: (.tags // []) | map(.value)
	}`

	toQuery, err := gojq.Parse(toAPIQuery)
	if err != nil {
		t.Fatalf("Failed to parse to-api query: %v", err)
	}

	// Use the enriched schema as input for the reverse transform
	toResult, err := RunSchema(context.Background(), toQuery, fromResult.Schema, opts)
	if err != nil {
		t.Fatalf("to-api transform failed: %v", err)
	}

	// The output should be back to array of strings
	apiTags := GetProperty(toResult.Schema, "tags", opts)
	if apiTags == nil {
		t.Fatal("Expected 'tags' in API schema")
	}

	t.Logf("API tags type: %v", getType(apiTags))
	if apiTags.Items != nil {
		if itemSchema, ok := derefJSONSchema(newCollapseContext(), apiTags.Items); ok {
			itemType := getType(itemSchema)
			t.Logf("API tags items type: %v", itemType)

			if itemType != "string" {
				t.Errorf("Expected API tags items to be strings, got %v", itemType)
			}

			// THIS IS THE KEY CHECK: items should NOT have properties
			hasProps := false
			if itemSchema.Properties != nil {
				for range itemSchema.Properties.All() {
					hasProps = true
					break
				}
			}
			if hasProps {
				t.Error("ERROR: String items should not have properties!")
				for k, v := range itemSchema.Properties.All() {
					if propSchema, ok := derefJSONSchema(newCollapseContext(), v); ok {
						propType := getType(propSchema)
						t.Errorf("  Found unexpected property '%s': type=%v, isTop=%v", k, propType, isTop(propSchema))

						if isTop(propSchema) {
							t.Errorf("CRITICAL: Found Top at $.properties.tags.items.properties.%s", k)
							t.Error("This is the error from the web playground!")
						}
					}
				}
			} else {
				t.Log("✓ API tags items correctly have no properties (simple strings)")
			}
		}
	}
}
