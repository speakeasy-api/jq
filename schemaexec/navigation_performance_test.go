package schemaexec

import (
	"bytes"
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/openapi"
)

func benchmarkProjectionFixture() *oas3.Schema {
	leaf := BuildObject(map[string]*oas3.Schema{
		"c": StringType(),
		"d": IntegerType(),
	}, []string{"c"})
	nested := BuildObject(map[string]*oas3.Schema{
		"b": ArrayType(leaf),
	}, []string{"b"})
	return BuildObject(map[string]*oas3.Schema{
		"a": nested,
		"e": StringType(),
	}, []string{"a", "e"})
}

func benchmarkRefItemsSchema(b *testing.B) *oas3.Schema {
	b.Helper()
	doc, validationErrs, err := openapi.Unmarshal(context.Background(), bytes.NewReader([]byte(refItemsDoc)))
	if err != nil {
		b.Fatal(err)
	}
	if len(validationErrs) > 0 {
		b.Fatalf("document validation errors: %v", validationErrs)
	}
	if _, err := doc.ResolveAllReferences(context.Background(), openapi.ResolveAllOptions{}); err != nil {
		b.Fatal(err)
	}
	component, ok := doc.Components.Schemas.Get("List")
	if !ok || component.GetLeft() == nil {
		b.Fatal("List component is missing")
	}
	return component.GetLeft()
}

func benchmarkAnalyze(b *testing.B, expression string, input *oas3.Schema) {
	b.Helper()
	query, err := gojq.Parse(expression)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := Analyze(context.Background(), query, input); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRecursiveProjectionQueries(b *testing.B) {
	fixture := benchmarkProjectionFixture()
	queries := []struct {
		name       string
		expression string
	}{
		{"StringsLength", `[.. | strings] | length`},
		{"WalkDelete", `walk(if type == "object" then del(.d) else . end)`},
		{"ToStreamLength", `tostream | length`},
		{"PathsLength", `[paths] | length`},
	}
	for _, query := range queries {
		b.Run(query.name, func(b *testing.B) {
			benchmarkAnalyze(b, query.expression, fixture)
		})
	}
}

func BenchmarkReferenceRecursiveQueries(b *testing.B) {
	fixture := benchmarkRefItemsSchema(b)
	queries := []struct {
		name       string
		expression string
	}{
		{"WalkDelete", `walk(if type == "object" then del(.d) else . end)`},
		{"ToStreamLength", `tostream | length`},
	}
	for _, query := range queries {
		b.Run(query.name, func(b *testing.B) {
			benchmarkAnalyze(b, query.expression, fixture)
		})
	}
}
