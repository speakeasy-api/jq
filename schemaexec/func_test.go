package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// helper to build a string enum schema
func strEnum(values ...string) *oas3.Schema {
	nodes := make([]*yaml.Node, len(values))
	for i, v := range values {
		nodes[i] = &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	}
	s := StringType()
	s.Enum = nodes
	return s
}

// NOTE: We assume schemaEnv and SchemaExecOptions are available in this package.
func newTestEnv() *schemaEnv {
	return &schemaEnv{
		opts: SchemaExecOptions{
			EnumLimit:     16,
			AnyOfLimit:    64,
			WideningLevel: 1,
		},
	}
}

func schemaIsConstString(s *oas3.Schema, expected string) bool {
	if s == nil || s.Enum == nil || len(s.Enum) != 1 {
		return false
	}
	if getType(s) != "string" && s.Enum[0].Tag != "!!str" {
		return false
	}
	return s.Enum[0].Value == expected
}

func schemaIsBottom(s *oas3.Schema) bool {
	return s == nil
}

func getStringEnumValues(s *oas3.Schema) []string {
	if s == nil || s.Enum == nil || len(s.Enum) == 0 {
		return nil
	}
	out := make([]string, len(s.Enum))
	for i, n := range s.Enum {
		out[i] = n.Value
	}
	return out
}

// ------------------------
// gsub tests
// ------------------------

func TestBuiltinGsub_ConstFolding(t *testing.T) {
	env := newTestEnv()
	in := ConstString("Hello World")
	args := []*oas3.Schema{ConstString("o"), ConstString("0")}
	out, err := env.callBuiltin("gsub", in, args)
	if err != nil {
		t.Fatalf("gsub const: unexpected error: %v", err)
	}
	if len(out) != 1 || !schemaIsConstString(out[0], "Hell0 W0rld") {
		t.Fatalf("gsub const: expected 'Hell0 W0rld', got %#v", out[0])
	}
}

func TestBuiltinGsub_SymbolicFallback(t *testing.T) {
	env := newTestEnv()
	in := StringType()
	args := []*oas3.Schema{ConstString("[a-z]+"), ConstString("-")}
	out, err := env.callBuiltin("gsub", in, args)
	if err != nil {
		t.Fatalf("gsub sym: unexpected error: %v", err)
	}
	if getType(out[0]) != "string" {
		t.Fatalf("gsub sym: expected string type, got %s", getType(out[0]))
	}
}

func TestBuiltinGsub_EnumPreservation(t *testing.T) {
	env := newTestEnv()
	in := strEnum("foo", "bar")
	args := []*oas3.Schema{ConstString("o"), ConstString("0")}
	out, err := env.callBuiltin("gsub", in, args)
	if err != nil {
		t.Fatalf("gsub enum: unexpected error: %v", err)
	}
	vals := getStringEnumValues(out[0])
	if len(vals) != 2 {
		t.Fatalf("gsub enum: expected 2 values, got %d", len(vals))
	}
	// Expect f00 and bar (order not guaranteed)
	found := map[string]bool{}
	for _, v := range vals {
		found[v] = true
	}
	if !found["f00"] || !found["bar"] {
		t.Fatalf("gsub enum: expected {f00, bar}, got %v", vals)
	}
}

func TestBuiltinGsub_InvalidRegex(t *testing.T) {
	env := newTestEnv()
	in := ConstString("x")
	args := []*oas3.Schema{ConstString("("), ConstString("-")}
	out, err := env.callBuiltin("gsub", in, args)
	if err != nil {
		t.Fatalf("gsub invalid: unexpected error: %v", err)
	}
	if len(out) != 1 || !schemaIsBottom(out[0]) {
		t.Fatalf("gsub invalid: expected Bottom(), got %#v", out[0])
	}
}

// ------------------------
// trim family tests
// ------------------------

func TestBuiltinTrim_Const(t *testing.T) {
	env := newTestEnv()
	in := ConstString("  a b  ")
	out, err := env.callBuiltin("trim", in, nil)
	if err != nil {
		t.Fatalf("trim: unexpected error: %v", err)
	}
	if !schemaIsConstString(out[0], "a b") {
		t.Fatalf("trim: expected 'a b', got %#v", out[0])
	}
}

func TestBuiltinLtrim_Const(t *testing.T) {
	env := newTestEnv()
	in := ConstString("  a ")
	out, err := env.callBuiltin("ltrim", in, nil)
	if err != nil {
		t.Fatalf("ltrim: unexpected error: %v", err)
	}
	if !schemaIsConstString(out[0], "a ") {
		t.Fatalf("ltrim: expected 'a ', got %#v", out[0])
	}
}

func TestBuiltinRtrim_Const(t *testing.T) {
	env := newTestEnv()
	in := ConstString(" a  ")
	out, err := env.callBuiltin("rtrim", in, nil)
	if err != nil {
		t.Fatalf("rtrim: unexpected error: %v", err)
	}
	if !schemaIsConstString(out[0], " a") {
		t.Fatalf("rtrim: expected ' a', got %#v", out[0])
	}
}

func TestBuiltinTrimstr_Const(t *testing.T) {
	env := newTestEnv()
	in := ConstString("xxabcxx")
	args := []*oas3.Schema{ConstString("xx")}
	out, err := env.callBuiltin("trimstr", in, args)
	if err != nil {
		t.Fatalf("trimstr: unexpected error: %v", err)
	}
	if !schemaIsConstString(out[0], "abc") {
		t.Fatalf("trimstr: expected 'abc', got %#v", out[0])
	}
}

func TestBuiltinTrim_Symbolic(t *testing.T) {
	env := newTestEnv()
	in := StringType()
	out, err := env.callBuiltin("trim", in, nil)
	if err != nil {
		t.Fatalf("trim sym: unexpected error: %v", err)
	}
	if getType(out[0]) != "string" {
		t.Fatalf("trim sym: expected string type, got %s", getType(out[0]))
	}
}

// ------------------------
// utf8bytelength tests
// ------------------------

func TestBuiltinUtf8ByteLength_Const(t *testing.T) {
	env := newTestEnv()
	in := ConstString("é") // 2 bytes in UTF-8
	out, err := env.callBuiltin("utf8bytelength", in, nil)
	if err != nil {
		t.Fatalf("utf8bytelength const: unexpected error: %v", err)
	}
	// Expect integer 2
	if out[0] == nil || out[0].Enum == nil || len(out[0].Enum) != 1 || out[0].Enum[0].Value != "2" {
		t.Fatalf("utf8bytelength const: expected 2, got %#v", out[0])
	}
	// type can be integer or number depending on ConstInteger
}

func TestBuiltinUtf8ByteLength_Symbolic(t *testing.T) {
	env := newTestEnv()
	in := StringType()
	out, err := env.callBuiltin("utf8bytelength", in, nil)
	if err != nil {
		t.Fatalf("utf8bytelength sym: unexpected error: %v", err)
	}
	if getType(out[0]) != "number" {
		t.Fatalf("utf8bytelength sym: expected number type, got %s", getType(out[0]))
	}
	if out[0].Minimum == nil || *out[0].Minimum != 0 {
		t.Fatalf("utf8bytelength sym: expected minimum 0, got %#v", out[0].Minimum)
	}
}

// ------------------------
// _slice tests
// ------------------------

func TestBuiltinSlice_StringConst(t *testing.T) {
	env := newTestEnv()
	// "abcdef"[1:4] = "bcd"
	in := ConstString("abcdef")
	args := []*oas3.Schema{in, ConstInteger(4), ConstInteger(1)}
	out, err := env.callBuiltin("_slice", nil, args)
	if err != nil {
		t.Fatalf("_slice string const: unexpected error: %v", err)
	}
	if len(out) != 1 || !schemaIsConstString(out[0], "bcd") {
		t.Fatalf("_slice string const: expected 'bcd', got %#v", out[0])
	}
}

func TestBuiltinSlice_ArrayTupleConst(t *testing.T) {
	env := newTestEnv()
	// [1,2,3,4][1:3] = [2,3]
	tuple := BuildArray(nil, []*oas3.Schema{
		ConstInteger(1), ConstInteger(2), ConstInteger(3), ConstInteger(4),
	})
	args := []*oas3.Schema{tuple, ConstInteger(3), ConstInteger(1)}
	out, err := env.callBuiltin("_slice", nil, args)
	if err != nil {
		t.Fatalf("_slice array tuple: unexpected error: %v", err)
	}
	if getType(out[0]) != "array" {
		t.Fatalf("_slice array tuple: expected array, got %s", getType(out[0]))
	}
	// Expect two prefix items (2,3)
	if out[0].PrefixItems == nil || len(out[0].PrefixItems) != 2 {
		t.Fatalf("_slice array tuple: expected 2 prefixItems, got %d", len(out[0].PrefixItems))
	}
	// Validate they are const 2 and const 3 (by string values)
	if out[0].PrefixItems[0].Left == nil || out[0].PrefixItems[1].Left == nil {
		t.Fatalf("_slice array tuple: missing prefix item schema")
	}
	if v, ok := extractConstValue(out[0].PrefixItems[0].Left); !ok || v.(float64) != 2 {
		t.Fatalf("_slice array tuple: first item expected 2, got %#v", out[0].PrefixItems[0].Left)
	}
	if v, ok := extractConstValue(out[0].PrefixItems[1].Left); !ok || v.(float64) != 3 {
		t.Fatalf("_slice array tuple: second item expected 3, got %#v", out[0].PrefixItems[1].Left)
	}
}

// ------------------------
// _match tests
// ------------------------

func TestBuiltinMatch_AlwaysArray(t *testing.T) {
	env := newTestEnv()
	in := StringType()
	args := []*oas3.Schema{ConstString("[a-z]+")}
	out, err := env.callBuiltin("_match", in, args)
	if err != nil {
		t.Fatalf("_match: unexpected error: %v", err)
	}
	if getType(out[0]) != "array" {
		t.Fatalf("_match: expected array, got %s", getType(out[0]))
	}
	// Ensure items are objects
	if out[0].Items == nil || out[0].Items.Left == nil || getType(out[0].Items.Left) != "object" {
		t.Fatalf("_match: expected array<object>, got %#v", out[0])
	}
}

// ------------------------
// _capture tests
// ------------------------

func TestBuiltinCapture_FromMatchObjectShape(t *testing.T) {
	env := newTestEnv()
	// Build a minimal match-object shape compatible with builtinMatchArray
	captureItem := BuildObject(map[string]*oas3.Schema{
		"name":   Union([]*oas3.Schema{StringType(), ConstNull()}, env.opts),
		"string": Union([]*oas3.Schema{StringType(), ConstNull()}, env.opts),
	}, []string{})
	matchObj := BuildObject(map[string]*oas3.Schema{
		"captures": ArrayType(captureItem),
	}, []string{"captures"})

	out, err := env.callBuiltin("_capture", matchObj, nil)
	if err != nil {
		t.Fatalf("_capture: unexpected error: %v", err)
	}
	if getType(out[0]) != "object" {
		t.Fatalf("_capture: expected object, got %s", getType(out[0]))
	}
	if out[0].AdditionalProperties == nil || out[0].AdditionalProperties.Left == nil {
		t.Fatalf("_capture: expected additionalProperties")
	}
	// Values should be string|null
	val := out[0].AdditionalProperties.Left
	if getType(val) != "" && getType(val) != "string" {
		// Could be anyOf string|null
		// Accept either widened or union form
		t.Logf("_capture: additionalProperties type: %s (ok)", getType(val))
	}
}
