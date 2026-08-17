package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
	"gopkg.in/yaml.v3"
)

// untypedObject builds an object-shaped schema WITHOUT an explicit type,
// as commonly authored in real-world OpenAPI documents.
func untypedObject(props map[string]*oas3.Schema, required []string) *oas3.Schema {
	s := BuildObject(props, required)
	s.Type = nil
	return s
}

// untypedArray builds an array-shaped schema (items set) without a type.
func untypedArray(items *oas3.Schema) *oas3.Schema {
	s := ArrayType(items)
	s.Type = nil
	return s
}

func yamlScalar(value, tag string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: value, Tag: tag}
}

// TestImpliedTypeOf_Rules exercises the structural inference table directly.
// The rules must exactly mirror the structural inference Speakeasy's
// generators apply to untyped schemas.
func TestImpliedTypeOf_Rules(t *testing.T) {
	apSchema := oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	tests := []struct {
		name   string
		schema *oas3.Schema
		want   string
	}{
		{"enum implies string", &oas3.Schema{Enum: []*yaml.Node{yamlScalar("a", "!!str")}}, "string"},
		{"integer-valued enum still implies string (generator rule)", &oas3.Schema{Enum: []*yaml.Node{yamlScalar("1", "!!int")}}, "string"},
		{"const bool implies boolean", &oas3.Schema{Const: yamlScalar("true", "!!bool")}, "boolean"},
		{"const float implies number", &oas3.Schema{Const: yamlScalar("1.5", "!!float")}, "number"},
		{"const string implies string", &oas3.Schema{Const: yamlScalar("x", "!!str")}, "string"},
		{"const int implies integer", &oas3.Schema{Const: yamlScalar("42", "!!int")}, "integer"},
		{"const null implies nothing", &oas3.Schema{Const: yamlScalar("null", "!!null")}, ""},
		{"properties imply object", untypedObject(map[string]*oas3.Schema{"id": StringType()}, nil), "object"},
		{"additionalProperties implies object", &oas3.Schema{AdditionalProperties: apSchema}, "object"},
		{"items imply array", untypedArray(StringType()), "array"},
		{"empty properties map does not imply object", &oas3.Schema{Properties: sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()}, ""},
		{"bare schema implies nothing", &oas3.Schema{}, ""},
		{"nil implies nothing", nil, ""},
		{"enum wins over properties (generator precedence)", func() *oas3.Schema {
			s := untypedObject(map[string]*oas3.Schema{"id": StringType()}, nil)
			s.Enum = []*yaml.Node{yamlScalar("a", "!!str")}
			return s
		}(), "string"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := impliedTypeOf(tt.schema); got != tt.want {
				t.Errorf("impliedTypeOf() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestGetType_UsesImpliedTypes verifies getType consults structural inference
// when no explicit type is present, and stays out of the way of combinators
// and explicit types.
func TestGetType_UsesImpliedTypes(t *testing.T) {
	if got := getType(untypedObject(map[string]*oas3.Schema{"id": StringType()}, nil)); got != "object" {
		t.Errorf("untyped-with-properties: getType = %q, want object", got)
	}
	if got := getType(untypedArray(StringType())); got != "array" {
		t.Errorf("untyped-with-items: getType = %q, want array", got)
	}
	// Explicit type wins over structure.
	typed := ArrayType(StringType())
	if got := getType(typed); got != "array" {
		t.Errorf("typed array: getType = %q, want array", got)
	}
	// allOf/oneOf present: no implication (their collapse paths own the shape).
	withAllOf := untypedObject(map[string]*oas3.Schema{"id": StringType()}, nil)
	withAllOf.AllOf = []*oas3.JSONSchema[oas3.Referenceable]{oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())}
	if got := getType(withAllOf); got != "" {
		t.Errorf("untyped-with-allOf: getType = %q, want \"\"", got)
	}
}

func TestGetType_CombinatorsAndConditionalsSuppressImpliedTypes(t *testing.T) {
	for _, keyword := range []string{"not", "if", "then", "else"} {
		t.Run(keyword, func(t *testing.T) {
			in := untypedObject(map[string]*oas3.Schema{"x": StringType()}, nil)
			wrapped := oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())
			switch keyword {
			case "not":
				in.Not = wrapped
			case "if":
				in.If = wrapped
			case "then":
				in.Then = wrapped
			case "else":
				in.Else = wrapped
			}
			if got := getType(in); got != "" {
				t.Fatalf("getType = %q, want unknown", got)
			}
		})
	}
}

// TestMightBeType_ConservativeForUntyped verifies the MightBeX helpers stay
// CONSERVATIVE for untyped schemas: they gate builtins, and a false negative
// would prune real outputs. (Structural inference narrows navigation dispatch
// via getType, not these guards.)
func TestMightBeType_ConservativeForUntyped(t *testing.T) {
	obj := untypedObject(map[string]*oas3.Schema{"id": StringType()}, nil)
	if !MightBeObject(obj) {
		t.Error("untyped-with-properties should might-be object")
	}
	if !MightBeString(obj) {
		t.Error("untyped-with-properties must remain might-be string (guards must not prune)")
	}
	arr := untypedArray(StringType())
	if !MightBeArray(arr) {
		t.Error("untyped-with-items should might-be array")
	}
	if !MightBeObject(arr) {
		t.Error("untyped-with-items must remain might-be object (guards must not prune)")
	}
	// Explicitly typed schemas still gate exactly.
	if MightBeString(ArrayType(StringType())) {
		t.Error("typed array should not might-be string")
	}
	// Bare schema: could be anything.
	if !MightBeString(&oas3.Schema{}) || !MightBeObject(&oas3.Schema{}) {
		t.Error("bare schema should might-be anything")
	}
}

// execExpr runs a jq expression against a schema and returns the output schema.
func execExpr(t *testing.T, expr string, input *oas3.Schema) *oas3.Schema {
	t.Helper()
	q, err := gojq.Parse(expr)
	if err != nil {
		t.Fatalf("parse %q: %v", expr, err)
	}
	res, err := RunSchema(context.Background(), q, input)
	if err != nil {
		t.Fatalf("RunSchema(%q): %v", expr, err)
	}
	return res.Schema
}

// TestImpliedTypes_PropertyAccess: `.id` on an untyped-with-properties schema
// must reach the property schema instead of widening to Top.
func TestImpliedTypes_PropertyAccess(t *testing.T) {
	in := untypedObject(map[string]*oas3.Schema{
		"id":    StringType(),
		"count": IntegerType(),
	}, []string{"id", "count"})

	if got := getType(execExpr(t, ".id", in)); got != "string" {
		t.Errorf(".id: got %q, want string", got)
	}
	if got := getType(execExpr(t, ".count", in)); got != "integer" {
		t.Errorf(".count: got %q, want integer", got)
	}
}

// TestImpliedTypes_NestedUntypedObjects: property access through two levels of
// untyped objects.
func TestImpliedTypes_NestedUntypedObjects(t *testing.T) {
	inner := untypedObject(map[string]*oas3.Schema{"total": IntegerType()}, []string{"total"})
	in := untypedObject(map[string]*oas3.Schema{"usage": inner}, []string{"usage"})

	if got := getType(execExpr(t, ".usage.total", in)); got != "integer" {
		t.Errorf(".usage.total: got %q, want integer", got)
	}
}

// TestImpliedTypes_Iteration: `.[]` over an untyped-with-items schema must
// produce the item schema (not Bottom, which the default dispatch produced).
func TestImpliedTypes_Iteration(t *testing.T) {
	in := untypedArray(StringType())
	if got := getType(execExpr(t, ".[]", in)); got != "string" {
		t.Errorf(".[]: got %q, want string", got)
	}
}

// TestImpliedTypes_IterationOverUntypedObject: `.[]` over an untyped object
// unions the property values.
func TestImpliedTypes_IterationOverUntypedObject(t *testing.T) {
	in := untypedObject(map[string]*oas3.Schema{"a": StringType()}, []string{"a"})
	if got := getType(execExpr(t, ".[]", in)); got != "string" {
		t.Errorf(".[] over untyped object: got %q, want string", got)
	}
}

// TestImpliedTypes_Indexing: `.[0]` on an untyped-with-items schema must
// produce the item schema.
func TestImpliedTypes_Indexing(t *testing.T) {
	in := untypedArray(IntegerType())
	out := execExpr(t, ".[0]", in)
	if out == nil {
		t.Fatal(".[0]: got Bottom")
	}
	if !MightBeNumber(out) && getType(out) != "integer" {
		t.Errorf(".[0]: got %s, want integer-compatible", schemaTypeSummary(out, 2))
	}
}

// TestImpliedTypes_UntypedArrayProperty: an untyped object holding an untyped
// array whose items are untyped objects — the real-world worst case.
func TestImpliedTypes_UntypedArrayProperty(t *testing.T) {
	item := untypedObject(map[string]*oas3.Schema{"name": StringType()}, []string{"name"})
	in := untypedObject(map[string]*oas3.Schema{
		"entries": untypedArray(item),
	}, []string{"entries"})

	if got := getType(execExpr(t, ".entries[].name", in)); got != "string" {
		t.Errorf(".entries[].name: got %q, want string", got)
	}
}

// TestImpliedTypes_UntypedEnumProperty: untyped enum accessed as a value is
// treated as string (generator rule).
func TestImpliedTypes_UntypedEnumProperty(t *testing.T) {
	enum := &oas3.Schema{Enum: []*yaml.Node{yamlScalar("active", "!!str"), yamlScalar("done", "!!str")}}
	in := untypedObject(map[string]*oas3.Schema{"status": enum}, []string{"status"})

	out := execExpr(t, ".status", in)
	if got := getType(out); got != "string" {
		t.Errorf(".status: got %q, want string", got)
	}
	// The enum must survive: length is a cheap probe that it is string-shaped.
	if got := getType(execExpr(t, ".status | ascii_downcase", in)); got != "string" {
		t.Errorf(".status | ascii_downcase: got %q, want string", got)
	}
}

// TestImpliedTypes_ConstProperty: untyped const values infer their scalar type.
func TestImpliedTypes_ConstProperty(t *testing.T) {
	in := untypedObject(map[string]*oas3.Schema{
		"version": {Const: yamlScalar("2", "!!int")},
		"flag":    {Const: yamlScalar("true", "!!bool")},
	}, []string{"version", "flag"})

	if got := getType(execExpr(t, ".version", in)); got != "integer" {
		t.Errorf(".version: got %q, want integer", got)
	}
	if got := getType(execExpr(t, ".flag", in)); got != "boolean" {
		t.Errorf(".flag: got %q, want boolean", got)
	}
}

// TestImpliedTypes_AdditionalPropertiesObject: untyped schema with only
// additionalProperties is an object; `.anything` yields the AP schema ∪ null.
func TestImpliedTypes_AdditionalPropertiesObject(t *testing.T) {
	in := &oas3.Schema{
		AdditionalProperties: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](IntegerType()),
	}
	out := execExpr(t, ".anything", in)
	if out == nil {
		t.Fatal(".anything: got Bottom")
	}
	if isTopSchema(out) {
		t.Fatal(".anything: got Top; expected integer ∪ null via additionalProperties")
	}
	if !mightBeType(out, oas3.SchemaTypeInteger) && !mightBeType(out, oas3.SchemaTypeNull) {
		t.Errorf(".anything: got %s, want integer∪null", schemaTypeSummary(out, 2))
	}
}

// TestImpliedTypes_BareSchemaStillWidens: a schema with no structure must keep
// the existing conservative behavior (property access widens to Top).
func TestImpliedTypes_BareSchemaStillWidens(t *testing.T) {
	out := execExpr(t, ".x", &oas3.Schema{})
	if !isTopSchema(out) {
		t.Errorf(".x on bare schema: got %s, want Top", schemaTypeSummary(out, 2))
	}
}
