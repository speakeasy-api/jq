package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestNullableUnion(t *testing.T) {
	ctx := context.Background()
	opts := SchemaExecOptions{
		EnableWarnings: false,
		MaxDepth:       100,
		AnyOfLimit:     20,
	}

	t.Run("StringOrNull", func(t *testing.T) {
		// Test: .field // null  (produces string | null)
		query, _ := gojq.Parse(`.field // null`)
		input := BuildObject(map[string]*oas3.Schema{
			"field": StringType(),
		}, []string{"field"})

		result, err := RunSchema(ctx, query, input, opts)
		if err != nil {
			t.Fatal(err)
		}

		// Should be {type: string, nullable: true}, not anyOf
		if getType(result.Schema) != "string" {
			t.Errorf("Expected type string, got %s", getType(result.Schema))
		}
		
		if result.Schema.Nullable == nil || !*result.Schema.Nullable {
			t.Error("Expected nullable: true")
		}
		
		// Should NOT have anyOf
		if result.Schema.AnyOf != nil && len(result.Schema.AnyOf) > 0 {
			t.Error("Should not have anyOf, should use nullable instead")
		}
	})

	t.Run("NumberOrNull", func(t *testing.T) {
		query, _ := gojq.Parse(`.count // null`)
		input := BuildObject(map[string]*oas3.Schema{
			"count": NumberType(),
		}, []string{"count"})

		result, err := RunSchema(ctx, query, input, opts)
		if err != nil {
			t.Fatal(err)
		}

		if getType(result.Schema) != "number" {
			t.Errorf("Expected type number, got %s", getType(result.Schema))
		}
		
		if result.Schema.Nullable == nil || !*result.Schema.Nullable {
			t.Error("Expected nullable: true")
		}
	})

	t.Run("OptionalProperty", func(t *testing.T) {
		// Test: if .optional then .optional else null end
		query, _ := gojq.Parse(`if .optional then .optional else null end`)
		input := BuildObject(map[string]*oas3.Schema{
			"optional": StringType(),
		}, []string{}) // Not required

		result, err := RunSchema(ctx, query, input, opts)
		if err != nil {
			t.Fatal(err)
		}

		// Should produce nullable string
		if result.Schema.Nullable == nil || !*result.Schema.Nullable {
			t.Error("Expected nullable: true for optional property")
		}
	})
}
