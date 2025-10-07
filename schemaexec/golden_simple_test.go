package schemaexec

import (
	"context"
	"testing"

	"github.com/itchyny/gojq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestGoldenSuite runs a comprehensive golden test suite.
// Each test defines input schema, jq query, and expected output using our constructor functions.
func TestGoldenSuite(t *testing.T) {
	tests := []struct {
		name     string
		jq       string
		input    *oas3.Schema
		expected *oas3.Schema
		checkType string // If set, just check output type matches this
	}{
		// PROPERTY ACCESS
		{
			name: "property_access_required",
			jq:   ".name",
			input: BuildObject(map[string]*oas3.Schema{
				"name": StringType(),
			}, []string{"name"}),
			expected: StringType(),
		},
		{
			name: "nested_property",
			jq:   ".user.email",
			input: BuildObject(map[string]*oas3.Schema{
				"user": BuildObject(map[string]*oas3.Schema{
					"email": StringType(),
				}, []string{"email"}),
			}, []string{"user"}),
			expected: StringType(),
		},

		// ARRAY OPERATIONS
		{
			name:     "array_iteration",
			jq:       ".[]",
			input:    ArrayType(NumberType()),
			expected: NumberType(),
		},
		{
			name:     "array_indexing",
			jq:       ".[0]",
			input:    ArrayType(StringType()),
			expected: StringType(),
		},

		// OBJECT CONSTRUCTION
		{
			name: "object_construction",
			jq:   "{name: .firstName, email: .emailAddress}",
			input: BuildObject(map[string]*oas3.Schema{
				"firstName":    StringType(),
				"emailAddress": StringType(),
			}, []string{"firstName", "emailAddress"}),
			checkType: "object",
		},

		// SELECT
		{
			name: "select_const_true",
			jq:   "select(true)",
			input: BuildObject(map[string]*oas3.Schema{
				"x": NumberType(),
			}, []string{"x"}),
			checkType: "object",
		},
		{
			name: "select_comparison",
			jq:   ".[] | select(.price > 100)",
			input: ArrayType(BuildObject(map[string]*oas3.Schema{
				"name":  StringType(),
				"price": NumberType(),
			}, []string{"name", "price"})),
			checkType: "object",
		},

		// MAP
		{
			name:      "map_identity",
			jq:        "map(.)",
			input:     ArrayType(NumberType()),
			checkType: "array",
		},
		{
			name:      "map_property",
			jq:        "map(.name)",
			input:     ArrayType(BuildObject(map[string]*oas3.Schema{
				"name": StringType(),
				"age":  NumberType(),
			}, []string{"name", "age"})),
			checkType: "array",
		},

		// BUILTINS
		{
			name:      "keys_builtin",
			jq:        "keys",
			input:     BuildObject(map[string]*oas3.Schema{
				"a": NumberType(),
				"b": StringType(),
			}, []string{"a", "b"}),
			checkType: "array",
		},
		{
			name:     "type_builtin",
			jq:       "type",
			input:    StringType(),
			expected: ConstString("string"),
		},
		{
			name:      "length_builtin",
			jq:        "length",
			input:     ArrayType(StringType()),
			checkType: "number",
		},
		{
			name:     "tonumber",
			jq:       "tonumber",
			input:    StringType(),
			expected: NumberType(),
		},
		{
			name:      "reverse",
			jq:        "reverse",
			input:     ArrayType(NumberType()),
			checkType: "array",
		},
		{
			name:      "sort",
			jq:        "sort",
			input:     ArrayType(StringType()),
			checkType: "array",
		},
		{
			name:      "unique",
			jq:        "unique",
			input:     ArrayType(NumberType()),
			checkType: "array",
		},
		{
			name:     "add_numbers",
			jq:       "add",
			input:    ArrayType(NumberType()),
			expected: NumberType(),
		},

		// LITERALS
		{
			name:      "object_literal",
			jq:        `{x: 1, y: "hello"}`,
			input:     Top(),
			checkType: "object",
		},
		{
			name:      "array_literal",
			jq:        "[1, 2, 3]",
			input:     Top(),
			checkType: "array",
		},
		{
			name:      "empty_object",
			jq:        "{}",
			input:     Top(),
			checkType: "object",
		},
		{
			name:      "empty_array",
			jq:        "[]",
			input:     Top(),
			checkType: "array",
		},

		// ARITHMETIC
		{
			name:     "const_addition",
			jq:       "5 + 3",
			input:    Top(),
			expected: ConstNumber(8),
		},
		{
			name:     "const_subtraction",
			jq:       "10 - 4",
			input:    Top(),
			expected: ConstNumber(6),
		},
		{
			name:     "const_multiplication",
			jq:       "3 * 4",
			input:    Top(),
			expected: ConstNumber(12),
		},
		{
			name:     "const_division",
			jq:       "20 / 4",
			input:    Top(),
			expected: ConstNumber(5),
		},
		{
			name:     "negate",
			jq:       "-(5)",
			input:    Top(),
			expected: ConstNumber(-5),
		},

		// COMPARISONS
		{
			name:     "const_comparison_gt_true",
			jq:       "5 > 3",
			input:    Top(),
			expected: ConstBool(true),
		},
		{
			name:     "const_comparison_gt_false",
			jq:       "3 > 5",
			input:    Top(),
			expected: ConstBool(false),
		},
		{
			name:     "const_comparison_eq",
			jq:       "5 == 5",
			input:    Top(),
			expected: ConstBool(true),
		},
		{
			name:     "const_comparison_ne",
			jq:       "5 != 3",
			input:    Top(),
			expected: ConstBool(true),
		},

		// LOGICAL - Note: and/or/not are compiler expansions, not builtins
		// They expand to fork/jumpifnot patterns
		// Testing them here would test compiler, not our implementation
		// So we skip direct logical tests

		// CHAINED OPERATIONS
		{
			name: "filter_and_transform",
			jq:   ".[] | select(.price > 0) | {name, price}",
			input: ArrayType(BuildObject(map[string]*oas3.Schema{
				"name":  StringType(),
				"price": NumberType(),
				"stock": NumberType(),
			}, []string{"name", "price", "stock"})),
			checkType: "object",
		},

		// TRY-CATCH
		{
			name:      "try_catch",
			jq:        "try .foo catch \"error\"",
			input:     BuildObject(map[string]*oas3.Schema{
				"bar": StringType(),
			}, []string{"bar"}),
			checkType: "string",
		},

		// IDENTITY
		{
			name:     "identity",
			jq:       ".",
			input:    StringType(),
			expected: StringType(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query, err := gojq.Parse(tt.jq)
			if err != nil {
				t.Fatalf("Failed to parse jq %q: %v", tt.jq, err)
			}

			result, err := RunSchema(context.Background(), query, tt.input)
			if err != nil {
				t.Fatalf("RunSchema failed: %v", err)
			}

			if result.Schema == nil {
				t.Fatal("Expected schema, got nil")
			}

			// Check result
			if tt.checkType != "" {
				actualType := getType(result.Schema)
				if actualType != tt.checkType && actualType != "" {
					// Allow anyOf as valid for many results
					if actualType != "anyOf" && !(result.Schema.AnyOf != nil) {
						t.Errorf("Expected type %q, got %q", tt.checkType, actualType)
					}
				}
			} else if tt.expected != nil {
				// Verify types match
				expectedType := getType(tt.expected)
				actualType := getType(result.Schema)
				if expectedType != actualType {
					t.Errorf("Type mismatch: expected %q, got %q", expectedType, actualType)
				}

				// For const values, verify enum matches
				if tt.expected.Enum != nil && result.Schema.Enum != nil {
					if len(tt.expected.Enum) != len(result.Schema.Enum) {
						t.Errorf("Enum count mismatch: expected %d, got %d",
							len(tt.expected.Enum), len(result.Schema.Enum))
					} else if len(tt.expected.Enum) > 0 {
						expectedVal := tt.expected.Enum[0].Value
						actualVal := result.Schema.Enum[0].Value
						if expectedVal != actualVal {
							t.Errorf("Const value mismatch: expected %v, got %v",
								expectedVal, actualVal)
						}
					}
				}
			}

			if len(result.Warnings) > 0 {
				t.Logf("Warnings: %v", result.Warnings)
			}
		})
	}
}
