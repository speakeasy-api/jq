package schemaexec

import (
	"context"
	"sync"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestConcurrentRunSchema pins that concurrent RunSchema calls are safe.
// Closure tracking (opPushPC/opCallPC) used to write to an unsynchronized
// package-global map, a write/write data race between executions. The
// closure registry now lives on the per-execution env. Run with -race to
// verify; in normal mode this still exercises the concurrent path cheaply.
func TestConcurrentRunSchema(t *testing.T) {
	query, err := gojq.Parse(`def apply(f): f; apply(.a)`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			input := BuildObject(map[string]*oas3.Schema{"a": IntegerType()}, []string{"a"})
			result, err := RunSchema(context.Background(), query, input)
			if err != nil {
				errs[i] = err
				return
			}
			if result == nil || result.Schema == nil {
				return
			}
			if got := getType(result.Schema); got != "integer" {
				t.Errorf("goroutine %d: result type = %s, want integer", i, got)
			}
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: RunSchema: %v", i, err)
		}
	}
}
