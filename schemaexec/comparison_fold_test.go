package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// The comparison builtins (_equal, _less, ...) are compiled as two-argument
// calls (compileCall(op.getFunc(), [Left, Right])), matching the concrete
// runtime's argFunc2 convention: args[0] is the LEFT operand, args[1] the
// RIGHT. They used to guard len(args) != 1 and therefore never const-folded.
func requireConstBool(t *testing.T, s *oas3.Schema, want bool) {
	t.Helper()
	if getType(s) != "boolean" {
		t.Fatalf("output type = %s, want boolean (%s)", getType(s), schemaTypeSummary(s, 3))
	}
	val, ok := extractConstValue(s)
	if !ok {
		t.Fatalf("output is not a const boolean: %s", schemaTypeSummary(s, 3))
	}
	got, ok := val.(bool)
	if !ok || got != want {
		t.Fatalf("output const = %v, want %v", val, want)
	}
}

func TestComparisonConstFolding(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{}, nil)
	cases := []struct {
		expr string
		want bool
	}{
		{`1 == 1`, true},
		{`1 == 2`, false},
		{`1 != 2`, true},
		{`1 < 2`, true},
		{`2 < 1`, false}, // pins operand order (args[0] = left)
		{`2 > 1`, true},
		{`1 >= 1`, true},
		{`2 <= 1`, false},
		{`"a" == "a"`, true},
		{`"a" == "b"`, false},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			a := analyzeExpr(t, tc.expr, input)
			if a.Verdict != VerdictProven {
				t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
			}
			requireConstBool(t, a.Output, tc.want)
		})
	}
}

func TestComparisonUnknownOperandsStayBoolean(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{
		"a": IntegerType(),
		"b": IntegerType(),
	}, []string{"a", "b"})
	a := analyzeExpr(t, `.a == .b`, input)
	if a.Verdict == VerdictProvenBroken {
		t.Fatalf("verdict = %s (causes: %v)", a.Verdict, a.Causes)
	}
	out := a.Output
	if getType(out) != "boolean" {
		t.Fatalf("output type = %s, want boolean: %s", getType(out), schemaTypeSummary(out, 3))
	}
	if _, isConst := extractConstValue(out); isConst {
		t.Fatalf("unknown == unknown must not fold to a const: %s", schemaTypeSummary(out, 3))
	}
}
