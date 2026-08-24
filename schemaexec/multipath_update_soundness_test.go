package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func assertMultiPathUpdateSound(t *testing.T, names []string) {
	t.Helper()
	props := map[string]*oas3.Schema{}
	expr := "("
	for i, name := range names {
		props[name] = NumberType()
		if i > 0 {
			expr += ","
		}
		expr += "." + name
	}
	expr += ") |= tostring"
	input := BuildObject(props, names)
	analysis := analyzeWithOptions(t, expr, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	for _, name := range names {
		wrapper, ok := analysis.Output.Properties.Get(name)
		if !ok {
			t.Fatalf("n=%d: property %s missing: %s", len(names), name, schemaTypeSummary(analysis.Output, 5))
		}
		if left := resolvedLeft(wrapper); !MightBeString(left) && !isTopSchema(left) {
			t.Errorf("n=%d: property %s = %s cannot be string after |= tostring", len(names), name, schemaTypeSummary(left, 3))
		}
	}
}

// Multi-path update assignment across enough alternatives to trigger frontier
// merging during path collection. Regression for two joined-state bugs:
// subsumption discarding states whose collected write path differed, and
// joinState keeping only one side's currentPath / pending update controls.
func TestMultiPathUpdateSurvivesFrontierMerging(t *testing.T) {
	assertMultiPathUpdateSound(t, []string{"a", "b"})
	assertMultiPathUpdateSound(t, []string{"a", "b", "c", "d"})
	assertMultiPathUpdateSound(t, []string{"a", "b", "c", "d", "e", "f"})
	assertMultiPathUpdateSound(t, []string{"a", "b", "c", "d", "e", "f", "g", "h"})
}

func TestMultiPathUpdateSixteenPaths(t *testing.T) {
	names := []string{}
	for c := 'a'; c <= 'p'; c++ {
		names = append(names, string(c))
	}
	assertMultiPathUpdateSound(t, names)
}

// Mixed-depth multi-path update: joining collected paths of different lengths
// widens to an unknown-depth tail; the write must widen rather than drop
// either path.
func TestMixedDepthMultiPathUpdateStaysSound(t *testing.T) {
	inner := BuildObject(map[string]*oas3.Schema{"c": NumberType()}, []string{"c"})
	props := map[string]*oas3.Schema{"a": NumberType(), "b": inner, "d": NumberType(), "e": NumberType(), "f": NumberType(), "g": NumberType(), "h": NumberType(), "i": NumberType()}
	input := BuildObject(props, []string{"a", "b", "d", "e", "f", "g", "h", "i"})
	analysis := analyzeWithOptions(t, `(.a,.b.c,.d,.e,.f,.g,.h,.i) |= tostring`, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	for _, name := range []string{"a", "d", "e", "f", "g", "h", "i"} {
		wrapper, ok := analysis.Output.Properties.Get(name)
		if !ok {
			continue
		}
		if left := resolvedLeft(wrapper); !MightBeString(left) && !isTopSchema(left) {
			t.Errorf("property %s = %s cannot be string", name, schemaTypeSummary(left, 3))
		}
	}
}

func assertMultiDelDropsRequired(t *testing.T, names []string) {
	t.Helper()
	props := map[string]*oas3.Schema{}
	expr := "del("
	for i, name := range names {
		props[name] = NumberType()
		if i > 0 {
			expr += ","
		}
		expr += "." + name
	}
	expr += ")"
	props["keep"] = StringType()
	input := BuildObject(props, append(append([]string{}, names...), "keep"))
	analysis := analyzeWithOptions(t, expr, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	for _, name := range names {
		if isRequired(analysis.Output.Required, name) {
			t.Errorf("n=%d: %s still required after del", len(names), name)
		}
	}
	if wrapper, ok := analysis.Output.Properties.Get("keep"); ok {
		if left := resolvedLeft(wrapper); !MightBeString(left) && !isTopSchema(left) {
			t.Errorf("n=%d: keep = %s cannot be string", len(names), schemaTypeSummary(left, 3))
		}
	}
}

// Multi-path del: disjunctive merging can flatten collected path tuples into
// one enum-headed tuple; each enum value must still be treated as a possible
// delete (weakly), never silently dropped.
func TestMultiPathDeleteDropsRequired(t *testing.T) {
	assertMultiDelDropsRequired(t, []string{"a", "b"})
	assertMultiDelDropsRequired(t, []string{"a", "b", "c"})
	assertMultiDelDropsRequired(t, []string{"a", "b", "c", "d"})
	assertMultiDelDropsRequired(t, []string{"a", "b", "c", "d", "e"})
	assertMultiDelDropsRequired(t, []string{"a", "b", "c", "d", "e", "f"})
}

// KNOWN UNSOUNDNESS (pre-existing, reproduced and root-caused during the
// round-3 review): with seven or more deleted paths, widening inside the
// [path(f)] collect loop breaks the accumulator's pointer-identity chain; the
// surviving exit lineage loads the original empty array, delpaths sees a
// schema indistinguishable from a genuinely empty path list, and the deletes
// silently no-op while the verdict stays Proven. The fix belongs in the
// collect/backtrack machinery (per-state must-cardinality and join-aware
// accumulator identity), not in the delpaths consumer.
func TestMultiPathDeleteSevenPlusKnownUnsound(t *testing.T) {
	t.Skip("known pre-existing unsoundness: collect-loop widening loses the paths accumulator for >=7 paths; see PR #3 follow-ups")
	assertMultiDelDropsRequired(t, []string{"a", "b", "c", "d", "e", "f", "g"})
	assertMultiDelDropsRequired(t, []string{"a", "b", "c", "d", "e", "f", "g", "h"})
}

// KNOWN UNSOUNDNESS (pre-existing, reproduced): when the RHS branches of a
// multi-path update reconverge and merge mid-update, a post-merge write can
// fail to reach the pending eager alternative's snapshot (its pointer-keyed
// replacement chain was broken by the value join). The provenance-based lazy
// rebase (joinedValueSources) repairs scope-level joins, but this shape
// breaks through a channel it does not yet cover.
func TestPostMergeWriteReachesPendingAlternativeKnownUnsound(t *testing.T) {
	t.Skip("known pre-existing unsoundness: post-merge writes can miss pending update alternatives; see PR #3 follow-ups")
	arm := func(v int64) *oas3.Schema {
		return BuildObject(map[string]*oas3.Schema{"k": ConstInteger(v)}, []string{"k"})
	}
	inner := anyOfSchemas(arm(0), arm(1), arm(2), arm(3), arm(4), arm(5), arm(6), arm(7))
	input := BuildObject(map[string]*oas3.Schema{"a": inner, "b": inner}, []string{"a", "b"})
	expr := `(.a, .b) |= (
		(if .k == 0 then {x: 0} elif .k == 1 then {x: 1} elif .k == 2 then {x: 2}
		 elif .k == 3 then {x: 3} elif .k == 4 then {x: 4} elif .k == 5 then {x: 5}
		 elif .k == 6 then {x: 6} else {x: 7} end)
		| .y = 1
	)`
	analysis := analyzeWithOptions(t, expr, input, DefaultOptions())
	if analysis.Verdict != VerdictProven {
		return
	}
	for _, name := range []string{"a", "b"} {
		wrapper, ok := analysis.Output.Properties.Get(name)
		if !ok {
			t.Fatalf("missing %s", name)
		}
		left := resolvedLeft(wrapper)
		admitsY := isTopSchema(left) || left.AdditionalProperties != nil
		if !admitsY && left != nil && left.Properties != nil {
			_, admitsY = left.Properties.Get("y")
		}
		if !admitsY {
			for _, br := range left.AnyOf {
				b := resolvedLeft(br)
				if b == nil {
					continue
				}
				if b.Properties != nil {
					if _, ok := b.Properties.Get("y"); ok {
						admitsY = true
						break
					}
				}
				if b.AdditionalProperties != nil || isTopSchema(b) {
					admitsY = true
					break
				}
			}
		}
		if !admitsY {
			t.Errorf("%s = %s cannot contain y, but concrete always sets y=1", name, schemaTypeSummary(left, 4))
		}
	}
}
