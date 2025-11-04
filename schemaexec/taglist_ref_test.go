package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestTagList_RefHandling tests the TagList schema with $ref to Thing
// This reproduces the error: "failed to marshal panel2: EitherValue has neither Left nor Right set"
func TestTagList_RefHandling(t *testing.T) {
	// Define the Thing schema (array of strings)
	thingSchema := ArrayType(StringType())

	// Input schema: represents the API input with simple tags array
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": thingSchema, // $ref to Thing (array of strings)
	}, []string{})

	// The jq transform from x-speakeasy-transform-from-api
	// This should enrich the tags array by adding slug and length fields
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

	// Test both with and without strict mode
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("strict=%v", strict), func(t *testing.T) {
			opts := DefaultOptions()
			opts.StrictMode = strict

			result, err := RunSchema(context.Background(), query, inputSchema, opts)
			if err != nil {
				t.Fatalf("RunSchema failed: %v", err)
			}

			verifyTagListResult(t, result, opts)
		})
	}
}

func verifyTagListResult(t *testing.T, result *SchemaExecResult, opts SchemaExecOptions) {
	if result.Schema == nil {
		t.Fatal("Expected non-nil result schema")
	}

	// Check tags property exists
	tagsProp := GetProperty(result.Schema, "tags", opts)
	if tagsProp == nil {
		t.Fatal("Expected 'tags' property in result")
	}

	// Verify tags is an array
	if getType(tagsProp) != "array" {
		t.Errorf("Expected tags to be array, got %v", getType(tagsProp))
	}

	// Verify array items have the enriched structure
	if tagsProp.Items != nil {
		if tagsProp.Items.Left == nil {
			t.Error("Expected tags.items.Left to be set, but it's nil - this may cause the EitherValue error")
			t.Logf("tagsProp.Items: %+v", tagsProp.Items)
		} else {
			itemSchema := tagsProp.Items.Left
			if getType(itemSchema) != "object" {
				t.Errorf("Expected tags items to be objects, got %v", getType(itemSchema))
			}

			// Verify enriched properties exist
			valueProp := GetProperty(itemSchema, "value", opts)
			slugProp := GetProperty(itemSchema, "slug", opts)
			lengthProp := GetProperty(itemSchema, "length", opts)

			if valueProp == nil {
				t.Error("Expected 'value' property in tag item")
			}
			if slugProp == nil {
				t.Error("Expected 'slug' property in tag item")
			}
			if lengthProp == nil {
				t.Error("Expected 'length' property in tag item")
			}

			t.Logf("Successfully created enriched tag schema with properties: value, slug, length")
		}
	} else {
		t.Error("Expected tags.Items to be set")
	}

	// Check primarySlug property
	primarySlugProp := GetProperty(result.Schema, "primarySlug", opts)
	if primarySlugProp == nil {
		t.Fatal("Expected 'primarySlug' property in result")
	}

	// primarySlug should be string or null (anyOf)
	propType := getType(primarySlugProp)
	t.Logf("primarySlug type: %v", propType)

	t.Logf("Test completed - checking if schema can be marshaled")
}

// TestTagList_MissingProperty tests the case where tags property is missing
// This reproduces the original error by forcing the (.tags // []) path
func TestTagList_MissingProperty(t *testing.T) {
	// Input schema WITHOUT tags property - forces the [] branch
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"other": StringType(),
	}, []string{})

	// Transform with (.tags // []) - will produce empty array
	transformQuery := `{
		tags: (.tags // []) | map({
			value: .,
			length: (. | length)
		})
	}`

	query, err := gojq.Parse(transformQuery)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Test both with and without strict mode
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("strict=%v", strict), func(t *testing.T) {
			opts := DefaultOptions()
			opts.StrictMode = strict

			result, err := RunSchema(context.Background(), query, inputSchema, opts)
			if err != nil {
				t.Fatalf("RunSchema failed: %v", err)
			}

			if result.Schema == nil {
				t.Fatal("Expected non-nil result schema")
			}

			// Verify the result has tags property with empty array
			tagsProp := GetProperty(result.Schema, "tags", opts)
			if tagsProp == nil {
				t.Fatal("Expected 'tags' property in result")
			}

			if getType(tagsProp) != "array" {
				t.Errorf("Expected tags to be array, got %v", getType(tagsProp))
			}

			// Check MaxItems=0 for empty array
			if tagsProp.MaxItems == nil || *tagsProp.MaxItems != 0 {
				t.Logf("Warning: Expected MaxItems=0 for empty array, got %v", tagsProp.MaxItems)
			}

			// Verify Items is properly handled (should be nil or have valid Left)
			if tagsProp.Items != nil {
				if tagsProp.Items.Left == nil && tagsProp.Items.GetResolvedSchema() == nil {
					t.Error("ERROR: tags.items has neither Left nor GetResolvedSchema - this causes EitherValue marshaling error")
				}
			}

			t.Logf("Test passed - empty array correctly represented without invalid EitherValue wrapper")
		})
	}
}

// TestTagList_SimplifiedRef tests a simpler version to isolate the issue
func TestTagList_SimplifiedRef(t *testing.T) {
	// Simple input: array of strings
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"tags": ArrayType(StringType()),
	}, []string{})

	// Transform: map over array and create objects
	transformQuery := `{
		tags: (.tags // []) | map({
			value: .,
			length: (. | length)
		})
	}`

	query, err := gojq.Parse(transformQuery)
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Test both with and without strict mode
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprintf("strict=%v", strict), func(t *testing.T) {
			opts := DefaultOptions()
			opts.StrictMode = strict

			result, err := RunSchema(context.Background(), query, inputSchema, opts)
			if err != nil {
				t.Fatalf("RunSchema failed: %v", err)
			}

			verifySimplifiedTagListResult(t, result, opts)
		})
	}
}

func verifySimplifiedTagListResult(t *testing.T, result *SchemaExecResult, opts SchemaExecOptions) {
	if result.Schema == nil {
		t.Fatal("Expected non-nil result schema")
	}

	// Verify the result
	tagsProp := GetProperty(result.Schema, "tags", opts)
	if tagsProp == nil {
		t.Fatal("Expected 'tags' property in result")
	}

	if tagsProp.Items != nil {
		if tagsProp.Items.Left == nil {
			t.Error("ERROR: tags.items.Left is nil - this will cause EitherValue marshaling error")
			t.Logf("tagsProp.Items structure: %+v", tagsProp.Items)

			// Check if GetResolvedSchema returns anything
			if resolved := tagsProp.Items.GetResolvedSchema(); resolved != nil {
				t.Logf("GetResolvedSchema() returned: %+v", resolved)
				if schema := resolved.GetLeft(); schema != nil {
					t.Logf("GetResolvedSchema().GetLeft() returned a schema")
				} else {
					t.Logf("GetResolvedSchema().GetLeft() returned nil")
				}
			} else {
				t.Logf("GetResolvedSchema() returned nil")
			}
		} else {
			t.Logf("SUCCESS: tags.items.Left is properly set")
		}
	} else {
		t.Error("tags.Items is nil")
	}
}
