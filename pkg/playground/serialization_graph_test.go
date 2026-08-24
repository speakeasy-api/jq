package playground

import (
	"testing"
	"time"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
)

func TestSchemaForSerializationMemoizesSharedDAG(t *testing.T) {
	const depth = 14
	shared := &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeString)}
	for range depth {
		properties := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		properties.Set("left", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](shared))
		properties.Set("right", oas3.NewJSONSchemaFromSchema[oas3.Referenceable](shared))
		shared = &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeObject), Properties: properties}
	}

	started := time.Now()
	serialized := schemaForSerialization(shared, 100)
	elapsed := time.Since(started)
	seen := make(map[*oas3.Schema]bool)
	var countNodes func(*oas3.Schema) int
	countNodes = func(schema *oas3.Schema) int {
		if schema == nil || seen[schema] {
			return 0
		}
		seen[schema] = true
		total := 1
		if schema.Properties != nil {
			for _, wrapper := range schema.Properties.All() {
				total += countNodes(wrapper.GetLeft())
			}
		}
		return total
	}
	if nodes := countNodes(serialized); nodes != depth+1 {
		t.Fatalf("serialized graph has %d nodes, want %d", nodes, depth+1)
	}
	if elapsed > time.Second {
		t.Fatalf("serializing a %d-node shared DAG took %s", depth+1, elapsed)
	}
	t.Logf("serialized %d shared nodes in %s", depth+1, elapsed)
}
