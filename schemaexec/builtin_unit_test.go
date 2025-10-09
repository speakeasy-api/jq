package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestBuiltinSplitDirect tests split builtin directly
func TestBuiltinSplitDirect(t *testing.T) {
	env := &schemaEnv{opts: DefaultOptions()}

	t.Run("ConstFolding", func(t *testing.T) {
		input := ConstString("a,b,c")
		args := []*oas3.Schema{ConstString(",")}

		result, err := builtinSplit(input, args, env)
		if err != nil {
			t.Fatal(err)
		}

		if len(result) != 1 {
			t.Fatalf("Expected 1 result, got %d", len(result))
		}

		schema := result[0]
		if getType(schema) != "array" {
			t.Errorf("Expected array, got %s", getType(schema))
		}

		// Check prefixItems
		if schema.PrefixItems == nil {
			t.Error("Expected prefixItems")
		} else {
			t.Logf("Got %d prefixItems", len(schema.PrefixItems))
			for i, item := range schema.PrefixItems {
				if item.Left != nil {
					if s, ok := extractConstString(item.Left); ok {
						t.Logf("Item %d: %s", i, s)
					}
				}
			}
		}
	})

	t.Run("Symbolic", func(t *testing.T) {
		input := StringType()
		args := []*oas3.Schema{ConstString(",")}

		result, err := builtinSplit(input, args, env)
		if err != nil {
			t.Fatal(err)
		}

		schema := result[0]
		if getType(schema) != "array" {
			t.Errorf("Expected array, got %s", getType(schema))
		}

		if schema.Items == nil || schema.Items.Left == nil {
			t.Error("Expected Items to be set")
		} else if getType(schema.Items.Left) != "string" {
			t.Errorf("Expected Items to be string, got %s", getType(schema.Items.Left))
		}
	})
}
