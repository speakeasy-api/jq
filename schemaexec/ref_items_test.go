package schemaexec

import (
	"bytes"
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/openapi"
)

// refItemsDoc is a minimal OpenAPI document where an array property's items
// are declared via $ref. After openapi.ResolveAllReferences, the $ref wrapper
// carries its resolution in wrapper-level caches (GetResolvedSchema), NOT in
// the inline Left schema (which remains a bare "$ref shell" with only Ref set).
//
// This mirrors how resolved real-world documents deliver list responses:
// `items: {type: array, items: {$ref: '#/components/schemas/Item'}}`.
const refItemsDoc = `openapi: 3.1.0
info:
  title: ref-items repro
  version: 1.0.0
paths: {}
components:
  schemas:
    Item:
      type: object
      properties:
        name:
          type: string
        count:
          type: integer
      required: [name]
    List:
      type: object
      properties:
        items:
          type: array
          items:
            $ref: '#/components/schemas/Item'
        owner:
          $ref: '#/components/schemas/Item'
      required: [items]
`

// loadComponentSchema unmarshals an OpenAPI document, resolves all references,
// and returns the named component schema.
func loadComponentSchema(t *testing.T, doc, name string) *oas3.Schema {
	t.Helper()
	ctx := context.Background()
	d, validationErrs, err := openapi.Unmarshal(ctx, bytes.NewReader([]byte(doc)))
	if err != nil {
		t.Fatalf("failed to unmarshal document: %v", err)
	}
	if len(validationErrs) > 0 {
		t.Fatalf("document validation errors: %v", validationErrs)
	}
	if _, err := d.ResolveAllReferences(ctx, openapi.ResolveAllOptions{}); err != nil {
		t.Fatalf("failed to resolve references: %v", err)
	}
	js, ok := d.Components.Schemas.Get(name)
	if !ok {
		t.Fatalf("no component schema %q", name)
	}
	s := js.GetLeft()
	if s == nil {
		t.Fatalf("component schema %q is not an inline schema", name)
	}
	return s
}

// runQuery executes a jq expression symbolically against the input schema.
func runQuery(t *testing.T, expr string, input *oas3.Schema) *oas3.Schema {
	t.Helper()
	q, err := gojq.Parse(expr)
	if err != nil {
		t.Fatalf("failed to parse %q: %v", expr, err)
	}
	res, err := RunSchema(context.Background(), q, input)
	if err != nil {
		t.Fatalf("RunSchema(%q) failed: %v", expr, err)
	}
	return res.Schema
}

// TestRefItems_IterationThroughRefItems: `.items[].name` must resolve the
// $ref'd item schema and yield string, not Top.
func TestRefItems_IterationThroughRefItems(t *testing.T) {
	list := loadComponentSchema(t, refItemsDoc, "List")
	out := runQuery(t, ".items[].name", list)
	if got := getType(out); got != "string" {
		t.Errorf(".items[].name: expected string, got %q (schema: %s)", got, schemaTypeSummary(out, 2))
	}
}

// TestRefItems_ExplicitPipeThroughRefItems: `.items[] | .name` is the same
// program shape jq's map() desugars to.
func TestRefItems_ExplicitPipeThroughRefItems(t *testing.T) {
	list := loadComponentSchema(t, refItemsDoc, "List")
	out := runQuery(t, ".items[] | .name", list)
	if got := getType(out); got != "string" {
		t.Errorf(".items[] | .name: expected string, got %q (schema: %s)", got, schemaTypeSummary(out, 2))
	}
}

// TestRefItems_IndexThroughRefItems: `.items[0].name` must resolve the $ref'd
// item schema. jq semantics: index out of range yields null, so string∪null is
// also acceptable; Top is not.
func TestRefItems_IndexThroughRefItems(t *testing.T) {
	list := loadComponentSchema(t, refItemsDoc, "List")
	out := runQuery(t, ".items[0].name", list)
	if out == nil {
		t.Fatal(".items[0].name: got Bottom")
	}
	if isTopSchema(out) {
		t.Fatalf(".items[0].name: got Top, want string (or string|null)")
	}
	if !MightBeString(out) {
		t.Errorf(".items[0].name: expected a string-compatible schema, got %s", schemaTypeSummary(out, 2))
	}
}

// TestRefItems_BareIndexThroughRefItems: `.items[0]` must produce the resolved
// Item object schema, not the bare $ref shell (which reads as Top).
func TestRefItems_BareIndexThroughRefItems(t *testing.T) {
	list := loadComponentSchema(t, refItemsDoc, "List")
	out := runQuery(t, ".items[0]", list)
	if out == nil {
		t.Fatal(".items[0]: got Bottom")
	}
	if isTopSchema(out) {
		t.Fatalf(".items[0]: got Top, want the resolved Item object schema")
	}
	if !MightBeObject(out) {
		t.Errorf(".items[0]: expected an object-compatible schema, got %s", schemaTypeSummary(out, 2))
	}
}

// TestRefItems_MapHasTypedItems: `.items | map(.name)` must produce an array
// whose ITEMS are typed — not just an array shell with Top items.
func TestRefItems_MapHasTypedItems(t *testing.T) {
	list := loadComponentSchema(t, refItemsDoc, "List")
	out := runQuery(t, ".items | map(.name)", list)
	if got := getType(out); got != "array" {
		t.Fatalf(".items | map(.name): expected array, got %q", got)
	}
	if out.Items == nil || out.Items.Left == nil {
		t.Fatal(".items | map(.name): array has no items schema")
	}
	if got := getType(out.Items.Left); got != "string" {
		t.Errorf(".items | map(.name): expected string items, got %q (items: %s)",
			got, schemaTypeSummary(out.Items.Left, 2))
	}
}

// TestRefItems_PropertyRefStillWorks: property-level $refs (the reference
// behavior) must keep resolving: `.owner.name` → string.
func TestRefItems_PropertyRefStillWorks(t *testing.T) {
	list := loadComponentSchema(t, refItemsDoc, "List")
	out := runQuery(t, ".owner.name", list)
	// owner is optional → jq may yield null for a missing key; string∪null ok.
	if out == nil {
		t.Fatal(".owner.name: got Bottom")
	}
	if isTopSchema(out) {
		t.Fatalf(".owner.name: got Top, want string-compatible schema")
	}
	if !MightBeString(out) {
		t.Errorf(".owner.name: expected string-compatible schema, got %s", schemaTypeSummary(out, 2))
	}
}
