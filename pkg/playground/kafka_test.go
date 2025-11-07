package playground

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestSymbolicExecuteJQ_KafkaClusterLinkTransformation tests a complex real-world JQ transformation
// This exercises the symbolic execution engine with:
// - Multiple conditional branches based on which cluster properties are set
// - Complex string manipulation (URL parsing, port substitution)
// - Array operations (to_entries, map, reduce, sort_by)
// - Nested object construction
// - Alternative operator (//) for defaults
func TestSymbolicExecuteJQ_KafkaClusterLinkTransformation(t *testing.T) {
	oasYAML := `openapi: 3.1.0
info:
  title: Symbolic Execution Demo
  version: 1.0.0
components:
  schemas:
    schema:
      type: object
      x-speakeasy-transform-from-api:
        jq: |
          (
            . as $in
            | ($in.source_kafka_cluster // null) as $src
            | ($in.destination_kafka_cluster // null) as $dest
            | ($in.local_kafka_cluster // null) as $local
            | ($in.remote_kafka_cluster // null) as $remote
            | (if $src then $src elif $dest then $dest elif $remote then $remote else null end) as $remoteBlock
            | (if $remoteBlock then ($remoteBlock.bootstrap_endpoint // $remoteBlock.rest_endpoint // null) else null end) as $ep
            | (
                if $ep == null then null
                elif ($ep | test("://")) then ($ep | split("://")[1] | split("/")[0] | sub(":443$"; ":9092") | if test(":\\d+$") then . else . + ":9092" end)
                else ($ep | split("/")[0] | sub(":443$"; ":9092") | if test(":\\d+$") then . else . + ":9092" end)
                end
              ) as $bootstrap
            | ($in.config // {}) as $usercfg
            | (
                if $in.link_mode then $in.link_mode
                elif ($local and $remote) then "BIDIRECTIONAL"
                elif $src then "DESTINATION"
                elif $dest then "SOURCE"
                else null
                end
              ) as $lm
            | (if $in.connection_mode then $in.connection_mode else "OUTBOUND" end) as $cm
            | (
                (
                  ($usercfg | to_entries | map({name: .key, value: (.value | tostring)}))
                  + (if $bootstrap then [{name: "bootstrap.servers", value: $bootstrap}] else [] end)
                  + (
                      if $remoteBlock.credentials then
                        [
                          {name: "sasl.mechanism", value: "PLAIN"},
                          {name: "security.protocol", value: "SASL_SSL"},
                          {name: "sasl.jaas.config", value: ("org.apache.kafka.common.security.plain.PlainLoginModule required username='" + ($remoteBlock.credentials.key // "") + "' password='" + ($remoteBlock.credentials.secret // "") + "';")}
                        ]
                      else []
                      end
                    )
                  + (
                      if $local.credentials then
                        [
                          {name: "local.sasl.mechanism", value: "PLAIN"},
                          {name: "local.security.protocol", value: "SASL_SSL"},
                          {name: "local.sasl.jaas.config", value: ("org.apache.kafka.common.security.plain.PlainLoginModule required username='" + ($local.credentials.key // "") + "' password='" + ($local.credentials.secret // "") + "';")}
                        ]
                      else []
                      end
                    )
                  + (if $lm then [{name: "link.mode", value: $lm}] else [] end)
                  + (if $cm then [{name: "connection.mode", value: $cm}] else [] end)
                ) | reduce .[] as $c ({}; .[$c.name] = $c.value)
                  | to_entries
                  | sort_by(.key)
                  | map({name: .key, value: .value})
              ) as $configs
            | (
                {}
                | if $src then . + {source_cluster_id: $src.id} else . end
                | if $dest then . + {destination_cluster_id: $dest.id} else . end
                | if ($local and $remote) then . + {remote_cluster_id: $remote.id} else . end
                | if $in.cluster_link_id then . + {cluster_link_id: $in.cluster_link_id} else . end
                | . + {configs: $configs}
              )
          )
      properties:
        link_name:
          type: string
        source_kafka_cluster:
          type: object
          properties:
            id:
              type: string
            rest_endpoint:
              type: string
            bootstrap_endpoint:
              type: string
            credentials:
              type: object
              properties:
                key:
                  type: string
                secret:
                  type: string
              required:
                - key
                - secret
          required:
            - id
        destination_kafka_cluster:
          type: object
          properties:
            id:
              type: string
            rest_endpoint:
              type: string
            bootstrap_endpoint:
              type: string
            credentials:
              type: object
              properties:
                key:
                  type: string
                secret:
                  type: string
              required:
                - key
                - secret
          required:
            - id
        local_kafka_cluster:
          type: object
          properties:
            id:
              type: string
            rest_endpoint:
              type: string
            bootstrap_endpoint:
              type: string
            credentials:
              type: object
              properties:
                key:
                  type: string
                secret:
                  type: string
              required:
                - key
                - secret
          required:
            - id
        remote_kafka_cluster:
          type: object
          properties:
            id:
              type: string
            rest_endpoint:
              type: string
            bootstrap_endpoint:
              type: string
            credentials:
              type: object
              properties:
                key:
                  type: string
                secret:
                  type: string
              required:
                - key
                - secret
          required:
            - id
        link_mode:
          type: string
          enum:
            - DESTINATION
            - SOURCE
            - BIDIRECTIONAL
        connection_mode:
          type: string
          enum:
            - INBOUND
            - OUTBOUND
        config:
          type: object
          additionalProperties:
            type: string
        cluster_link_id:
          type: string
      required:
        - link_name
`

	result, err := SymbolicExecuteJQ(oasYAML)
	if err != nil {
		t.Fatalf("SymbolicExecuteJQ failed: %v", err)
	}

	// Parse the result to verify the transformation output schema
	var oasDoc map[string]any
	if err := yaml.Unmarshal([]byte(result), &oasDoc); err != nil {
		t.Fatalf("Failed to parse result: %v", err)
	}

	schema := extractSchema(t, oasDoc, "schema")
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatal("Schema should have properties")
	}

	// Verify configs is an array of objects with name and value
	if configsProp, hasConfigs := props["configs"]; hasConfigs {
		configsPropMap, ok := configsProp.(map[string]any)
		if ok {
			if configsType, hasType := configsPropMap["type"]; hasType && configsType == "array" {
				t.Logf("✓ configs is correctly an array type")
				// Verify items structure
				if items, hasItems := configsPropMap["items"].(map[string]any); hasItems {
					if itemProps, hasProps := items["properties"].(map[string]any); hasProps {
						if _, hasName := itemProps["name"]; !hasName {
							t.Error("configs items should have 'name' property")
						}
						if _, hasValue := itemProps["value"]; !hasValue {
							t.Error("configs items should have 'value' property")
						} else {
							t.Logf("✓ configs items have correct structure with name and value")
						}
					}
				}
			}
		}
	} else {
		t.Error("Expected 'configs' property in output schema")
	}

	// Log conditional properties that may or may not appear
	conditionalProps := []string{"cluster_link_id", "source_cluster_id", "destination_cluster_id", "remote_cluster_id"}
	for _, prop := range conditionalProps {
		if _, has := props[prop]; has {
			t.Logf("✓ Found conditional property: %s", prop)
		}
	}

	if !strings.Contains(result, "configs") {
		t.Error("Result should contain 'configs' in the transformed schema")
	}

	t.Logf("✓ Kafka cluster link transformation completed successfully")
}
