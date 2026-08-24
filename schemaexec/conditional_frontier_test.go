package schemaexec

import (
	"context"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func TestConditionalFrontierPreservesTruthyOperandRefinement(t *testing.T) {
	credentials := BuildObject(map[string]*oas3.Schema{
		"key":    StringType(),
		"secret": StringType(),
	}, []string{"key", "secret"})
	cluster := BuildObject(map[string]*oas3.Schema{
		"id":                 StringType(),
		"rest_endpoint":      StringType(),
		"bootstrap_endpoint": StringType(),
		"credentials":        credentials,
	}, []string{"id"})
	config := ObjectType()
	config.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](StringType())
	input := BuildObject(map[string]*oas3.Schema{
		"link_name":                 StringType(),
		"source_kafka_cluster":      cluster,
		"destination_kafka_cluster": cluster,
		"local_kafka_cluster":       cluster,
		"remote_kafka_cluster":      cluster,
		"link_mode":                 StringType(),
		"connection_mode":           StringType(),
		"config":                    config,
	}, []string{"link_name"})
	expression := `
		. as $in
		| ($in.source_kafka_cluster // null) as $source
		| ($in.destination_kafka_cluster // null) as $destination
		| ($in.local_kafka_cluster // null) as $local
		| ($in.remote_kafka_cluster // null) as $remote
		| (if $source then $source elif $destination then $destination elif $remote then $remote else null end) as $remote_block
		| (if $remote_block then ($remote_block.bootstrap_endpoint // $remote_block.rest_endpoint // null) else null end) as $endpoint
		| (
			if $endpoint == null then null
			elif ($endpoint | test("://")) then ($endpoint | split("://")[1] | split("/")[0])
			else ($endpoint | split("/")[0])
			end
		) as $bootstrap
		| ($in.config // {}) as $user_config
		| (
			if $in.link_mode then $in.link_mode
			elif ($local and $remote) then "BIDIRECTIONAL"
			elif $source then "DESTINATION"
			elif $destination then "SOURCE"
			else null
			end
		) as $link_mode
		| (if $in.connection_mode then $in.connection_mode else "OUTBOUND" end) as $connection_mode
		| (
			(
				($user_config | to_entries | map({name: .key, value: (.value | tostring)}))
				+ (if $bootstrap then [{name: "bootstrap.servers", value: $bootstrap}] else [] end)
				+ (if $remote_block.credentials then [
					{name: "sasl.mechanism", value: "PLAIN"},
					{name: "security.protocol", value: "SASL_SSL"},
					{name: "sasl.key", value: ($remote_block.credentials.key // "")}
				] else [] end)
				+ (if $local.credentials then [
					{name: "local.sasl.mechanism", value: "PLAIN"},
					{name: "local.security.protocol", value: "SASL_SSL"},
					{name: "local.sasl.key", value: ($local.credentials.key // "")}
				] else [] end)
				+ (if $link_mode then [{name: "link.mode", value: $link_mode}] else [] end)
				+ (if $connection_mode then [{name: "connection.mode", value: $connection_mode}] else [] end)
			)
			| reduce .[] as $entry ({}; .[$entry.name] = $entry.value)
			| to_entries
			| sort_by(.key)
			| map({name: .key, value: .value})
		) as $configs
		| {}
		| if $source then . + {source_id: $source.id} else . end
		| if $destination then . + {destination_id: $destination.id} else . end
		| if ($local and $remote) then . + {x: $remote.id} else . end
		| . + {configs: $configs}
	`
	instance := map[string]any{
		"link_name":                 "link",
		"source_kafka_cluster":      map[string]any{"id": "source"},
		"destination_kafka_cluster": map[string]any{"id": "destination"},
		"local_kafka_cluster":       map[string]any{"id": "local"},
		"remote_kafka_cluster":      map[string]any{"id": "remote"},
	}
	concrete, ok := concreteJQResult(t, expression, instance).(map[string]any)
	if !ok || concrete["x"] != "remote" {
		t.Fatalf("concrete output = %#v, want x=remote", concrete)
	}

	query, err := gojq.Parse(expression)
	if err != nil {
		t.Fatal(err)
	}
	result, err := RunSchema(context.Background(), query, input)
	if err != nil {
		t.Fatal(err)
	}
	property, ok := result.Schema.Properties.Get("x")
	if !ok {
		t.Fatalf("output has no x property: %s", schemaTypeSummary(result.Schema, 4))
	}
	value := resolvedLeft(property)
	if getType(value) != "string" || mightBeType(value, oas3.SchemaTypeNull) || value.Nullable != nil && *value.Nullable {
		t.Fatalf("x value = %s, want non-null string", schemaTypeSummary(value, 4))
	}
}
