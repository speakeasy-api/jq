package playground

import (
	"fmt"
	"strings"

	gojq "github.com/speakeasy-api/jq"
	"gopkg.in/yaml.v3"
)

// TransformerType is a string enum that can right now only be "jq"
type TransformerType string

const (
	Jq TransformerType = "jq"
)

// TransformerFunc represents a transformation function configuration
type TransformerFunc struct {
	Type   TransformerType
	Config string // The actual JQ expression
}

// ParseTransformExtension parses the x-speakeasy-transform-from-json extension
func ParseTransformExtension(yamlNode *yaml.Node) (*TransformerFunc, error) {
	// Check YAML node is a mapping (object)
	if yamlNode.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json must be an object")
	}

	// Parse mapping to find "jq" key
	var jqValue string
	found := false

	// YAML MappingNode stores content as alternating key/value pairs
	for i := 0; i < len(yamlNode.Content); i += 2 {
		if i+1 >= len(yamlNode.Content) {
			break
		}

		keyNode := yamlNode.Content[i]
		valueNode := yamlNode.Content[i+1]

		if keyNode.Value == "jq" {
			// Validate the value is a string
			if valueNode.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("x-speakeasy-transform-from-json: 'jq' value must be a string")
			}
			jqValue = strings.TrimSpace(valueNode.Value)
			found = true
			break
		}
	}

	if !found {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json requires 'jq' key")
	}

	if jqValue == "" {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json: 'jq' requires a jq expression")
	}

	// Validate the JQ expression can be parsed
	q, err := gojq.Parse(jqValue)
	if err != nil {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json: '%s' is an invalid jq expression: %w", jqValue, err)
	}

	// Use the parsed query's string representation
	value := q.String()

	return &TransformerFunc{
		Type:   Jq,
		Config: value,
	}, nil
}
