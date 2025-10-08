package schemaexec

import (
	"testing"

	gojq "github.com/speakeasy-api/jq"
)

func TestTraceShorthand(t *testing.T) {
	query, _ := gojq.Parse(`{sku, total: (.price * .quantity)}`)
	code, _ := gojq.Compile(query)
	
	codes := code.GetCodes()
	t.Logf("Total opcodes: %d\n", len(codes))
	for i, c := range codes {
		t.Logf("  [%2d] %-15s value=%v", i, c.OpString(), c.GetValue())
	}
}
