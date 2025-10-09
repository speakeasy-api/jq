package schemaexec

import (
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
)

// TestDumpBytecode dumps the bytecode for the failing query to understand
// the exact sequence of operations and stack discipline.
func TestDumpBytecode(t *testing.T) {
	queries := []string{
		`.id`,                     // Simple property access
		`{id: .id}`,              // Object construction with property
		`map({id: .id})`,         // Map with property passthrough
		`{id: .id, status: "x"}`, // Mixed: passthrough + computed
		`map({id: .id, title: .title, status: (if .active then "active" else "inactive" end)})`, // Full failing case
	}

	for _, jqExpr := range queries {
		t.Run(jqExpr, func(t *testing.T) {
			query, err := gojq.Parse(jqExpr)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}

			code, err := gojq.Compile(query)
			if err != nil {
				t.Fatalf("Compile failed: %v", err)
			}

			// Dump bytecode
			rawCodes := code.GetCodes()
			t.Logf("\n=== Bytecode for: %s ===", jqExpr)
			t.Logf("Total instructions: %d\n", len(rawCodes))

			for i, rc := range rawCodes {
				op := getCodeOp(rc)
				val := getCodeValue(rc)

				opName := opcodeToString(op)
				t.Logf("%3d: %-15s %v", i, opName, val)
			}
			t.Log("")
		})
	}
}

// opcodeToString converts opcode number to readable name
func opcodeToString(op int) string {
	names := map[int]string{
		0:  "nop",
		1:  "push",
		2:  "pop",
		3:  "dup",
		4:  "const",
		5:  "load",
		6:  "store",
		7:  "object",
		8:  "append",
		9:  "fork",
		10: "forkTryBegin",
		11: "forkTryEnd",
		12: "forkAlt",
		13: "forkLabel",
		14: "backtrack",
		15: "jump",
		16: "jumpIfNot",
		17: "index",
		18: "indexArray",
		19: "call",
		20: "callRec",
		21: "pushPC",
		22: "callPC",
		23: "scope",
		24: "ret",
		25: "iter",
		26: "expBegin",
		27: "expEnd",
		28: "pathBegin",
		29: "pathEnd",
	}

	if name, ok := names[op]; ok {
		return name
	}
	return fmt.Sprintf("op%d", op)
}
