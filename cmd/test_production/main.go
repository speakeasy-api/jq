package main

import (
	"fmt"
	"os"

	"github.com/speakeasy-api/jq/pkg/playground"
	"gopkg.in/yaml.v3"
)

func main() {
	// Load ClusterLinkCreate input
	yamlData, _ := os.ReadFile("pkg/playground/testdata/ClusterLinkCreate.in.yaml")
	
	var inputSchema map[string]any
	yaml.Unmarshal(yamlData, &inputSchema)
	
	// Build minimal OAS document
	oasDoc := map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{"title": "Test", "version": "1.0.0"},
		"components": map[string]any{
			"schemas": map[string]any{
				"ClusterLinkCreate": inputSchema,
			},
		},
	}
	
	oasYAML, _ := yaml.Marshal(oasDoc)
	
	// Execute the transform
	result, err := playground.SymbolicExecuteJQ(string(oasYAML))
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		return
	}
	
	// Parse result and inspect the value property
	var resultDoc map[string]any
	yaml.Unmarshal([]byte(result), &resultDoc)
	
	components := resultDoc["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	schema := schemas["ClusterLinkCreate"].(map[string]any)
	props := schema["properties"].(map[string]any)
	configs := props["configs"].(map[string]any)
	items := configs["items"].(map[string]any)
	itemProps := items["properties"].(map[string]any)
	value := itemProps["value"]
	
	fmt.Printf("value property: %#v\n", value)
	
	valueYAML, _ := yaml.Marshal(value)
	fmt.Printf("value as YAML:\n%s\n", string(valueYAML))
}
