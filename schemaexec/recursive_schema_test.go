package schemaexec

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

const recursiveNodeDoc = `openapi: 3.1.0
info:
  title: recursive schema
  version: 1.0.0
paths: {}
components:
  schemas:
    Node:
      type: object
      properties:
        name:
          type: string
        children:
          type: array
          items:
            $ref: '#/components/schemas/Node'
      required: [name, children]
`

func TestRecursiveSchemaQueriesTerminate(t *testing.T) {
	node := loadComponentSchema(t, recursiveNodeDoc, "Node")
	tests := []struct {
		expr       string
		wantString bool
	}{
		{expr: `.children[].name`, wantString: true},
		{expr: `.children[].children[].name`, wantString: true},
		{expr: `[.. | .name?]`},
		{expr: `[recurse(.children[]) | .name]`},
	}

	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			query, err := gojq.Parse(tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			analysis, err := Analyze(ctx, query, node)
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			if err := ctx.Err(); err != nil {
				t.Fatalf("query exceeded deadline: %v", err)
			}
			if analysis.Verdict == VerdictProvenBroken {
				t.Fatalf("verdict = %s, want live or unverifiable output", analysis.Verdict)
			}
			if tt.wantString && (analysis.Verdict != VerdictProven || getType(analysis.Output) != "string") {
				t.Fatalf("output = %s (%s), want proven string", schemaTypeSummary(analysis.Output, 3), analysis.Verdict)
			}
			t.Logf("completed in %s with %s", time.Since(started), analysis.Verdict)
		})
	}
}

func TestRecursiveNormalizationPreservesCycleAndMemoIdentity(t *testing.T) {
	node := loadComponentSchema(t, recursiveNodeDoc, "Node")
	env := newSchemaEnv(context.Background(), DefaultOptions())
	normalized, err := normalizeSchema(env.normalizationContext(), node)
	if err != nil {
		t.Fatal(err)
	}
	children := GetProperty(normalized, "children", env.opts)
	first, ok := derefJSONSchema(env.normalizationContext(), children.Items)
	if !ok {
		t.Fatal("children items did not dereference")
	}
	second, ok := derefJSONSchema(env.normalizationContext(), children.Items)
	if !ok {
		t.Fatal("children items did not dereference twice")
	}
	if first != normalized || second != first {
		t.Fatalf("recursive item pointers = %p, %p; normalized root = %p", first, second, normalized)
	}
	again, err := normalizeSchema(env.normalizationContext(), normalized)
	if err != nil {
		t.Fatal(err)
	}
	if again != normalized {
		t.Fatalf("normalizing a result changed identity: %p -> %p", normalized, again)
	}
}

func TestCollapseCompatibilityWrappersUseNormalizer(t *testing.T) {
	input := loadComponentSchema(t, recursiveNodeDoc, "Node")
	tests := []struct {
		name string
		call func() (*oas3.Schema, error)
	}{
		{name: "allOf context", call: func() (*oas3.Schema, error) {
			return collapseAllOfCtx(newCollapseContext(), input)
		}},
		{name: "anyOf", call: func() (*oas3.Schema, error) {
			return collapseAnyOf(input)
		}},
		{name: "anyOf context", call: func() (*oas3.Schema, error) {
			return collapseAnyOfCtx(newCollapseContext(), input)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call()
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || getType(got) != "object" {
				t.Fatalf("result = %s", schemaTypeSummary(got, 2))
			}
		})
	}
}

func TestRecursiveSchemaWalkersTerminate(t *testing.T) {
	input := loadComponentSchema(t, recursiveNodeDoc, "Node")
	first, err := normalizeSchema(newCollapseContext(), input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeSchema(newCollapseContext(), input)
	if err != nil {
		t.Fatal(err)
	}

	if schemaFingerprint(first) == "" {
		t.Fatal("empty fingerprint")
	}
	if !isSubschemaOf(first, first) {
		t.Fatal("schema is not a subschema of itself")
	}
	_ = isSubschemaOf(first, second)
	if merged, err := mergeSchemas(first, second); err != nil || merged == nil {
		t.Fatalf("mergeSchemas = %v, %v", merged, err)
	}
	if cloned := cloneSchema(first); cloned == nil || cloned == first {
		t.Fatalf("cloneSchema = %p", cloned)
	}
	if isTopSchema(first) || isBottomSchema(first) {
		t.Fatal("recursive object misclassified as Top or Bottom")
	}
	if got := schemaTypeSummary(first, 4); got == "" {
		t.Fatal("empty schema summary")
	}
	_ = collectSchemaIssues(first, nil)
	if got := deduplicateSchemas([]*oas3.Schema{first, second}); len(got) != 1 {
		t.Fatalf("deduplicateSchemas kept %d equivalent graphs", len(got))
	}
	if got := Union([]*oas3.Schema{first, second}, DefaultOptions()); got == nil {
		t.Fatal("Union returned Bottom")
	}
	if got := stripNullUnion(first, DefaultOptions()); got == nil {
		t.Fatal("stripNullUnion returned Bottom")
	}
	if got := impliedTypeOf(first); got != "object" {
		t.Fatalf("impliedTypeOf = %q, want object", got)
	}
}

func TestNormalizationHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nctx := newNormCtx(ctx)
	_, err := normalizeSchema(withNormCtx(ctx, nctx), ObjectType())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("normalize error = %v, want context canceled", err)
	}
}

func TestNestedSchemaNormalizationCost(t *testing.T) {
	for _, depth := range []int{8, 12, 16, 20} {
		t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
			input := nestedObjectChain(depth)
			query, err := gojq.Parse(`.a.a`)
			if err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			if _, err := RunSchema(context.Background(), query, input); err != nil {
				t.Fatal(err)
			}
			if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
				t.Fatalf("depth %d took %s", depth, elapsed)
			} else {
				t.Logf("depth %d completed in %s", depth, elapsed)
			}
		})
	}
}

func nestedObjectChain(depth int) *oas3.Schema {
	result := StringType()
	for range depth {
		result = BuildObject(map[string]*oas3.Schema{
			"a": result,
			"b": IntegerType(),
		}, []string{"a", "b"})
	}
	return result
}
