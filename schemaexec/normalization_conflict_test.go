package schemaexec

import (
	"context"
	"testing"
	"time"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestNormalizationMergeConflictIsLocalized(t *testing.T) {
	conflict := &oas3.Schema{
		AllOf: []*oas3.JSONSchema[oas3.Referenceable]{
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
			oas3.NewJSONSchemaFromSchema[oas3.Referenceable](IntegerType()),
		},
	}
	input := BuildObject(map[string]*oas3.Schema{
		"x": conflict,
		"y": StringType(),
	}, []string{"x", "y"})

	tests := []struct {
		expr        string
		wantVerdict Verdict
		wantType    string
	}{
		{expr: `.y`, wantVerdict: VerdictProven, wantType: "string"},
		{expr: `.x`, wantVerdict: VerdictUnverifiable},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			query, err := gojq.Parse(tt.expr)
			if err != nil {
				t.Fatal(err)
			}
			analysis, err := Analyze(context.Background(), query, input)
			if err != nil {
				t.Fatalf("Analyze: %v", err)
			}
			if analysis.Verdict != tt.wantVerdict {
				t.Fatalf("verdict = %s, want %s; output=%s", analysis.Verdict, tt.wantVerdict, schemaTypeSummary(analysis.Output, 2))
			}
			if tt.wantType != "" && getType(analysis.Output) != tt.wantType {
				t.Fatalf("type = %q, want %q", getType(analysis.Output), tt.wantType)
			}
		})
	}
}

func TestRecursiveNormalizationMergeConflictTerminates(t *testing.T) {
	recursive := BuildObject(map[string]*oas3.Schema{
		"name": StringType(),
	}, []string{"name"})
	recursive.Properties.Set("next", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](recursive))
	recursive.AllOf = []*oas3.JSONSchema[oas3.Referenceable]{
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
		oas3.NewJSONSchemaFromSchema[oas3.Referenceable](IntegerType()),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	env := newSchemaEnv(ctx, DefaultOptions())
	normalized, err := normalizeSchema(env.normalizationContext(), recursive)
	if err != nil {
		t.Fatalf("normalizeSchema: %v", err)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("normalization exceeded deadline: %v", err)
	}
	if normalized == nil || !isTopSchema(normalized) {
		t.Fatalf("normalized = %s, want Top", schemaTypeSummary(normalized, 2))
	}
	if memoized, ok := env.norm.memo[recursive]; !ok || memoized != normalized {
		t.Fatalf("memoized result = %p, want Top shell %p", memoized, normalized)
	}

	query, err := gojq.Parse(`.`)
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := Analyze(ctx, query, recursive)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if analysis.Verdict != VerdictUnverifiable {
		t.Fatalf("verdict = %s, want unverifiable", analysis.Verdict)
	}
}
