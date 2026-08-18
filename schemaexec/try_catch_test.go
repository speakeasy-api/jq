package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestTryCatchHandlerReceivesUnknownErrorValue(t *testing.T) {
	in := BuildObject(nil, nil)
	a := analyzeExpr(t, `try .missing[] catch .`, in)
	if a.Verdict != VerdictUnverifiable {
		t.Fatalf("verdict = %s, want unverifiable", a.Verdict)
	}
	if !schemaAdmitsString(a.Output, "cannot iterate over: null") {
		t.Fatalf("caught value must admit a runtime error string: %s", schemaTypeSummary(a.Output, 2))
	}
}

func TestTryCatchConstantHandlerRemainsProven(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{"a": StringType()}, []string{"a"})
	for _, expr := range []string{`try .a catch null`, `try .a catch "x"`, `try .a catch empty`} {
		t.Run(expr, func(t *testing.T) {
			a := analyzeExpr(t, expr, in)
			if a.Verdict != VerdictProven {
				t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
			}
		})
	}
}

func TestOptionalPropertyAccessRemainsProven(t *testing.T) {
	in := BuildObject(map[string]*oas3.Schema{"a": StringType()}, nil)
	a := analyzeExpr(t, `.a?`, in)
	if a.Verdict != VerdictProven {
		t.Fatalf("verdict = %s, want proven (causes: %v)", a.Verdict, a.Causes)
	}
}
