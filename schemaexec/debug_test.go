package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestDebug_ObjectConstruction debugs what opcodes are generated for {name: .x}
func TestDebug_ObjectConstruction(t *testing.T) {
	// Parse query
	query, err := gojq.Parse("{name: .x}")
	if err != nil {
		t.Fatal(err)
	}

	// Compile
	code, err := gojq.Compile(query)
	if err != nil {
		t.Fatal(err)
	}

	// Print opcodes
	codes := code.GetCodes()
	t.Logf("Total opcodes: %d", len(codes))
	for i, c := range codes {
		t.Logf("  [%d] %s (value: %v)", i, c.OpString(), c.GetValue())
	}
}

// TestDebug_SimpleProperty debugs what opcodes are generated for .foo
func TestDebug_SimpleProperty(t *testing.T) {
	query, err := gojq.Parse(".foo")
	if err != nil {
		t.Fatal(err)
	}

	code, err := gojq.Compile(query)
	if err != nil {
		t.Fatal(err)
	}

	codes := code.GetCodes()
	t.Logf("Total opcodes for '.foo': %d", len(codes))
	for i, c := range codes {
		t.Logf("  [%d] %s (value: %v)", i, c.OpString(), c.GetValue())
	}
}

// TestDebug_TraceObjectConstruction traces object construction step-by-step
func TestDebug_TraceObjectConstruction(t *testing.T) {
	query, _ := gojq.Parse("{name: .x}")
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"x": StringType(),
	}, []string{"x"})

	// Create env and execute with tracing
	code, _ := gojq.Compile(query)
	opts := DefaultOptions()
	env := newSchemaEnv(context.Background(), opts)

	// Get bytecode
	rawCodes := code.GetCodes()
	env.codes = make([]codeOp, len(rawCodes))
	for i, rc := range rawCodes {
		env.codes[i] = codeOp{
			op:    getCodeOp(rc),
			value: getCodeValue(rc),
		}
	}

	env.stack.pushSchema(inputSchema)

	t.Logf("Initial stack depth: %d", env.stack.len())

	for env.pc < len(env.codes) {
		c := env.codes[env.pc]
		t.Logf("[PC=%d] Executing opcode %d (value: %v), stack depth before: %d",
			env.pc, c.op, c.value, env.stack.len())

		err := env.executeOp(&c)
		if err != nil {
			t.Logf("  ERROR: %v", err)
			break
		}

		t.Logf("  Stack depth after: %d", env.stack.len())
		if !env.stack.empty() {
			top := env.stack.topSchema()
			t.Logf("  Top of stack: type=%s", getType(top))
		}

		env.pc++
	}

	t.Logf("Final stack depth: %d", env.stack.len())
}
