package playground

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// context_start_text:
// components:
//   schemas:
//     TagList:
//       type: object
//       x-speakeasy-transform-from-api:
//         jq: >
//           { ... }
//       properties:
//         tags:
//           type: array
//           items:
//             type: string
// context_end_text.

// TestSymbolicExecuteJQ_ChaosRefs validates that externalizing array items into components/schemas via $ref
// yields semantically identical transformed root schemas as running with inline schemas.
// Focus: array items externalization only, deterministic naming; no ref-chains or combinators yet.
func TestSymbolicExecuteJQ_ChaosRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping chaos ref tests in -short mode")
	}

	type tc struct {
		name    string
		root    string
		oasYAML string
	}

	// TagList mirrors the case that previously broke when items used $ref.
	tagList := `
openapi: 3.1.0
info:
  title: TagEnrichment
  version: 1.0.0
paths: {}
components:
  schemas:
    TagList:
      type: object
      x-speakeasy-transform-from-api:
        jq: >
          {
            tags: (.tags // []) | map({
              value: .,
              slug: (. | ascii_downcase),
              length: (. | length)
            })
          }
      x-speakeasy-transform-to-api:
        jq: >
          {
            tags: (.tags // []) | map(.value)
          }
      properties:
        tags:
          type: array
          items:
            type: string
`

	// ProductInput - simple pass-through transform with an array-of-strings to stress items $ref externalization.
	productInput := `
openapi: 3.1.0
info:
  title: ProductInput
  version: 1.0.0
paths: {}
components:
  schemas:
    ProductInput:
      type: object
      x-speakeasy-transform-from-api:
        jq: "."
      x-speakeasy-transform-to-api:
        jq: "."
      properties:
        name:
          type: string
        tags:
          type: array
          items:
            type: string
`

	// CartInput - pass-through transform with an array-of-objects and nested array-of-strings.
	cartInput := `
openapi: 3.1.0
info:
  title: CartInput
  version: 1.0.0
paths: {}
components:
  schemas:
    CartInput:
      type: object
      x-speakeasy-transform-from-api:
        jq: "."
      x-speakeasy-transform-to-api:
        jq: "."
      properties:
        items:
          type: array
          items:
            type: object
            properties:
              sku:
                type: string
              qty:
                type: integer
              tags:
                type: array
                items:
                  type: string
`

	cases := []tc{
		{name: "TagList", root: "TagList", oasYAML: tagList},
		{name: "ProductInput", root: "ProductInput", oasYAML: productInput},
		{name: "CartInput", root: "CartInput", oasYAML: cartInput},
	}

	percents := []float64{0.0, 0.5, 1.0}
	nSeeds := 1
	if v := os.Getenv("CHAOS_SEEDS"); v != "" {
		var parsed int
		_, _ = fmt.Sscanf(v, "%d", &parsed)
		if parsed > 0 {
			nSeeds = parsed
		}
	}

	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			for _, p := range percents {
				p := p
				t.Run(fmt.Sprintf("externalize_%.2f", p), func(t *testing.T) {
					for s := 0; s < nSeeds; s++ {
						seed := int64(1337 + s)
						t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
							// 1) Run inline baseline
							inlineOut, err := SymbolicExecuteJQ(c.oasYAML)
							if err != nil {
								t.Fatalf("inline SymbolicExecuteJQ failed: %v", err)
							}

							// 2) Externalize array items to components and run again
							docMap, err := yamlToMap(c.oasYAML)
							if err != nil {
								t.Fatalf("failed to parse base OAS: %v", err)
							}
							extDoc, count, err := externalizeSchemas(docMap, p, seed)
							if err != nil {
								t.Fatalf("externalizeSchemas failed: %v", err)
							}
							_ = count // available for future assertions or logs
							extYAML, err := mapToYAML(extDoc)
							if err != nil {
								t.Fatalf("failed to marshal externalized OAS: %v", err)
							}

							extOut, err := SymbolicExecuteJQ(extYAML)
							if err != nil {
								t.Fatalf("externalized SymbolicExecuteJQ failed: %v", err)
							}

							// 3) Extract and deref the root schema from both outputs, then compare semantically
							inlineDoc, err := yamlToMap(inlineOut)
							if err != nil {
								t.Fatalf("failed to parse inline output YAML: %v", err)
							}
							extDocOut, err := yamlToMap(extOut)
							if err != nil {
								t.Fatalf("failed to parse externalized output YAML: %v", err)
							}

							inlineRoot, err := getRootSchema(inlineDoc, c.root)
							if err != nil {
								t.Fatalf("inline output missing root schema %q: %v", c.root, err)
							}
							extRoot, err := getRootSchema(extDocOut, c.root)
							if err != nil {
								t.Fatalf("externalized output missing root schema %q: %v", c.root, err)
							}

							inlineDeref := derefLocal(inlineDoc, inlineRoot, make(map[string]bool))
							extDeref := derefLocal(extDocOut, extRoot, make(map[string]bool))

							normInline := normalizeForCompare(inlineDeref)
							normExt := normalizeForCompare(extDeref)

							if !reflect.DeepEqual(normInline, normExt) {
								// Provide a compact pointer diff
								var diffs []string
								collectDiffPointers(normInline, normExt, "", &diffs, 3)
								t.Fatalf("semantic mismatch after deref; percent=%.2f seed=%d; first diffs=%v", p, seed, diffs)
							}

							// 4) Assert no "Top-like" empty schemas in the deref'd root
							if tops := findTopLikeSchemas(extDeref, ""); len(tops) > 0 {
								t.Fatalf("found Top-like empty schemas at %v (percent=%.2f, seed=%d)", tops, p, seed)
							}
						})
					}
				})
			}
		})
	}
}

// externalizeSchemas walks components/schemas and replaces inline array "items" with $ref
// to new component schemas under components/schemas, controlled by a probability "percent".
// Deterministic names:
//   - If item is a simple string schema: ExternalString
//   - Otherwise: External<SchemaName><PropOrContext>Item
// Only local internal $refs are produced. No ref-chains or combinators in this first iteration.
func externalizeSchemas(doc map[string]any, percent float64, seed int64) (map[string]any, int, error) {
	if percent < 0 || percent > 1 {
		return nil, 0, fmt.Errorf("percent must be in [0,1], got %v", percent)
	}
	docCopy, err := deepCopy(doc)
	if err != nil {
		return nil, 0, err
	}

	components := upsertMap(docCopy, "components")
	schemas := upsertMap(components, "schemas")

	rng := rand.New(rand.NewSource(seed))
	used := make(map[string]struct{})
	// record existing names to avoid clashes
	for k := range schemas {
		used[k] = struct{}{}
	}

	total := 0

	for schemaName, v := range schemas {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		c := extWalkSchemaForItems(m, docCopy, schemas, used, rng, percent, fmt.Sprintf("%s", schemaName), "")
		total += c
		schemas[schemaName] = m
	}

	return docCopy, total, nil
}

func extWalkSchemaForItems(
	schema map[string]any,
	doc map[string]any,
	allSchemas map[string]any,
	used map[string]struct{},
	rng *rand.Rand,
	percent float64,
	rootName string,
	ctx string,
) int {
	count := 0

	// If array or has items, consider externalizing items
	if _, isArr := schema["type"]; isArr && strEq(schema["type"], "array") || schema["items"] != nil {
		if itm, ok := schema["items"].(map[string]any); ok {
			// skip if already a $ref
			if _, hasRef := itm["$ref"]; !hasRef {
				if shouldPick(rng, percent) {
					// derive name
					name := suggestComponentNameForItems(itm, rootName, ctx)
					name = ensureUniqueName(name, used)

					// add to components/schemas
					allSchemas[name] = mustDeepCopy(itm)

					// replace items with $ref
					schema["items"] = map[string]any{
						"$ref": "#/components/schemas/" + name,
					}
					count++
				} else {
					// Recurse into items (if we didn't externalize it)
					count += extWalkSchemaForItems(itm, doc, allSchemas, used, rng, percent, rootName, ctx+"Items")
				}
			}
		}
	}

	// properties
	if p, ok := schema["properties"].(map[string]any); ok {
		keys := make([]string, 0, len(p))
		for k := range p {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if sub, ok := p[k].(map[string]any); ok {
				count += extWalkSchemaForItems(sub, doc, allSchemas, used, rng, percent, rootName, k)
			}
		}
	}

	// additionalProperties as schema
	if ap, ok := schema["additionalProperties"].(map[string]any); ok {
		count += extWalkSchemaForItems(ap, doc, allSchemas, used, rng, percent, rootName, ctx+"AdditionalProperties")
	}

	// conditionals
	for _, key := range []string{"if", "then", "else", "not"} {
		if sub, ok := schema[key].(map[string]any); ok {
			count += extWalkSchemaForItems(sub, doc, allSchemas, used, rng, percent, rootName, ctx+strings.Title(key))
		}
	}

	// combinators
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if arr, ok := schema[key].([]any); ok {
			for i := range arr {
				if m, ok := arr[i].(map[string]any); ok {
					count += extWalkSchemaForItems(m, doc, allSchemas, used, rng, percent, rootName, fmt.Sprintf("%s%d", key, i))
				}
			}
		}
	}

	// prefixItems (tuples) - ignored for now per first-iteration scope

	return count
}

func shouldPick(rng *rand.Rand, percent float64) bool {
	if percent >= 1.0 {
		return true
	}
	if percent <= 0.0 {
		return false
	}
	return rng.Float64() < percent
}

func suggestComponentNameForItems(item map[string]any, rootName, ctx string) string {
	// Simple type-only string -> ExternalString
	if strEq(item["type"], "string") && len(item) == 1 {
		return "ExternalString"
	}
	// Otherwise External<Root><Ctx>Item
	root := toCamel(rootName)
	context := toCamel(ctx)
	if context == "" {
		context = "Item"
	} else {
		context = context + "Item"
	}
	return "External" + root + context
}

func ensureUniqueName(name string, used map[string]struct{}) string {
	base := name
	i := 2
	for {
		if _, ok := used[name]; !ok {
			used[name] = struct{}{}
			return name
		}
		name = fmt.Sprintf("%s%d", base, i)
		i++
	}
}

func toCamel(s string) string {
	if s == "" {
		return ""
	}
	parts := splitTokens(s)
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + strings.ToLower(parts[i][1:])
	}
	return strings.Join(parts, "")
}

func splitTokens(s string) []string {
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)
	return parts
}

func upsertMap(m map[string]any, key string) map[string]any {
	if v, ok := m[key]; ok {
		if mm, ok := v.(map[string]any); ok {
			return mm
		}
	}
	n := make(map[string]any)
	m[key] = n
	return n
}

func strEq(v any, s string) bool {
	if v == nil {
		return false
	}
	switch vv := v.(type) {
	case string:
		return vv == s
	case []any:
		// OAS 3.1 allows array type
		for _, e := range vv {
			if es, ok := e.(string); ok && es == s {
				return true
			}
		}
	}
	return false
}

func mustDeepCopy(v any) any {
	cp, err := deepCopy(v)
	if err != nil {
		panic(err)
	}
	return cp
}

func deepCopy[T any](v T) (T, error) {
	var out T
	b, err := yaml.Marshal(v)
	if err != nil {
		return out, err
	}
	err = yaml.Unmarshal(b, &out)
	return out, err
}

func yamlToMap(s string) (map[string]any, error) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

func mapToYAML(m map[string]any) (string, error) {
	b, err := yaml.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func getRootSchema(doc map[string]any, root string) (map[string]any, error) {
	comp, ok := doc["components"].(map[string]any)
	if !ok {
		return nil, errors.New("no components")
	}
	schemas, ok := comp["schemas"].(map[string]any)
	if !ok {
		return nil, errors.New("no components.schemas")
	}
	raw, ok := schemas[root]
	if !ok {
		return nil, fmt.Errorf("components.schemas.%s not found", root)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("components.schemas.%s is not an object", root)
	}
	return m, nil
}

// derefLocal fully dereferences internal component $refs within the provided schema m,
// returning a new structure without $ref nodes (only internal #/components/schemas/... are supported).
func derefLocal(doc map[string]any, m map[string]any, visiting map[string]bool) map[string]any {
	cp := mustDeepCopy(m).(map[string]any)

	// If this node is a $ref wrapper, inline it
	if ref, ok := cp["$ref"].(string); ok {
		target := resolveLocalRef(doc, ref)
		if target != nil {
			return derefLocal(doc, target, visiting)
		}
		// Unknown/unsupported ref; leave as-is
		return cp
	}

	// Recurse properties
	if p, ok := cp["properties"].(map[string]any); ok {
		for k, v := range p {
			if mm, ok := v.(map[string]any); ok {
				p[k] = derefLocal(doc, mm, visiting)
			}
		}
	}

	// items
	if it, ok := cp["items"].(map[string]any); ok {
		cp["items"] = derefLocal(doc, it, visiting)
	}

	// additionalProperties schema
	if ap, ok := cp["additionalProperties"].(map[string]any); ok {
		cp["additionalProperties"] = derefLocal(doc, ap, visiting)
	}

	// combinators
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if arr, ok := cp[key].([]any); ok {
			for i, it := range arr {
				if mm, ok := it.(map[string]any); ok {
					arr[i] = derefLocal(doc, mm, visiting)
				}
			}
		}
	}

	// conditionals
	for _, key := range []string{"if", "then", "else", "not"} {
		if mm, ok := cp[key].(map[string]any); ok {
			cp[key] = derefLocal(doc, mm, visiting)
		}
	}

	// prefixItems ignored in this first iteration

	return cp
}

func resolveLocalRef(doc map[string]any, ref string) map[string]any {
	if !strings.HasPrefix(ref, "#/components/schemas/") {
		return nil
	}
	name := strings.TrimPrefix(ref, "#/components/schemas/")
	comp, ok := doc["components"].(map[string]any)
	if !ok {
		return nil
	}
	schemas, ok := comp["schemas"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := schemas[name]
	if !ok {
		return nil
	}
	m, _ := raw.(map[string]any)
	return m
}

// normalizeForCompare canonicalizes values for deep equality:
// - Recursively normalizes maps and arrays
// - Sorts arrays for keys {required, enum, oneOf, anyOf, allOf} where order is not semantically relevant
func normalizeForCompare(v any) any {
	return normalizeWithPath(v, nil)
}

func normalizeWithPath(v any, path []string) any {
	switch vv := v.(type) {
	case map[string]any:
		// normalize keys to string->any and recurse
		// IMPORTANT: Skip x-* extension properties (like x-speakeasy-transform-from-api)
		// These are metadata/directives, not part of the schema structure
		out := make(map[string]any, len(vv))
		for k, val := range vv {
			if strings.HasPrefix(k, "x-") {
				continue
			}
			out[k] = normalizeWithPath(val, append(path, k))
		}
		return out
	case []any:
		// detect key context
		key := ""
		if len(path) > 0 {
			key = path[len(path)-1]
		}
		out := make([]any, 0, len(vv))
		for _, it := range vv {
			out = append(out, normalizeWithPath(it, path))
		}
		if key == "required" || key == "enum" || key == "oneOf" || key == "anyOf" || key == "allOf" {
			sort.Slice(out, func(i, j int) bool {
				return canonicalString(out[i]) < canonicalString(out[j])
			})
		}
		return out
	default:
		return vv
	}
}

// canonicalString produces a deterministic string for any normalized value.
// For maps, keys are ordered; for arrays, items are in-order already or sorted by caller.
func canonicalString(v any) string {
	switch vv := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(vv))
		for k := range vv {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteString("{")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(k)
			b.WriteString(":")
			b.WriteString(canonicalString(vv[k]))
		}
		b.WriteString("}")
		return b.String()
	case []any:
		var b strings.Builder
		b.WriteString("[")
		for i, it := range vv {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(canonicalString(it))
		}
		b.WriteString("]")
		return b.String()
	default:
		// Use JSON to get stable scalars rendering; fallback to fmt otherwise
		js, err := json.Marshal(vv)
		if err == nil {
			return string(js)
		}
		return fmt.Sprintf("%v", vv)
	}
}

// collectDiffPointers gathers up to 'limit' JSON-pointer-like paths where a and b differ.
func collectDiffPointers(a, b any, path string, acc *[]string, limit int) {
	if len(*acc) >= limit {
		return
	}
	if reflect.DeepEqual(a, b) {
		return
	}
	switch aa := a.(type) {
	case map[string]any:
		bb, ok := b.(map[string]any)
		if !ok {
			*acc = append(*acc, pathOrRoot(path))
			return
		}
		// union of keys
		keys := map[string]struct{}{}
		for k := range aa {
			keys[k] = struct{}{}
		}
		for k := range bb {
			keys[k] = struct{}{}
		}
		ks := make([]string, 0, len(keys))
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			if len(*acc) >= limit {
				return
			}
			collectDiffPointers(aa[k], bb[k], path+"/"+escapeJSONPointer(k), acc, limit)
		}
	case []any:
		bb, ok := b.([]any)
		if !ok {
			*acc = append(*acc, pathOrRoot(path))
			return
		}
		n := len(aa)
		if len(bb) > n {
			n = len(bb)
		}
		for i := 0; i < n; i++ {
			if len(*acc) >= limit {
				return
			}
			var av, bv any
			if i < len(aa) {
				av = aa[i]
			}
			if i < len(bb) {
				bv = bb[i]
			}
			collectDiffPointers(av, bv, fmt.Sprintf("%s/%d", path, i), acc, limit)
		}
	default:
		*acc = append(*acc, pathOrRoot(path))
	}
}

func escapeJSONPointer(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	s = strings.ReplaceAll(s, "/", "~1")
	return s
}

func pathOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// findTopLikeSchemas traverses a dereferenced schema tree and returns paths where a node
// appears "Top-like": no $ref, none of type/properties/items/combinators/not/enum/const present.
func findTopLikeSchemas(v any, path string) []string {
	var out []string
	m, ok := v.(map[string]any)
	if !ok {
		return out
	}
	// If $ref present, not Top-like (and deref should've removed these anyway)
	if _, hasRef := m["$ref"]; !hasRef {
		hasSpecificity := false
		for _, k := range []string{"type", "properties", "items", "oneOf", "anyOf", "allOf", "not", "enum", "const"} {
			if _, ok := m[k]; ok {
				hasSpecificity = true
				break
			}
		}
		if !hasSpecificity {
			out = append(out, pathOrRoot(path))
		}
	}

	// Recurse
	if p, ok := m["properties"].(map[string]any); ok {
		for k, sub := range p {
			out = append(out, findTopLikeSchemas(sub, path+"/"+escapeJSONPointer("properties")+"/"+escapeJSONPointer(k))...)
		}
	}
	if it, ok := m["items"].(map[string]any); ok {
		out = append(out, findTopLikeSchemas(it, path+"/"+escapeJSONPointer("items"))...)
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		if arr, ok := m[key].([]any); ok {
			for i, it := range arr {
				out = append(out, findTopLikeSchemas(it, fmt.Sprintf("%s/%s/%d", path, escapeJSONPointer(key), i))...)
			}
		}
	}
	for _, key := range []string{"if", "then", "else", "not"} {
		if sub, ok := m[key].(map[string]any); ok {
			out = append(out, findTopLikeSchemas(sub, path+"/"+escapeJSONPointer(key))...)
		}
	}
	if ap, ok := m["additionalProperties"].(map[string]any); ok {
		out = append(out, findTopLikeSchemas(ap, path+"/"+escapeJSONPointer("additionalProperties"))...)
	}
	return out
}
