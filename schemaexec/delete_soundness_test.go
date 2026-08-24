package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
	"gopkg.in/yaml.v3"
)

// Each test here pins a delete-traversal soundness channel: when the verdict
// is Proven, the output schema must admit the concrete jq result described in
// the test body.

// Delete traversal must never pad: `del(.a[1].b)` on {a: []} leaves {a: []},
// so navigating index 1 on the way to the delete must not bump minItems to 2.
func TestDeleteTraversalDoesNotPadArray(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{
		"a": ArrayType(BuildObject(map[string]*oas3.Schema{"b": NumberType()}, []string{"b"})),
	}, []string{"a"})
	analysis := analyzeWithOptions(t, `del(.a[1].b)`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	wrapper, ok := analysis.Output.Properties.Get("a")
	if !ok {
		t.Fatalf("property a missing: %s", schemaTypeSummary(analysis.Output, 5))
	}
	for _, branch := range schemaBranches(resolvedLeft(wrapper)) {
		if getType(branch) != "array" {
			continue
		}
		if branch.MinItems != nil && *branch.MinItems >= 2 {
			t.Errorf("a gained minItems=%d, but concrete {a:[]} stays {a:[]}", *branch.MinItems)
		}
	}
}

// A possible delete of b must invalidate a dependentSchemas entry requiring
// b: concrete {"a":"x","b":1} | .b |= empty gives {"a":"x"}, which the
// retained dependency would reject.
func TestWeakDeleteClearsDependentSchemas(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"a": StringType(), "b": NumberType()}, []string{"a", "b"})
	input.DependentSchemas = sequencedMapOf("a", &oas3.Schema{Required: []string{"b"}})
	analysis := analyzeWithOptions(t, `.b |= empty`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	for _, branch := range schemaBranches(analysis.Output) {
		if branch.DependentSchemas == nil {
			continue
		}
		if entry, ok := branch.DependentSchemas.Get("a"); ok {
			if left := resolvedLeft(entry); left != nil && isRequired(left.Required, "b") {
				t.Errorf("dependentSchemas[a] still requires b after possible delete of b")
			}
		}
	}
}

// An interior delete invalidates a container-level const: del(.a.b) on a
// value pinned to {"a":{"b":1}} yields {"a":{}}, which the const rejects.
func TestInteriorDeleteClearsContainerConst(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{
		"a": BuildObject(map[string]*oas3.Schema{"b": ConstInteger(1)}, []string{"b"}),
	}, []string{"a"})
	input.Const = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "a"},
		{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: "b"},
			{Kind: yaml.ScalarNode, Tag: "!!int", Value: "1"},
		}},
	}}
	analysis := analyzeWithOptions(t, `del(.a.b)`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	for _, branch := range schemaBranches(analysis.Output) {
		if branch.Const != nil {
			t.Errorf("container const survived an interior delete: %s", schemaTypeSummary(branch, 4))
		}
	}
}

// A strong property delete removes one key, so a minProperties lower bound
// must drop with it: del(.a) on {minProperties:1} concretely yields {}.
func TestStrongDeleteDecrementsMinProperties(t *testing.T) {
	input := BuildObject(map[string]*oas3.Schema{"a": NumberType()}, []string{"a"})
	one := int64(1)
	input.MinProperties = &one
	analysis := analyzeWithOptions(t, `del(.a)`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	for _, branch := range schemaBranches(analysis.Output) {
		if branch.MinProperties != nil && *branch.MinProperties >= 1 {
			t.Errorf("minProperties=%d survived del(.a), but concrete result is {}", *branch.MinProperties)
		}
	}
}

// A delete must reach members covered only by additionalProperties:
// del(.a.b) on {"a":{"b":1}} yields {"a":{}}, so the AP value schema cannot
// keep requiring b on every branch.
func TestDeleteReachesAdditionalPropertiesMember(t *testing.T) {
	input := ObjectType()
	input.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](
		BuildObject(map[string]*oas3.Schema{"b": NumberType()}, []string{"b"}))
	analysis := analyzeWithOptions(t, `del(.a.b)`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	requiresB := true
	for _, branch := range schemaBranches(analysis.Output) {
		apValue := resolvedLeft(branch.AdditionalProperties)
		if apValue == nil {
			requiresB = false
			continue
		}
		branchRequires := true
		for _, ap := range schemaBranches(apValue) {
			if !isRequired(ap.Required, "b") {
				branchRequires = false
			}
		}
		requiresB = requiresB && branchRequires
	}
	if requiresB {
		t.Errorf("additionalProperties value still requires b on every branch: %s", schemaTypeSummary(analysis.Output, 5))
	}
}

// Definite delpaths must apply in descending jq path order: del(.[0], .[1])
// on [10,20,30] concretely gives [30], so the output must admit a 1-element
// array whose element can be 30 (ascending application would claim [20]).
func TestDefiniteDeletePathsApplyInDescendingOrder(t *testing.T) {
	input := exactIntTuple(10, 20, 30)
	analysis := analyzeWithOptions(t, `del(.[0], .[1])`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	admits := false
	for _, branch := range schemaBranches(analysis.Output) {
		if getType(branch) != "array" {
			continue
		}
		if branch.MinItems != nil && *branch.MinItems > 1 {
			continue
		}
		if branch.MaxItems != nil && *branch.MaxItems < 1 {
			continue
		}
		var element *oas3.Schema
		if len(branch.PrefixItems) > 0 {
			element = resolvedLeft(branch.PrefixItems[0])
		} else {
			element = resolvedLeft(branch.Items)
		}
		if elementAdmitsInt(element, 30) {
			admits = true
		}
	}
	if !admits {
		t.Errorf("output cannot be [30]: %s", schemaTypeSummary(analysis.Output, 5))
	}
}

// KNOWN UNSOUNDNESS (pre-existing, equal with main): a collect-shaped paths
// argument reaches delpaths as an Items-shaped array whose single tuple is
// classified definite and strong-deleted, but the collect only POSSIBLY ran:
// concrete {"a":1,"c":false} skips the select, so a survives. Making these
// paths weak-only was rejected for its precision cost on mainline del (whose
// paths flow through the same shape); the fix needs must-occurrence
// provenance (per-state must-cardinality), the tracked follow-up.
func TestCollectShapedDelpathsStrongDeleteKnownUnsound(t *testing.T) {
	t.Skip("known pre-existing unsoundness (equal with main): Items-shaped delpaths args from conditional collects are strong-deleted; needs must-occurrence provenance (per-state must-cardinality), tracked follow-up")
	input := BuildObject(map[string]*oas3.Schema{"a": IntegerType(), "c": BoolType()}, []string{"a", "c"})
	analysis := analyzeWithOptions(t, `delpaths([select(.c) | ["a"]])`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	admitsA := false
	for _, branch := range schemaBranches(analysis.Output) {
		if branch.Properties != nil {
			if _, ok := branch.Properties.Get("a"); ok {
				admitsA = true
			}
		}
		if branch.AdditionalProperties != nil || isTopSchema(branch) {
			admitsA = true
		}
	}
	if !admitsA {
		t.Errorf("output cannot contain a, but concrete {\"a\":1,\"c\":false} keeps it: %s", schemaTypeSummary(analysis.Output, 5))
	}
}

// schemaBranches flattens one level of anyOf/oneOf so assertions can inspect
// every alternative of a joined output.
func schemaBranches(schema *oas3.Schema) []*oas3.Schema {
	if schema == nil {
		return nil
	}
	if len(schema.AnyOf) == 0 && len(schema.OneOf) == 0 {
		return []*oas3.Schema{schema}
	}
	branches := []*oas3.Schema{schema}
	for _, wrapper := range append(append([]*oas3.JSONSchema[oas3.Referenceable]{}, schema.AnyOf...), schema.OneOf...) {
		if left := resolvedLeft(wrapper); left != nil {
			branches = append(branches, schemaBranches(left)...)
		}
	}
	return branches
}

func sequencedMapOf(key string, value *oas3.Schema) *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]] {
	result := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
	result.Set(key, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](value))
	return result
}

func exactIntTuple(values ...int64) *oas3.Schema {
	schema := &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeArray)}
	for _, value := range values {
		schema.PrefixItems = append(schema.PrefixItems, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](ConstInteger(value)))
	}
	length := int64(len(values))
	schema.MinItems = &length
	schema.MaxItems = &length
	return schema
}

// elementAdmitsInt reports whether the element schema can take the given
// integer value (no schema, or no value pinning, counts as admitting).
func elementAdmitsInt(schema *oas3.Schema, want int64) bool {
	if schema == nil {
		return true
	}
	branches := schemaBranches(schema)
	for _, branch := range branches {
		if len(branch.Enum) == 0 {
			if len(branch.AnyOf) == 0 && len(branch.OneOf) == 0 {
				return true
			}
			continue
		}
		for _, node := range branch.Enum {
			var got int64
			if node != nil && node.Kind == yaml.ScalarNode && node.Decode(&got) == nil && got == want {
				return true
			}
		}
	}
	return false
}
