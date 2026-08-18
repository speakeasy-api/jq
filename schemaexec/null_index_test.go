package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// jq indexes null without erroring: `null | .foo` and `null | .[0]` both
// yield null. Chained access through a definitely-absent property
// (.missing.deep under closed-world semantics) must therefore stay null
// instead of widening to Top — otherwise `.missing.deep // fallback`
// cannot strip the null and the fallback's precise type is lost.

func runOn(t *testing.T, src string, input *oas3.Schema) *oas3.Schema {
	t.Helper()
	query, err := gojq.Parse(src)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatalf("RunSchema %q: %v", src, err)
	}
	if result.Schema == nil {
		t.Fatalf("RunSchema %q: nil schema", src)
	}
	return result.Schema
}

func closedObj() *oas3.Schema {
	return BuildObject(map[string]*oas3.Schema{
		"x": {Type: oas3.NewTypeFromString(oas3.SchemaTypeString)},
	}, []string{"x"})
}

func TestNullIndex_PropertyAccessOnNullIsNull(t *testing.T) {
	got := runOn(t, ".missing.deep", closedObj())
	if typ := getType(got); typ != "null" {
		t.Errorf("null property access: expected type 'null', got %q (%s)", typ, mustYAML(got))
	}
}

func TestNullIndex_DeepChainStaysNull(t *testing.T) {
	got := runOn(t, ".missing.deep.deeper.deepest", closedObj())
	if typ := getType(got); typ != "null" {
		t.Errorf("chained null access: expected type 'null', got %q (%s)", typ, mustYAML(got))
	}
}

func TestNullIndex_CoalesceRecoversConstant(t *testing.T) {
	got := runOn(t, `.missing.deep // "fallback"`, closedObj())
	if typ := getType(got); typ != "string" {
		t.Fatalf("coalesce after null access: expected 'string', got %q (%s)", typ, mustYAML(got))
	}
	if v, ok := extractConstString(got); !ok || v != "fallback" {
		t.Errorf("coalesce after null access: expected const \"fallback\", got %s", mustYAML(got))
	}
}

func TestNullIndex_ConcatThroughNullAccessStaysString(t *testing.T) {
	got := runOn(t, `"a" + (.missing.deep // "") + "b"`, closedObj())
	if typ := getType(got); typ != "string" {
		t.Errorf("concat through null access: expected 'string', got %q (%s)", typ, mustYAML(got))
	}
}

func TestNullIndex_ArrayIndexOnNullIsNull(t *testing.T) {
	got := runOn(t, ".missing[0]", closedObj())
	if typ := getType(got); typ != "null" {
		t.Errorf("array index on null: expected type 'null', got %q (%s)", typ, mustYAML(got))
	}
}

func mustYAML(s *oas3.Schema) string {
	return FingerprintSchema(s)
}
