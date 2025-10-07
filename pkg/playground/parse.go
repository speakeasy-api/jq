package playground

import (
	"fmt"
	"strings"
	"unicode"

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
	// Check YAML node is a string
	if yamlNode.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json must be a string")
	}
	
	rawValue := strings.TrimSpace(yamlNode.Value)
	if rawValue == "" {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json is required")
	}

	const jqPrefix = "jq"
	if !strings.HasPrefix(rawValue, jqPrefix) {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json currently only supports 'jq'")
	}

	configValue := rawValue[len(jqPrefix):]
	if configValue != "" {
		trimmed := strings.TrimLeftFunc(configValue, unicode.IsSpace)
		if len(trimmed) == len(configValue) {
			return nil, fmt.Errorf("x-speakeasy-transform-from-json currently only supports 'jq' (missing space after 'jq')")
		}
		configValue = trimmed
	}

	if configValue == "" {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json requires a jq expression")
	}

	// Handle single-quoted expressions
	if configValue[0] == '\'' {
		if len(configValue) < 2 || configValue[len(configValue)-1] != '\'' {
			return nil, fmt.Errorf("x-speakeasy-transform-from-json has unmatched single quotes")
		}
		configValue = configValue[1 : len(configValue)-1]
		if configValue == "" {
			return nil, fmt.Errorf("x-speakeasy-transform-from-json requires a jq expression")
		}
	}

	// Validate the JQ expression can be parsed
	q, err := gojq.Parse(configValue)
	if err != nil {
		return nil, fmt.Errorf("x-speakeasy-transform-from-json: '%s' is an invalid jq expression: %w", configValue, err)
	}

	// Use the parsed query's string representation
	value := q.String()

	return &TransformerFunc{
		Type:   Jq,
		Config: value,
	}, nil
}
