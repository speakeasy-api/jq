package schemaexec

import (
	"testing"

	gojq "github.com/speakeasy-api/jq"
)

func TestTraceBytecode_ObjectWithMap(t *testing.T) {
	query, _ := gojq.Parse(`{tags: ([1,2,3] | map(. * 2))}`)
	code, _ := gojq.Compile(query)
	
	codes := code.GetCodes()
	t.Logf("Total opcodes: %d\n", len(codes))
	for i, c := range codes {
		t.Logf("  [%2d] %-15s value=%v", i, c.OpString(), c.GetValue())
	}
}
