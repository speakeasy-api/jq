// Package playground provides utilities for executing jq transformations on OpenAPI specifications.
package playground

import (
	"context"
	"fmt"
	"strings"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/jq/schemaexec"
	"github.com/speakeasy-api/openapi/extensions"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/openapi"
)

// SymbolicExecuteJQ validates an OpenAPI spec and performs symbolic execution
func SymbolicExecuteJQ(oasYAML string) (string, error) {
	ctx := context.Background()
	// Parse the OpenAPI spec
	reader := strings.NewReader(oasYAML)
	doc, validationErrs, err := openapi.Unmarshal(ctx, reader)
	if err != nil {
		return "", fmt.Errorf("failed to parse OpenAPI document: %w", err)
	}

	// Check validation errors
	if len(validationErrs) > 0 {
		// Return first validation error
		return "", fmt.Errorf("OpenAPI validation failed: %v", validationErrs[0])
	}

	// Resolve all $refs
	if _, err := doc.ResolveAllReferences(ctx, openapi.ResolveAllOptions{}); err != nil {
		return "", fmt.Errorf("failed to resolve $refs: %w", err)
	}

	// Transform schemas with x-speakeasy-transform-from-api using Walk API
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
					if _, ok := schema.GetExtensions().Get("x-speakeasy-transform-from-api"); ok {
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

	transformExpr, ok := ext.Get("x-speakeasy-transform-from-api")
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

	// DEBUG: Check configs.items.properties.value BEFORE ensurePropertiesInitialized
	if location == "root.components.schemas.ClusterLinkCreate" {
		if props := result.Schema.Properties; props != nil {
			if configsProp, ok := props.Get("configs"); ok && configsProp.Left != nil {
				configs := configsProp.Left
				if configs.Items != nil && configs.Items.Left != nil {
					items := configs.Items.Left
					if items.Properties != nil {
						if valueProp, ok := items.Properties.Get("value"); ok {
							fmt.Printf("DEBUG ClusterLinkCreate BEFORE ensureProps: value property = %+v\n", valueProp)
							if valueProp.Left != nil {
								fmt.Printf("DEBUG value.Left type fields: Type=%v, AnyOf=%v\n",
									valueProp.Left.Type, len(valueProp.Left.AnyOf))
							}
						}
					}
				}
			}
		}
	}

	// Ensure all nested properties are properly initialized
	ensurePropertiesInitialized(result.Schema)

	// DEBUG: Check configs.items.properties.value AFTER ensurePropertiesInitialized
	if location == "root.components.schemas.ClusterLinkCreate" {
		if props := result.Schema.Properties; props != nil {
			if configsProp, ok := props.Get("configs"); ok && configsProp.Left != nil {
				configs := configsProp.Left
				if configs.Items != nil && configs.Items.Left != nil {
					items := configs.Items.Left
					if items.Properties != nil {
						if valueProp, ok := items.Properties.Get("value"); ok {
							fmt.Printf("DEBUG ClusterLinkCreate AFTER ensureProps: value property = %+v\n", valueProp)
							if valueProp.Left != nil {
								fmt.Printf("DEBUG value.Left type fields: Type=%v, AnyOf=%v\n",
									valueProp.Left.Type, len(valueProp.Left.AnyOf))
							}
						}
					}
				}
			}
		}
	}

	// Preserve original extensions (including the transform extension so it's visible in output)
	originalExtensions := schema.GetExtensions()

	// Copy over all extensions from the original schema
	if originalExtensions != nil && originalExtensions.Len() > 0 {
		if result.Schema.Extensions == nil {
			result.Schema.Extensions = extensions.New()
		}
		// Copy all extensions from original
		for k, v := range originalExtensions.All() {
			result.Schema.Extensions.Set(k, v)
		}
	}

	// Replace the original schema with the symbolically executed result
	// Need to create a new JSONSchema from the result schema
	newJSONSchema := oas3.NewJSONSchemaFromSchema[oas3.Referenceable](result.Schema)
	*schema = *newJSONSchema

	return nil
}

// PipelineResult contains the three panels
type PipelineResult struct {
	Panel1         string   `json:"panel1"`
	Panel2         string   `json:"panel2"`
	Panel3         string   `json:"panel3"`
	AppliedFromAPI bool     `json:"appliedFromApi"`
	AppliedToAPI   bool     `json:"appliedToApi"`
	Warnings       []string `json:"warnings"`
}

// SymbolicExecuteJQPipeline performs sequential transformation pipeline
func SymbolicExecuteJQPipeline(oasYAML string, strict bool) (*PipelineResult, error) {
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
	// Resolve all $refs
	if _, err := doc1.ResolveAllReferences(ctx, openapi.ResolveAllOptions{}); err != nil {
		return nil, fmt.Errorf("failed to resolve $refs: %w", err)
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

	// Apply from-api transformation
	appliedFrom, warnings := applyTransformationsToDoc(ctx, doc2, "x-speakeasy-transform-from-api", strict)
	result.AppliedFromAPI = appliedFrom
	// If there are transformation errors and strict mode is enabled, return them as errors
	if strict && len(warnings) > 0 {
		return nil, fmt.Errorf("%s", FormatTransformErrors(warnings))
	}
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

	// Apply to-api transformation
	appliedTo, warningsTo := applyTransformationsToDoc(ctx, doc3, "x-speakeasy-transform-to-api", strict)
	result.AppliedToAPI = appliedTo
	// If there are transformation errors and strict mode is enabled, return them as errors
	if strict && len(warningsTo) > 0 {
		return nil, fmt.Errorf("%s", FormatTransformErrors(warningsTo))
	}
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
	// Resolve all $refs in the cloned document
	if _, err := doc.ResolveAllReferences(ctx, openapi.ResolveAllOptions{}); err != nil {
		return nil, fmt.Errorf("failed to resolve $refs: %w", err)
	}
	return doc, nil
}

// applyTransformationsToDoc applies transformations with the given extension name
func applyTransformationsToDoc(ctx context.Context, doc *openapi.OpenAPI, extensionName string, strict bool) (bool, []string) {
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
		if err := transformSchemaWithExtension(st.schema, st.location, extensionName, strict); err != nil {
			transformErrors = append(transformErrors, fmt.Sprintf("%s: %v", st.location, err))
		}
	}

	return applied, transformErrors
}

// transformSchemaWithExtension applies transformation using the specified extension
func transformSchemaWithExtension(schema *oas3.JSONSchema[oas3.Referenceable], location string, extensionName string, strict bool) error {
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
	opts := schemaexec.DefaultOptions()
	opts.StrictMode = strict
	result, err := schemaexec.RunSchema(context.Background(), query, schemaValue, opts)
	if err != nil {
		return fmt.Errorf("symbolic execution failed: %w", err)
	}

	if result.Schema == nil {
		return fmt.Errorf("symbolic execution produced no output schema")
	}

	// Ensure all nested properties are properly initialized
	ensurePropertiesInitialized(result.Schema)

	// Preserve original extensions (except the one we're processing)
	originalExtensions := schema.GetExtensions()

	// Remove the transform extension from the result
	if result.Schema.Extensions != nil {
		result.Schema.Extensions.Delete(extensionName)
	}

	// Copy over any other extensions from the original schema that should be preserved
	if originalExtensions != nil && originalExtensions.Len() > 0 {
		if result.Schema.Extensions == nil {
			result.Schema.Extensions = extensions.New()
		}
		// Copy all extensions from original except the one we're processing
		for k, v := range originalExtensions.All() {
			if k != extensionName {
				result.Schema.Extensions.Set(k, v)
			}
		}
	}

	// Replace the schema
	newJSONSchema := oas3.NewJSONSchemaFromSchema[oas3.Referenceable](result.Schema)
	*schema = *newJSONSchema

	return nil
}

// ensurePropertiesInitialized recursively ensures all Properties in a schema are properly initialized as JSONSchemas
func ensurePropertiesInitialized(schema *oas3.Schema) {
	if schema == nil {
		return
	}

	// If Properties exist, ensure each property is properly wrapped as a JSONSchema
	if schema.Properties != nil && schema.Properties.Len() > 0 {
		for name, prop := range schema.Properties.All() {
			if prop == nil {
				// Skip nil properties
				continue
			}
			// Get the underlying schema
			propSchema := prop.GetLeft()
			if propSchema == nil {
				// Property is a reference or has no schema - skip it
				continue
			}
			// Recursively ensure nested properties are initialized
			ensurePropertiesInitialized(propSchema)

			// Recreate the JSONSchema to ensure it's properly initialized
			newProp := oas3.NewJSONSchemaFromSchema[oas3.Referenceable](propSchema)
			schema.Properties.Set(name, newProp)
		}
	}

	// Handle other schema fields that contain JSONSchemas
	if schema.Items != nil {
		itemSchema := schema.Items.GetLeft()
		if itemSchema != nil {
			ensurePropertiesInitialized(itemSchema)
			schema.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](itemSchema)
		}
	}

	if schema.AdditionalProperties != nil {
		addSchema := schema.AdditionalProperties.GetLeft()
		if addSchema != nil {
			ensurePropertiesInitialized(addSchema)
			schema.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](addSchema)
		}
	}

	// Handle composition keywords
	if len(schema.AllOf) > 0 {
		for i, allOfSchema := range schema.AllOf {
			if allOfSchema == nil {
				continue
			}
			innerSchema := allOfSchema.GetLeft()
			if innerSchema != nil {
				ensurePropertiesInitialized(innerSchema)
				schema.AllOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](innerSchema)
			}
		}
	}

	if len(schema.AnyOf) > 0 {
		for i, anyOfSchema := range schema.AnyOf {
			if anyOfSchema == nil {
				continue
			}
			innerSchema := anyOfSchema.GetLeft()
			if innerSchema != nil {
				ensurePropertiesInitialized(innerSchema)
				schema.AnyOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](innerSchema)
			}
		}
	}

	if len(schema.OneOf) > 0 {
		for i, oneOfSchema := range schema.OneOf {
			if oneOfSchema == nil {
				continue
			}
			innerSchema := oneOfSchema.GetLeft()
			if innerSchema != nil {
				ensurePropertiesInitialized(innerSchema)
				schema.OneOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](innerSchema)
			}
		}
	}
}
