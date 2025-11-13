package schemaexec

import (
	"context"
	"fmt"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestMapAsVariable is a minimal reproduction of the ClusterLinkCreate bug:
// - map(...) creates an accumulator array
// - as $var stores the result in a variable
// - The variable should contain the non-empty accumulated array
// - But instead it contains an empty array
func TestMapAsVariable(t *testing.T) {
	// Start with an array of objects [{key: string, value: string}]
	entryObject := BuildObject(map[string]*oas3.Schema{
		"key":   StringType(),
		"value": StringType(),
	}, []string{"key", "value"})

	inputSchema := ArrayType(entryObject)

	// JQ expression: map({name: .key, val: .value}) as $items | {items: $items}
	jqExpr := `map({name: .key, val: .value}) as $items | {items: $items}`

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse jq: %v", err)
	}

	opts := DefaultOptions()
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Check the result
	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Should be an object with an "items" property
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	itemsProp, ok := result.Schema.Properties.Get("items")
	if !ok {
		t.Fatal("Missing 'items' property")
	}

	itemsSchema := itemsProp.Left
	if itemsSchema == nil {
		t.Fatal("items property is nil")
	}

	// The items property should be an array
	if getType(itemsSchema) != "array" {
		t.Fatalf("Expected items to be array, got: %s", getType(itemsSchema))
	}

	// CRITICAL: The array should NOT be empty
	isEmpty := itemsSchema.MaxItems != nil && *itemsSchema.MaxItems == 0
	hasItems := itemsSchema.Items != nil && itemsSchema.Items.Left != nil

	if isEmpty {
		t.Error("BUG REPRODUCED: items array is empty (MaxItems=0)")
	}

	if !hasItems {
		t.Error("BUG REPRODUCED: items array has no Items schema")
	}

	if isEmpty || !hasItems {
		t.Logf("Expected: array with items schema")
		t.Logf("Actual: empty=%v, hasItems=%v", isEmpty, hasItems)
		t.FailNow()
	}

	// If we get here, the bug is fixed
	itemType := getType(itemsSchema.Items.Left)
	fmt.Printf("SUCCESS: items is non-empty array with itemType=%s\n", itemType)
}

// TestMapAsVariableWithSortBy tests the same pattern but with sort_by before map
// This more closely mirrors the ClusterLinkCreate case
func TestMapAsVariableWithSortBy(t *testing.T) {
	// Start with an array of objects [{key: string, value: string}]
	entryObject := BuildObject(map[string]*oas3.Schema{
		"key":   StringType(),
		"value": StringType(),
	}, []string{"key", "value"})

	inputSchema := ArrayType(entryObject)

	// JQ expression: sort_by(.key) | map({name: .key, val: .value}) as $items | {items: $items}
	jqExpr := `sort_by(.key) | map({name: .key, val: .value}) as $items | {items: $items}`

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse jq: %v", err)
	}

	opts := DefaultOptions()
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Check the result
	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	itemsProp, ok := result.Schema.Properties.Get("items")
	if !ok {
		t.Fatal("Missing 'items' property")
	}

	itemsSchema := itemsProp.Left
	if itemsSchema == nil {
		t.Fatal("items property is nil")
	}

	if getType(itemsSchema) != "array" {
		t.Fatalf("Expected items to be array, got: %s", getType(itemsSchema))
	}

	// CRITICAL: The array should NOT be empty
	isEmpty := itemsSchema.MaxItems != nil && *itemsSchema.MaxItems == 0
	hasItems := itemsSchema.Items != nil && itemsSchema.Items.Left != nil

	if isEmpty {
		t.Error("BUG REPRODUCED: items array is empty (MaxItems=0) after sort_by")
	}

	if !hasItems {
		t.Error("BUG REPRODUCED: items array has no Items schema after sort_by")
	}

	if isEmpty || !hasItems {
		t.Logf("Expected: array with items schema")
		t.Logf("Actual: empty=%v, hasItems=%v", isEmpty, hasItems)
		t.FailNow()
	}

	// If we get here, the bug is fixed
	itemType := getType(itemsSchema.Items.Left)
	fmt.Printf("SUCCESS: items is non-empty array with itemType=%s (with sort_by)\n", itemType)
}

// TestMapAsVariableWithEmptyFallback tests the ClusterLinkCreate pattern:
// - Start with object that may have a config property
// - Use // {} fallback
// - to_entries on the result
// - map over it
// - store in variable
func TestMapAsVariableWithEmptyFallback(t *testing.T) {
	// Input: object with optional config property
	configObject := BuildObject(map[string]*oas3.Schema{
		"key":   StringType(),
		"value": StringType(),
	}, nil)

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"config": configObject,
	}, nil)

	// JQ expression: (.config // {}) | to_entries | sort_by(.key) | map({name: .key, val: .value}) as $items | {items: $items}
	jqExpr := `(.config // {}) | to_entries | sort_by(.key) | map({name: .key, val: .value}) as $items | {items: $items}`

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse jq: %v", err)
	}

	opts := DefaultOptions()
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Check the result
	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	itemsProp, ok := result.Schema.Properties.Get("items")
	if !ok {
		t.Fatal("Missing 'items' property")
	}

	itemsSchema := itemsProp.Left
	if itemsSchema == nil {
		t.Fatal("items property is nil")
	}

	if getType(itemsSchema) != "array" {
		t.Fatalf("Expected items to be array, got: %s", getType(itemsSchema))
	}

	// CRITICAL: The array should NOT be empty
	isEmpty := itemsSchema.MaxItems != nil && *itemsSchema.MaxItems == 0
	hasItems := itemsSchema.Items != nil && itemsSchema.Items.Left != nil

	if isEmpty {
		t.Error("BUG REPRODUCED: items array is empty (MaxItems=0) with empty fallback")
	}

	if !hasItems {
		t.Error("BUG REPRODUCED: items array has no Items schema with empty fallback")
	}

	if isEmpty || !hasItems {
		t.Logf("Expected: array with items schema")
		t.Logf("Actual: empty=%v, hasItems=%v", isEmpty, hasItems)
		t.FailNow()
	}

	// If we get here, the bug is fixed
	itemType := getType(itemsSchema.Items.Left)
	fmt.Printf("SUCCESS: items is non-empty array with itemType=%s (with empty fallback)\n", itemType)
}

// TestMapAsVariable_MultiState_Merge forces 16 states via 4 booleans
// This exercises frontier merging and tests if as $var works correctly under merge pressure
func TestMapAsVariable_MultiState_Merge(t *testing.T) {
	// Input schema: arr is array<string>, b1..b4 are booleans with unknown truthiness
	arr := ArrayType(StringType())

	boolType := func() *oas3.Schema {
		return &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeBoolean)}
	}

	input := BuildObject(map[string]*oas3.Schema{
		"arr": arr,
		"b1":  boolType(),
		"b2":  boolType(),
		"b3":  boolType(),
		"b4":  boolType(),
	}, nil)

	jqExpr := `
        (
          (if .b1 then .arr else [] end)
          + (if .b2 then .arr else [] end)
          + (if .b3 then .arr else [] end)
          + (if .b4 then .arr else [] end)
        )
        | map(.) as $xs
        | {xs: $xs}
    `

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse jq: %v", err)
	}

	opts := DefaultOptions()
	opts.EnableWarnings = true
	// Keep defaults for MaxDepth; merging triggers once frontier >= 8

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	xsProp, ok := result.Schema.Properties.Get("xs")
	if !ok {
		t.Fatal("Missing 'xs' property")
	}
	xs := xsProp.Left
	if xs == nil {
		t.Fatal("xs property is nil")
	}
	if getType(xs) != "array" {
		t.Fatalf("Expected xs to be array, got: %s", getType(xs))
	}

	isEmpty := xs.MaxItems != nil && *xs.MaxItems == 0
	hasItems := xs.Items != nil && xs.Items.Left != nil

	if isEmpty || !hasItems {
		t.Errorf("BUG REPRODUCED (multi-state): xs array is empty or lacks items; empty=%v hasItems=%v", isEmpty, hasItems)
		t.FailNow()
	}

	itemType := getType(xs.Items.Left)
	fmt.Printf("SUCCESS: xs is non-empty array with itemType=%s (multi-state merge)\n", itemType)
}

// TestMapAsVariable_MultiState_ToEntries mirrors ClusterLinkCreate more closely:
// - (.cfg // {}) | to_entries | map(...)
// - Store in $configs
// - Then fork 4 ways AFTER the as $configs (forces scope merging with $configs in scope)
func TestMapAsVariable_MultiState_ToEntries(t *testing.T) {
	// cfg: object with additionalProperties: string (so to_entries yields array<{key:string,value:string}>)
	cfg := &oas3.Schema{
		Type:                 oas3.NewTypeFromString(oas3.SchemaTypeObject),
		AdditionalProperties: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
	}
	boolType := func() *oas3.Schema {
		return &oas3.Schema{Type: oas3.NewTypeFromString(oas3.SchemaTypeBoolean)}
	}
	input := BuildObject(map[string]*oas3.Schema{
		"cfg": cfg,
		"g1":  boolType(),
		"g2":  boolType(),
		"g3":  boolType(),
		"g4":  boolType(),
	}, nil)

	jqExpr := `
        (
          (.cfg // {})
          | to_entries
          | (if . then . else [] end)
          | sort_by(.key)
          | map({name: .key, value: .value})
        ) as $configs
        | (
            {}
            | (if .g1 then . + {a: 1} else . end)
            | (if .g2 then . + {b: 1} else . end)
            | (if .g3 then . + {c: 1} else . end)
            | (if .g4 then . + {d: 1} else . end)
            | . + {configs: $configs}
          )
    `
	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse jq: %v", err)
	}

	opts := DefaultOptions()
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, input, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}
	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	cfgsProp, ok := result.Schema.Properties.Get("configs")
	if !ok {
		t.Fatal("Missing 'configs' property")
	}
	cfgs := cfgsProp.Left
	if cfgs == nil {
		t.Fatal("configs property is nil")
	}
	if getType(cfgs) != "array" {
		t.Fatalf("Expected configs to be array, got: %s", getType(cfgs))
	}

	isEmpty := cfgs.MaxItems != nil && *cfgs.MaxItems == 0
	hasItems := cfgs.Items != nil && cfgs.Items.Left != nil

	if isEmpty || !hasItems {
		t.Errorf("BUG REPRODUCED (multi-state to_entries): configs array empty or lacks items; empty=%v hasItems=%v", isEmpty, hasItems)
		t.FailNow()
	}

	itemType := getType(cfgs.Items.Left)
	fmt.Printf("SUCCESS: configs is non-empty array with itemType=%s (multi-state to_entries)\n", itemType)
}

// TestReduceToEntriesMap_FAILING tests the exact pattern from ClusterLinkCreate that's failing:
// 1. Start with array of {name: string, value: string}
// 2. Use reduce to build object with dynamic keys: reduce .[] as $c ({}; .[$c.name] = $c.value)
// 3. Call to_entries to convert back to array
// 4. Call map to transform entries
// 5. Store result in variable with as $var
// 6. Use the variable in object construction
//
// This test currently FAILS because:
// - The reduce creates an object, but it loses its additionalProperties type during merging
// - to_entries on the merged object sees no properties and no AP, returns empty array
// - The final map doesn't iterate, so the accumulator stays empty
func TestReduceToEntriesMap_FAILING(t *testing.T) {
	// Input: array of config entries [{name: string, value: string}]
	entryObject := BuildObject(map[string]*oas3.Schema{
		"name":  StringType(),
		"value": StringType(),
	}, []string{"name", "value"})
	inputSchema := ArrayType(entryObject)

	// JQ: Exactly the pattern from ClusterLinkCreate:
	// reduce .[] as $c ({}; .[$c.name] = $c.value) | to_entries | map({name: .key, value: .value}) as $configs | {configs: $configs}
	jqExpr := `reduce .[] as $c ({}; .[$c.name] = $c.value) | to_entries | map({name: .key, value: .value}) as $configs | {configs: $configs}`

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse jq: %v", err)
	}

	opts := DefaultOptions()
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Should be an object with configs property
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	configsProp, ok := result.Schema.Properties.Get("configs")
	if !ok {
		t.Fatal("Missing 'configs' property")
	}

	configs := configsProp.Left
	if configs == nil {
		t.Fatal("configs property is nil")
	}

	if getType(configs) != "array" {
		t.Fatalf("Expected configs to be array, got: %s", getType(configs))
	}

	// CRITICAL: The array should NOT be empty
	isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
	hasItems := configs.Items != nil && configs.Items.Left != nil

	if isEmpty {
		t.Error("❌ BUG REPRODUCED: configs array is empty after reduce → to_entries → map pipeline")
		t.Logf("This is the EXACT bug in ClusterLinkCreate:")
		t.Logf("1. reduce creates object with dynamic keys")
		t.Logf("2. Object loses additionalProperties during merging")
		t.Logf("3. to_entries sees unconstrained object, returns empty array")
		t.Logf("4. map doesn't iterate over empty array")
		t.Logf("5. Final accumulator stays empty")
	}

	if !hasItems {
		t.Error("❌ BUG REPRODUCED: configs array has no Items schema")
	}

	if isEmpty || !hasItems {
		t.Logf("Expected: configs = array<{name: string, value: string}>")
		t.Logf("Actual: empty=%v, hasItems=%v", isEmpty, hasItems)
		t.FailNow()
	}

	// If we get here, the bug is fixed!
	itemType := getType(configs.Items.Left)
	fmt.Printf("✅ SUCCESS: reduce → to_entries → map pipeline works! itemType=%s\n", itemType)
}

// TestMapAsVariable_GPT5Minimal is the exact minimal test case recommended by GPT-5
// to isolate the variable-backed array accumulator bug.
// This is the most direct reproduction: object with additionalProperties → to_entries → map → as $var
func TestMapAsVariable_GPT5Minimal(t *testing.T) {
	// Input: object with additionalProperties: string (symbolic object)
	inputSchema := &oas3.Schema{
		Type:                 oas3.NewTypeFromString(oas3.SchemaTypeObject),
		AdditionalProperties: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType()),
	}

	// JQ: (. | to_entries | map({name: .key, value: (.value | tostring)})) as $configs | { configs: $configs }
	jqExpr := `(. | to_entries | map({name: .key, value: (.value | tostring)})) as $configs | { configs: $configs }`

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		t.Fatalf("Failed to parse jq: %v", err)
	}

	opts := DefaultOptions()
	opts.EnableWarnings = true

	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Assertions
	if result.Schema == nil {
		t.Fatal("Result schema is nil")
	}

	// Should be an object
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	// Should have a "configs" property
	configsProp, ok := result.Schema.Properties.Get("configs")
	if !ok {
		t.Fatal("Missing 'configs' property")
	}

	configs := configsProp.Left
	if configs == nil {
		t.Fatal("configs property is nil")
	}

	// configs should be an array
	if getType(configs) != "array" {
		t.Fatalf("Expected configs to be array, got: %s", getType(configs))
	}

	// CRITICAL: The array should NOT be empty
	isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
	hasItems := configs.Items != nil && configs.Items.Left != nil

	if isEmpty {
		t.Error("BUG REPRODUCED (GPT-5 minimal): configs array is empty (MaxItems=0)")
		t.Logf("This indicates that the variable-backed array accumulator path is broken")
		t.Logf("The canonical array mutations are not reaching the $configs variable")
	}

	if !hasItems {
		t.Error("BUG REPRODUCED (GPT-5 minimal): configs array has no Items schema")
		t.Logf("Items should be object with name:string and value:string")
	}

	if isEmpty || !hasItems {
		t.Logf("Expected: configs = array<{name: string, value: string}>")
		t.Logf("Actual: empty=%v, hasItems=%v", isEmpty, hasItems)

		// Provide diagnostic info
		if configs.MaxItems != nil {
			t.Logf("MaxItems = %d", *configs.MaxItems)
		}
		if configs.Items != nil {
			t.Logf("Items is not nil, Left = %v", configs.Items.Left)
		} else {
			t.Logf("Items is nil")
		}

		t.FailNow()
	}

	// Verify the item schema
	itemSchema := configs.Items.Left
	if getType(itemSchema) != "object" {
		t.Errorf("Expected item to be object, got: %s", getType(itemSchema))
	}

	// Check for name and value properties
	nameProp, hasName := itemSchema.Properties.Get("name")
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasName || nameProp.Left == nil || getType(nameProp.Left) != "string" {
		t.Errorf("Expected name property to be string")
	}

	if !hasValue || valueProp.Left == nil || getType(valueProp.Left) != "string" {
		t.Errorf("Expected value property to be string")
	}

	// If we get here, the bug is fixed!
	fmt.Printf("✓ SUCCESS (GPT-5 minimal): configs is array<{name: string, value: string}>\n")
}
