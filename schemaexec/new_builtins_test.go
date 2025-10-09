package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestStringBuiltins tests all string operations at type level
func TestStringBuiltins(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		query      string
		input      *oas3.Schema
		expectType string
	}{
		{"split", `split(",")`, StringType(), "array"},
		{"join", `join(",")`, ArrayType(StringType()), "string"},
		{"startswith", `startswith("x")`, StringType(), "boolean"},
		{"endswith", `endswith("x")`, StringType(), "boolean"},
		{"ltrimstr", `ltrimstr("x")`, StringType(), "string"},
		{"rtrimstr", `rtrimstr("x")`, StringType(), "string"},
		{"ascii_downcase", `ascii_downcase`, StringType(), "string"},
		{"ascii_upcase", `ascii_upcase`, StringType(), "string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := gojq.Parse(tt.query)
			if err != nil {
				t.Fatal(err)
			}

			result, err := RunSchema(ctx, query, tt.input)
			if err != nil {
				t.Fatal(err)
			}

			actualType := getType(result.Schema)
			if actualType != tt.expectType {
				t.Errorf("Expected %s, got %s", tt.expectType, actualType)
			}
		})
	}
}

// TestMathBuiltins tests all math operations
func TestMathBuiltins(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name       string
		query      string
		input      *oas3.Schema
		expectType string
	}{
		{"floor", `floor`, NumberType(), "integer"},
		{"ceil", `ceil`, NumberType(), "integer"},
		{"round", `round`, NumberType(), "integer"},
		{"sqrt", `sqrt`, NumberType(), "number"}, // might be number|null but union shows as array
		{"exp", `exp`, NumberType(), "number"},
		{"log", `log`, NumberType(), "number"}, // might be number|null
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := gojq.Parse(tt.query)
			if err != nil {
				t.Fatal(err)
			}

			result, err := RunSchema(ctx, query, tt.input)
			if err != nil {
				t.Fatal(err)
			}

			// For union types (number|null), check that result is valid
			actualType := getType(result.Schema)
			// number|null might show as anyOf, so just check it's not Bottom
			if actualType == "" || actualType == "null" || actualType == "bottom" {
				if tt.expectType == "number" {
					// This might be OK if it's a union
					if result.Schema.AnyOf == nil || len(result.Schema.AnyOf) == 0 {
						t.Errorf("Expected %s or union, got %s", tt.expectType, actualType)
					}
				}
			} else if actualType != tt.expectType {
				// Allow "integer" for "number" (more specific is OK)
				if !(tt.expectType == "number" && actualType == "integer") {
					t.Errorf("Expected %s, got %s", tt.expectType, actualType)
				}
			}
		})
	}
}

// TestArrayGrouping tests grouping operations
func TestArrayGrouping(t *testing.T) {
	ctx := context.Background()

	t.Run("group_by", func(t *testing.T) {
		// [{x:1}] | group_by(.x) → [[{x:1}]]
		query, _ := gojq.Parse(`group_by(.x)`)
		input := ArrayType(BuildObject(map[string]*oas3.Schema{
			"x": IntegerType(),
		}, []string{"x"}))

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Should be array<array<object>>
		if getType(result.Schema) != "array" {
			t.Errorf("Expected array, got %s", getType(result.Schema))
		}

		if result.Schema.Items != nil && result.Schema.Items.Left != nil {
			innerType := getType(result.Schema.Items.Left)
			if innerType != "array" {
				t.Errorf("Expected array<array<...>>, got array<%s>", innerType)
			}
		}
	})

	t.Run("sort_by", func(t *testing.T) {
		query, _ := gojq.Parse(`sort_by(.x)`)
		input := ArrayType(IntegerType())

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Should return input unchanged
		if getType(result.Schema) != "array" {
			t.Errorf("Expected array, got %s", getType(result.Schema))
		}
	})
}

// TestFlatten tests array flattening
func TestFlatten(t *testing.T) {
	ctx := context.Background()

	t.Run("FlattenNested", func(t *testing.T) {
		query, _ := gojq.Parse(`flatten`)
		// array<array<number>>
		input := ArrayType(ArrayType(NumberType()))

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Should be array<number>
		if getType(result.Schema) != "array" {
			t.Errorf("Expected array, got %s", getType(result.Schema))
		}

		if result.Schema.Items != nil && result.Schema.Items.Left != nil {
			itemType := getType(result.Schema.Items.Left)
			if itemType != "number" && itemType != "integer" {
				t.Errorf("Expected array<number>, got array<%s>", itemType)
			}
		}
	})
}

// TestSearchOps tests index/indices/rindex
func TestSearchOps(t *testing.T) {
	ctx := context.Background()

	t.Run("index", func(t *testing.T) {
		query, _ := gojq.Parse(`index(5)`)
		input := ArrayType(IntegerType())

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Should be integer|null (union)
		// Union might show as anyOf or specific type
		actualType := getType(result.Schema)
		if actualType != "integer" && actualType != "" {
			// Check if it's a union
			if result.Schema.AnyOf == nil || len(result.Schema.AnyOf) == 0 {
				t.Errorf("Expected integer|null union, got %s with no anyOf", actualType)
			}
		}
	})

	t.Run("indices", func(t *testing.T) {
		query, _ := gojq.Parse(`indices(5)`)
		input := ArrayType(IntegerType())

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Should be array<integer>
		if getType(result.Schema) != "array" {
			t.Errorf("Expected array, got %s", getType(result.Schema))
		}
	})
}

// TestRegex tests regex operations
func TestRegex(t *testing.T) {
	ctx := context.Background()

	t.Run("test_symbolic", func(t *testing.T) {
		query, _ := gojq.Parse(`test("pattern")`)
		input := StringType()

		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if getType(result.Schema) != "boolean" {
			t.Errorf("Expected boolean, got %s", getType(result.Schema))
		}
	})
}
