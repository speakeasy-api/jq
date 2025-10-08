package playground

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/jq/schemaexec"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/openapi"
)

// SymbolicExecuteJQ validates an OpenAPI spec and performs symbolic execution
func SymbolicExecuteJQ(oasYAML string) (string, error) {
	// Parse the OpenAPI spec
	reader := strings.NewReader(oasYAML)
	doc, validationErrs, err := openapi.Unmarshal(context.Background(), reader)
	if err != nil {
		return "", fmt.Errorf("failed to parse OpenAPI document: %w", err)
	}

	// Check validation errors
	if len(validationErrs) > 0 {
		// Return first validation error
		return "", fmt.Errorf("OpenAPI validation failed: %v", validationErrs[0])
	}

	// Transform schemas with x-speakeasy-transform-from-json using Walk API
	ctx := context.Background()
	var transformErrors []string

	// Collect all schemas that need transformation (depth-first via iteration order)
	type schemaToTransform struct {
		schema   *oas3.JSONSchema[oas3.Referenceable]
		location string
	}
	var schemasToTransform []schemaToTransform

	for item := range openapi.Walk(ctx, doc) {
		err := item.Match(openapi.Matcher{
			Schema: func(schema *oas3.JSONSchema[oas3.Referenceable]) error {
				if schema.GetExtensions() != nil {
					if _, ok := schema.GetExtensions().Get("x-speakeasy-transform-from-json"); ok {
						locationStr := fmt.Sprintf("%v", item.Location)
						schemasToTransform = append(schemasToTransform, schemaToTransform{
							schema:   schema,
							location: locationStr,
						})
					}
				}
				return nil
			},
		})
		if err != nil {
			transformErrors = append(transformErrors, fmt.Sprintf("walk error: %v", err))
		}
	}

	// Process transformations in reverse order (depth-first children before parents)
	for i := len(schemasToTransform) - 1; i >= 0; i-- {
		st := schemasToTransform[i]
		if err := transformSchema(st.schema, st.location); err != nil {
			transformErrors = append(transformErrors, fmt.Sprintf("%s: %v", st.location, err))
		}
	}

	// Report transformation errors if any
	if len(transformErrors) > 0 {
		errMsg := "Schema transformation errors:\n"
		for _, terr := range transformErrors {
			errMsg += fmt.Sprintf("  %s\n", terr)
		}
		return "", fmt.Errorf("%s", errMsg)
	}

	// Marshal the transformed document back to YAML
	var buf strings.Builder
	if err := openapi.Marshal(ctx, doc, &buf); err != nil {
		return "", fmt.Errorf("failed to marshal transformed document: %w", err)
	}

	return buf.String(), nil
}

// transformSchema applies the JQ transformation to a schema with the extension
func transformSchema(schema *oas3.JSONSchema[oas3.Referenceable], location string) error {
	ext := schema.GetExtensions()
	if ext == nil {
		return nil
	}

	transformExpr, ok := ext.Get("x-speakeasy-transform-from-json")
	if !ok {
		return nil
	}

	// Parse the extension using the proper parser
	transformFunc, err := ParseTransformExtension(transformExpr)
	if err != nil {
		return err
	}

	// Get the validated and normalized JQ expression
	exprStr := transformFunc.Config

	// Parse the JQ query for symbolic execution
	query, err := gojq.Parse(exprStr)
	if err != nil {
		return fmt.Errorf("failed to parse JQ query: %w", err)
	}

	// Get the schema value (need to extract from JSONSchema wrapper)
	// JSONSchema is EitherValue, get the Left (Schema) side
	schemaValue := schema.GetLeft()
	if schemaValue == nil {
		return fmt.Errorf("schema is a reference or boolean, cannot transform")
	}

	// Symbolically execute the JQ on the schema to get the output schema
	result, err := schemaexec.RunSchema(context.Background(), query, schemaValue)
	if err != nil {
		return fmt.Errorf("symbolic execution failed: %w", err)
	}

	if result.Schema == nil {
		return fmt.Errorf("symbolic execution produced no output schema")
	}

	// Remove the transform extension from the result to prevent re-application
	if result.Schema.Extensions != nil {
		result.Schema.Extensions.Delete("x-speakeasy-transform-from-json")
	}

	// Replace the original schema with the symbolically executed result
	// Need to create a new JSONSchema from the result schema
	newJSONSchema := oas3.NewJSONSchemaFromSchema[oas3.Referenceable](result.Schema)
	*schema = *newJSONSchema

	return nil
}

// PipelineResult contains the three panels
type PipelineResult struct {
	Panel1          string   `json:"panel1"`
	Panel2          string   `json:"panel2"`
	Panel3          string   `json:"panel3"`
	AppliedFromJson bool     `json:"appliedFromJson"`
	AppliedToJson   bool     `json:"appliedToJson"`
	Warnings        []string `json:"warnings"`
}

// SymbolicExecuteJQPipeline performs sequential transformation pipeline
func SymbolicExecuteJQPipeline(oasYAML string) (*PipelineResult, error) {
	ctx := context.Background()
	result := &PipelineResult{
		Warnings: []string{},
	}

	// Parse original spec
	reader := strings.NewReader(oasYAML)
	doc1, validationErrs, err := openapi.Unmarshal(ctx, reader)
	if err != nil {
		return nil, fmt.Errorf("failed to parse OpenAPI document: %w", err)
	}
	if len(validationErrs) > 0 {
		return nil, fmt.Errorf("OpenAPI validation failed: %v", validationErrs[0])
	}

	// Marshal panel1 (original)
	var buf1 strings.Builder
	if err := openapi.Marshal(ctx, doc1, &buf1); err != nil {
		return nil, fmt.Errorf("failed to marshal panel1: %w", err)
	}
	result.Panel1 = buf1.String()

	// Deep clone for panel2
	doc2, err := cloneDocument(ctx, result.Panel1)
	if err != nil {
		return nil, fmt.Errorf("failed to clone for panel2: %w", err)
	}

	// Apply from-json transformation
	appliedFrom, warnings := applyTransformationsToDoc(ctx, doc2, "x-speakeasy-transform-from-json")
	result.AppliedFromJson = appliedFrom
	result.Warnings = append(result.Warnings, warnings...)

	// Marshal panel2
	var buf2 strings.Builder
	if err := openapi.Marshal(ctx, doc2, &buf2); err != nil {
		return nil, fmt.Errorf("failed to marshal panel2: %w", err)
	}
	result.Panel2 = buf2.String()

	// Deep clone for panel3 (from panel2, not panel1)
	doc3, err := cloneDocument(ctx, result.Panel2)
	if err != nil {
		return nil, fmt.Errorf("failed to clone for panel3: %w", err)
	}

	// Apply to-json transformation
	appliedTo, warningsTo := applyTransformationsToDoc(ctx, doc3, "x-speakeasy-transform-to-json")
	result.AppliedToJson = appliedTo
	result.Warnings = append(result.Warnings, warningsTo...)

	// Marshal panel3
	var buf3 strings.Builder
	if err := openapi.Marshal(ctx, doc3, &buf3); err != nil {
		return nil, fmt.Errorf("failed to marshal panel3: %w", err)
	}
	result.Panel3 = buf3.String()

	return result, nil
}

// cloneDocument deep clones a document by marshaling and unmarshaling
func cloneDocument(ctx context.Context, yamlStr string) (*openapi.OpenAPI, error) {
	reader := strings.NewReader(yamlStr)
	doc, _, err := openapi.Unmarshal(ctx, reader)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// applyTransformationsToDoc applies transformations with the given extension name
func applyTransformationsToDoc(ctx context.Context, doc *openapi.OpenAPI, extensionName string) (bool, []string) {
	var transformErrors []string
	applied := false

	type schemaToTransform struct {
		schema   *oas3.JSONSchema[oas3.Referenceable]
		location string
	}
	var schemasToTransform []schemaToTransform

	// Collect schemas with the extension
	for item := range openapi.Walk(ctx, doc) {
		err := item.Match(openapi.Matcher{
			Schema: func(schema *oas3.JSONSchema[oas3.Referenceable]) error {
				if schema.GetExtensions() != nil {
					if _, ok := schema.GetExtensions().Get(extensionName); ok {
						locationStr := fmt.Sprintf("%v", item.Location)
						schemasToTransform = append(schemasToTransform, schemaToTransform{
							schema:   schema,
							location: locationStr,
						})
						applied = true
					}
				}
				return nil
			},
		})
		if err != nil {
			transformErrors = append(transformErrors, fmt.Sprintf("walk error: %v", err))
		}
	}

	// Process transformations in reverse order
	for i := len(schemasToTransform) - 1; i >= 0; i-- {
		st := schemasToTransform[i]
		if err := transformSchemaWithExtension(st.schema, st.location, extensionName); err != nil {
			transformErrors = append(transformErrors, fmt.Sprintf("%s: %v", st.location, err))
		}
	}

	return applied, transformErrors
}

// transformSchemaWithExtension applies transformation using the specified extension
func transformSchemaWithExtension(schema *oas3.JSONSchema[oas3.Referenceable], location string, extensionName string) error {
	ext := schema.GetExtensions()
	if ext == nil {
		return nil
	}

	transformExpr, ok := ext.Get(extensionName)
	if !ok {
		return nil
	}

	// Parse the extension
	transformFunc, err := ParseTransformExtension(transformExpr)
	if err != nil {
		return err
	}

	// Get the validated JQ expression
	exprStr := transformFunc.Config

	// Parse the JQ query
	query, err := gojq.Parse(exprStr)
	if err != nil {
		return fmt.Errorf("failed to parse JQ query: %w", err)
	}

	// Get the schema value
	schemaValue := schema.GetLeft()
	if schemaValue == nil {
		return fmt.Errorf("schema is a reference or boolean, cannot transform")
	}

	// Symbolically execute the JQ
	result, err := schemaexec.RunSchema(context.Background(), query, schemaValue)
	if err != nil {
		return fmt.Errorf("symbolic execution failed: %w", err)
	}

	if result.Schema == nil {
		return fmt.Errorf("symbolic execution produced no output schema")
	}

	// Remove the transform extension from the result
	if result.Schema.Extensions != nil {
		result.Schema.Extensions.Delete(extensionName)
	}

	// Replace the schema
	newJSONSchema := oas3.NewJSONSchemaFromSchema[oas3.Referenceable](result.Schema)
	*schema = *newJSONSchema

	return nil
}

// executeJQInternal runs a JQ query against JSON input and returns the result
func executeJQInternal(query, jsonInput string) (string, error) {
	// Parse the JQ query
	q, err := gojq.Parse(query)
	if err != nil {
		return "", fmt.Errorf("failed to parse jq query: %w", err)
	}

	// Parse the JSON input
	var input any
	if err := json.Unmarshal([]byte(jsonInput), &input); err != nil {
		return "", fmt.Errorf("failed to parse JSON input: %w", err)
	}

	// Compile the query
	code, err := gojq.Compile(q)
	if err != nil {
		return "", fmt.Errorf("failed to compile jq query: %w", err)
	}

	// Execute the query
	iter := code.Run(input)
	var results []any
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			return "", fmt.Errorf("execution error: %w", err)
		}
		results = append(results, v)
	}

	// If there's only one result, return it directly
	// Otherwise return an array of results
	var output any
	if len(results) == 1 {
		output = results[0]
	} else {
		output = results
	}

	// Marshal the result back to JSON
	outBytes, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %w", err)
	}

	return string(outBytes), nil
}
