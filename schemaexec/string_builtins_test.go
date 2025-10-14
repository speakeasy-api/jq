package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestSplit tests string split operation
func TestSplit(t *testing.T) {
	ctx := context.Background()

	t.Run("ConstFolding", func(t *testing.T) {
		query, _ := gojq.Parse(`split(",")`)
		input := ConstString("a,b,c")
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Should return array of strings
		if getType(result.Schema) != "array" {
			t.Errorf("Expected array, got %s", getType(result.Schema))
		}

		// Check if it's a const array (prefixItems) or symbolic array (items)
		// For const folding, we expect prefixItems with 3 elements
		if result.Schema.PrefixItems != nil {
			if len(result.Schema.PrefixItems) != 3 {
				t.Errorf("Expected 3 prefix items, got %d", len(result.Schema.PrefixItems))
			}
		} else if result.Schema.Items != nil && result.Schema.Items.Left != nil {
			// Symbolic case - items should be string
			if getType(result.Schema.Items.Left) != "string" {
				t.Errorf("Expected items to be string, got %s", getType(result.Schema.Items.Left))
			}
		} else {
			t.Error("Expected either prefixItems or items to be set")
		}
	})

	t.Run("Symbolic", func(t *testing.T) {
		query, _ := gojq.Parse(`split(",")`)
		input := StringType()
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if getType(result.Schema) != "array" {
			t.Errorf("Expected array, got %s", getType(result.Schema))
		}

		if result.Schema.Items == nil || result.Schema.Items.Left == nil {
			t.Error("Expected Items to be set")
		} else if getType(result.Schema.Items.Left) != "string" {
			t.Errorf("Expected array<string>, got array<%s>", getType(result.Schema.Items.Left))
		}
	})
}

// TestJoin tests array join operation
func TestJoin(t *testing.T) {
	ctx := context.Background()

	t.Run("ConstFolding", func(t *testing.T) {
		query, _ := gojq.Parse(`join(",")`)
		// Build array with const strings
		input := buildArrayOfConstStrings([]string{"a", "b", "c"})
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		// Should return const string "a,b,c"
		if s, ok := extractConstString(result.Schema); ok {
			if s != "a,b,c" {
				t.Errorf("Expected 'a,b,c', got '%s'", s)
			}
		} else {
			t.Error("Expected const string result")
		}
	})

	t.Run("Symbolic", func(t *testing.T) {
		query, _ := gojq.Parse(`join(",")`)
		input := ArrayType(StringType())
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if getType(result.Schema) != "string" {
			t.Errorf("Expected string, got %s", getType(result.Schema))
		}
	})
}

// TestStartswith tests string prefix check
func TestStartswith(t *testing.T) {
	ctx := context.Background()

	t.Run("ConstTrue", func(t *testing.T) {
		query, _ := gojq.Parse(`startswith("hel")`)
		input := ConstString("hello")
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if val, ok := extractConstValue(result.Schema); ok {
			if b, ok := val.(bool); ok {
				if !b {
					t.Error("Expected true")
				}
			} else {
				t.Error("Expected boolean value")
			}
		} else {
			t.Error("Expected const boolean")
		}
	})

	t.Run("ConstFalse", func(t *testing.T) {
		query, _ := gojq.Parse(`startswith("xyz")`)
		input := ConstString("hello")
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if val, ok := extractConstValue(result.Schema); ok {
			if b, ok := val.(bool); ok {
				if b {
					t.Error("Expected false")
				}
			}
		}
	})

	t.Run("Symbolic", func(t *testing.T) {
		query, _ := gojq.Parse(`startswith("x")`)
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

// TestAsciiCase tests case conversion
func TestAsciiCase(t *testing.T) {
	ctx := context.Background()

	t.Run("Downcase", func(t *testing.T) {
		query, _ := gojq.Parse(`ascii_downcase`)
		input := ConstString("HeLLo")
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if s, ok := extractConstString(result.Schema); ok {
			if s != "hello" {
				t.Errorf("Expected 'hello', got '%s'", s)
			}
		} else {
			t.Error("Expected const string")
		}
	})

	t.Run("Upcase", func(t *testing.T) {
		query, _ := gojq.Parse(`ascii_upcase`)
		input := ConstString("hello")
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if s, ok := extractConstString(result.Schema); ok {
			if s != "HELLO" {
				t.Errorf("Expected 'HELLO', got '%s'", s)
			}
		}
	})
}

// TestLtrimRtrim tests prefix/suffix removal
func TestLtrimRtrim(t *testing.T) {
	ctx := context.Background()

	t.Run("Ltrimstr", func(t *testing.T) {
		query, _ := gojq.Parse(`ltrimstr("pre")`)
		input := ConstString("prefix")
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if s, ok := extractConstString(result.Schema); ok {
			if s != "fix" {
				t.Errorf("Expected 'fix', got '%s'", s)
			}
		}
	})

	t.Run("Rtrimstr", func(t *testing.T) {
		query, _ := gojq.Parse(`rtrimstr("fix")`)
		input := ConstString("suffix")
		result, err := RunSchema(ctx, query, input)
		if err != nil {
			t.Fatal(err)
		}

		if s, ok := extractConstString(result.Schema); ok {
			if s != "suf" {
				t.Errorf("Expected 'suf', got '%s'", s)
			}
		}
	})
}

// TestSplitSliceJoin reproduces the bug where a pipeline involving join results in Top.
func TestSplitSliceJoin(t *testing.T) {
	ctx := context.Background()

	t.Run("split | slice | join pipeline", func(t *testing.T) {
		query, _ := gojq.Parse(`.fullName | split(" ") | .[1:] | join(" ")`)
		input := BuildObject(map[string]*oas3.Schema{
			"fullName": StringType(),
		}, []string{"fullName"})

		// Enable warnings to see the diagnostic output from builtinJoin
		opts := SchemaExecOptions{
			EnableWarnings: true,
			EnumLimit:      10,
			AnyOfLimit:     10,
			WideningLevel:  1,
			MaxDepth:       100,
		}

		result, err := RunSchema(ctx, query, input, opts)
		if err != nil {
			t.Fatal(err)
		}

		// Print warnings to help debug
		if len(result.Warnings) > 0 {
			t.Logf("Warnings (%d):", len(result.Warnings))
			for _, w := range result.Warnings {
				t.Logf("  - %s", w)
			}
		}

		// The final result should be a string, not Top (`{}`)
		if isUnconstrainedSchema(result.Schema) {
			t.Errorf("Result is an unconstrained schema (Top), expected string. Got: %+v", result.Schema)
		}

		if getType(result.Schema) != "string" {
			t.Errorf("Expected schema type string, got %q", getType(result.Schema))
		}
	})
}
