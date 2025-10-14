package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// Test for regression: pagination flattening transformation
func TestPaginationTransform(t *testing.T) {
	ctx := context.Background()

	// Build input schema: {data: {items: array<{id, title, active}>, pagination: {nextCursor, total}}}
	itemSchema := BuildObject(map[string]*oas3.Schema{
		"id":     StringType(),
		"title":  StringType(),
		"active": BoolType(),
	}, []string{})

	paginationSchema := BuildObject(map[string]*oas3.Schema{
		"nextCursor": func() *oas3.Schema {
			s := StringType()
			nullable := true
			s.Nullable = &nullable
			return s
		}(),
		"total": IntegerType(),
	}, []string{})

	dataSchema := BuildObject(map[string]*oas3.Schema{
		"items":      ArrayType(itemSchema),
		"pagination": paginationSchema,
	}, []string{})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"data": dataSchema,
	}, []string{})

	// JQ transform
	jqExpr := `{
		items: (.data.items // []) | map({
			id: .id,
			title: .title,
			status: (if (.active // false) then "active" else "inactive" end)
		}),
		hasMore: (.data.pagination.nextCursor != null),
		total: (.data.pagination.total // 0),
		nextCursor: (.data.pagination.nextCursor // null)
	}`

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	result, err := RunSchema(ctx, query, inputSchema)
	if err != nil {
		t.Fatalf("Symbolic execution error: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Expected output schema should have:
	// - items: array of {id: string, title: string, status: string}
	// - hasMore: boolean
	// - total: integer
	// - nextCursor: string | null

	// Check root is object
	rootType := getType(result.Schema)
	if rootType != "object" {
		t.Errorf("Expected root type=object, got %s", rootType)
	}

	// Check properties exist
	if result.Schema.Properties == nil {
		t.Fatal("Result schema has no properties")
	}

	// Check items property
	itemsProp, ok := result.Schema.Properties.Get("items")
	if !ok {
		t.Fatal("Result schema missing 'items' property")
	}
	itemsSchema := itemsProp.GetLeft()
	if itemsSchema == nil {
		t.Fatal("items property has no schema")
	}
	if getType(itemsSchema) != "array" {
		t.Errorf("Expected items type=array, got %s", getType(itemsSchema))
	}

	// Check items array element schema
	if itemsSchema.Items == nil {
		t.Fatal("items array has no items schema")
	}
	itemElementSchema := itemsSchema.Items.GetLeft()
	if itemElementSchema == nil {
		t.Fatal("items array element has no schema")
	}
	if getType(itemElementSchema) != "object" {
		t.Errorf("Expected item element type=object, got %s", getType(itemElementSchema))
	}

	// Check item properties: id, title, status
	if itemElementSchema.Properties == nil {
		t.Fatal("Item element has no properties")
	}

	idProp, ok := itemElementSchema.Properties.Get("id")
	if !ok {
		t.Error("Item element missing 'id' property")
	} else {
		idSchema := idProp.GetLeft()
		if getType(idSchema) != "string" {
			t.Errorf("Expected id type=string, got %s", getType(idSchema))
		}
	}

	titleProp, ok := itemElementSchema.Properties.Get("title")
	if !ok {
		t.Error("Item element missing 'title' property")
	} else {
		titleSchema := titleProp.GetLeft()
		if getType(titleSchema) != "string" {
			t.Errorf("Expected title type=string, got %s", getType(titleSchema))
		}
	}

	statusProp, ok := itemElementSchema.Properties.Get("status")
	if !ok {
		t.Error("Item element missing 'status' property")
	} else {
		statusSchema := statusProp.GetLeft()
		if getType(statusSchema) != "string" {
			t.Errorf("Expected status type=string, got %s", getType(statusSchema))
		}
		// Should have enum [active, inactive]
		if statusSchema.Enum == nil || len(statusSchema.Enum) != 2 {
			t.Errorf("Expected status to have enum with 2 values, got %d", len(statusSchema.Enum))
		}
	}

	// Check hasMore property
	hasMoreProp, ok := result.Schema.Properties.Get("hasMore")
	if !ok {
		t.Error("Result schema missing 'hasMore' property")
	} else {
		hasMoreSchema := hasMoreProp.GetLeft()
		if getType(hasMoreSchema) != "boolean" {
			t.Errorf("Expected hasMore type=boolean, got %s", getType(hasMoreSchema))
		}
	}

	// Check total property
	totalProp, ok := result.Schema.Properties.Get("total")
	if !ok {
		t.Error("Result schema missing 'total' property")
	} else {
		totalSchema := totalProp.GetLeft()
		if getType(totalSchema) != "integer" && getType(totalSchema) != "number" {
			t.Errorf("Expected total type=integer or number, got %s", getType(totalSchema))
		}
	}

	// Check nextCursor property
	nextCursorProp, ok := result.Schema.Properties.Get("nextCursor")
	if !ok {
		t.Error("Result schema missing 'nextCursor' property")
	} else {
		nextCursorSchema := nextCursorProp.GetLeft()
		schemaType := getType(nextCursorSchema)
		// Should be string or union of string|null
		if schemaType != "string" && schemaType != "" {
			t.Errorf("Expected nextCursor type=string or union, got %s", schemaType)
		}
	}

	t.Log("Pagination transform test passed")
}
