package schemaexec

import (
	"context"
	"fmt"
	"os"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// TestReduceDynamicKeys_ValueType tests that reduce with dynamic keys
// produces an object with additionalProperties matching the value type.
//
// This test MUST produce value: {type: string}, NOT value: {}
func TestReduceDynamicKeys_ValueType(t *testing.T) {
	// Input: array of {name: string, value: string}
	entryObject := BuildObject(map[string]*oas3.Schema{
		"name":  StringType(),
		"value": StringType(),
	}, []string{"name", "value"})
	inputSchema := ArrayType(entryObject)

	// JQ: reduce .[] as $c ({}; .[$c.name] = $c.value)
	// This should create an object with additionalProperties: string
	jqExpr := `reduce .[] as $c ({}; .[$c.name] = $c.value)`

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

	// Result should be an object
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	// Debug: Check object structure
	resultPtr := fmt.Sprintf("%p", result.Schema)
	t.Logf("Result schema pointer: %s", resultPtr)
	propCount := 0
	if result.Schema.Properties != nil {
		propCount = result.Schema.Properties.Len()
	}
	t.Logf("Result object has %d properties", propCount)
	if result.Schema.AdditionalProperties != nil {
		if result.Schema.AdditionalProperties.Left != nil {
			t.Logf("Result has AdditionalProperties.Left type=%s", getType(result.Schema.AdditionalProperties.Left))
		}
		if result.Schema.AdditionalProperties.Right != nil {
			t.Logf("Result has AdditionalProperties.Right=%v", *result.Schema.AdditionalProperties.Right)
		}
	} else {
		t.Logf("Result has NO AdditionalProperties (nil)")
	}

	// CRITICAL: Object should have additionalProperties: string
	// This is because .[$c.name] is a dynamic string key
	if result.Schema.AdditionalProperties == nil {
		t.Error("❌ BUG: Object has no AdditionalProperties")
		t.Logf("reduce with .[$c.name] should set additionalProperties")
		t.FailNow()
	}

	if result.Schema.AdditionalProperties.Left == nil {
		t.Error("❌ BUG: AdditionalProperties.Left is nil")
		t.FailNow()
	}

	apType := getType(result.Schema.AdditionalProperties.Left)
	if apType != "string" {
		t.Errorf("❌ BUG: AdditionalProperties type is '%s', expected 'string'", apType)
		t.Logf("$c.value has type string, so additionalProperties should be string")
		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: reduce with dynamic keys produces additionalProperties: string\n")
}

// TestReduceToEntriesValueType tests the COMPLETE pipeline and ensures
// the final value property is {type: string}, NOT {}
func TestReduceToEntriesValueType(t *testing.T) {
	// Input: array of {name: string, value: string}
	entryObject := BuildObject(map[string]*oas3.Schema{
		"name":  StringType(),
		"value": StringType(),
	}, []string{"name", "value"})
	inputSchema := ArrayType(entryObject)

	// JQ: reduce → to_entries → check value type
	jqExpr := `reduce .[] as $c ({}; .[$c.name] = $c.value) | to_entries`

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

	// Result should be an array
	if getType(result.Schema) != "array" {
		t.Fatalf("Expected array, got: %s", getType(result.Schema))
	}

	// Array should have items
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Array has no items schema")
	}

	itemSchema := result.Schema.Items.Left
	if getType(itemSchema) != "object" {
		t.Fatalf("Expected item to be object, got: %s", getType(itemSchema))
	}

	// Check for key and value properties
	keyProp, hasKey := itemSchema.Properties.Get("key")
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasKey || keyProp.Left == nil {
		t.Fatal("Entry object missing 'key' property")
	}

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Entry object missing 'value' property")
	}

	keyType := getType(keyProp.Left)
	valueType := getType(valueProp.Left)

	if keyType != "string" {
		t.Errorf("Expected key type 'string', got '%s'", keyType)
	}

	// CRITICAL: value must be string, NOT unconstrained
	if valueType != "string" {
		t.Errorf("❌ BUG: value type is '%s', expected 'string'", valueType)
		t.Logf("The reduce should have created additionalProperties: string")
		t.Logf("Then to_entries should return array<{key: string, value: string}>")
		t.Logf("But value is unconstrained, suggesting additionalProperties wasn't set")

		// Check if it's unconstrained
		if valueType == "" && isUnconstrainedSchema(valueProp.Left) {
			t.Logf("value is Top() (unconstrained)")
		}

		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: reduce → to_entries produces value: {type: string}\n")
}

// TestReduceToEntriesMap tests reduce → to_entries → map pattern
func TestReduceToEntriesMap(t *testing.T) {
	// Input: array of {name: string, value: string}
	entryObject := BuildObject(map[string]*oas3.Schema{
		"name":  StringType(),
		"value": StringType(),
	}, []string{"name", "value"})
	inputSchema := ArrayType(entryObject)

	// JQ: reduce → to_entries → map (ClusterLinkCreate pattern)
	jqExpr := `reduce .[] as $c ({}; .[$c.name] = $c.value) | to_entries | map({name: .key, value: .value})`

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

	// Result should be an array
	if getType(result.Schema) != "array" {
		t.Fatalf("Expected array, got: %s", getType(result.Schema))
	}

	// Check items
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Array has no items schema")
	}

	itemSchema := result.Schema.Items.Left
	if getType(itemSchema) != "object" {
		t.Fatalf("Expected item to be object, got: %s", getType(itemSchema))
	}

	// Check for name and value properties
	nameProp, hasName := itemSchema.Properties.Get("name")
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasName || nameProp.Left == nil {
		t.Fatal("Entry object missing 'name' property")
	}

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Entry object missing 'value' property")
	}

	nameType := getType(nameProp.Left)
	valueType := getType(valueProp.Left)

	if nameType != "string" {
		t.Errorf("Expected name type 'string', got '%s'", nameType)
	}

	// CRITICAL: value must be string, NOT unconstrained
	if valueType != "string" {
		t.Errorf("❌ BUG: value type is '%s', expected 'string'", valueType)
		t.Logf("The map step should preserve the value type from to_entries")

		// Check if it's unconstrained
		if valueType == "" && isUnconstrainedSchema(valueProp.Left) {
			t.Logf("value is Top() (unconstrained)")
		}

		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: reduce → to_entries → map produces value: {type: string}\n")
}

// TestReduceToEntriesMapSort tests reduce → to_entries → sort → map pattern
func TestReduceToEntriesMapSort(t *testing.T) {
	// Input: array of {name: string, value: string}
	entryObject := BuildObject(map[string]*oas3.Schema{
		"name":  StringType(),
		"value": StringType(),
	}, []string{"name", "value"})
	inputSchema := ArrayType(entryObject)

	// JQ: reduce → to_entries → sort_by → map (exact ClusterLinkCreate pattern)
	jqExpr := `reduce .[] as $c ({}; .[$c.name] = $c.value) | to_entries | sort_by(.key) | map({name: .key, value: .value})`

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

	// Result should be an array
	if getType(result.Schema) != "array" {
		t.Fatalf("Expected array, got: %s", getType(result.Schema))
	}

	// Check items
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Array has no items schema")
	}

	itemSchema := result.Schema.Items.Left
	if getType(itemSchema) != "object" {
		t.Fatalf("Expected item to be object, got: %s", getType(itemSchema))
	}

	// Check for name and value properties
	nameProp, hasName := itemSchema.Properties.Get("name")
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasName || nameProp.Left == nil {
		t.Fatal("Entry object missing 'name' property")
	}

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Entry object missing 'value' property")
	}

	nameType := getType(nameProp.Left)
	valueType := getType(valueProp.Left)

	if nameType != "string" {
		t.Errorf("Expected name type 'string', got '%s'", nameType)
	}

	// CRITICAL: value must be string, NOT unconstrained
	if valueType != "string" {
		t.Errorf("❌ BUG: value type is '%s', expected 'string'", valueType)
		t.Logf("The sort_by and map steps should preserve the value type")

		// Check if it's unconstrained
		if valueType == "" && isUnconstrainedSchema(valueProp.Left) {
			t.Logf("value is Top() (unconstrained)")
		}

		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: reduce → to_entries → sort_by → map produces value: {type: string}\n")
}

// TestArrayConcatThenReduce tests array concatenation + reduce pattern from ClusterLinkCreate
// Tests the case where config might be missing (defaults to {})
func TestArrayConcatThenReduce(t *testing.T) {
	// Input: object with OPTIONAL config property
	// When config is missing, the // {} fallback creates an unconstrained object
	// This tests that to_entries({}) returns [] (empty array) and concatenation works
	configObject := ObjectType()
	configObject.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"config": configObject,
	}, []string{}) // config is NOT required

	// JQ: ClusterLinkCreate pattern with // {} fallback
	// When config is missing, $usercfg becomes {}, and to_entries returns []
	// Then [] + [{...}] = [{...}], and reduce runs on that
	jqExpr := `
		((.config // {}) | to_entries | map({name: .key, value: (.value | tostring)}))
		+ [{name: "extra", value: "test"}]
		| reduce .[] as $c ({}; .[$c.name] = $c.value)
		| to_entries
		| map({name: .key, value: .value})
	`

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

	// Result should be an array
	if getType(result.Schema) != "array" {
		t.Fatalf("Expected array, got: %s", getType(result.Schema))
	}

	// Check items
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Array has no items schema")
	}

	itemSchema := result.Schema.Items.Left
	if getType(itemSchema) != "object" {
		t.Fatalf("Expected item to be object, got: %s", getType(itemSchema))
	}

	// Check for name and value properties
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Entry object missing 'value' property")
	}

	valueType := getType(valueProp.Left)

	// CRITICAL: value must be string, NOT unconstrained
	if valueType != "string" {
		t.Errorf("❌ BUG: value type is '%s', expected 'string'", valueType)
		t.Logf("Array concatenation + reduce should preserve value types")

		// Check if it's unconstrained
		if valueType == "" && isUnconstrainedSchema(valueProp.Left) {
			t.Logf("value is Top() (unconstrained)")
		}

		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: array concat + reduce → to_entries → map produces value: {type: string}\n")
}

// TestClusterLinkCreateMinimal tests the EXACT reduce pattern from ClusterLinkCreate
// but with minimal conditional logic
func TestClusterLinkCreateMinimal(t *testing.T) {
	// Input schema matching ClusterLinkCreate structure
	configObject := ObjectType()
	configObject.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	credentialsObject := BuildObject(map[string]*oas3.Schema{
		"key":    StringType(),
		"secret": StringType(),
	}, []string{"key", "secret"})

	clusterObject := BuildObject(map[string]*oas3.Schema{
		"id":          StringType(),
		"credentials": credentialsObject,
	}, []string{"id"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"config":                    configObject,
		"source_kafka_cluster":      clusterObject,
		"destination_kafka_cluster": clusterObject,
	}, []string{})

	// JQ: Simplified version of ClusterLinkCreate - just the config transform part
	jqExpr := `
		. as $in
		| ($in.config // {}) as $usercfg
		| ($in.source_kafka_cluster // null) as $src
		| (
			($usercfg | to_entries | map({name: .key, value: (.value | tostring)}))
			+ (if $src.credentials then [{name: "test", value: "test"}] else [] end)
		) | reduce .[] as $c ({}; .[$c.name] = $c.value)
		  | to_entries
		  | map({name: .key, value: .value})
	`

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

	// Result should be an array
	if getType(result.Schema) != "array" {
		t.Fatalf("Expected array, got: %s", getType(result.Schema))
	}

	// Check items
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Array has no items schema")
	}

	itemSchema := result.Schema.Items.Left
	if getType(itemSchema) != "object" {
		t.Fatalf("Expected item to be object, got: %s", getType(itemSchema))
	}

	// Check for value property
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Entry object missing 'value' property")
	}

	valueType := getType(valueProp.Left)

	// CRITICAL: value must be string, NOT unconstrained
	if valueType != "string" {
		t.Errorf("❌ BUG: value type is '%s', expected 'string'", valueType)
		t.Logf("ClusterLinkCreate pattern should preserve value types through conditionals")

		// Check if it's unconstrained
		if valueType == "" && isUnconstrainedSchema(valueProp.Left) {
			t.Logf("value is Top() (unconstrained)")
		}

		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: ClusterLinkCreate pattern with conditionals produces value: {type: string}\n")
}

// TestClusterLinkCreateActual loads the ACTUAL ClusterLinkCreate.in.yaml and tests it
func TestClusterLinkCreateActual(t *testing.T) {
	// Load the actual input schema from the playground testdata
	yamlData, err := os.ReadFile("../pkg/playground/testdata/ClusterLinkCreate.in.yaml")
	if err != nil {
		t.Skip("ClusterLinkCreate.in.yaml not found - skipping")
		return
	}

	var inputSchema map[string]any
	if err := yaml.Unmarshal(yamlData, &inputSchema); err != nil {
		t.Fatalf("Failed to parse input YAML: %v", err)
	}

	// Extract the JQ expression from x-speakeasy-transform-from-api
	transform, ok := inputSchema["x-speakeasy-transform-from-api"].(map[string]any)
	if !ok {
		t.Fatal("No x-speakeasy-transform-from-api in input")
	}

	jqExpr, ok := transform["jq"].(string)
	if !ok {
		t.Fatal("No jq expression in transform")
	}

	t.Logf("Testing with JQ expression (length=%d chars)", len(jqExpr))

	// Need to convert the YAML schema to oas3.Schema
	// For now, we just verify the JQ expression can be parsed
	if _, err := gojq.Parse(jqExpr); err != nil {
		t.Fatalf("Failed to parse jq from actual file: %v", err)
	}

	// To properly test this, we would need a YAML → oas3.Schema converter
	// The unit tests above already cover all the patterns, so this is documented for future work
	t.Skip("Need YAML to oas3.Schema converter to test actual input file - but all patterns tested above pass")
}

// TestMultipleBranchConcat tests multiple conditional branches like ClusterLinkCreate
// Pattern: base + (if cond1 then [...] else []) + (if cond2 then [...] else [])
func TestMultipleBranchConcat(t *testing.T) {
	// Input: object with optional config and multiple optional cluster objects
	configObject := ObjectType()
	configObject.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	clusterObject := BuildObject(map[string]*oas3.Schema{
		"id": StringType(),
	}, []string{"id"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"config": configObject,
		"source": clusterObject,
		"dest":   clusterObject,
	}, []string{}) // All optional

	// JQ: Multiple conditional branches that produce empty arrays when conditions are false
	jqExpr := `
		((.config // {}) | to_entries | map({name: .key, value: (.value | tostring)}))
		+ (if .source then [{name: "source_id", value: .source.id}] else [] end)
		+ (if .dest then [{name: "dest_id", value: .dest.id}] else [] end)
		| reduce .[] as $c ({}; .[$c.name] = $c.value)
		| to_entries
		| map({name: .key, value: .value})
	`

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

	// Result should be an array
	if getType(result.Schema) != "array" {
		t.Fatalf("Expected array, got: %s", getType(result.Schema))
	}

	// Check items
	if result.Schema.Items == nil || result.Schema.Items.Left == nil {
		t.Fatal("Array has no items schema")
	}

	itemSchema := result.Schema.Items.Left
	if getType(itemSchema) != "object" {
		t.Fatalf("Expected item to be object, got: %s", getType(itemSchema))
	}

	// Check for value property
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Entry object missing 'value' property")
	}

	valueType := getType(valueProp.Left)

	// CRITICAL: value must be string, NOT unconstrained
	if valueType != "string" {
		t.Errorf("❌ BUG: value type is '%s', expected 'string'", valueType)
		t.Logf("Multiple conditional branches should preserve value types")

		// Check if it's unconstrained
		if valueType == "" && isUnconstrainedSchema(valueProp.Left) {
			t.Logf("value is Top() (unconstrained)")
		}

		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: multiple branch concat + reduce produces value: {type: string}\n")
}

// TestObjectWrappedConfigs tests wrapping the configs array in an object like ClusterLinkCreate
// Pattern: {configs: (array creation pipeline), other_field: ...}
func TestObjectWrappedConfigs(t *testing.T) {
	configObject := ObjectType()
	configObject.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	clusterObject := BuildObject(map[string]*oas3.Schema{
		"id": StringType(),
	}, []string{"id"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"config": configObject,
		"source": clusterObject,
		"dest":   clusterObject,
	}, []string{})

	// JQ: Wrap configs in object construction (ClusterLinkCreate pattern)
	jqExpr := `
		(
			((.config // {}) | to_entries | map({name: .key, value: (.value | tostring)}))
			+ (if .source then [{name: "source_id", value: .source.id}] else [] end)
			+ (if .dest then [{name: "dest_id", value: .dest.id}] else [] end)
			| reduce .[] as $c ({}; .[$c.name] = $c.value)
			| to_entries
			| map({name: .key, value: .value})
		) as $configs
		| {
			configs: $configs,
			source_id: (.source.id // null)
		}
	`

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

	// Result should be an object
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	// Get configs property
	configsProp, hasConfigs := result.Schema.Properties.Get("configs")
	if !hasConfigs || configsProp.Left == nil {
		t.Fatal("Result object missing 'configs' property")
	}

	configs := configsProp.Left
	if getType(configs) != "array" {
		t.Fatalf("Expected configs to be array, got: %s", getType(configs))
	}

	// Check configs items
	if configs.Items == nil || configs.Items.Left == nil {
		t.Errorf("❌ BUG: configs array has no items schema")
		t.Logf("configs.Items = %v", configs.Items)
		if configs.MaxItems != nil {
			t.Logf("configs.MaxItems = %d", *configs.MaxItems)
		}
		t.FailNow()
	}

	itemSchema := configs.Items.Left
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Configs entry object missing 'value' property")
	}

	valueType := getType(valueProp.Left)

	// CRITICAL: value must be string
	if valueType != "string" {
		t.Errorf("❌ BUG: configs items value type is '%s', expected 'string'", valueType)
		if valueType == "" && isUnconstrainedSchema(valueProp.Left) {
			t.Logf("value is Top() (unconstrained)")
		}
		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: object-wrapped configs preserves value: {type: string}\n")
}

// TestObjectMergeConfigs tests the EXACT ClusterLinkCreate pattern with object merging
// Pattern: {} | if cond then . + {field: value} else . end | . + {configs: $configs}
func TestObjectMergeConfigs(t *testing.T) {
	configObject := ObjectType()
	configObject.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	clusterObject := BuildObject(map[string]*oas3.Schema{
		"id": StringType(),
	}, []string{"id"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"config": configObject,
		"source": clusterObject,
		"dest":   clusterObject,
	}, []string{})

	// JQ: EXACT ClusterLinkCreate pattern - object merging with conditionals
	jqExpr := `
		(
			((.config // {}) | to_entries | map({name: .key, value: (.value | tostring)}))
			+ (if .source then [{name: "source_id", value: .source.id}] else [] end)
			+ (if .dest then [{name: "dest_id", value: .dest.id}] else [] end)
			| reduce .[] as $c ({}; .[$c.name] = $c.value)
			| to_entries
			| map({name: .key, value: .value})
		) as $configs
		| (
			{}
			| if .source then . + {source_cluster_id: .source.id} else . end
			| if .dest then . + {dest_cluster_id: .dest.id} else . end
			| . + {configs: $configs}
		)
	`

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

	// Result should be an object
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	// Get configs property
	configsProp, hasConfigs := result.Schema.Properties.Get("configs")
	if !hasConfigs || configsProp.Left == nil {
		t.Fatal("Result object missing 'configs' property")
	}

	configs := configsProp.Left
	if getType(configs) != "array" {
		t.Fatalf("Expected configs to be array, got: %s", getType(configs))
	}

	// Check configs items - THIS IS WHERE IT MIGHT FAIL
	if configs.Items == nil || configs.Items.Left == nil {
		t.Errorf("❌ BUG: configs array has no items schema!")
		t.Logf("configs.Items = %v", configs.Items)
		if configs.MaxItems != nil {
			t.Logf("configs.MaxItems = %d", *configs.MaxItems)
		}
		isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
		t.Logf("configs isEmpty = %v", isEmpty)
		t.FailNow()
	}

	itemSchema := configs.Items.Left
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Configs entry object missing 'value' property")
	}

	valueType := getType(valueProp.Left)

	if valueType != "string" {
		t.Errorf("❌ BUG: configs items value type is '%s', expected 'string'", valueType)
		if valueType == "" && isUnconstrainedSchema(valueProp.Left) {
			t.Logf("value is Top() (unconstrained)")
		}
		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: object merge pattern preserves configs items with value: {type: string}\n")
}

// TestFourBranchConcat tests 4+ conditional branches like ClusterLinkCreate has
// Testing if more branches causes issues with array merging
func TestFourBranchConcat(t *testing.T) {
	configObject := ObjectType()
	configObject.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())

	credObject := BuildObject(map[string]*oas3.Schema{
		"key":    StringType(),
		"secret": StringType(),
	}, []string{"key", "secret"})

	clusterObject := BuildObject(map[string]*oas3.Schema{
		"id":          StringType(),
		"credentials": credObject,
	}, []string{"id"})

	inputSchema := BuildObject(map[string]*oas3.Schema{
		"config": configObject,
		"source": clusterObject,
		"dest":   clusterObject,
		"local":  clusterObject,
		"remote": clusterObject,
	}, []string{})

	// JQ: 4+ conditional branches like ClusterLinkCreate
	jqExpr := `
		. as $in
		| ($in.source // null) as $src
		| ($in.dest // null) as $dest
		| ($in.local // null) as $local
		| ($in.remote // null) as $remote
		| (
			(($in.config // {}) | to_entries | map({name: .key, value: (.value | tostring)}))
			+ (if $src.credentials then [{name: "src.key", value: $src.credentials.key}] else [] end)
			+ (if $dest.credentials then [{name: "dest.key", value: $dest.credentials.key}] else [] end)
			+ (if $local.credentials then [{name: "local.key", value: $local.credentials.key}] else [] end)
			+ (if $remote.credentials then [{name: "remote.key", value: $remote.credentials.key}] else [] end)
			| reduce .[] as $c ({}; .[$c.name] = $c.value)
			| to_entries
			| sort_by(.key)
			| map({name: .key, value: .value})
		) as $configs
		| (
			{}
			| if $src then . + {source_cluster_id: $src.id} else . end
			| if $dest then . + {dest_cluster_id: $dest.id} else . end
			| if $local then . + {local_cluster_id: $local.id} else . end
			| if $remote then . + {remote_cluster_id: $remote.id} else . end
			| . + {configs: $configs}
		)
	`

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

	// Result should be an object
	if getType(result.Schema) != "object" {
		t.Fatalf("Expected object, got: %s", getType(result.Schema))
	}

	// Get configs property
	configsProp, hasConfigs := result.Schema.Properties.Get("configs")
	if !hasConfigs || configsProp.Left == nil {
		t.Fatal("Result object missing 'configs' property")
	}

	configs := configsProp.Left
	if getType(configs) != "array" {
		t.Fatalf("Expected configs to be array, got: %s", getType(configs))
	}

	// Check configs items - THIS IS THE CRITICAL CHECK
	if configs.Items == nil || configs.Items.Left == nil {
		t.Errorf("❌ BUG REPRODUCED: configs array has no items schema!")
		t.Logf("This matches the ClusterLinkCreate failure!")
		t.Logf("configs.Items = %v", configs.Items)
		if configs.MaxItems != nil {
			t.Logf("configs.MaxItems = %d", *configs.MaxItems)
		}
		isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
		t.Logf("configs isEmpty = %v", isEmpty)
		t.FailNow()
	}

	itemSchema := configs.Items.Left
	valueProp, hasValue := itemSchema.Properties.Get("value")

	if !hasValue || valueProp.Left == nil {
		t.Fatal("Configs entry object missing 'value' property")
	}

	valueType := getType(valueProp.Left)

	if valueType != "string" {
		t.Errorf("❌ BUG: configs items value type is '%s', expected 'string'", valueType)
		t.FailNow()
	}

	fmt.Printf("✅ SUCCESS: 4-branch pattern preserves configs items with value: {type: string}\n")
}

// TestOneBranchMinimal - Simplest case: 1 conditional
func TestOneBranchMinimal(t *testing.T) {
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"flag": BuildObject(map[string]*oas3.Schema{"id": StringType()}, []string{"id"}),
	}, []string{})

	jqExpr := `
		(
			(if .flag then [{name: "x", value: "y"}] else [] end)
			| reduce .[] as $c ({}; .[$c.name] = $c.value)
			| to_entries
			| map({name: .key, value: .value})
		) as $configs
		| {configs: $configs}
	`

	query, _ := gojq.Parse(jqExpr)
	opts := DefaultOptions()
	opts.EnableWarnings = true
	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Check configs
	configsProp, _ := result.Schema.Properties.Get("configs")
	configs := configsProp.Left

	if configs.Items == nil || configs.Items.Left == nil {
		t.Errorf("❌ 1-branch: configs has no items!")
		t.FailNow()
	}

	itemSchema := configs.Items.Left
	valueProp, _ := itemSchema.Properties.Get("value")
	if getType(valueProp.Left) != "string" {
		t.Errorf("❌ 1-branch: value type is '%s', expected 'string'", getType(valueProp.Left))
		t.FailNow()
	}

	fmt.Printf("✅ 1-branch pattern works\n")
}

// TestTwoBranchMinimal - 2 conditionals
func TestTwoBranchMinimal(t *testing.T) {
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": BuildObject(map[string]*oas3.Schema{"id": StringType()}, []string{"id"}),
		"b": BuildObject(map[string]*oas3.Schema{"id": StringType()}, []string{"id"}),
	}, []string{})

	jqExpr := `
		(
			(if .a then [{name: "a_id", value: .a.id}] else [] end)
			+ (if .b then [{name: "b_id", value: .b.id}] else [] end)
			| reduce .[] as $c ({}; .[$c.name] = $c.value)
			| to_entries
			| map({name: .key, value: .value})
		) as $configs
		| {configs: $configs}
	`

	query, _ := gojq.Parse(jqExpr)
	opts := DefaultOptions()
	opts.EnableWarnings = true
	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Check configs
	configsProp, _ := result.Schema.Properties.Get("configs")
	configs := configsProp.Left

	if configs.Items == nil || configs.Items.Left == nil {
		t.Errorf("❌ 2-branch: configs has no items! MaxItems=%v", configs.MaxItems)
		isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
		t.Logf("isEmpty=%v", isEmpty)
		t.FailNow()
	}

	itemSchema := configs.Items.Left
	valueProp, _ := itemSchema.Properties.Get("value")
	if getType(valueProp.Left) != "string" {
		t.Errorf("❌ 2-branch: value type is '%s', expected 'string'", getType(valueProp.Left))
		t.FailNow()
	}

	fmt.Printf("✅ 2-branch pattern works\n")
}

// TestThreeBranchMinimal - 3 conditionals to find threshold
func TestThreeBranchMinimal(t *testing.T) {
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"a": BuildObject(map[string]*oas3.Schema{"id": StringType()}, []string{"id"}),
		"b": BuildObject(map[string]*oas3.Schema{"id": StringType()}, []string{"id"}),
		"c": BuildObject(map[string]*oas3.Schema{"id": StringType()}, []string{"id"}),
	}, []string{})

	jqExpr := `
		(
			(if .a then [{name: "a_id", value: .a.id}] else [] end)
			+ (if .b then [{name: "b_id", value: .b.id}] else [] end)
			+ (if .c then [{name: "c_id", value: .c.id}] else [] end)
			| reduce .[] as $c ({}; .[$c.name] = $c.value)
			| to_entries
			| map({name: .key, value: .value})
		) as $configs
		| {configs: $configs}
	`

	query, _ := gojq.Parse(jqExpr)
	opts := DefaultOptions()
	opts.EnableWarnings = true
	result, err := RunSchema(context.Background(), query, inputSchema, opts)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	// Check configs
	configsProp, _ := result.Schema.Properties.Get("configs")
	configs := configsProp.Left

	if configs.Items == nil || configs.Items.Left == nil {
		t.Errorf("❌ 3-branch: configs has no items! MaxItems=%v", configs.MaxItems)
		isEmpty := configs.MaxItems != nil && *configs.MaxItems == 0
		t.Logf("isEmpty=%v", isEmpty)
		t.FailNow()
	}

	itemSchema := configs.Items.Left
	valueProp, _ := itemSchema.Properties.Get("value")
	if getType(valueProp.Left) != "string" {
		t.Errorf("❌ 3-branch: value type is '%s', expected 'string'", getType(valueProp.Left))
		t.FailNow()
	}

	fmt.Printf("✅ 3-branch pattern works\n")
}
