package schemaexec

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"github.com/speakeasy-api/openapi/sequencedmap"
	"gopkg.in/yaml.v3"
)

// schemaEnv is the execution environment for symbolic execution over schemas.
// It's analogous to the concrete env in execute.go but operates on schemas.
type schemaEnv struct {
	ctx      context.Context
	opts     SchemaExecOptions
	codes    []codeOp // Simplified view of bytecode
	stack    *schemaStack
	warnings []string
	scopes   *scopeFrames // Scope frame stack for variable management
	logger   Logger       // Logger for debug tracing
	execID   string       // Unique execution ID
	strict   bool         // If true, fail on unsupported ops and Top/Bottom results

	// Tracks why a Top schema was created during execution.
	// Keyed by the exact Top() schema pointer identity.
	topCauses map[*oas3.Schema]string
}

// NewTopWithCause creates a Top schema and records the reason why it was created.
func (env *schemaEnv) NewTopWithCause(cause string) *oas3.Schema {
	s := Top()
	if env.topCauses == nil {
		env.topCauses = make(map[*oas3.Schema]string)
	}
	env.topCauses[s] = cause
	return s
}

// scopeFrames manages nested variable scopes.
type scopeFrames struct {
	frames []map[string]*oas3.Schema // Stack of variable frames
}

// newScopeFrames creates a new scope frame manager.
func newScopeFrames() *scopeFrames {
	return &scopeFrames{
		frames: make([]map[string]*oas3.Schema, 0, 8),
	}
}

// pushFrame creates a new variable scope.
func (sf *scopeFrames) pushFrame() {
	sf.frames = append(sf.frames, make(map[string]*oas3.Schema))
}

// codeOp represents a bytecode operation for schema execution.
type codeOp struct {
	op     int    // Opcode as int (from GetOp())
	value  any    // Opcode value
	opName string // Opcode name string (from OpString())
}

// Opcode constants matching gojq's internal opcodes
const (
	opNop int = iota
	opPush
	opPop
	opDup
	opConst
	opLoad
	opStore
	opObject
	opAppend
	opFork
	opForkTryBegin
	opForkTryEnd
	opForkAlt
	opForkLabel
	opBacktrack
	opJump
	opJumpIfNot
	opIndex
	opIndexArray
	opCall
	opCallRec
	opPushPC
	opCallPC
	opScope
	opRet
	opIter
	opExpBegin
	opExpEnd
	opPathBegin
	opPathEnd
)

// newSchemaEnv creates a new schema execution environment.
func newSchemaEnv(ctx context.Context, opts SchemaExecOptions) *schemaEnv {
	scopes := newScopeFrames()
	scopes.pushFrame() // Initial global frame

	// Create logger
	var logger Logger
	if opts.LogLevel != "" {
		level := ParseLogLevel(opts.LogLevel)
		logger = NewLogger(level, nil)
	} else {
		logger = newNoopLogger()
	}

	// Generate unique execution ID
	execID := fmt.Sprintf("e%d", time.Now().UnixNano()%1000000)

	return &schemaEnv{
		ctx:      ctx,
		opts:     opts,
		stack:    newSchemaStack(),
		warnings: make([]string, 0),
		scopes:   scopes,
		logger:   logger,
		execID:   execID,
		strict:   opts.StrictMode,
	}
}

// derefJSONSchema attempts to dereference a JSONSchema wrapper to get the actual Schema.
// Tries GetResolvedSchema() first for $ref cases, then falls back to inline Left schemas.
// Uses the provided normCtx for cycle-aware collapsing.
// Returns (schema, true) if successful, (nil, false) if unresolved or invalid.
func derefJSONSchema(ctx context.Context, js *oas3.JSONSchema[oas3.Referenceable]) (*oas3.Schema, bool) {
	if js == nil {
		return nil, false
	}

	// 1) Try GetResolvedSchema() first (handles $refs after ResolveAllReferences).
	if resolved := js.GetResolvedSchema(); resolved != nil {
		if schema := resolved.GetLeft(); schema != nil {
			collapsed, err := collapseAllOfCtx(ctx, schema)
			if err != nil {
				return schema, true
			}
			collapsed, err = collapseAnyOfCtx(ctx, collapsed)
			if err != nil {
				return collapsed, true
			}
			return collapsed, true
		}
	}

	// 2) Fall back to inline Left if GetResolvedSchema didn't work.
	// This handles schemas created by wrapping with NewJSONSchemaFromSchema.
	if js.Left != nil {
		s := js.Left
		collapsed, err := collapseAllOfCtx(ctx, s)
		if err != nil {
			return s, true
		}
		collapsed, err = collapseAnyOfCtx(ctx, collapsed)
		if err != nil {
			return collapsed, true
		}
		return collapsed, true
	}

	// Unresolved or unknown wrapper
	return nil, false
}

// MergeMode determines how schemas are merged
type MergeMode int

const (
	// MergeConjunctive represents allOf semantics (intersection)
	// - Properties: union of keys, recursively merge overlapping
	// - Required: union (field required in ANY subschema)
	// - Types: intersection (must be compatible)
	MergeConjunctive MergeMode = iota

	// MergeDisjunctive represents anyOf semantics (union/LUB)
	// - Properties: union of keys, union overlapping property schemas
	// - Required: intersection (field required in ALL subschemas)
	// - Types: union (more permissive)
	MergeDisjunctive
)

// normCtx holds state for cycle-safe schema normalization (collapse operations)
type normCtx struct {
	inProgress map[*oas3.Schema]struct{}     // Cycle detection: schemas currently being processed
	memo       map[*oas3.Schema]*oas3.Schema // Memoization for DAG sharing
	depth      int                            // Current recursion depth
	maxDepth   int                            // Maximum depth guard
}

func newNormCtx() *normCtx {
	return &normCtx{
		inProgress: make(map[*oas3.Schema]struct{}, 64),
		memo:       make(map[*oas3.Schema]*oas3.Schema, 256),
		depth:      0,
		maxDepth:   10000, // Guard against pathological cases
	}
}

// Context key for normCtx
type normCtxKey struct{}

// withNormCtx adds a normalization context to a context.Context
func withNormCtx(ctx context.Context, nctx *normCtx) context.Context {
	return context.WithValue(ctx, normCtxKey{}, nctx)
}

// getNormCtx retrieves the normalization context from a context.Context
// Returns nil if not present
func getNormCtx(ctx context.Context) *normCtx {
	if ctx == nil {
		return nil
	}
	nctx, _ := ctx.Value(normCtxKey{}).(*normCtx)
	return nctx
}

// newCollapseContext creates a new context with normalization state for collapse operations
func newCollapseContext() context.Context {
	return withNormCtx(context.Background(), newNormCtx())
}

// collapseAllOf recursively collapses allOf constraints in a schema by deep merging.
// Returns the collapsed schema or an error if schemas are incompatible.
func collapseAllOf(schema *oas3.Schema) (*oas3.Schema, error) {
	return collapseAllOfCtx(newCollapseContext(), schema)
}

// collapseAllOfCtx is the cycle-aware implementation of collapseAllOf
func collapseAllOfCtx(ctx context.Context, schema *oas3.Schema) (*oas3.Schema, error) {
	nctx := getNormCtx(ctx)
	if nctx == nil {
		// No normalization context, create one
		nctx = newNormCtx()
		ctx = withNormCtx(ctx, nctx)
	}
	if schema == nil {
		return nil, nil
	}

	// Check memo (DAG sharing)
	if result, ok := nctx.memo[schema]; ok {
		return result, nil
	}

	// Check for cycle (in-progress)
	if _, inProgress := nctx.inProgress[schema]; inProgress {
		// Cycle detected: return schema as-is to break recursion
		return schema, nil
	}

	// Depth guard
	nctx.depth++
	if nctx.depth > nctx.maxDepth {
		nctx.depth--
		return schema, nil // Too deep, return as-is
	}
	defer func() { nctx.depth-- }()

	// Mark in-progress
	nctx.inProgress[schema] = struct{}{}
	defer delete(nctx.inProgress, schema)

	// If no allOf, process nested schemas and return
	if len(schema.AllOf) == 0 {
		// Recursively collapse nested schemas
		collapsed, err := collapseNestedSchemasCtx(ctx, schema)
		if err != nil {
			return nil, err
		}
		nctx.memo[schema] = collapsed
		return collapsed, nil
	}

	// Extract and dereference all allOf subschemas
	subschemas := make([]*oas3.Schema, 0, len(schema.AllOf))
	for _, schemaOrRef := range schema.AllOf {
		if schemaOrRef.Left != nil {
			// Recursively collapse the subschema
			collapsed, err := collapseAllOfCtx(ctx, schemaOrRef.Left)
			if err != nil {
				return nil, err
			}
			subschemas = append(subschemas, collapsed)
		}
	}

	// Handle empty allOf (should not happen but be safe)
	if len(subschemas) == 0 {
		// Empty allOf means unconstrained (Top)
		base := cloneSchema(schema)
		base.AllOf = nil
		result, err := collapseNestedSchemasCtx(ctx, base)
		if err != nil {
			return nil, err
		}
		nctx.memo[schema] = result
		return result, nil
	}

	// Handle single allOf subschema (identity case)
	if len(subschemas) == 1 {
		// Merge the single subschema with the parent schema (if any parent constraints exist)
		base := cloneSchema(schema)
		base.AllOf = nil
		merged, err := mergeSchemas(base, subschemas[0])
		if err != nil {
			return nil, err
		}
		result, err := collapseNestedSchemasCtx(ctx, merged)
		if err != nil {
			return nil, err
		}
		nctx.memo[schema] = result
		return result, nil
	}

	// Merge all subschemas together
	base := cloneSchema(schema)
	base.AllOf = nil

	result := base
	for _, sub := range subschemas {
		merged, err := mergeSchemas(result, sub)
		if err != nil {
			return nil, err
		}
		result = merged
	}

	collapsed, err := collapseNestedSchemasCtx(ctx, result)
	if err != nil {
		return nil, err
	}
	nctx.memo[schema] = collapsed
	return collapsed, nil
}

// collapseAnyOf recursively collapses anyOf constraints in a schema by disjunctive merging.
// Returns the collapsed schema or Bottom if anyOf is empty.
// Keeps anyOf structure if branches have incompatible types.
func collapseAnyOf(schema *oas3.Schema) (*oas3.Schema, error) {
	return collapseAnyOfCtx(newCollapseContext(), schema)
}

// collapseAnyOfCtx is the cycle-aware implementation of collapseAnyOf
func collapseAnyOfCtx(ctx context.Context, schema *oas3.Schema) (*oas3.Schema, error) {
	nctx := getNormCtx(ctx)
	if nctx == nil {
		nctx = newNormCtx()
		ctx = withNormCtx(ctx, nctx)
	}
	if schema == nil {
		return nil, nil
	}

	// Check memo (DAG sharing)
	if result, ok := nctx.memo[schema]; ok {
		return result, nil
	}

	// Check for cycle (in-progress)
	if _, inProgress := nctx.inProgress[schema]; inProgress {
		// Cycle detected: return schema as-is to break recursion
		return schema, nil
	}

	// Depth guard
	nctx.depth++
	if nctx.depth > nctx.maxDepth {
		nctx.depth--
		return schema, nil // Too deep, return as-is
	}
	defer func() { nctx.depth-- }()

	// Mark in-progress
	nctx.inProgress[schema] = struct{}{}
	defer delete(nctx.inProgress, schema)

	// If no anyOf, process nested schemas and return
	if len(schema.AnyOf) == 0 {
		// Recursively collapse nested schemas
		collapsed, err := collapseNestedSchemasCtx(ctx, schema)
		if err != nil {
			return nil, err
		}
		nctx.memo[schema] = collapsed
		return collapsed, nil
	}

	// Extract and collapse all anyOf subschemas
	subschemas := make([]*oas3.Schema, 0, len(schema.AnyOf))
	for _, schemaOrRef := range schema.AnyOf {
		if schemaOrRef.Left != nil {
			// First collapse allOf within this branch, then anyOf
			collapsed, err := collapseAllOfCtx(ctx, schemaOrRef.Left)
			if err != nil {
				return nil, err
			}
			collapsed, err = collapseAnyOfCtx(ctx, collapsed)
			if err != nil {
				return nil, err
			}
			subschemas = append(subschemas, collapsed)
		}
	}

	// Handle empty anyOf - unsatisfiable schema (Bottom)
	if len(subschemas) == 0 {
		result := Bottom()
		nctx.memo[schema] = result
		return result, nil
	}

	// Distribute base constraints into each branch
	base := cloneSchema(schema)
	base.AnyOf = nil

	branches := make([]*oas3.Schema, 0, len(subschemas))
	for _, sub := range subschemas {
		// Merge base with this branch using allOf semantics
		merged, err := mergeSchemas(base, sub)
		if err != nil {
			return nil, err
		}
		branches = append(branches, merged)
	}

	// Check if any branch is Top - if so, result is Top
	for _, branch := range branches {
		if isTopSchema(branch) {
			result := Top()
			nctx.memo[schema] = result
			return result, nil
		}
	}

	// Handle single anyOf subschema (identity case after base merge)
	if len(branches) == 1 {
		result, err := collapseNestedSchemasCtx(ctx, branches[0])
		if err != nil {
			return nil, err
		}
		nctx.memo[schema] = result
		return result, nil
	}

	// Try to flatten anyOf if all branches share the same type
	commonType := getType(branches[0])
	allSameType := commonType != ""
	for i := 1; i < len(branches); i++ {
		t := getType(branches[i])
		if t != commonType {
			allSameType = false
			break
		}
	}

	if !allSameType {
		// Cannot flatten - keep anyOf structure with normalized branches
		result := cloneSchema(schema)
		result.AnyOf = make([]*oas3.JSONSchema[oas3.Referenceable], len(branches))
		for i, branch := range branches {
			result.AnyOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branch)
		}
		nctx.memo[schema] = result
		return result, nil
	}

	// All branches have the same type - attempt disjunctive merge
	result := branches[0]
	for i := 1; i < len(branches); i++ {
		merged, err := mergeSchemasMode(result, branches[i], MergeDisjunctive)
		if err != nil {
			return nil, err
		}
		result = merged
	}

	collapsed, err := collapseNestedSchemasCtx(ctx, result)
	if err != nil {
		return nil, err
	}
	nctx.memo[schema] = collapsed
	return collapsed, nil
}

// collapseNestedSchemas recursively collapses allOf in nested schemas (properties, items, etc.)
func collapseNestedSchemas(schema *oas3.Schema) (*oas3.Schema, error) {
	return collapseNestedSchemasCtx(newCollapseContext(), schema)
}

// collapseNestedSchemasCtx is the cycle-aware implementation of collapseNestedSchemas
func collapseNestedSchemasCtx(ctx context.Context, schema *oas3.Schema) (*oas3.Schema, error) {
	if schema == nil {
		return nil, nil
	}

	result := cloneSchema(schema)

	// Collapse properties
	if result.Properties != nil && result.Properties.Len() > 0 {
		newProps := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for key, prop := range result.Properties.All() {
			if prop != nil && prop.Left != nil {
				collapsed, err := collapseAllOfCtx(ctx, prop.Left)
				if err != nil {
					return nil, err
				}
				collapsed, err = collapseAnyOfCtx(ctx, collapsed)
				if err != nil {
					return nil, err
				}
				newProps.Set(key, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](collapsed))
			} else {
				newProps.Set(key, prop)
			}
		}
		result.Properties = newProps
	}

	// Collapse array items
	if result.Items != nil && result.Items.Left != nil {
		collapsed, err := collapseAllOfCtx(ctx, result.Items.Left)
		if err != nil {
			return nil, err
		}
		collapsed, err = collapseAnyOfCtx(ctx, collapsed)
		if err != nil {
			return nil, err
		}
		result.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](collapsed)
	}

	// Collapse additionalProperties
	if result.AdditionalProperties != nil && result.AdditionalProperties.Left != nil {
		collapsed, err := collapseAllOfCtx(ctx, result.AdditionalProperties.Left)
		if err != nil {
			return nil, err
		}
		collapsed, err = collapseAnyOfCtx(ctx, collapsed)
		if err != nil {
			return nil, err
		}
		result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](collapsed)
	}

	// Collapse anyOf branches
	if len(result.AnyOf) > 0 {
		newAnyOf := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(result.AnyOf))
		for _, branch := range result.AnyOf {
			if branch.Left != nil {
				collapsed, err := collapseAllOfCtx(ctx, branch.Left)
				if err != nil {
					return nil, err
				}
				collapsed, err = collapseAnyOfCtx(ctx, collapsed)
				if err != nil {
					return nil, err
				}
				newAnyOf = append(newAnyOf, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](collapsed))
			}
		}
		result.AnyOf = newAnyOf
	}

	// TODO: Collapse oneOf, not if needed

	return result, nil
}

// mergeSchemasMode deep merges two schemas according to the specified mode.
// Returns an error if the schemas have incompatible constraints.
func mergeSchemasMode(s1, s2 *oas3.Schema, mode MergeMode) (*oas3.Schema, error) {
	// Handle nil cases
	if s1 == nil && s2 == nil {
		return nil, nil
	}
	if s1 == nil {
		return cloneSchema(s2), nil
	}
	if s2 == nil {
		return cloneSchema(s1), nil
	}

	// Fast-path: identical pointer (handles self-referential "next: self" case)
	// This prevents infinite recursion when merging circular schemas
	if s1 == s2 {
		return cloneSchema(s1), nil
	}

	result := cloneSchema(s1)

	// Merge type
	if s2.Type != nil {
		if result.Type == nil {
			result.Type = s2.Type
		} else {
			// Both have types
			t1 := getType(result)
			t2 := getType(s2)

			if mode == MergeConjunctive {
				// allOf: types must be compatible (intersection)
				if t1 != "" && t2 != "" && t1 != t2 {
					return nil, fmt.Errorf("incompatible types: %s and %s", t1, t2)
				}
				// Keep the more specific type (non-empty)
				if t1 == "" {
					result.Type = s2.Type
				}
			} else {
				// anyOf: types are unioned (more permissive)
				// If types differ, we can't represent type union in a single schema
				// The caller should keep anyOf structure for mixed types
				if t1 != "" && t2 != "" && t1 != t2 {
					// Mixed types - cannot flatten to single schema
					// Return an indicator that this should stay as anyOf
					result.Type = nil // Clear type to indicate multi-type
				} else if t1 == "" {
					result.Type = s2.Type
				}
			}
		}
	}

	// Merge properties (union of keys, recursively merge overlapping)
	if s2.Properties != nil && s2.Properties.Len() > 0 {
		if result.Properties == nil {
			result.Properties = sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		}

		for key, prop2 := range s2.Properties.All() {
			if prop1, exists := result.Properties.Get(key); exists {
				// Merge the two property schemas
				if prop1 != nil && prop1.Left != nil && prop2 != nil && prop2.Left != nil {
					merged, err := mergeSchemasMode(prop1.Left, prop2.Left, mode)
					if err != nil {
						return nil, fmt.Errorf("incompatible property %q: %w", key, err)
					}
					result.Properties.Set(key, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](merged))
				} else if prop2 != nil {
					result.Properties.Set(key, prop2)
				}
			} else {
				// New property from s2
				result.Properties.Set(key, prop2)
			}
		}
	}

	// Merge required fields based on mode
	if mode == MergeConjunctive {
		// allOf: union of required fields (field required in ANY subschema)
		if len(s2.Required) > 0 {
			requiredSet := make(map[string]bool)
			for _, r := range result.Required {
				requiredSet[r] = true
			}
			for _, r := range s2.Required {
				if !requiredSet[r] {
					result.Required = append(result.Required, r)
					requiredSet[r] = true
				}
			}
			// Sort for determinism
			sort.Strings(result.Required)
		}
	} else {
		// anyOf: intersection of required fields (field required in ALL subschemas)
		if len(result.Required) > 0 && len(s2.Required) > 0 {
			s1Set := make(map[string]bool)
			for _, r := range result.Required {
				s1Set[r] = true
			}
			s2Set := make(map[string]bool)
			for _, r := range s2.Required {
				s2Set[r] = true
			}
			// Keep only fields that are in both sets
			intersection := make([]string, 0)
			for r := range s1Set {
				if s2Set[r] {
					intersection = append(intersection, r)
				}
			}
			sort.Strings(intersection)
			result.Required = intersection
		} else {
			// If either has no required fields, intersection is empty
			result.Required = nil
		}
	}

	// Merge array items
	if s2.Items != nil && s2.Items.Left != nil {
		if result.Items == nil || result.Items.Left == nil {
			result.Items = s2.Items
		} else {
			// Merge the item schemas
			merged, err := mergeSchemasMode(result.Items.Left, s2.Items.Left, mode)
			if err != nil {
				return nil, fmt.Errorf("incompatible array items: %w", err)
			}
			result.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](merged)
		}
	}

	// TODO: Merge numeric constraints (minimum, maximum, exclusiveMinimum, exclusiveMaximum)
	//   - Conjunctive (allOf): Intersect intervals - take tighter bounds (max of minimums, min of maximums)
	//   - Disjunctive (anyOf): Convex union if intervals overlap/touch; otherwise keep anyOf structure
	//   - Handle exclusive bounds correctly (exclusive dominates inclusive at equal values)
	//   - Return error for allOf if intersection is empty (e.g., min > max)

	// TODO: Merge cardinality constraints (minLength, maxLength, minItems, maxItems, minProperties, maxProperties)
	//   - Same logic as numeric constraints but with integer bounds (always inclusive)
	//   - Conjunctive: max(mins), min(maxs)
	//   - Disjunctive: Convex union only if ranges are adjacent or overlapping

	// TODO: Merge enum constraints
	//   - Conjunctive (allOf): Set intersection using deep JSON equality
	//     - Empty intersection = unsatisfiable (return error)
	//   - Disjunctive (anyOf): Set union with deduplication
	//   - Handle const as single-element enum

	// TODO: Merge multipleOf constraints
	//   - Conjunctive (allOf): LCM using rational arithmetic (big.Rat to avoid float precision issues)
	//     - Only commit back to float64 if exactly representable
	//     - Otherwise keep allOf structure
	//   - Disjunctive (anyOf): Only flatten if one divides the other exactly
	//     - Otherwise keep anyOf structure to remain exact

	// TODO: Merge additionalProperties constraints
	//   - Conjunctive (allOf):
	//     - false AND anything = false (most restrictive)
	//     - true AND X = X
	//     - schema AND schema = merge recursively using allOf mode
	//     - CRITICAL: Without unevaluatedProperties, can only safely flatten when:
	//       * No patternProperties in any branch
	//       * All branches have AP=true or AP=absent
	//       * Otherwise MUST keep allOf structure to preserve "additional" locality
	//   - Disjunctive (anyOf):
	//     - true OR anything = true (most permissive)
	//     - false OR X = X
	//     - schema OR schema = merge recursively using anyOf mode
	//     - Default: Keep anyOf structure; flattening loses locality semantics

	// TODO: Merge pattern constraints
	//   - Conjunctive: Keep both patterns (don't combine into single regex due to complexity/performance)
	//   - Disjunctive: Keep separate branches (alternation can cause catastrophic backtracking)
	//   - Preserve structure in both modes

	// TODO: Merge format constraints
	//   - In JSON Schema 2020-12, format is annotation by default (not validation)
	//   - Conjunctive: Keep both formats as annotations unless Format-Assertion is enabled
	//   - Disjunctive: Keep per-branch; different formats cannot be represented in single schema

	// TODO: Handle incompatible constraints and decide when to error vs. keep structure
	//   - Conjunctive (allOf) - return error for:
	//     * Empty numeric interval (min > max, including exclusive boundary conflicts)
	//     * Empty cardinality range (minLength > maxLength, etc.)
	//     * Empty enum intersection
	//     * Conflicting const values
	//     * Different formats when Format-Assertion is enabled
	//   - Disjunctive (anyOf) - keep structure (don't error) for:
	//     * Non-convex unions (disjoint or exclusive-touching intervals)
	//     * multipleOf where neither divides the other
	//     * Different patterns or formats
	//     * Object schemas with different additionalProperties

	return result, nil
}

// mergeSchemas is a convenience wrapper for allOf-style merging (conjunctive).
// Kept for backward compatibility with existing code.
func mergeSchemas(s1, s2 *oas3.Schema) (*oas3.Schema, error) {
	return mergeSchemasMode(s1, s2, MergeConjunctive)
}

// execute runs the bytecode on the input schema and returns the result.
// Uses multi-state execution to handle jq's backtracking semantics.
func (env *schemaEnv) execute(c *gojq.Code, input *oas3.Schema) (*SchemaExecResult, error) {
	// Get bytecode from Code
	rawCodes := c.GetCodes()

	// Convert to our code representation
	env.codes = make([]codeOp, len(rawCodes))
	for i, rc := range rawCodes {
		env.codes[i] = codeOp{
			op:     getCodeOp(rc),
			value:  getCodeValue(rc),
			opName: getCodeOpName(rc),
		}
	}

	// Create initial state
	initialState := newExecState(input)

	// Create worklist
	worklist := newStateWorklist()
	worklist.push(initialState)

	// Log execution start
	env.logger.With(map[string]any{
		"exec":  env.execID,
		"codes": len(env.codes),
	}).Infof("Starting symbolic execution")

	// Outputs accumulator
	outputs := make([]*oas3.Schema, 0)

	// Track accumulator for array construction results (legacy single-map fallback)
	var sharedAccum map[string]*oas3.Schema
	var sharedSchemaToAlloc map[*oas3.Schema]string

	// NEW: Track all terminal states to merge accumulators across all terminal paths
	terminalStates := make([]*execState, 0, 32)

	// Multi-state execution loop
	maxIterations := env.opts.MaxDepth * 1000 // Safeguard against infinite loops
	iterations := 0
	for !worklist.isEmpty() {
		// SAFEGUARD: Check iteration limit
		iterations++
		if iterations > maxIterations {
			return nil, fmt.Errorf("exceeded maximum iterations (%d) - possible infinite loop", maxIterations)
		}

		// Check context cancellation
		select {
		case <-env.ctx.Done():
			return nil, env.ctx.Err()
		default:
		}

		// Merge the execution frontier periodically to prevent exponential branch explosion.
		// Merge frequently (threshold: 8) to control state count even with string builtins
		const mergeThreshold = 8
		if len(worklist.states) >= mergeThreshold {
			frontier := make([]*execState, 0, len(worklist.states))
			for !worklist.isEmpty() {
				frontier = append(frontier, worklist.pop())
			}
			frontier = env.mergeFrontierByPC(frontier)
			for _, s := range frontier {
				worklist.push(s)
			}
		}

		// Get next state
		state := worklist.pop()
		if state == nil {
			// Worklist empty (shouldn't happen but guard against it)
			break
		}

		// Check if we've seen this state (memoization) — currently disabled (see code comments)
		_ = worklist

		// Check depth limit
		if state.depth > env.opts.MaxDepth {
			if env.strict {
				return nil, fmt.Errorf("strict mode: exceeded maximum execution depth (MaxDepth=%d)", env.opts.MaxDepth)
			}
			env.addWarning("max depth exceeded, widening to Top")
			outputs = append(outputs, Top())
			continue
		}

		// Execute one step
		if state.pc >= len(env.codes) {
			// Terminal state - collect output
			outputSchema := state.top()
			if outputSchema != nil {
				// DEBUG: Show stack state at terminal
				if env.opts.EnableWarnings {
					fmt.Printf("DEBUG Terminal state: stack length=%d, top ptr=%p, top type=%s\n",
						len(state.stack), outputSchema, getType(outputSchema))
					if getType(outputSchema) == "object" {
						hasAP := outputSchema.AdditionalProperties != nil && outputSchema.AdditionalProperties.Left != nil
						var apType string
						if hasAP {
							apType = getType(outputSchema.AdditionalProperties.Left)
						}
						fmt.Printf("DEBUG Terminal object: hasAP=%v, apType=%s\n", hasAP, apType)
					}
					for i, sv := range state.stack {
						fmt.Printf("DEBUG Terminal stack[%d]: ptr=%p, type=%s\n", i, sv.Schema, getType(sv.Schema))
					}
				}
				outputs = append(outputs, outputSchema)

				// Log terminal state
				env.logger.With(map[string]any{
					"exec":    env.execID,
					"state":   fmt.Sprintf("s%d", state.id),
					"lineage": state.lineage,
					"result":  schemaTypeSummary(outputSchema, 1),
				}).Debugf("Terminal state reached")
			}
			// Save first map as legacy fallback
			if sharedAccum == nil && state.accum != nil {
				sharedAccum = state.accum
				sharedSchemaToAlloc = state.schemaToAlloc
			}
			// NEW: collect every terminal state for accumulator merging
			terminalStates = append(terminalStates, state)
			continue
		}

		code := env.codes[state.pc]

		// Log before opcode execution
		topType := "empty"
		if state.top() != nil {
			topType = schemaTypeSummary(state.top(), 1)
		}
		env.logger.With(map[string]any{
			"exec":    env.execID,
			"state":   fmt.Sprintf("s%d", state.id),
			"lineage": state.lineage,
			"pc":      state.pc,
			"op":      code.opName,
			"depth":   state.depth,
			"stack":   len(state.stack),
			"top":     topType,
		}).Debugf("Executing %s", code.opName)

		// Execute opcode on this state
		newStates, err := env.executeOpMultiState(state, &code)
		if err != nil {
			return nil, fmt.Errorf("error at pc=%d op=%s: %w", state.pc, code.opName, err)
		}

		// Assign IDs to successor states and log their creation
		for _, newState := range newStates {
			// Assign new state ID if this is a new state (not the same as parent)
			if newState != state && newState.id == state.id {
				newState.id = worklist.nextStateID
				worklist.nextStateID++
				newState.parentID = state.id

				// Log successor creation
				env.logger.With(map[string]any{
					"exec":    env.execID,
					"state":   fmt.Sprintf("s%d", newState.id),
					"parent":  fmt.Sprintf("s%d", newState.parentID),
					"lineage": newState.lineage,
					"pc":      newState.pc,
					"op":      code.opName,
				}).Debugf("Created successor state")
			}
			worklist.push(newState)
		}
	}

	// Union all outputs FIRST
	var result *oas3.Schema
	if len(outputs) == 0 {
		result = Bottom()
	} else if len(outputs) == 1 {
		result = outputs[0]
	} else {
		if env.opts.EnableWarnings {
			env.addWarning("Merging %d outputs via Union", len(outputs))
		}
		result = Union(outputs, env.opts)
	}

	// NEW: Materialize arrays using a MERGED accumulator built from ALL terminal states
	if len(terminalStates) > 0 {
		mergedAccum, mergedTags := env.mergeTerminalAccumulators(terminalStates)

		// Compute allocID redirects to resolve multi-allocID fragmentation
		redirect := env.computeAllocRedirect(terminalStates, mergedAccum)

		// Diagnostics for allocator/map coverage
		if env.opts.EnableWarnings {
			distinctMaps := make(map[string]int)
			for _, s := range terminalStates {
				distinctMaps[fmt.Sprintf("%p", s.accum)]++
			}
			env.addWarning("terminal states=%d, distinct accum maps=%d", len(terminalStates), len(distinctMaps))

			nonEmptyAlloc := 0
			for k, arr := range mergedAccum {
				if arr != nil && getType(arr) == "array" {
					isEmpty := arr.MaxItems != nil && *arr.MaxItems == 0
					hasItems := arr.Items != nil && arr.Items.Left != nil
					if !isEmpty && hasItems {
						nonEmptyAlloc++
					}
					if k == "[44 0]" { // will never match; alloc keys are "allocN", but keep a breadcrumb
						env.addWarning("merged accum has literal key [44 0] (unexpected); hasItems=%v", hasItems)
					}
				}
			}
			env.addWarning("merged accum allocs=%d, non-empty allocs=%d", len(mergedAccum), nonEmptyAlloc)
		}

		result = env.materializeArrays(result, mergedAccum, mergedTags, redirect)
	} else if sharedAccum != nil && sharedSchemaToAlloc != nil {
		// Legacy fallback: single-map materialization (kept for safety)
		result = env.materializeArrays(result, sharedAccum, sharedSchemaToAlloc)
	}

	// Log execution completion
	env.logger.With(map[string]any{
		"exec":        env.execID,
		"outputs":     len(outputs),
		"result_type": schemaTypeSummary(result, 1),
		"warnings":    len(env.warnings),
	}).Infof("Execution completed")

	// Strict mode: validate result does not contain Top or Bottom
	if env.strict {
		if err := env.validateStrictResult(result); err != nil {
			return nil, err
		}
	}

	return &SchemaExecResult{
		Schema:   result,
		Warnings: env.warnings,
	}, nil
}

// mergeTwoAccumulatorSets merges two accumulator/tag sets, unioning array items when a key
// is present in both accum maps and ensuring the resulting canonical arrays are (re)tagged.
func mergeTwoAccumulatorSets(
	accumA map[string]*oas3.Schema,
	tagsA map[*oas3.Schema]string,
	accumB map[string]*oas3.Schema,
	tagsB map[*oas3.Schema]string,
	opts SchemaExecOptions,
) (map[string]*oas3.Schema, map[*oas3.Schema]string) {
	// Fast-path: one side empty
	if len(accumA) == 0 {
		mergedAccum := make(map[string]*oas3.Schema, len(accumB))
		for k, v := range accumB {
			mergedAccum[k] = v
		}
		mergedTags := make(map[*oas3.Schema]string, len(tagsB))
		for p, id := range tagsB {
			mergedTags[p] = id
		}
		// ensure canonical pointers are tagged
		for id, canon := range mergedAccum {
			if _, ok := mergedTags[canon]; !ok {
				mergedTags[canon] = id
			}
		}
		return mergedAccum, mergedTags
	}
	if len(accumB) == 0 {
		mergedAccum := make(map[string]*oas3.Schema, len(accumA))
		for k, v := range accumA {
			mergedAccum[k] = v
		}
		mergedTags := make(map[*oas3.Schema]string, len(tagsA))
		for p, id := range tagsA {
			mergedTags[p] = id
		}
		for id, canon := range mergedAccum {
			if _, ok := mergedTags[canon]; !ok {
				mergedTags[canon] = id
			}
		}
		return mergedAccum, mergedTags
	}

	mergedAccum := make(map[string]*oas3.Schema, len(accumA)+len(accumB))
	for k, v := range accumA {
		mergedAccum[k] = v
	}
	// Union values for overlapping keys
	for k, v := range accumB {
		if existing, ok := mergedAccum[k]; ok {
			// Union array items if both are arrays
			if getType(existing) == "array" && getType(v) == "array" {
				var existingItems, vItems *oas3.Schema
				if existing.Items != nil && existing.Items.Left != nil {
					existingItems = existing.Items.Left
				} else {
					existingItems = Bottom()
				}
				if v.Items != nil && v.Items.Left != nil {
					vItems = v.Items.Left
				} else {
					vItems = Bottom()
				}
				// Union array items; only set Items if unioned is non-nil
				unioned := Union([]*oas3.Schema{existingItems, vItems}, opts)
				var merged *oas3.Schema
				if unioned == nil {
					// Both inputs had no items (Bottom) - preserve empty array constraint if both are empty
					existingEmpty := existing.MaxItems != nil && *existing.MaxItems == 0
					vEmpty := v.MaxItems != nil && *v.MaxItems == 0
					if existingEmpty && vEmpty {
						// Both are empty arrays - result is empty array
						merged = ArrayType(nil) // This creates maxItems=0 with Items=nil
					} else {
						// Keep an unconstrained array; do not create an invalid Items wrapper
						merged = &oas3.Schema{
							Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
						}
					}
				} else {
					merged = &oas3.Schema{
						Type:  oas3.NewTypeFromString(oas3.SchemaTypeArray),
						Items: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](unioned),
					}
				}
				mergedAccum[k] = merged
			}
			// else: keep existing (arrays should be the only values here)
		} else {
			mergedAccum[k] = v
		}
	}

	// Merge tags and ensure canonical pointers are tagged with their allocID
	mergedTags := make(map[*oas3.Schema]string, len(tagsA)+len(tagsB)+len(mergedAccum))
	for p, id := range tagsA {
		mergedTags[p] = id
	}
	for p, id := range tagsB {
		if _, ok := mergedTags[p]; !ok {
			mergedTags[p] = id
		}
	}
	for id, canon := range mergedAccum {
		if _, ok := mergedTags[canon]; !ok {
			mergedTags[canon] = id
		}
	}
	return mergedAccum, mergedTags
}

// mergeTerminalAccumulators folds all terminal states’ accumulators/tags into a single map pair.
func (env *schemaEnv) mergeTerminalAccumulators(states []*execState) (map[string]*oas3.Schema, map[*oas3.Schema]string) {
	var mergedAccum map[string]*oas3.Schema
	var mergedTags map[*oas3.Schema]string

	// Optional diagnostics for a specific var key across terminals: "[44 0]"
	if env.opts.EnableWarnings {
		for _, s := range states {
			// Search all frames for this variable (from top to bottom)
			var val *oas3.Schema
			for i := len(s.scopes) - 1; i >= 0; i-- {
				if v, ok := s.scopes[i]["[44 0]"]; ok {
					val = v
					break
				}
			}
			if val != nil && getType(val) == "array" {
				isEmpty := val.MaxItems != nil && *val.MaxItems == 0
				hasItems := val.Items != nil && val.Items.Left != nil
				var itemType string
				if hasItems {
					itemType = getType(val.Items.Left)
				}
				tag, tagged := s.schemaToAlloc[val]
				env.addWarning("mergeTermAcc: state s%d var='[44 0]' array empty=%v hasItems=%v itemType=%s tagged=%v allocTag=%s",
					s.id, isEmpty, hasItems, itemType, tagged, tag)
			}
		}
	}

	for i, s := range states {
		if i == 0 {
			// seed from first state (shallow copies)
			mergedAccum = make(map[string]*oas3.Schema, len(s.accum))
			for k, v := range s.accum {
				mergedAccum[k] = v
			}
			mergedTags = make(map[*oas3.Schema]string, len(s.schemaToAlloc))
			for p, id := range s.schemaToAlloc {
				mergedTags[p] = id
			}
			// ensure canon tagged
			for id, canon := range mergedAccum {
				if _, ok := mergedTags[canon]; !ok {
					mergedTags[canon] = id
				}
			}
			continue
		}
		mergedAccum, mergedTags = mergeTwoAccumulatorSets(mergedAccum, mergedTags, s.accum, s.schemaToAlloc, env.opts)
	}

	return mergedAccum, mergedTags
}

// computeAllocRedirect builds allocID->bestAllocID mappings using rank:
// 2 = has concrete items, 1 = unconstrained array, 0 = empty array
// Uses co-occurrence grouping to connect variables that appear together in states
func (env *schemaEnv) computeAllocRedirect(states []*execState, mergedAccum map[string]*oas3.Schema) map[string]string {
	// varKey -> set of allocIDs
	varToAllocs := make(map[string]map[string]struct{})

	// Union-find for variable grouping by co-occurrence
	parent := make(map[string]string)
	find := func(x string) string {
		if _, ok := parent[x]; !ok {
			parent[x] = x
		}
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	// Collect var->allocID and build co-occurrence groups
	for _, s := range states {
		// Gather all tagged array vars in this state
		varsInState := make([]string, 0, 8)
		for _, frame := range s.scopes {
			for k, v := range frame {
				if v == nil || getType(v) != "array" {
					continue
				}
				if allocID, ok := s.schemaToAlloc[v]; ok && allocID != "" {
					if _, ok := varToAllocs[k]; !ok {
						varToAllocs[k] = make(map[string]struct{}, 4)
					}
					varToAllocs[k][allocID] = struct{}{}
					// Init DSU parent
					if _, ok := parent[k]; !ok {
						parent[k] = k
					}
					varsInState = append(varsInState, k)
				}
			}
		}
		// Union all vars that co-occur in this state
		for i := 0; i < len(varsInState); i++ {
			for j := i + 1; j < len(varsInState); j++ {
				union(varsInState[i], varsInState[j])
			}
		}
	}

	rank := func(arr *oas3.Schema) int {
		if arr == nil || getType(arr) != "array" {
			return -1
		}
		if arr.MaxItems != nil && *arr.MaxItems == 0 {
			return 0 // empty
		}
		if arr.Items != nil && arr.Items.Left != nil {
			return 2 // concrete items
		}
		if len(arr.PrefixItems) > 0 {
			return 2
		}
		return 1 // unconstrained
	}

	// Build best-allocID-by-fingerprint table for intent-driven redirects
	bestByFP := make(map[string]string, 64)
	for id, arr := range mergedAccum {
		if arr == nil {
			continue
		}
		if rank(arr) == 2 && arr.Items != nil && arr.Items.Left != nil {
			fp := schemaFingerprint(arr.Items.Left)
			if _, ok := bestByFP[fp]; !ok {
				bestByFP[fp] = id
			}
		}
	}

	redirect := make(map[string]string, 64)

	// INTENT-DRIVEN REDIRECT: Use var history + desired FP to connect orphaned allocIDs
	for _, s := range states {
		for varKey, varFP := range s.varDesiredItemFP {
			target, ok := bestByFP[varFP]
			if !ok || target == "" {
				if env.opts.EnableWarnings {
					fmt.Printf("DEBUG computeAllocRedirect(intent): no rank-2 alloc for fp=%s... (var=%s)\n",
						varFP[:min(16, len(varFP))], varKey)
				}
				continue
			}
			// Gather all allocIDs this var ever held
			hist := s.varAllocHistory[varKey]
			for allocID := range hist {
				if allocID != target {
					// Theory 10: Only redirect within the same DSU class
					if len(states) > 0 && states[0].dsu != nil {
						d := states[0].dsu
						if d.Find(allocID) != d.Find(target) {
							continue
						}
					}
					redirect[allocID] = target
					if env.opts.EnableWarnings {
						fmt.Printf("DEBUG computeAllocRedirect(intent): var=%s fp=%s... redirect %s -> %s\n",
							varKey, varFP[:min(16, len(varFP))], allocID, target)
					}
				}
			}
		}

		// FALLBACK: If var has no intent, check if any of its allocIDs have allocDesiredFP
		for varKey, hist := range s.varAllocHistory {
			if _, hasVarIntent := s.varDesiredItemFP[varKey]; hasVarIntent {
				continue // Already handled above
			}
			// Check if any allocID in history has a known FP
			for allocID := range hist {
				if fp, ok := s.allocDesiredFP[allocID]; ok {
					if target, ok := bestByFP[fp]; ok && target != "" && allocID != target {
						// Theory 10: Only redirect within the same DSU class
						if len(states) > 0 && states[0].dsu != nil {
							d := states[0].dsu
							if d.Find(allocID) != d.Find(target) {
								continue
							}
						}
						redirect[allocID] = target
						if env.opts.EnableWarnings {
							fmt.Printf("DEBUG computeAllocRedirect(alloc-intent): var=%s alloc=%s fp=%s... redirect %s -> %s\n",
								varKey, allocID, fp[:min(16, len(fp))], allocID, target)
						}
					}
				}
			}
		}
	}

	// Build co-occurrence groups (fallback for vars without intent)
	for v := range varToAllocs {
		if _, ok := parent[v]; !ok {
			parent[v] = v
		}
	}
	groups := make(map[string][]string)
	for v := range parent {
		r := find(v)
		groups[r] = append(groups[r], v)
	}

	// For each co-occurrence group, pick group-best alloc by rank
	for groupRoot, members := range groups {
		// Gather all candidate allocIDs across group members
		candidates := make([]string, 0, 16)
		candidateSet := make(map[string]struct{})
		for _, varKey := range members {
			for allocID := range varToAllocs[varKey] {
				if _, seen := candidateSet[allocID]; !seen {
					candidates = append(candidates, allocID)
					candidateSet[allocID] = struct{}{}
				}
			}
		}
		if len(candidates) == 0 {
			continue
		}

		// Find best by rank
		best := ""
		bestRank := -1
		rank2IDs := make([]string, 0, len(candidates))
		for _, allocID := range candidates {
			r := rank(mergedAccum[allocID])
			if r > bestRank {
				bestRank = r
				best = allocID
			}
			if r == 2 {
				rank2IDs = append(rank2IDs, allocID)
			}
		}
		if best == "" {
			continue
		}

		// If multiple rank-2, union their items into best
		if len(rank2IDs) > 1 {
			items := make([]*oas3.Schema, 0, len(rank2IDs))
			for _, id := range rank2IDs {
				if arr := mergedAccum[id]; arr != nil && arr.Items != nil && arr.Items.Left != nil {
					items = append(items, arr.Items.Left)
				}
			}
			if len(items) > 0 {
				if u := Union(items, env.opts); u != nil {
					mergedAccum[best] = &oas3.Schema{
						Type:  oas3.NewTypeFromString(oas3.SchemaTypeArray),
						Items: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](u),
					}
				}
			}
		}

		// Redirect every allocID in the group to the chosen best
		for _, varKey := range members {
			for allocID := range varToAllocs[varKey] {
				if allocID != best {
					// Theory 10: Only redirect within the same DSU class
					if len(states) > 0 && states[0].dsu != nil {
						d := states[0].dsu
						if d.Find(allocID) != d.Find(best) {
							continue
						}
					}
					redirect[allocID] = best
					if env.opts.EnableWarnings {
						fmt.Printf("DEBUG computeAllocRedirect(group): var=%s redirecting %s -> %s (groupBestRank=%d, group=%s)\n",
							varKey, allocID, best, bestRank, groupRoot)
					}
				}
			}
		}
	}

	// Theory 10: Overlay DSU canonicalization and ensure mergedAccum has class roots
	if len(states) > 0 && states[0].dsu != nil {
		d := states[0].dsu
		for id := range mergedAccum {
			root := d.Find(id)
			if root == "" {
				continue
			}
			if root != id {
				fmt.Printf("TRACE DSU computeAllocRedirect: redirect %s -> %s (DSU root)\n", id, root)
				redirect[id] = root
			}
			// Ensure we have an entry at the root and union siblings there
			if root != id {
				if rootArr, ok := mergedAccum[root]; ok && rootArr != mergedAccum[id] {
					mergedAccum[root] = joinTwoSchemas(rootArr, mergedAccum[id])
					fmt.Printf("TRACE DSU computeAllocRedirect: merged %s into root %s\n", id, root)
				} else if _, ok := mergedAccum[root]; !ok {
					mergedAccum[root] = mergedAccum[id]
					fmt.Printf("TRACE DSU computeAllocRedirect: created root entry %s from %s\n", root, id)
				}
			}
		}
	}

	return redirect
}

// executeOpMultiState executes an opcode on a state and returns successor states.
// This is the multi-state version that handles forks and backtracking.
func (env *schemaEnv) executeOpMultiState(state *execState, c *codeOp) ([]*execState, error) {
	// Clone state and advance PC for normal continuation
	next := state.clone()
	next.pc++

	switch c.op {
	case opNop:
		return []*execState{next}, nil

	case opPush:
		return env.execPushMulti(next, c)

	case opPop:
		next.pop()
		return []*execState{next}, nil

	case opConst:
		return env.execConstMulti(next, c)

	case opIndex:
		return env.execIndexMulti(next, c)

	case opIndexArray:
		return env.execIndexMulti(next, c) // Same as index for now

	case opIter:
		return env.execIterMulti(next, c)

	case opObject:
		return env.execObjectMulti(next, c)

	case opScope:
		// Only push frame for function call scopes, not filter-local scopes
		// This prevents scope depth variance from fragmenting partition shapes
		if len(next.callstack) > 0 {
			next.pushFrame()
		}
		return []*execState{next}, nil

	case opStore:
		key := fmt.Sprintf("%v", c.value)

		if len(next.stack) > 0 {
			val := next.pop()

			// CRITICAL FIX: For arrays, check if there's a canonical version to store
			// This ensures variables always have the mutated canonical, not stale references
			finalVal := val
			if getType(val) == "array" {
				if accumKey, tagged := next.schemaToAlloc[val]; tagged {
					if canonical, exists := next.accum[accumKey]; exists {
						if env.opts.EnableWarnings {
							valEmpty := val.MaxItems != nil && *val.MaxItems == 0
							canonEmpty := canonical.MaxItems != nil && *canonical.MaxItems == 0
							canonHasItems := canonical.Items != nil && canonical.Items.Left != nil
							var itemType string
							if canonHasItems {
								itemType = getType(canonical.Items.Left)
							}
							fmt.Printf("DEBUG opStore: var='%s', accumKey=%s - storing canonical instead of stale (valEmpty=%v, canonEmpty=%v, canonHasItems=%v, itemType=%s)\n",
								key, accumKey, valEmpty, canonEmpty, canonHasItems, itemType)
						}
						finalVal = canonical  // Store canonical, not stale reference!
					}
				}
			}

			next.storeVar(key, finalVal)

			// DEBUG: Log variable storage for arrays
			if getType(finalVal) == "array" && env.opts.EnableWarnings {
				isEmpty := finalVal.MaxItems != nil && *finalVal.MaxItems == 0
				hasItems := finalVal.Items != nil && finalVal.Items.Left != nil
				var itemType string
				if hasItems {
					itemType = getType(finalVal.Items.Left)
				}
				accumPtr := fmt.Sprintf("%p", next.accum)
				fmt.Printf("DEBUG opStore: STORED var='%s' - empty=%v, hasItems=%v, itemType=%s, accumPtr=%s\n",
					key, isEmpty, hasItems, itemType, accumPtr)
			}

			// If storing an array, assign unique allocID and tag the schema
			// KLEE-style: arrays carry their identity via schema pointer → allocID mapping
			if getType(finalVal) == "array" {
				// Check if already tagged (when storing canonical)
				if _, alreadyTagged := next.schemaToAlloc[finalVal]; !alreadyTagged {
					// Theory 10: Use origin-aware allocator (also seeds DSU parent)
					accumKey := next.allocateArrayWithOrigin(next.pc-1, "opStore")
					// Initialize canonical array
					if _, exists := next.accum[accumKey]; !exists {
						next.accum[accumKey] = finalVal
					}
					// Tag this schema with its allocID
					next.schemaToAlloc[finalVal] = accumKey

					// CRITICAL: Lift pointer-intent to alloc-intent when minting fresh allocID
					// This connects arrays created by map/sort/etc to their intended items
					if next.schemaFPIntent != nil {
						if fp, ok := next.schemaFPIntent[finalVal]; ok && fp != "" {
							if next.allocDesiredFP == nil {
								next.allocDesiredFP = make(map[string]string)
							}
							next.allocDesiredFP[accumKey] = fp
							next.recordVarAlloc(key, accumKey)
							next.varDesiredItemFP[key] = fp
							fmt.Printf("DEBUG opStore: lifted pointer-intent to new alloc %s for var=%s, fp=%s...\n",
								accumKey, key, fp[:min(20, len(fp))])
						}
					}
				}
				// Record history and intent for variable intent tracking
				if ak := next.schemaToAlloc[finalVal]; ak != "" {
					next.recordVarAlloc(key, ak)
					if canon := next.accum[ak]; canon != nil && canon.Items != nil && canon.Items.Left != nil {
						next.recordDesiredFP(key, canon.Items.Left)
					}
					// CRITICAL: Also lift pointer-intent for already-tagged arrays
					// This handles arrays that were created earlier and are now being stored to a new variable
					if next.schemaFPIntent != nil {
						if fp, ok := next.schemaFPIntent[finalVal]; ok && fp != "" {
							if next.allocDesiredFP == nil {
								next.allocDesiredFP = make(map[string]string)
							}
							// Update allocDesiredFP for this allocID if not already set
							if _, hasAllocFP := next.allocDesiredFP[ak]; !hasAllocFP {
								next.allocDesiredFP[ak] = fp
								next.varDesiredItemFP[key] = fp
								fmt.Printf("DEBUG opStore: lifted pointer-intent to existing alloc %s for var=%s, fp=%s...\n",
									ak, key, fp[:min(20, len(fp))])
							}
						}
					}
				}

				// Theory 10: Intra-state DSU union - unify current allocID with any prior allocIDs
				// this variable had that share the same origin. Merge canonical arrays and cardinality.
				if currID, ok := next.schemaToAlloc[finalVal]; ok && currID != "" {
					if priorSet, ok := next.varAllocHistory[key]; ok {
						currOrigin := next.allocOrigin[currID]
						for priorID := range priorSet {
							if priorID == currID {
								continue
							}
							prevOrigin := next.allocOrigin[priorID]
							if prevOrigin != nil && currOrigin != nil &&
								prevOrigin.PC == currOrigin.PC && prevOrigin.Context == currOrigin.Context {

								// Union classes
								fmt.Printf("TRACE DSU opStore: var=%s union %s with %s (same origin PC=%d, ctx=%s)\n",
									key, priorID, currID, currOrigin.PC, currOrigin.Context)
								next.dsu.Union(priorID, currID)
								root := next.dsu.Find(currID)

								// Merge canonical arrays onto the root
								arr1 := next.accum[priorID]
								arr2 := next.accum[currID]
								var merged *oas3.Schema
								switch {
								case arr1 == nil:
									merged = arr2
								case arr2 == nil:
									merged = arr1
								default:
									merged = joinTwoSchemas(arr1, arr2)
								}
								if merged != nil {
									next.accum[root] = merged
									// Alias both ids to the merged canonical to be robust
									next.accum[priorID] = merged
									next.accum[currID] = merged
									next.schemaToAlloc[merged] = root
									// Store canonical back to the variable (preserve pointer identity)
									next.storeVar(key, merged)
									fmt.Printf("TRACE DSU opStore: merged arrays to root=%s\n", root)
								}

								// Lattice-join cardinality into the root key
								if next.allocCardinality != nil {
									c1 := next.allocCardinality[priorID]
									c2 := next.allocCardinality[currID]
									var joined *ArrayCardinality
									switch {
									case c1 == nil:
										joined = c2
									case c2 == nil:
										joined = c1
									default:
										joined = c1.Join(c2)
									}
									next.allocCardinality[root] = joined
									if joined != nil && joined.MinItems != nil {
										fmt.Printf("TRACE DSU opStore: joined cardinality to root=%s MinItems=%d\n",
											root, *joined.MinItems)
									}
								}

								// Record variable now points to the root ID
								next.recordVarAlloc(key, root)
							}
						}
					}
				}
			}
		}
		return []*execState{next}, nil

	case opLoad:
		key := fmt.Sprintf("%v", c.value)
		// Load from normal variable frames (arrays are tagged with allocID)
		if val, ok := next.loadVar(key); ok {
			// CRITICAL FIX: For arrays, check if there's a canonical version in accum
			// Arrays get mutated in-place in state.accum during append operations,
			// but the original schema stays in the variable frame. We need to return
			// the mutated canonical version.
			finalVal := val
			if getType(val) == "array" {
				if accumKey, tagged := next.schemaToAlloc[val]; tagged {
					if canonical, exists := next.accum[accumKey]; exists {
						canonHasItems := canonical.Items != nil && canonical.Items.Left != nil
						if env.opts.EnableWarnings {
							origEmpty := val.MaxItems != nil && *val.MaxItems == 0
							canonEmpty := canonical.MaxItems != nil && *canonical.MaxItems == 0
							var itemType string
							if canonHasItems {
								itemType = getType(canonical.Items.Left)
							}
							samePtr := val == canonical
							accumPtr := fmt.Sprintf("%p", next.accum)
							valPtr := fmt.Sprintf("%p", val)
							canonPtr := fmt.Sprintf("%p", canonical)
							fmt.Printf("DEBUG opLoad: var='%s', accumKey=%s - origEmpty=%v, canonEmpty=%v, hasItems=%v, itemType=%s, samePtr=%v, accumPtr=%s, valPtr=%s, canonPtr=%s\n",
								key, accumKey, origEmpty, canonEmpty, canonHasItems, itemType, samePtr, accumPtr, valPtr, canonPtr)
						}
						// Use the canonical (mutated) version, not the original
						finalVal = canonical
						if env.opts.EnableWarnings && canonHasItems {
							finalPtr := fmt.Sprintf("%p", finalVal)
							fmt.Printf("DEBUG opLoad: PUSHING non-empty canonical to stack, ptr=%s\n", finalPtr)
						}
					} else if env.opts.EnableWarnings {
						fmt.Printf("DEBUG opLoad: var='%s' tagged as %s but NOT in accum\n", key, accumKey)
					}
				} else if env.opts.EnableWarnings {
					isEmpty := val.MaxItems != nil && *val.MaxItems == 0
					hasItems := val.Items != nil && val.Items.Left != nil
					var itemType string
					if hasItems {
						itemType = getType(val.Items.Left)
					}
					fmt.Printf("DEBUG opLoad: loading var='%s' (not tagged) - empty=%v, hasItems=%v, itemType=%s\n",
						key, isEmpty, hasItems, itemType)
				}
			}
			next.push(finalVal)
		} else {
			if env.strict {
				return nil, fmt.Errorf("strict mode: variable %s not found at pc=%d (state=%d)", key, state.pc, next.id)
			}
			next.push(Top())
			env.addWarning("variable %s not found (scopeDepth=%d, state=%d, pc=%d) - pushing Top()",
				key, len(next.scopes), next.id, next.pc)
		}
		return []*execState{next}, nil

	case opRet:
		// Return from closure
		next.popFrame()
		if len(next.callstack) > 0 {
			// Pop return address and jump back
			retPC := next.callstack[len(next.callstack)-1]
			next.callstack = next.callstack[:len(next.callstack)-1]
			next.pc = retPC
			// NOTE: Accumulator changes are preserved in next.accum
			// When multiple returns merge, Union will handle the lattice join
			return []*execState{next}, nil
		}
		// No caller - terminate this path
		next.pc = len(env.codes)
		return []*execState{next}, nil

	case opDup:
		if top := next.top(); top != nil {
			next.push(top)
		}
		return []*execState{next}, nil

	case opAppend:
		return env.execAppendMulti(next, c)

	case opFork:
		// Fork creates two execution paths
		return env.execFork(state, c)

	case opBacktrack:
		// Backtrack terminates this path (return no successors)
		return []*execState{}, nil

	case opJump:
		// Unconditional jump
		next.pc = c.value.(int)
		return []*execState{next}, nil

	case opJumpIfNot:
		// Conditional jump
		return env.execJumpIfNot(next, c)

	case opForkAlt:
		// Alternative fork (for // operator)
		return env.execForkAlt(state, c)

	case opCall:
		// Function call
		return env.execCallMulti(next, c)

	case opPushPC:
		// Capture closure - create a schema that represents the closure
		if pc, ok := c.value.(int); ok {
			closureSchema := newClosureSchema(pc)
			next.push(closureSchema)
		} else {
			next.push(Top())
		}
		return []*execState{next}, nil

	case opCallPC:
		// Call closure: pop it, jump to its PC, push return address
		clos := next.pop()
		if clos == nil {
			next.push(Top())
			return []*execState{next}, nil
		}
		if pc, ok := getClosurePC(clos); ok {
			// Push return address (next.pc is already incremented by executeOpMultiState)
			next.callstack = append(next.callstack, next.pc)

			// Push new scope frame for closure (balanced by opRet)
			next.pushFrame()

			// Jump to closure PC
			next.pc = pc
			return []*execState{next}, nil
		}
		// Unknown closure
		if env.strict {
			return nil, fmt.Errorf("strict mode: attempted to call unknown closure at pc=%d", state.pc)
		}
		next.push(Top())
		return []*execState{next}, nil

	case opCallRec:
		// Recursive call - similar to CallPC
		if env.strict {
			return nil, fmt.Errorf("strict mode: recursive calls (opCallRec) are not supported")
		}
		if len(next.stack) > 0 {
			next.pop()
		}
		next.push(Top())
		env.addWarning("opCallRec not fully supported, widening result to Top")
		return []*execState{next}, nil

	case opForkTryBegin:
		// try-catch begin - fork to handle both success and error cases
		// For schema execution, we conservatively assume both paths are possible
		targetPC := c.value.(int)

		// Create two states: one continues (success), one jumps (error handler)
		continueState := state.clone()
		continueState.pc++
		continueState.lineage = state.lineage + ".S" // Success branch

		errorState := state.clone()
		errorState.pc = targetPC
		errorState.lineage = state.lineage + ".E" // Error branch

		return []*execState{continueState, errorState}, nil

	case opForkTryEnd:
		// try-catch end - marks end of try block
		// For schema execution, just continue normally
		return []*execState{next}, nil

	case opExpBegin, opExpEnd:
		// Expression boundary markers - used for error messages
		// For schema execution, these are no-ops
		return []*execState{next}, nil

	case opPathBegin:
		// Enter path collection mode
		next.pathMode = true
		next.currentPath = make([]PathSegment, 0, 4)
		return []*execState{next}, nil

	case opPathEnd:
		// Exit path collection mode and convert currentPath to schema
		next.pathMode = false
		pathSchema := buildPathSchemaFromSegments(next.currentPath)
		next.push(pathSchema)
		next.currentPath = nil
		return []*execState{next}, nil

	case opForkLabel:
		// Label for fork operations - used in some control flow
		// For schema execution, treat as no-op
		return []*execState{next}, nil

	// Unsupported opcodes
	default:
		if env.strict {
			return nil, fmt.Errorf("strict mode: unsupported opcode %s (%d) at pc=%d", c.opName, c.op, state.pc)
		}
		// Permissive: widen to Top
		if len(next.stack) > 0 {
			next.pop()
		}
		next.push(Top())
		env.addWarning("unsupported opcode %d, widened to Top", c.op)
		return []*execState{next}, nil
	}
}

// executeOp executes a single bytecode operation (legacy single-state version).
// Kept for backwards compatibility, not used in multi-state execution.
// addWarning adds a warning message to the execution result.
func (env *schemaEnv) addWarning(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)

	// Always log warnings via logger
	env.logger.Warnf("%s", msg)

	// Also collect in warnings array if enabled
	if env.opts.EnableWarnings {
		env.warnings = append(env.warnings, msg)
	}
}

// getCodeOp extracts the opcode int from gojq's code.
func getCodeOp(c any) int {
	// Access via the public GetOp method we added to code
	if code, ok := c.(interface{ GetOp() int }); ok {
		return code.GetOp()
	}
	return -1 // Unknown opcode
}

// getCodeValue extracts the value from gojq's code.
func getCodeValue(c any) any {
	// Access via the public GetValue method we added to code
	if code, ok := c.(interface{ GetValue() any }); ok {
		return code.GetValue()
	}
	return nil
}

// getCodeOpName extracts the opcode name string from gojq's code.
func getCodeOpName(c any) string {
	// Access via the public OpString method we added to code
	if code, ok := c.(interface{ OpString() string }); ok {
		return code.OpString()
	}
	return "unknown"
}

// isSliceIndex checks if index is an array slice (e.g., .[1:], .[:2], .[1:3])
func isSliceIndex(v any) bool {
	m, ok := v.(map[string]any)
	if !ok {
		return false
	}
	// Slice maps have "start", "end", and/or "step" keys
	if _, ok := m["start"]; ok {
		return true
	}
	if _, ok := m["end"]; ok {
		return true
	}
	if _, ok := m["step"]; ok {
		return true
	}
	return false
}

// getArrayElement returns the schema for arr[index].
// Handles prefixItems, items, and unknown indices.
func getArrayElement(arr *oas3.Schema, indexKey any, opts SchemaExecOptions) *oas3.Schema {
	// Try to extract constant integer index
	if idx, ok := indexKey.(int); ok {
		// Check prefixItems for tuple access
		if arr.PrefixItems != nil && idx >= 0 && idx < len(arr.PrefixItems) {
			if schema, ok := derefJSONSchema(newCollapseContext(), arr.PrefixItems[idx]); ok {
				return schema
			}
		}

		// Fall through to items for indices beyond prefixItems
		if arr.Items != nil {
			if schema, ok := derefJSONSchema(newCollapseContext(), arr.Items); ok {
				return schema
			}
		}

		// No schema for this index
		return Top()
	}

	// Non-constant or unknown index - union all possible element types
	schemas := make([]*oas3.Schema, 0)

	// Add all prefixItems
	if arr.PrefixItems != nil {
		for _, item := range arr.PrefixItems {
			if schema, ok := derefJSONSchema(newCollapseContext(), item); ok {
				schemas = append(schemas, schema)
			} else {
				// Unresolved reference in tuple element - widen conservatively
				schemas = append(schemas, Top())
			}
		}
	}

	// Add items schema
	if arr.Items != nil {
		if schema, ok := derefJSONSchema(newCollapseContext(), arr.Items); ok {
			schemas = append(schemas, schema)
		} else {
			// Unresolved reference in items - widen conservatively
			schemas = append(schemas, Top())
		}
	}

	if len(schemas) == 0 {
		return Top()
	}

	return Union(schemas, opts)
}

// ============================================================================
// MULTI-STATE OPCODE HANDLERS
// ============================================================================

// execPushMulti handles push in multi-state mode.
func (env *schemaEnv) execPushMulti(state *execState, c *codeOp) ([]*execState, error) {
	schema := env.valueToSchema(c.value)
	state.push(schema)
	return []*execState{state}, nil
}

// execConstMulti handles const in multi-state mode.
func (env *schemaEnv) execConstMulti(state *execState, c *codeOp) ([]*execState, error) {
	state.pop()
	schema := env.valueToSchema(c.value)
	state.push(schema)
	return []*execState{state}, nil
}

// execIndexMulti handles index in multi-state mode.
func (env *schemaEnv) execIndexMulti(state *execState, c *codeOp) ([]*execState, error) {
	// PATH MODE: Collect path segment instead of navigating
	if state.pathMode {
		indexKey := c.value
		// Array slicing in path mode: treat as wildcard (symbolic index)
		if isSliceIndex(indexKey) {
			state.currentPath = append(state.currentPath, PathSegment{
				Key:        PathWildcard{},
				IsSymbolic: true,
			})
			return []*execState{state}, nil
		}
		state.currentPath = append(state.currentPath, PathSegment{
			Key:        indexKey,
			IsSymbolic: false,
		})
		return []*execState{state}, nil
	}

	// NORMAL MODE: Navigate schema
	base := state.pop()
	if base == nil {
		return []*execState{state}, nil
	}

	indexKey := c.value
	var result *oas3.Schema

	baseType := getType(base)
	switch baseType {
	case "object":
		if key, ok := indexKey.(string); ok {
			// Union-aware property lookup with jq semantics: missing → null
			result = accessPropertyUnion(base, key, env.opts)
		} else {
			result = Top()
		}
	case "array":
		// Array slicing: .[start:end] returns same array type
		if isSliceIndex(indexKey) {
			result = base
		} else {
			result = getArrayElement(base, indexKey, env.opts)
		}
	default:
		// Non-object type. If we are attempting property access (string key), annotate the Top with cause.
		if _, ok := indexKey.(string); ok {
			cause := "property access on non-object type"
			if baseType != "" {
				cause += " (got " + baseType + ")"
			}
			result = env.NewTopWithCause(cause)
		} else {
			// Not a property access; keep existing conservative behavior.
			result = Top()
		}
	}

	state.push(result)
	return []*execState{state}, nil
}

// execIterMulti handles iteration in multi-state mode.
func (env *schemaEnv) execIterMulti(state *execState, c *codeOp) ([]*execState, error) {
	// PATH MODE: Add wildcard segment for symbolic iteration
	if state.pathMode {
		// Symbolic iteration: .[] means "any index"
		// Represent as a wildcard in the path
		state.currentPath = append(state.currentPath, PathSegment{
			Key:        PathWildcard{},
			IsSymbolic: true,
		})
		// Don't pop in path mode - we're building a path, not evaluating
		return []*execState{state}, nil
	}

	// NORMAL MODE: Iterate and push item schema
	val := state.pop()

	// DEBUG: Log what array was popped for iteration
	if env.opts.EnableWarnings && val != nil && getType(val) == "array" {
		isEmpty := val.MaxItems != nil && *val.MaxItems == 0
		hasItems := val.Items != nil && val.Items.Left != nil
		var itemType string
		if hasItems {
			itemType = getType(val.Items.Left)
		}
		env.addWarning("ITER pop: empty=%v hasItems=%v itemType=%s", isEmpty, hasItems, itemType)
	}

	if val == nil {
		return []*execState{state}, nil
	}

	baseType := getType(val)
	var itemSchema *oas3.Schema

	switch baseType {
	case "array":
		// Check for empty array sentinel first, regardless of Items being set
		// Empty arrays are represented with MaxItems=0 and Items may be nil
		if val.MaxItems != nil && *val.MaxItems == 0 {
			// Empty array sentinel: produce no iteration values
			if env.opts.EnableWarnings {
				env.addWarning("ITER: empty array sentinel detected, producing no iterations")
			}
			return []*execState{}, nil
		}

		if val.Items != nil {
			// Try to dereference items schema (handles both inline and $ref)
			if schema, ok := derefJSONSchema(newCollapseContext(), val.Items); ok {
				itemSchema = schema
			} else {
				// Unresolved reference - widen conservatively with cause
				itemSchema = env.NewTopWithCause("iter: failed to resolve array.items (GetResolvedSchema().Left==nil)")
				if env.opts.EnableWarnings {
					env.addWarning("iter: failed to resolve array.items schema; widening to Top")
				}
			}
		} else {
			// Items field not set at all - unconstrained array, items can be any type
			itemSchema = Top()
		}
	case "object":
		itemSchema = unionAllObjectValues(val, env.opts)
	default:
		itemSchema = Bottom()
	}

	// Don't push Bottom - it should terminate paths earlier
	if itemSchema == Bottom() {
		return []*execState{}, nil
	}

	state.push(itemSchema)
	return []*execState{state}, nil
}

// execObjectMulti handles object construction in multi-state mode.
func (env *schemaEnv) execObjectMulti(state *execState, c *codeOp) ([]*execState, error) {
	n := c.value.(int)
	props := make(map[string]*oas3.Schema)
	required := make([]string, 0, n)

	if env.opts.EnableWarnings {
		env.addWarning("opObject: constructing with %d pairs, stack size=%d", n, len(state.stack))
	}

	for i := 0; i < n; i++ {
		if len(state.stack) < 2 {
			return nil, fmt.Errorf("stack underflow on object construction (pair %d, need 2, have %d)", i, len(state.stack))
		}

		val := state.pop()
		key := state.pop()

		// Ensure arrays placed into objects are the canonical arrays (items populated)
		if getType(val) == "array" {
			beforeEmpty := val.MaxItems != nil && *val.MaxItems == 0
			beforeHasItems := val.Items != nil && val.Items.Left != nil
			val = env.materializeArrays(val, state.accum, state.schemaToAlloc)
			if env.opts.EnableWarnings {
				afterEmpty := val.MaxItems != nil && *val.MaxItems == 0
				afterHasItems := val.Items != nil && val.Items.Left != nil
				fmt.Printf("DEBUG opObject: materialized array for property; before(empty=%v,hasItems=%v) → after(empty=%v,hasItems=%v)\n",
					beforeEmpty, beforeHasItems, afterEmpty, afterHasItems)
			}
		}

		if env.opts.EnableWarnings {
			keyStr := "<?>"
			if getType(key) == "string" && len(key.Enum) > 0 && key.Enum[0].Kind == yaml.ScalarNode {
				keyStr = key.Enum[0].Value
			}
			env.addWarning("opObject: pair %d: key=%s, valType=%s", i, keyStr, getType(val))

			// DEBUG: For configs key, log detailed array info including pointer
			if keyStr == "configs" && getType(val) == "array" {
				isEmpty := val.MaxItems != nil && *val.MaxItems == 0
				hasItems := val.Items != nil && val.Items.Left != nil
				var itemType string
				if hasItems {
					itemType = getType(val.Items.Left)
				}
				valPtr := fmt.Sprintf("%p", val)
				fmt.Printf("DEBUG opObject: configs POPPED - empty=%v, hasItems=%v, itemType=%s, ptr=%s\n",
					isEmpty, hasItems, itemType, valPtr)
			}
		}

		if getType(key) == "string" && len(key.Enum) > 0 {
			keyNode := key.Enum[0]
			if keyNode.Kind == yaml.ScalarNode {
				// Guard against nil values (would create invalid JSONSchema wrappers)
				if val == nil {
					env.addWarning("opObject: nil value for key %q; widening to Top", keyNode.Value)
					val = Top()
				}
				props[keyNode.Value] = val
				required = append(required, keyNode.Value)
			}
		}
	}

	if env.opts.EnableWarnings {
		env.addWarning("opObject: built object with %d properties", len(props))
	}

	obj := BuildObject(props, required)
	state.push(obj)
	return []*execState{state}, nil
}

// execAppendMulti handles array element appending in multi-state mode.
// This is used for array construction: [.[] | f]
// Each state has its own accumulator map. Merging happens via lattice join when paths converge.
// allocateArrayWithOrigin creates a new allocID with origin tracking
func (state *execState) allocateArrayWithOrigin(pc int, context string) string {
	*state.allocCounter++
	allocID := fmt.Sprintf("alloc%d", *state.allocCounter)

	// Track origin for DSU equivalence
	if state.allocOrigin == nil {
		state.allocOrigin = make(map[string]*AllocOrigin)
	}
	state.allocOrigin[allocID] = &AllocOrigin{
		PC:      pc,
		Context: context,
	}

	// Initialize cardinality as empty (MinItems=0, MaxItems=0)
	if state.allocCardinality == nil {
		state.allocCardinality = make(map[string]*ArrayCardinality)
	}
	zero := 0
	state.allocCardinality[allocID] = &ArrayCardinality{
		MinItems: &zero,
		MaxItems: &zero,
	}

	// Theory 10: Initialize DSU parent for this new allocID
	if state.dsu == nil {
		state.dsu = NewDSU()
	}
	state.dsu.Find(allocID) // seeds parent[allocID] = allocID

	return allocID
}

// setArrayNonEmpty marks an array as non-empty (MinItems=1)
func (state *execState) setArrayNonEmpty(allocID string) {
	if allocID == "" {
		return
	}
	if state.allocCardinality == nil {
		state.allocCardinality = make(map[string]*ArrayCardinality)
	}

	// Get or create cardinality
	card := state.allocCardinality[allocID]
	if card == nil {
		card = &ArrayCardinality{}
		state.allocCardinality[allocID] = card
	}

	// Set MinItems=1 (array must be non-empty)
	one := 1
	card.MinItems = &one
	// Remove MaxItems=0 constraint if present
	if card.MaxItems != nil && *card.MaxItems == 0 {
		card.MaxItems = nil
	}
}

func (env *schemaEnv) execAppendMulti(state *execState, c *codeOp) ([]*execState, error) {
	// Pop the value to append
	if len(state.stack) < 1 {
		return nil, fmt.Errorf("stack underflow on append (need at least value)")
	}

	val := state.pop()

	// opAppend operates on an array that's on the stack (from opLoad/opStore)
	// We need to find which accumulator this array belongs to
	// The array schema was tagged with its allocID when created in opStore

	// The variable key tells us which variable to look up
	key := ""
	if c.value != nil {
		key = fmt.Sprintf("%v", c.value)
	}

	// DEBUG: Log append key
	if env.opts.EnableWarnings {
		env.addWarning("APPEND key=%q (nil=%v)", key, c.value == nil)
	}

	// Get the array from the variable
	var targetArray *oas3.Schema
	fromVar := false

	// Prefer variable-backed accumulation if key is provided
	if key != "" {
		if arr, ok := state.loadVar(key); ok {
			targetArray = arr
			fromVar = true
		}
	}

	// FALLBACK: Stack-based accumulation (for del/path expressions)
	// ENHANCED: If the array we popped matches any variable by pointer identity,
	// treat it as variable-backed so we can update the variable frame with the canonical pointer.
	if targetArray == nil && len(state.stack) > 0 {
		candidate := state.pop()
		if getType(candidate) == "array" {
			// Try match by pointer to an existing variable
			foundKey := ""
			for i := len(state.scopes) - 1; i >= 0 && foundKey == ""; i-- {
				for k, v := range state.scopes[i] {
					if v == candidate {
						foundKey = k
						break
					}
				}
			}

			if foundKey != "" {
				key = foundKey
				targetArray = candidate
				fromVar = true
				if env.opts.EnableWarnings {
					fmt.Printf("DEBUG execAppendMulti: resolved stack array to variable '%s' by pointer\n", key)
				}
			} else {
				targetArray = candidate
				fromVar = false
			}
		} else {
			// Not an array; push it back
			state.push(candidate)
		}
	}

	// DEBUG: Log target decision
	if env.opts.EnableWarnings {
		env.addWarning("APPEND target fromVar=%v (has target=%v)", fromVar, targetArray != nil)
	}

	// Look up or assign allocID for this array
	var accumKey string
	if targetArray != nil {
		if ak, ok := state.schemaToAlloc[targetArray]; ok {
			accumKey = ak
		} else {
			// Not tagged yet - assign new allocID with origin tracking
			accumKey = state.allocateArrayWithOrigin(state.pc, "append")
			state.accum[accumKey] = targetArray
			state.schemaToAlloc[targetArray] = accumKey
		}
	}

	// Get or create the canonical array in the accumulator
	var canonicalArr *oas3.Schema
	if accumKey != "" {
		if existing, ok := state.accum[accumKey]; ok {
			canonicalArr = existing
		} else {
			// Shouldn't happen if we just tagged it, but handle gracefully
			// Create an empty array without items set (will be set when first element is appended)
			canonicalArr = &oas3.Schema{
				Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
			}
			state.accum[accumKey] = canonicalArr
		}
	} else {
		// No target found - create standalone
		// Create an empty array without items set (will be set when first element is appended)
		canonicalArr = &oas3.Schema{
			Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
		}
	}

	// Get prior items
	priorItems := Bottom()
	if getType(canonicalArr) == "array" && canonicalArr.Items != nil && canonicalArr.Items.Left != nil {
		priorItems = canonicalArr.Items.Left
	}

	// DEBUG: Log accumulator state
	if env.opts.EnableWarnings && accumKey != "" {
		wasEmpty := canonicalArr.MaxItems != nil && *canonicalArr.MaxItems == 0
		valType := getType(val)
		accumPtr := fmt.Sprintf("%p", state.accum)
		canonPtr := fmt.Sprintf("%p", canonicalArr)
		fmt.Printf("DEBUG execAppendMulti: accumKey=%s, wasEmpty=%v, appending type=%s, accumPtr=%s, canonPtr=%s\n",
			accumKey, wasEmpty, valType, accumPtr, canonPtr)
	}

	// For path tuples (arrays with prefixItems), don't union - collect multiple paths
	// This preserves the path structure for delpaths/getpath/setpath
	var unionedItems *oas3.Schema
	valType := getType(val)
	hasTuplePrefixItems := val != nil && len(val.PrefixItems) > 0

	if valType == "array" && hasTuplePrefixItems {
		// This is a path tuple - don't union, but collect multiple paths
		priorIsTuple := getType(priorItems) == "array" && len(priorItems.PrefixItems) > 0

		if priorItems == Bottom() || getType(priorItems) == "" {
			// First path - use directly
			unionedItems = val
		} else if priorIsTuple {
			// Multiple paths - create anyOf to preserve both tuples
			unionedItems = &oas3.Schema{
				Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
				AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
					oas3.NewJSONSchemaFromSchema[oas3.Referenceable](priorItems),
					oas3.NewJSONSchemaFromSchema[oas3.Referenceable](val),
				},
			}
		} else {
			// Prior exists but isn't a tuple - union normally
			unionedItems = Union([]*oas3.Schema{priorItems, val}, env.opts)
		}
	} else {
		// Normal array accumulation - union items
		// CRITICAL FIX: If priorItems is a nested array and val is not an array,
		// the nested array is likely incorrect (e.g., from a bytecode artifact).
		// In this case, use only val to avoid creating anyOf[array, object].
		priorType := getType(priorItems)
		valType := getType(val)
		if priorType == "array" && valType != "" && valType != "array" {
			// Prior is nested array, val is not an array - use val only
			if env.opts.EnableWarnings {
				fmt.Printf("DEBUG execAppendMulti: prior items is nested array (type=%s), val is %s - using val only\n",
					priorType, valType)
			}
			unionedItems = val
		} else {
			unionedItems = Union([]*oas3.Schema{priorItems, val}, env.opts)
		}
	}

	// MUTATE canonical array in-place - safe with unique keys!
	// All states/references to this array will see the update
	if getType(canonicalArr) == "array" {
		// If we've got MaxItems=0 (e.g. were created from a bottom()), drop that
		if canonicalArr.MaxItems != nil && *canonicalArr.MaxItems == 0 {
			canonicalArr.MaxItems = nil
		}

		// DEBUG: Log what we're setting as items
		if env.opts.EnableWarnings && accumKey != "" {
			unionedType := getType(unionedItems)
			isNil := unionedItems == nil
			fmt.Printf("DEBUG execAppendMulti: setting items on %s - unionedItems type=%s, isNil=%v\n",
				accumKey, unionedType, isNil)
		}

		canonicalArr.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](unionedItems)

		// Theory 10: Mark array as non-empty (MinItems=1) after appending
		if accumKey != "" {
			state.setArrayNonEmpty(accumKey)
		}

		// Record var and alloc intent when items become known
		if fromVar && key != "" && unionedItems != nil {
			state.recordDesiredFP(key, unionedItems)
		}
		// Also record allocID intent directly (for cross-variable propagation)
		if accumKey != "" && unionedItems != nil {
			if state.allocDesiredFP == nil {
				state.allocDesiredFP = make(map[string]string)
			}
			fp := schemaFingerprint(unionedItems)
			state.allocDesiredFP[accumKey] = fp
			// Record pointer-intent for the canonical array
			state.recordSchemaFP(canonicalArr, unionedItems)
			fmt.Printf("DEBUG execAppendMulti: recorded allocDesiredFP[%s] = %s...\n",
				accumKey, fp[:min(20, len(fp))])
		}

		// DEBUG: Verify the mutation actually took effect
		if env.opts.EnableWarnings && accumKey != "" {
			afterItems := canonicalArr.Items != nil && canonicalArr.Items.Left != nil
			afterEmpty := canonicalArr.MaxItems != nil && *canonicalArr.MaxItems == 0
			var afterItemType string
			if afterItems {
				afterItemType = getType(canonicalArr.Items.Left)
			}
			fmt.Printf("DEBUG execAppendMulti: AFTER mutation %s - empty=%v, hasItems=%v, itemType=%s\n",
				accumKey, afterEmpty, afterItems, afterItemType)
		}
	}

	// CRITICAL FIX: Update the variable frame with the canonical pointer
	// This ensures that when opLoad retrieves the variable, it gets the mutated canonical
	if fromVar && key != "" && accumKey != "" {
		state.storeVar(key, canonicalArr)
		if env.opts.EnableWarnings {
			fmt.Printf("DEBUG execAppendMulti: updated variable frame %s with canonical ptr=%p\n", key, canonicalArr)
		}
	}

	// Only push for stack-backed accumulation (fromVar == false)
	// For variable-backed accumulation (map/reduce), the compiler emits an explicit opLoad
	// after the loop to get the final result on the stack. Pushing here would break stack discipline.
	if !fromVar {
		state.push(canonicalArr)
	}

	return []*execState{state}, nil
}

// execFork handles fork opcode - creates two execution paths.
func (env *schemaEnv) execFork(state *execState, c *codeOp) ([]*execState, error) {
	// Fork to target PC
	targetPC := c.value.(int)

	// Create two states: one continues, one jumps to target
	continueState := state.clone()
	continueState.pc++
	continueState.lineage = state.lineage + ".C" // Continue branch

	forkState := state.clone()
	forkState.pc = targetPC
	forkState.depth++
	forkState.lineage = state.lineage + ".F" // Fork branch

	// Return fork target FIRST, continue SECOND
	// This ensures LIFO worklist processes continue state first,
	// which is critical for accumulator mutations (e.g., path collection)
	return []*execState{forkState, continueState}, nil
}

// execForkAlt handles alternative fork (// operator).
func (env *schemaEnv) execForkAlt(state *execState, c *codeOp) ([]*execState, error) {
	// Similar to fork but with alt semantics
	// For now, treat same as fork
	return env.execFork(state, c)
}

// execJumpIfNot handles conditional jump.
func (env *schemaEnv) execJumpIfNot(state *execState, c *codeOp) ([]*execState, error) {
	val := state.pop()

	// For schemas, we conservatively explore both paths
	// unless we can definitely determine truthiness

	isDefinitelyFalse := (val != nil && getType(val) == "boolean" &&
		len(val.Enum) == 1 &&
		val.Enum[0].Value == "false")

	isDefinitelyNull := (val != nil && getType(val) == "null")

	if isDefinitelyFalse || isDefinitelyNull {
		// Jump
		state.pc = c.value.(int)
		return []*execState{state}, nil
	}

	// Conservative: explore both paths
	jumpState := state.clone()
	jumpState.pc = c.value.(int)
	jumpState.lineage = state.lineage + ".F" // False branch (jump)

	continueState := state.clone()
	continueState.lineage = state.lineage + ".T" // True branch (continue)
	// NOTE: Do NOT increment pc here - it's already been incremented by the framework
	// before calling this handler (see executeOpMultiState line 224)

	// Truthiness refinement: (T | null) → then: T, else: null
	if val != nil {
		if nn := stripNullUnion(val, env.opts); nn != nil && !schemaEqual(nn, val) {
			// Then-branch: value is non-null
			refineVarRefs(continueState, val, nn)
			// Else-branch: value is null
			refineVarRefs(jumpState, val, ConstNull())
		}
	}

	return []*execState{continueState, jumpState}, nil
}

// execCallMulti handles function calls in multi-state mode.
func (env *schemaEnv) execCallMulti(state *execState, c *codeOp) ([]*execState, error) {
	switch v := c.value.(type) {
	case [3]any:
		// Builtin function
		argCount := 0
		if ac, ok := v[1].(int); ok {
			argCount = ac
		}

		funcName := ""
		if fn, ok := v[2].(string); ok {
			funcName = fn
		}

		// Pop input FIRST (top of stack holds the saved input `v` that compileCallInternal just pushed)
		if len(state.stack) == 0 {
			return nil, fmt.Errorf("stack underflow on call input")
		}
		input := state.pop()

		// Then pop args in left-to-right order (matching the push order from compiler)
		args := make([]*oas3.Schema, argCount)
		for i := 0; i < argCount; i++ {
			if len(state.stack) == 0 {
				return nil, fmt.Errorf("stack underflow on call args")
			}
			args[i] = state.pop()
		}

		// DEBUG: Trace builtin calls
		if env.opts.EnableWarnings && funcName == "delpaths" {
			env.addWarning("execCallMulti: calling %s with input type=%s, %d args", funcName, getType(input), len(args))
			for i, arg := range args {
				env.addWarning("execCallMulti: arg[%d] type=%s", i, getType(arg))
			}
		}

		// Call builtin
		results, err := env.callBuiltin(funcName, input, args)
		if err != nil {
			if env.strict {
				return nil, fmt.Errorf("strict mode: builtin %s failed: %v", funcName, err)
			}
			env.addWarning("builtin %s: %v", funcName, err)
			state.push(Top())
			return []*execState{state}, nil
		}

		// Rebind accumulator variables for setpath to ensure pointer identity is updated
		if (funcName == "setpath" || funcName == "_setpath") && len(results) == 1 && results[0] != nil {
			refineVarRefs(state, input, results[0])
			if env.opts.EnableWarnings {
				fmt.Printf("DEBUG execCallMulti: rebinding vars after %s (old ptr=%p, new ptr=%p)\n",
					funcName, input, results[0])
			}
		}

		// For single result, push and continue
		if len(results) == 1 {
			r := results[0]
			if r == nil {
				// Bottom/nil represents impossible execution path - terminate this state
				// Don't widen to Top as that pollutes unions with unconstrained types
				if env.strict {
					return nil, fmt.Errorf("strict mode: builtin %s produced Bottom (nil) result", funcName)
				}
				// Return empty state list (terminate this execution path)
				return []*execState{}, nil
			}
			// Record schema-level intent for arrays with items
			if getType(r) == "array" && r.Items != nil && r.Items.Left != nil {
				state.recordSchemaFP(r, r.Items.Left)
			}
			state.push(r)
			return []*execState{state}, nil
		}

		// For multiple results, create separate states for each
		// This handles builtins that can return different schemas
		states := make([]*execState, 0, len(results))
		for _, result := range results {
			if result == nil {
				// Bottom/nil represents impossible execution path - skip this branch
				// Don't widen to Top as that pollutes unions with unconstrained types
				if env.strict {
					return nil, fmt.Errorf("strict mode: builtin %s produced Bottom (nil) result", funcName)
				}
				// Skip this result (don't create a state for it)
				continue
			}
			s := state.clone()
			// Record schema-level intent for arrays with items
			if getType(result) == "array" && result.Items != nil && result.Items.Left != nil {
				s.recordSchemaFP(result, result.Items.Left)
			}
			s.push(result)
			states = append(states, s)
		}
		return states, nil

	case int:
		// User-defined function: record call site for 1-CFA and jump
		targetPC := v
		// Record call site PC (for accumulator disambiguation)
		// removed callSitePC = state.pc
		// Push return PC (state.pc is already incremented by dispatcher)
		state.callstack = append(state.callstack, state.pc)
		// Jump to function
		state.pc = targetPC
		state.depth++
		return []*execState{state}, nil

	default:
		// Unknown call format
		if env.strict {
			return nil, fmt.Errorf("strict mode: unknown function call format: %T", v)
		}
		env.addWarning("unknown function call format: %T", v)
		state.push(Top())
		return []*execState{state}, nil
	}
}

// valueToSchema converts a constant value to a schema.
func (env *schemaEnv) valueToSchema(v any) *oas3.Schema {
	switch val := v.(type) {
	case string:
		return ConstString(val)
	case float64:
		if val == math.Trunc(val) {
			return ConstInteger(int64(val))
		}
		return ConstNumber(val)
	case int:
		return ConstInteger(int64(val))
	case bool:
		return ConstBool(val)
	case nil:
		return ConstNull()
	case map[string]any:
		return buildObjectFromLiteral(val)
	case []any:
		return buildArrayFromLiteral(val)
	default:
		return Top()
	}
}

// unionAllObjectValues creates union of all property and additionalProperty schemas.
func unionAllObjectValues(obj *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	schemas := make([]*oas3.Schema, 0)

	// Add all property values
	if obj.Properties != nil {
		for k, v := range obj.Properties.All() {
			if schema, ok := derefJSONSchema(newCollapseContext(), v); ok {
				schemas = append(schemas, schema)
				if opts.EnableWarnings {
					fmt.Printf("DEBUG unionAllObjectValues: property %s type=%s\n", k, getType(schema))
				}
			} else {
				// Unresolved reference in property - widen conservatively
				schemas = append(schemas, Top())
				if opts.EnableWarnings {
					fmt.Printf("DEBUG unionAllObjectValues: property %s UNRESOLVED -> Top\n", k)
				}
			}
		}
	}

	// Add additionalProperties
	if obj.AdditionalProperties != nil {
		if schema, ok := derefJSONSchema(newCollapseContext(), obj.AdditionalProperties); ok {
			schemas = append(schemas, schema)
			if opts.EnableWarnings {
				fmt.Printf("DEBUG unionAllObjectValues: additionalProperties type=%s, unconstrained=%v\n",
					getType(schema), isUnconstrainedSchema(schema))
			}
		} else {
			// Unresolved reference in additionalProperties - widen conservatively
			schemas = append(schemas, Top())
			if opts.EnableWarnings {
				fmt.Printf("DEBUG unionAllObjectValues: additionalProperties UNRESOLVED -> Top\n")
			}
		}
	}

	// TODO: Add patternProperties

	if len(schemas) == 0 {
		if opts.EnableWarnings {
			fmt.Printf("DEBUG unionAllObjectValues: no schemas found -> Top\n")
		}
		return Top() // Unknown object values
	}

	result := Union(schemas, opts)
	if opts.EnableWarnings {
		fmt.Printf("DEBUG unionAllObjectValues: union result type=%s, unconstrained=%v (from %d schemas)\n",
			getType(result), isUnconstrainedSchema(result), len(schemas))
	}
	return result
}

// buildObjectFromLiteral creates a schema from a map literal.
func buildObjectFromLiteral(m map[string]any) *oas3.Schema {
	props := make(map[string]*oas3.Schema)
	required := make([]string, 0, len(m))

	for k, v := range m {
		props[k] = valueToSchemaStatic(v)
		required = append(required, k)
	}

	return BuildObject(props, required)
}

// buildArrayFromLiteral creates a schema from an array literal.
func buildArrayFromLiteral(arr []any) *oas3.Schema {
	if len(arr) == 0 {
		// Empty array constant: Set MaxItems=0 with Items unset (nil).
		// This avoids creating an invalid JSONSchema wrapper with neither Left nor Right.
		// Iteration logic detects emptiness via MaxItems=0.
		emptyArray := &oas3.Schema{
			Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
		}
		maxItems := int64(0)
		emptyArray.MaxItems = &maxItems
		// Leave Items nil to avoid invalid EitherValue wrapper
		return emptyArray
	}

	// Build prefixItems for tuple if heterogeneous
	prefixItems := make([]*oas3.Schema, len(arr))
	itemsSchema := Top()

	allSameType := true
	firstType := ""

	for i, v := range arr {
		schema := valueToSchemaStatic(v)
		prefixItems[i] = schema

		typ := getType(schema)
		if i == 0 {
			firstType = typ
			itemsSchema = schema
		} else if typ != firstType {
			allSameType = false
		}
	}

	if allSameType && len(arr) > 0 {
		// Homogeneous array - use items schema
		return ArrayType(itemsSchema)
	}

	// Heterogeneous array - use prefixItems
	return BuildArray(Top(), prefixItems)
}

// valueToSchemaStatic converts a constant value to a schema (static version, no env).
func valueToSchemaStatic(v any) *oas3.Schema {
	switch val := v.(type) {
	case string:
		return ConstString(val)
	case float64:
		if val == math.Trunc(val) {
			return ConstInteger(int64(val))
		}
		return ConstNumber(val)
	case int:
		return ConstInteger(int64(val))
	case bool:
		return ConstBool(val)
	case nil:
		return ConstNull()
	case map[string]any:
		return buildObjectFromLiteral(val)
	case []any:
		return buildArrayFromLiteral(val)
	default:
		return Top()
	}
}

// buildPathSchemaFromSegments converts path segments to a schema representation
// Path is represented as an array with prefixItems (tuple)
// Example: ["a", "b", 0] -> array with items [const"a", const"b", const 0]
func buildPathSchemaFromSegments(segments []PathSegment) *oas3.Schema {
	if len(segments) == 0 {
		// Empty path - represents root
		return ArrayType(Bottom())
	}

	prefixItems := make([]*oas3.Schema, len(segments))
	for i, seg := range segments {
		if seg.IsSymbolic {
			// Wildcard: represent as integer type (any index)
			prefixItems[i] = IntegerType()
		} else if s, ok := seg.Key.(string); ok {
			prefixItems[i] = ConstString(s)
		} else if n, ok := seg.Key.(int); ok {
			prefixItems[i] = ConstInteger(int64(n))
		} else {
			// Fallback for unknown segment type
			prefixItems[i] = Top()
		}
	}

	// Return tuple array (array with specific prefixItems)
	return BuildArray(Top(), prefixItems)
}

// Simple closure tracking (maps schema pointer to PC)
var closureRegistry = make(map[*oas3.Schema]int)

func newClosureSchema(pc int) *oas3.Schema {
	s := Top()
	closureRegistry[s] = pc
	return s
}

func getClosurePC(s *oas3.Schema) (int, bool) {
	pc, ok := closureRegistry[s]
	return pc, ok
}

// validateStrictResult performs a deep scan of the result schema to ensure
// it does not contain any Top or Bottom structures when in strict mode.
// Returns an error with a path to the first offending node if found.
func (env *schemaEnv) validateStrictResult(schema *oas3.Schema) error {
	return env.validateStrictResultPath(schema, "$")
}

// validateStrictResultPath recursively validates a schema at the given path.
func (env *schemaEnv) validateStrictResultPath(schema *oas3.Schema, path string) error {
	// Check if this is Bottom (which is represented as nil)
	if isBottomSchema(schema) {
		return fmt.Errorf("strict mode: result contains Bottom at %s", path)
	}

	// After Bottom check, if nil, it's safe to skip
	if schema == nil {
		return nil
	}

	// Check if this is Top
	if isTopSchema(schema) {
		if env.topCauses != nil {
			if cause, ok := env.topCauses[schema]; ok {
				return fmt.Errorf("strict mode: result contains Top at %s; cause: %s", path, cause)
			}
		}
		return fmt.Errorf("strict mode: result contains Top at %s", path)
	}

	// Recursively check array items
	if schema.Items != nil && schema.Items.Left != nil {
		if err := env.validateStrictResultPath(schema.Items.Left, path+".items"); err != nil {
			return err
		}
	}

	// Recursively check prefixItems (tuple items)
	if schema.PrefixItems != nil {
		for i, item := range schema.PrefixItems {
			if item.Left != nil {
				itemPath := fmt.Sprintf("%s.prefixItems[%d]", path, i)
				if err := env.validateStrictResultPath(item.Left, itemPath); err != nil {
					return err
				}
			}
		}
	}

	// Recursively check object properties
	if schema.Properties != nil {
		for k, prop := range schema.Properties.All() {
			if prop.Left != nil {
				propPath := fmt.Sprintf("%s.properties.%s", path, k)
				if err := env.validateStrictResultPath(prop.Left, propPath); err != nil {
					return err
				}
			}
		}
	}

	// Recursively check additionalProperties
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Left != nil {
		if err := env.validateStrictResultPath(schema.AdditionalProperties.Left, path+".additionalProperties"); err != nil {
			return err
		}
	}

	// Recursively check anyOf branches
	if schema.AnyOf != nil {
		for i, branch := range schema.AnyOf {
			if branch.Left != nil {
				branchPath := fmt.Sprintf("%s.anyOf[%d]", path, i)
				if err := env.validateStrictResultPath(branch.Left, branchPath); err != nil {
					return err
				}
			}
		}
	}

	// Recursively check allOf branches
	if schema.AllOf != nil {
		for i, branch := range schema.AllOf {
			if branch.Left != nil {
				branchPath := fmt.Sprintf("%s.allOf[%d]", path, i)
				if err := env.validateStrictResultPath(branch.Left, branchPath); err != nil {
					return err
				}
			}
		}
	}

	// Recursively check oneOf branches
	if schema.OneOf != nil {
		for i, branch := range schema.OneOf {
			if branch.Left != nil {
				branchPath := fmt.Sprintf("%s.oneOf[%d]", path, i)
				if err := env.validateStrictResultPath(branch.Left, branchPath); err != nil {
					return err
				}
			}
		}
	}

	// Recursively check not
	if schema.Not != nil && schema.Not.Left != nil {
		if err := env.validateStrictResultPath(schema.Not.Left, path+".not"); err != nil {
			return err
		}
	}

	// Recursively check contains
	if schema.Contains != nil && schema.Contains.Left != nil {
		if err := env.validateStrictResultPath(schema.Contains.Left, path+".contains"); err != nil {
			return err
		}
	}

	// Recursively check unevaluatedProperties
	if schema.UnevaluatedProperties != nil && schema.UnevaluatedProperties.Left != nil {
		if err := env.validateStrictResultPath(schema.UnevaluatedProperties.Left, path+".unevaluatedProperties"); err != nil {
			return err
		}
	}

	return nil
}

// isTopSchema checks if a schema is Top using both pointer identity and structural checks.
func isTopSchema(s *oas3.Schema) bool {
	// Pointer identity check
	if s == Top() {
		return true
	}

	// Structural check: empty type with no constraints
	if getType(s) == "" &&
		s.Properties == nil &&
		s.AdditionalProperties == nil &&
		s.Enum == nil &&
		s.AllOf == nil &&
		s.AnyOf == nil &&
		s.OneOf == nil &&
		s.Not == nil &&
		s.Const == nil &&
		s.Format == nil &&
		s.Pattern == nil &&
		s.MinLength == nil &&
		s.MaxLength == nil &&
		s.Minimum == nil &&
		s.Maximum == nil &&
		s.Items == nil &&
		s.PrefixItems == nil {
		return true
	}

	return false
}

// isBottomSchema checks if a schema is Bottom using pointer identity.
func isBottomSchema(s *oas3.Schema) bool {
	return s == Bottom()
}

// materializeArrays recursively walks a schema and replaces arrays with
// their final accumulated versions using schema pointer tagging.
// EXTENDED: now also traverses anyOf/oneOf/allOf and Array Items/PrefixItems.
func (env *schemaEnv) materializeArrays(schema *oas3.Schema, accum map[string]*oas3.Schema, schemaToAlloc map[*oas3.Schema]string, redirect ...map[string]string) *oas3.Schema {
	if schema == nil {
		return nil
	}

	// Apply allocID redirect if provided
	var allocRedirect map[string]string
	if len(redirect) > 0 {
		allocRedirect = redirect[0]
	}

	// If this is an array, check if it's tagged OR if it IS a canonical array
	if getType(schema) == "array" {
		// First try: check if tagged
		if allocID, ok := schemaToAlloc[schema]; ok {
			// Apply redirect if this allocID should use a different one
			finalAllocID := allocID
			if allocRedirect != nil {
				if redirectTo, ok := allocRedirect[allocID]; ok {
					finalAllocID = redirectTo
					if env.opts.EnableWarnings {
						fmt.Printf("DEBUG materialize: redirecting %s -> %s for array lookup\n", allocID, redirectTo)
					}
				}
			}
			if canonical, ok2 := accum[finalAllocID]; ok2 {
				if env.opts.EnableWarnings {
					canonicalItems := ""
					canonHasItems := canonical.Items != nil && canonical.Items.Left != nil
					isEmpty := canonical.MaxItems != nil && *canonical.MaxItems == 0
					if canonHasItems {
						canonicalItems = getType(canonical.Items.Left)
					}
					schemaPtr := fmt.Sprintf("%p", schema)
					canonPtr := fmt.Sprintf("%p", canonical)
					env.addWarning("materialize: tagged array → canonical %s (items=%s, empty=%v, hasItems=%v, schemaPtr=%s, canonPtr=%s)",
						allocID, canonicalItems, isEmpty, canonHasItems, schemaPtr, canonPtr)
				}
				// Recurse into the canonical array’s items/prefixItems before returning
				arr := *canonical
				changed := false
				// Items
				if arr.Items != nil && arr.Items.Left != nil {
					newItems := env.materializeArrays(arr.Items.Left, accum, schemaToAlloc, allocRedirect)
					if newItems != arr.Items.Left {
						arr.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newItems)
						changed = true
					}
				}
				// PrefixItems
				if arr.PrefixItems != nil && len(arr.PrefixItems) > 0 {
					newPrefix := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(arr.PrefixItems))
					for _, pi := range arr.PrefixItems {
						if pi != nil && pi.Left != nil {
							newPi := env.materializeArrays(pi.Left, accum, schemaToAlloc, allocRedirect)
							newPrefix = append(newPrefix, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newPi))
							if newPi != pi.Left {
								changed = true
							}
						} else {
							newPrefix = append(newPrefix, pi)
						}
					}
					if changed {
						arr.PrefixItems = newPrefix
					}
				}
				if changed {
					return &arr
				}
				return canonical
			}
		}

		// Fallback: if this array has empty items, check if it IS a canonical array
		// (handles arrays created by Union that aren't tagged)
		hasEmptyItems := (schema.Items == nil || schema.Items.Left == nil || getType(schema.Items.Left) == "")
		if hasEmptyItems {
			for allocID, canonical := range accum {
				if canonical == schema {
					// This IS a canonical array - recurse into its internals
					arr := *canonical
					changed := false
					if arr.Items != nil && arr.Items.Left != nil {
						newItems := env.materializeArrays(arr.Items.Left, accum, schemaToAlloc, allocRedirect)
						if newItems != arr.Items.Left {
							arr.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newItems)
							changed = true
						}
					}
					if arr.PrefixItems != nil && len(arr.PrefixItems) > 0 {
						newPrefix := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(arr.PrefixItems))
						for _, pi := range arr.PrefixItems {
							if pi != nil && pi.Left != nil {
								newPi := env.materializeArrays(pi.Left, accum, schemaToAlloc, allocRedirect)
								newPrefix = append(newPrefix, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newPi))
								if newPi != pi.Left {
                                    changed = true
                                }
							} else {
								newPrefix = append(newPrefix, pi)
							}
						}
						if changed {
							arr.PrefixItems = newPrefix
						}
					}
					if env.opts.EnableWarnings {
						env.addWarning("materialize: array is canonical %s (no replacement needed beyond recursive materialization)", allocID)
					}
					if changed {
						return &arr
					}
					return canonical
				}
			}
			// Not found in accum - fall through and recurse into internals
		}

		// Un-tagged array: still recurse into Items/PrefixItems
		clone := *schema
		changed := false
		if clone.Items != nil && clone.Items.Left != nil {
			newItems := env.materializeArrays(clone.Items.Left, accum, schemaToAlloc)
			if newItems != clone.Items.Left {
				clone.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newItems)
				changed = true
			}
		}
		if clone.PrefixItems != nil && len(clone.PrefixItems) > 0 {
			newPrefix := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(clone.PrefixItems))
			for _, pi := range clone.PrefixItems {
				if pi != nil && pi.Left != nil {
					newPi := env.materializeArrays(pi.Left, accum, schemaToAlloc, allocRedirect)
					newPrefix = append(newPrefix, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newPi))
					if newPi != pi.Left {
						changed = true
					}
				} else {
					newPrefix = append(newPrefix, pi)
				}
			}
			if changed {
				clone.PrefixItems = newPrefix
			}
		}
		if changed {
			return &clone
		}
		// No change
		return schema
	}

	// Recursively materialize object properties and additionalProperties
	if getType(schema) == "object" && schema.Properties != nil {
		modified := false
		newProps := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for k, propSchema := range schema.Properties.All() {
			if propSchema.Left != nil {
				materialized := env.materializeArrays(propSchema.Left, accum, schemaToAlloc, allocRedirect)
				if materialized != propSchema.Left {
					modified = true
				}
				newProps.Set(k, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](materialized))
			} else {
				newProps.Set(k, propSchema)
			}
		}
		clone := *schema
		if modified {
			if env.opts.EnableWarnings {
				propCount := 0
				for range newProps.All() {
					propCount++
				}
				env.addWarning("materialize: reconstructed object with %d properties", propCount)
			}
			clone.Properties = newProps
		}
		// additionalProperties
		if clone.AdditionalProperties != nil && clone.AdditionalProperties.Left != nil {
			newAP := env.materializeArrays(clone.AdditionalProperties.Left, accum, schemaToAlloc, allocRedirect)
			if newAP != clone.AdditionalProperties.Left {
				clone.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newAP)
				return &clone
			}
		}
		if modified {
			return &clone
		}
		return schema
	}

	// NEW: Traverse union structures (anyOf, oneOf, allOf)
	// anyOf
	if schema.AnyOf != nil && len(schema.AnyOf) > 0 {
		changed := false
		newAny := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(schema.AnyOf))
		for _, br := range schema.AnyOf {
			if br != nil && br.Left != nil {
				newBr := env.materializeArrays(br.Left, accum, schemaToAlloc, allocRedirect)
				newAny = append(newAny, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newBr))
				if newBr != br.Left {
					changed = true
				}
			} else {
				newAny = append(newAny, br)
			}
		}
		if changed {
			clone := *schema
			clone.AnyOf = newAny
			return &clone
		}
	}
	// oneOf
	if schema.OneOf != nil && len(schema.OneOf) > 0 {
		changed := false
		newOne := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(schema.OneOf))
		for _, br := range schema.OneOf {
			if br != nil && br.Left != nil {
				newBr := env.materializeArrays(br.Left, accum, schemaToAlloc, allocRedirect)
				newOne = append(newOne, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newBr))
				if newBr != br.Left {
					changed = true
				}
			} else {
				newOne = append(newOne, br)
			}
		}
		if changed {
			clone := *schema
			clone.OneOf = newOne
			return &clone
		}
	}
	// allOf
	if schema.AllOf != nil && len(schema.AllOf) > 0 {
		changed := false
		newAll := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(schema.AllOf))
		for _, br := range schema.AllOf {
			if br != nil && br.Left != nil {
				newBr := env.materializeArrays(br.Left, accum, schemaToAlloc, allocRedirect)
				newAll = append(newAll, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newBr))
				if newBr != br.Left {
					changed = true
				}
			} else {
				newAll = append(newAll, br)
			}
		}
		if changed {
			clone := *schema
			clone.AllOf = newAll
			return &clone
		}
	}

	return schema
}



// ============================================================================
// State Merging for Branch Explosion Control
// ============================================================================

// shouldMergeAtPC returns true if we should perform state merging at this PC.
// We merge at natural join points (opIter, opForkLabel) and hot PCs (threshold >= 2).
func (env *schemaEnv) shouldMergeAtPC(pc int, groupSize int) bool {
	if pc < 0 || pc >= len(env.codes) {
		return false
	}
	op := env.codes[pc].op
	// Always merge at natural reconvergence points
	if op == opIter || op == opForkLabel || op == opForkTryEnd {
		return true
	}
	// Also merge at hot join points (lowered threshold from 4 to 2)
	return groupSize >= 2
}

// mergeFrontierByPC merges states at hot loop headers (opIter) to prevent
// exponential branch explosion from multiple conditionals.
func (env *schemaEnv) mergeFrontierByPC(in []*execState) []*execState {
	if len(in) <= 1 {
		return in
	}

	// Group by PC
	byPC := make(map[int][]*execState)
	for _, s := range in {
		byPC[s.pc] = append(byPC[s.pc], s)
	}

	out := make([]*execState, 0, len(in))
	for pc, group := range byPC {
		// Only merge at hot PCs (opIter or high fan-in)
		if !env.shouldMergeAtPC(pc, len(group)) {
			out = append(out, group...)
			continue
		}

		// Partition by shape
		// At hot merge points, we relax scope-key matching to allow more aggressive merging
		relaxScopeKeys := env.codes[pc].op != opIter || len(group) >= 8
		partitions := partitionByShape(group, relaxScopeKeys)

		for _, partition := range partitions {
			if len(partition) == 1 {
				out = append(out, partition[0])
				continue
			}

			// Fast-path for large partitions: skip O(n²) subsumption and just fold with LUB
			const mergeCap = 16
			if len(partition) > mergeCap {
				// Linear-time merge: fold all states using join (LUB)
				merged := partition[0]
				for i := 1; i < len(partition); i++ {
					merged = joinState(merged, partition[i])
				}
				out = append(out, merged)
				continue
			}

			// For smaller partitions: subsumption checking to remove redundant states
			kept := make([]*execState, 0, len(partition))
			for _, candidate := range partition {
				subsumed := false
				for _, other := range partition {
					if candidate == other {
						continue
					}
					if subsumesState(other, candidate) {
						subsumed = true
						break
					}
				}
				if !subsumed {
					kept = append(kept, candidate)
				}
			}

			// Join remaining states pairwise until convergence
			for len(kept) > 1 {
				a := kept[0]
				b := kept[1]
				merged := joinState(a, b)
				kept = append([]*execState{merged}, kept[2:]...)
			}

			out = append(out, kept...)
		}
	}

	return out
}

// subsumesState checks if state 'dom' subsumes state 'sub' (dom ⊒ sub).
// This means dom's abstract values are at least as general as sub's.
func subsumesState(dom, sub *execState) bool {
	if dom.pc != sub.pc {
		return false
	}
	if len(dom.stack) != len(sub.stack) {
		return false
	}
	if len(dom.scopes) != len(sub.scopes) {
		return false
	}

	// Check stack subsumption
	for i := range dom.stack {
		if !schemaSubsumes(dom.stack[i].Schema, sub.stack[i].Schema) {
			return false
		}
	}

	// Check scope subsumption
	// dom subsumes sub if for every variable in sub, dom has that variable
	// and dom's schema subsumes sub's schema for that variable.
	// dom may have additional variables that sub doesn't have.
	for i := range dom.scopes {
		domScope := dom.scopes[i]
		subScope := sub.scopes[i]

		// Check that all of sub's variables are subsumed by dom's
		for k, subVal := range subScope {
			domVal, domHas := domScope[k]
			if !domHas {
				// dom doesn't have this variable, so it can't subsume sub
				return false
			}
			if !schemaSubsumes(domVal, subVal) {
				return false
			}
		}
		// Note: dom may have extra variables, but that's ok for subsumption
	}

	return true
}

// schemaSubsumes checks if schema 'dom' subsumes 'sub' (dom ⊒ sub).
// We use the property: Union(dom, sub) == dom implies sub ⊑ dom.
func schemaSubsumes(dom, sub *oas3.Schema) bool {
	if dom == sub {
		return true
	}
	if dom == nil || sub == nil {
		return dom == sub
	}

	joined := Union([]*oas3.Schema{dom, sub}, SchemaExecOptions{})
	return schemaEqual(joined, dom)
}

// schemaEqual performs deep equality check on schemas.
func schemaEqual(a, b *oas3.Schema) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Use reflect for deep comparison
	return reflect.DeepEqual(a, b)
}

// accumMapsEqualByIdentity checks if two accum maps are identical by key set and pointer values
func accumMapsEqualByIdentity(m1, m2 map[string]*oas3.Schema) bool {
	if (m1 == nil) != (m2 == nil) {
		return false
	}
	if len(m1) != len(m2) {
		return false
	}
	for k, v1 := range m1 {
		v2, ok := m2[k]
		if !ok || v1 != v2 {
			return false
		}
	}
	return true
}

// joinState merges two states using lattice join (LUB).
// When scope keys differ, union them and treat missing keys as if they were present
// with the value from the other state (join with implicit "undefined" = keep existing).
func joinState(a, b *execState) *execState {
	if a.pc != b.pc {
		panic("joinState: PC mismatch")
	}
	if len(a.stack) != len(b.stack) {
		panic("joinState: stack length mismatch")
	}
	if len(a.scopes) != len(b.scopes) {
		panic("joinState: scope length mismatch")
	}

	// CRITICAL FIX: Use robust map equality check instead of single-sample-key
	// The old single-key check incorrectly treated maps as "same" when key sets didn't overlap
	differentAccumMaps := !accumMapsEqualByIdentity(a.accum, b.accum)

	if differentAccumMaps {

		// Merge the accum maps by taking the union
		// This handles the case where states from different forks have separate accumulators
		mergedAccum := make(map[string]*oas3.Schema)
		mergedSchemaToAlloc := make(map[*oas3.Schema]string)

		// Copy from a
		for k, v := range a.accum {
			mergedAccum[k] = v
		}
		for k, v := range a.schemaToAlloc {
			mergedSchemaToAlloc[k] = v
		}

		// Merge from b
		for k, v := range b.accum {
			if existing, ok := mergedAccum[k]; ok {
				// Both have this key - union the arrays
				if getType(existing) == "array" && getType(v) == "array" {
					// Union the items
					var existingItems, vItems *oas3.Schema
					if existing.Items != nil && existing.Items.Left != nil {
						existingItems = existing.Items.Left
					} else {
						existingItems = Bottom()
					}
					if v.Items != nil && v.Items.Left != nil {
						vItems = v.Items.Left
					} else {
						vItems = Bottom()
					}
					// Theory 10: Check cardinality to prefer non-empty arrays
					var aCard, bCard *ArrayCardinality
					if a.allocCardinality != nil {
						aCard = a.allocCardinality[k]
					}
					if b.allocCardinality != nil {
						bCard = b.allocCardinality[k]
					}

					// Determine if either is definitely non-empty (MinItems >= 1)
					aNonEmpty := aCard != nil && aCard.MinItems != nil && *aCard.MinItems >= 1
					bNonEmpty := bCard != nil && bCard.MinItems != nil && *bCard.MinItems >= 1

					// If one is non-empty and other is empty, prefer the non-empty one's items
					var unionedItems *oas3.Schema
					if aNonEmpty && !bNonEmpty && existingItems != Bottom() {
						// Prefer 'a' (existing) items since it's non-empty
						unionedItems = existingItems
					} else if bNonEmpty && !aNonEmpty && vItems != Bottom() {
						// Prefer 'b' (v) items since it's non-empty
						unionedItems = vItems
					} else {
						// Both non-empty or both maybe-empty - union them
						unionedItems = Union([]*oas3.Schema{existingItems, vItems}, SchemaExecOptions{})
					}

					// Create new canonical array with unioned items
					// Only set Items when unionedItems is non-nil to avoid invalid wrapper
					var merged *oas3.Schema
					if unionedItems == nil {
						// Both inputs had no items - preserve empty array constraint if both are empty
						existingEmpty := existing.MaxItems != nil && *existing.MaxItems == 0
						vEmpty := v.MaxItems != nil && *v.MaxItems == 0
						if existingEmpty && vEmpty {
							// Both are empty arrays - result is empty array
							merged = ArrayType(nil) // This creates maxItems=0 with Items=nil
						} else {
							// Keep an unconstrained array; do not create an invalid Items wrapper
							merged = &oas3.Schema{
								Type: oas3.NewTypeFromString(oas3.SchemaTypeArray),
							}
						}
					} else {
						merged = &oas3.Schema{
							Type:  oas3.NewTypeFromString(oas3.SchemaTypeArray),
							Items: oas3.NewJSONSchemaFromSchema[oas3.Referenceable](unionedItems),
						}
					}
					mergedAccum[k] = merged
					// CRITICAL FIX: Re-tag the new canonical pointer so opStore can find it
					mergedSchemaToAlloc[merged] = k
				}
			} else {
				mergedAccum[k] = v
			}
		}
		for k, v := range b.schemaToAlloc {
			if _, ok := mergedSchemaToAlloc[k]; !ok {
				mergedSchemaToAlloc[k] = v
			}
		}

		// Theory 10: Merge cardinality maps using lattice join
		mergedCardinality := make(map[string]*ArrayCardinality)
		if a.allocCardinality != nil {
			for k, v := range a.allocCardinality {
				mergedCardinality[k] = v
			}
		}
		if b.allocCardinality != nil {
			for k, v := range b.allocCardinality {
				if existing, ok := mergedCardinality[k]; ok {
					// Use lattice join
					if existing != nil && v != nil {
						mergedCardinality[k] = existing.Join(v)
					} else if v != nil {
						mergedCardinality[k] = v
					}
				} else {
					mergedCardinality[k] = v
				}
			}
		}

		merged := &execState{
			pc:        a.pc,
			stack:     make([]SValue, len(a.stack)),
			scopes:    make([]map[string]*oas3.Schema, len(a.scopes)),
			depth:     maxInt(a.depth, b.depth),
			callstack: a.callstack,
			id:        a.id,
			parentID:  a.parentID,
			lineage:   a.lineage,
			// Use merged accumulators
			accum:         mergedAccum,
			schemaToAlloc: mergedSchemaToAlloc,
			allocCounter:  a.allocCounter,
			// Theory 10: Hybrid Origin-Lattice
			allocOrigin:      a.allocOrigin,      // SHARED (use from either)
			allocCardinality: mergedCardinality,  // Merged with lattice join
			dsu:              a.dsu,              // SHARED (use from either)
		}

		// Merge var history and intent
		merged.varAllocHistory = make(map[string]map[string]struct{}, len(a.varAllocHistory)+len(b.varAllocHistory))
		for k, set := range a.varAllocHistory {
			dst := make(map[string]struct{}, len(set))
			for id := range set {
				dst[id] = struct{}{}
			}
			merged.varAllocHistory[k] = dst
		}
		for k, set := range b.varAllocHistory {
			dst, ok := merged.varAllocHistory[k]
			if !ok {
				dst = make(map[string]struct{}, len(set))
				merged.varAllocHistory[k] = dst
			}
			for id := range set {
				dst[id] = struct{}{}
			}
		}

		merged.varDesiredItemFP = make(map[string]string, len(a.varDesiredItemFP)+len(b.varDesiredItemFP))
		for k, fp := range a.varDesiredItemFP {
			merged.varDesiredItemFP[k] = fp
		}
		// Prefer non-empty FP when disagree
		for k, fp := range b.varDesiredItemFP {
			if old, ok := merged.varDesiredItemFP[k]; !ok || old == "" {
				merged.varDesiredItemFP[k] = fp
			}
		}

		// Merge allocDesiredFP (different-accum branch - create new merged map)
		merged.allocDesiredFP = make(map[string]string, len(a.allocDesiredFP)+len(b.allocDesiredFP))
		for id, fp := range a.allocDesiredFP {
			merged.allocDesiredFP[id] = fp
		}
		for id, fp := range b.allocDesiredFP {
			if _, ok := merged.allocDesiredFP[id]; !ok {
				merged.allocDesiredFP[id] = fp
			}
		}

		// Merge schemaFPIntent (pointer-level intent)
		merged.schemaFPIntent = make(map[*oas3.Schema]string, len(a.schemaFPIntent)+len(b.schemaFPIntent))
		for ptr, fp := range a.schemaFPIntent {
			merged.schemaFPIntent[ptr] = fp
		}
		for ptr, fp := range b.schemaFPIntent {
			if _, ok := merged.schemaFPIntent[ptr]; !ok {
				merged.schemaFPIntent[ptr] = fp
			}
		}

		// Join stack values
		for i := range a.stack {
			merged.stack[i] = SValue{Schema: joinTwoSchemas(a.stack[i].Schema, b.stack[i].Schema)}
		}

		// Join scopes
		for i := range a.scopes {
			mergedScope := make(map[string]*oas3.Schema)
			aScope := a.scopes[i]
			bScope := b.scopes[i]

			allKeys := make(map[string]bool)
			for k := range aScope {
				allKeys[k] = true
			}
			for k := range bScope {
				allKeys[k] = true
			}

			for k := range allKeys {
				aVal, aHas := aScope[k]
				bVal, bHas := bScope[k]

				// DEBUG: Log merging of map accumulator variables (added "[44 0]", "[9 1]", "[10 0]")
				if k == "[18 0]" || k == "[20 0]" || k == "[22 0]" || k == "[32 0]" || k == "[44 0]" || k == "[9 1]" || k == "[10 0]" {
					aEmpty := getType(aVal) == "array" && aVal.MaxItems != nil && *aVal.MaxItems == 0
					bEmpty := getType(bVal) == "array" && bVal.MaxItems != nil && *bVal.MaxItems == 0
					fmt.Printf("DEBUG scope-merge: var=%s aHas=%v bHas=%v aType=%s bType=%s aEmpty=%v bEmpty=%v\n",
						k, aHas, bHas, getType(aVal), getType(bVal), aEmpty, bEmpty)
				}

				if aHas && bHas {
					// CRITICAL FIX: Preserve canonical pointer for arrays in scope
					// This is symmetric to the stack preservation logic above
					if getType(aVal) == "array" && getType(bVal) == "array" {
						aAlloc, aTagged := a.schemaToAlloc[aVal]
						bAlloc, bTagged := b.schemaToAlloc[bVal]

						// Theory 10: Cross-state DSU union - if both tagged and same origin, union them
						if aTagged && bTagged {
							oa := a.allocOrigin[aAlloc]
							ob := b.allocOrigin[bAlloc]
							if oa != nil && ob != nil && oa.PC == ob.PC && oa.Context == ob.Context {
								// Same origin => union classes
								fmt.Printf("TRACE DSU joinState(diff-accum): var=%s union %s with %s (same origin PC=%d, ctx=%s)\n",
									k, aAlloc, bAlloc, oa.PC, oa.Context)
								a.dsu.Union(aAlloc, bAlloc)
								root := a.dsu.Find(aAlloc)

								// Merge canonical arrays and bind canonical to merged scope
								joined := joinTwoSchemas(aVal, bVal)
								if joined != nil {
									merged.accum[root] = joined
									merged.schemaToAlloc[joined] = root
									mergedScope[k] = joined
									fmt.Printf("TRACE DSU joinState(diff-accum): merged to root=%s\n", root)
								}

								// Lattice-join cardinality under root
								var c1, c2 *ArrayCardinality
								if a.allocCardinality != nil {
									c1 = a.allocCardinality[aAlloc]
								}
								if b.allocCardinality != nil {
									c2 = b.allocCardinality[bAlloc]
								}
								var joinedCard *ArrayCardinality
								switch {
								case c1 == nil:
									joinedCard = c2
								case c2 == nil:
									joinedCard = c1
								default:
									joinedCard = c1.Join(c2)
								}
								if joinedCard != nil {
									merged.allocCardinality[root] = joinedCard
									if joinedCard.MinItems != nil {
										fmt.Printf("TRACE DSU joinState(diff-accum): cardinality root=%s MinItems=%d\n",
											root, *joinedCard.MinItems)
									}
								}

								// Alias old IDs to the root's canonical for robustness
								merged.accum[aAlloc] = joined
								merged.accum[bAlloc] = joined

								// We handled this var binding; continue to next key
								continue
							}
						}

						isEmpty := func(s *oas3.Schema) bool {
							return s != nil && getType(s) == "array" && s.MaxItems != nil && *s.MaxItems == 0
						}

						// DEBUG: Check isEmpty for tracked vars
						if k == "[9 1]" || k == "[10 0]" || k == "[10 2]" {
							fmt.Printf("DEBUG scope-merge (diff-accum): var=%s isEmpty(a)=%v isEmpty(b)=%v\n", k, isEmpty(aVal), isEmpty(bVal))
						}

						// Prefer non-empty over empty, regardless of tagging
						// This prevents empty arrays from clobbering real data during merges
						if isEmpty(aVal) && !isEmpty(bVal) {
							mergedScope[k] = bVal
							// Also update accumulator canonical if bVal is tagged
							if bTagged {
								mergedAccum[bAlloc] = bVal
							}
							fmt.Printf("DEBUG scope-merge (diff-accum): var=%s preferring non-empty b over empty a (bTagged=%v, bAlloc=%s)\n", k, bTagged, bAlloc)
							continue
						}
						if isEmpty(bVal) && !isEmpty(aVal) {
							mergedScope[k] = aVal
							// Also update accumulator canonical if aVal is tagged
							if aTagged {
								mergedAccum[aAlloc] = aVal
							}
							fmt.Printf("DEBUG scope-merge (diff-accum): var=%s preferring non-empty a over empty b (aTagged=%v, aAlloc=%s)\n", k, aTagged, aAlloc)
							continue
						}

						// Case 1: Both tagged with same allocID - use canonical
						if aTagged && bTagged && aAlloc == bAlloc {
							if canonical, ok := merged.accum[aAlloc]; ok {
								if _, tagged := merged.schemaToAlloc[canonical]; !tagged {
									merged.schemaToAlloc[canonical] = aAlloc
								}
								mergedScope[k] = canonical
								// Alias both IDs to canonical (defensive)
								if aTagged {
									merged.accum[aAlloc] = canonical
								}
								if bTagged {
									merged.accum[bAlloc] = canonical
								}
								if k == "[18 0]" || k == "[20 0]" || k == "[22 0]" || k == "[32 0]" || k == "[44 0]" {
									fmt.Printf("DEBUG scope-merge Case1: var=%s aAlloc=%s bAlloc=%s → canonical\n", k, aAlloc, bAlloc)
								}
								continue
							}
						}

						// Case 2: One tagged canonical, other empty - prefer canonical
						if aTagged && isEmpty(bVal) {
							if canonical, ok := merged.accum[aAlloc]; ok {
								if _, tagged := merged.schemaToAlloc[canonical]; !tagged {
									merged.schemaToAlloc[canonical] = aAlloc
								}
								mergedScope[k] = canonical
								// Alias both IDs to canonical
								merged.accum[aAlloc] = canonical
								if bTagged {
									merged.accum[bAlloc] = canonical
								}
								if k == "[18 0]" || k == "[20 0]" || k == "[22 0]" || k == "[32 0]" || k == "[44 0]" {
									fmt.Printf("DEBUG scope-merge Case2a: var=%s aAlloc=%s bAlloc=%s → canonical from a\n", k, aAlloc, bAlloc)
								}
								continue
							}
						}
						if bTagged && isEmpty(aVal) {
							if canonical, ok := merged.accum[bAlloc]; ok {
								if _, tagged := merged.schemaToAlloc[canonical]; !tagged {
									merged.schemaToAlloc[canonical] = bAlloc
								}
								mergedScope[k] = canonical
								// Alias both IDs to canonical
								merged.accum[bAlloc] = canonical
								if aTagged {
									merged.accum[aAlloc] = canonical
								}
								if k == "[18 0]" || k == "[20 0]" || k == "[22 0]" || k == "[32 0]" || k == "[44 0]" {
									fmt.Printf("DEBUG scope-merge Case2b: var=%s aAlloc=%s bAlloc=%s → canonical from b\n", k, aAlloc, bAlloc)
								}
								continue
							}
						}

						// Case 3: Join arrays and tag the result
						joined := joinTwoSchemas(aVal, bVal)
						if getType(joined) == "array" {
							if _, tagged := merged.schemaToAlloc[joined]; !tagged {
								*merged.allocCounter++
								id := fmt.Sprintf("alloc%d", *merged.allocCounter)
								merged.accum[id] = joined
								merged.schemaToAlloc[joined] = id
								// Propagate alloc intent from sources to new joined alloc
								propagated := false
								if aTagged {
									if fp, ok := merged.allocDesiredFP[aAlloc]; ok && fp != "" {
										merged.allocDesiredFP[id] = fp
										propagated = true
									}
								}
								if bTagged && !propagated {
									if fp, ok := merged.allocDesiredFP[bAlloc]; ok && fp != "" {
										merged.allocDesiredFP[id] = fp
										propagated = true
									}
								}
								if propagated {
									fmt.Printf("DEBUG Case3 (diff-accum): propagated intent to new alloc %s for var=%s\n", id, k)
								}
							}
							// Propagate pointer-intent to the joined pointer
							if merged.schemaFPIntent == nil {
								merged.schemaFPIntent = make(map[*oas3.Schema]string)
							}
							if fp, ok := a.schemaFPIntent[aVal]; ok && fp != "" {
								merged.schemaFPIntent[joined] = fp
							} else if fp, ok := b.schemaFPIntent[bVal]; ok && fp != "" {
								merged.schemaFPIntent[joined] = fp
							} else if aTagged {
								// Fallback to alloc-intent
								if fp, ok := merged.allocDesiredFP[aAlloc]; ok && fp != "" {
									merged.schemaFPIntent[joined] = fp
								}
							} else if bTagged {
								if fp, ok := merged.allocDesiredFP[bAlloc]; ok && fp != "" {
									merged.schemaFPIntent[joined] = fp
								}
							}
						}
						mergedScope[k] = joined
						// Alias both original IDs to the new joined canonical
						if aTagged {
							merged.accum[aAlloc] = joined
						}
						if bTagged {
							merged.accum[bAlloc] = joined
						}
						if k == "[18 0]" || k == "[20 0]" || k == "[22 0]" || k == "[32 0]" || k == "[44 0]" {
							joinedEmpty := joined.MaxItems != nil && *joined.MaxItems == 0
							joinedHasItems := joined.Items != nil && joined.Items.Left != nil
							fmt.Printf("DEBUG scope-merge Case3: var=%s aAlloc=%s bAlloc=%s → joined (empty=%v, hasItems=%v)\n",
								k, aAlloc, bAlloc, joinedEmpty, joinedHasItems)
						}
						continue
					}

					// Non-array or only one side array: default join
					mergedScope[k] = joinTwoSchemas(aVal, bVal)
				} else if aHas {
					mergedScope[k] = aVal
				} else {
					mergedScope[k] = bVal
				}
			}

			merged.scopes[i] = mergedScope
		}

		return merged
	}

	// States have same accum map - normal join
	merged := &execState{
		pc:        a.pc,
		stack:     make([]SValue, len(a.stack)),
		scopes:    make([]map[string]*oas3.Schema, len(a.scopes)),
		depth:     maxInt(a.depth, b.depth),
		callstack: a.callstack, // Assume same callstack in partition
		id:        a.id,
		parentID:  a.parentID,
		lineage:   a.lineage,
		// Preserve shared fields from first state
		accum:         a.accum,
		schemaToAlloc: a.schemaToAlloc,
		allocCounter:  a.allocCounter,
		// Theory 10: Hybrid Origin-Lattice (SHARED since accum is same)
		allocOrigin:      a.allocOrigin,
		allocCardinality: a.allocCardinality,
		dsu:              a.dsu,
	}

	// Merge var history and intent (same-accum branch)
	merged.varAllocHistory = make(map[string]map[string]struct{}, len(a.varAllocHistory)+len(b.varAllocHistory))
	for k, set := range a.varAllocHistory {
		dst := make(map[string]struct{}, len(set))
		for id := range set {
			dst[id] = struct{}{}
		}
		merged.varAllocHistory[k] = dst
	}
	for k, set := range b.varAllocHistory {
		dst, ok := merged.varAllocHistory[k]
		if !ok {
			dst = make(map[string]struct{}, len(set))
			merged.varAllocHistory[k] = dst
		}
		for id := range set {
			dst[id] = struct{}{}
		}
	}

	merged.varDesiredItemFP = make(map[string]string, len(a.varDesiredItemFP)+len(b.varDesiredItemFP))
	for k, fp := range a.varDesiredItemFP {
		merged.varDesiredItemFP[k] = fp
	}
	// Prefer non-empty FP when disagree
	for k, fp := range b.varDesiredItemFP {
		if old, ok := merged.varDesiredItemFP[k]; !ok || old == "" {
			merged.varDesiredItemFP[k] = fp
		}
	}

	// allocDesiredFP is SHARED when accum is shared - use a's map and merge b's entries
	merged.allocDesiredFP = a.allocDesiredFP
	if merged.allocDesiredFP == nil {
		merged.allocDesiredFP = make(map[string]string)
	}
	// Merge in b's entries (mutates shared map)
	for id, fp := range b.allocDesiredFP {
		if _, ok := merged.allocDesiredFP[id]; !ok {
			merged.allocDesiredFP[id] = fp
		}
	}

	// Merge schemaFPIntent (pointer-level intent, same-accum branch)
	merged.schemaFPIntent = make(map[*oas3.Schema]string, len(a.schemaFPIntent)+len(b.schemaFPIntent))
	for ptr, fp := range a.schemaFPIntent {
		merged.schemaFPIntent[ptr] = fp
	}
	for ptr, fp := range b.schemaFPIntent {
		if _, ok := merged.schemaFPIntent[ptr]; !ok {
			merged.schemaFPIntent[ptr] = fp
		}
	}

	// CRITICAL FIX: Merge schemaToAlloc even when accum maps are "same"
	// This ensures tags from both states are preserved
	for ptr, id := range b.schemaToAlloc {
		if _, ok := merged.schemaToAlloc[ptr]; !ok {
			merged.schemaToAlloc[ptr] = id
		}
	}

	// Join stack values
	for i := range a.stack {
		aSchema := a.stack[i].Schema
		bSchema := b.stack[i].Schema

		// CRITICAL FIX: Preserve pointer identity for accumulator arrays
		// If both sides are arrays with the same allocID, use the canonical from merged.accum
		// instead of creating a new union. This prevents losing the tag.
		if getType(aSchema) == "array" && getType(bSchema) == "array" {
			aAlloc, aTagged := a.schemaToAlloc[aSchema]
			bAlloc, bTagged := b.schemaToAlloc[bSchema]
			if aTagged && bTagged && aAlloc == bAlloc {
				// Both refer to the same accumulator - use the canonical
				if canonical, exists := merged.accum[aAlloc]; exists {
					// Verify the canonical is tagged in merged.schemaToAlloc
					if taggedAlloc, isTagged := merged.schemaToAlloc[canonical]; isTagged {
						fmt.Printf("DEBUG joinState: preserving canonical ptr for allocID=%s (stack pos %d), canonical IS tagged as %s\n", aAlloc, i, taggedAlloc)
					} else {
						fmt.Printf("DEBUG joinState: preserving canonical ptr for allocID=%s (stack pos %d), canonical NOT TAGGED! Re-tagging now.\n", aAlloc, i)
						merged.schemaToAlloc[canonical] = aAlloc
					}
					merged.stack[i] = SValue{Schema: canonical}
					continue
				}
			}
		}

		// Default: join via union
		merged.stack[i] = SValue{Schema: joinTwoSchemas(aSchema, bSchema)}
	}

	// Join scopes (union keys, join shared values)
	for i := range a.scopes {
		mergedScope := make(map[string]*oas3.Schema)
		aScope := a.scopes[i]
		bScope := b.scopes[i]

		// Add all keys from both scopes
		allKeys := make(map[string]bool)
		for k := range aScope {
			allKeys[k] = true
		}
		for k := range bScope {
			allKeys[k] = true
		}

		// Join each key
		for k := range allKeys {
			aVal, aHas := aScope[k]
			bVal, bHas := bScope[k]

			// DEBUG: Log merging of map accumulator variables (added "[44 0]", "[9 1]", "[10 0]")
			if k == "[18 0]" || k == "[20 0]" || k == "[22 0]" || k == "[32 0]" || k == "[44 0]" || k == "[9 1]" || k == "[10 0]" {
				aEmpty := getType(aVal) == "array" && aVal.MaxItems != nil && *aVal.MaxItems == 0
				bEmpty := getType(bVal) == "array" && bVal.MaxItems != nil && *bVal.MaxItems == 0
				fmt.Printf("DEBUG scope-merge (same-accum): var=%s aHas=%v bHas=%v aType=%s bType=%s aEmpty=%v bEmpty=%v\n",
					k, aHas, bHas, getType(aVal), getType(bVal), aEmpty, bEmpty)
			}

			if aHas && bHas {
				// CRITICAL FIX: Preserve canonical pointer for arrays in scope (same-accum branch)
				if getType(aVal) == "array" && getType(bVal) == "array" {
					aAlloc, aTagged := a.schemaToAlloc[aVal]
					bAlloc, bTagged := b.schemaToAlloc[bVal]

					// Theory 10: Cross-state DSU union - if both tagged and same origin, union them
					if aTagged && bTagged {
						oa := a.allocOrigin[aAlloc]
						ob := b.allocOrigin[bAlloc]
						if oa != nil && ob != nil && oa.PC == ob.PC && oa.Context == ob.Context {
							// Same origin => union classes
							fmt.Printf("TRACE DSU joinState(same-accum): var=%s union %s with %s (same origin PC=%d, ctx=%s)\n",
								k, aAlloc, bAlloc, oa.PC, oa.Context)
							a.dsu.Union(aAlloc, bAlloc)
							root := a.dsu.Find(aAlloc)

							// Merge canonical arrays and bind canonical to merged scope
							joined := joinTwoSchemas(aVal, bVal)
							if joined != nil {
								merged.accum[root] = joined
								merged.schemaToAlloc[joined] = root
								mergedScope[k] = joined
								fmt.Printf("TRACE DSU joinState(same-accum): merged to root=%s\n", root)
							}

							// Lattice-join cardinality under root
							var c1, c2 *ArrayCardinality
							if a.allocCardinality != nil {
								c1 = a.allocCardinality[aAlloc]
							}
							if b.allocCardinality != nil {
								c2 = b.allocCardinality[bAlloc]
							}
							var joinedCard *ArrayCardinality
							switch {
							case c1 == nil:
								joinedCard = c2
							case c2 == nil:
								joinedCard = c1
							default:
								joinedCard = c1.Join(c2)
							}
							if joinedCard != nil {
								merged.allocCardinality[root] = joinedCard
								if joinedCard.MinItems != nil {
									fmt.Printf("TRACE DSU joinState(same-accum): cardinality root=%s MinItems=%d\n",
										root, *joinedCard.MinItems)
								}
							}

							// Alias old IDs to the root's canonical for robustness
							merged.accum[aAlloc] = joined
							merged.accum[bAlloc] = joined

							// We handled this var binding; continue to next key
							continue
						}
					}

					isEmpty := func(s *oas3.Schema) bool {
						return s != nil && getType(s) == "array" && s.MaxItems != nil && *s.MaxItems == 0
					}

					// DEBUG: Check isEmpty for tracked vars
					if k == "[9 1]" || k == "[10 0]" || k == "[10 2]" {
						fmt.Printf("DEBUG scope-merge: var=%s isEmpty(a)=%v isEmpty(b)=%v\n", k, isEmpty(aVal), isEmpty(bVal))
					}

					// Prefer non-empty over empty, regardless of tagging
					// This prevents empty arrays from clobbering real data during merges
					if isEmpty(aVal) && !isEmpty(bVal) {
						mergedScope[k] = bVal
						// Also update accumulator canonical if bVal is tagged
						if bTagged {
							merged.accum[bAlloc] = bVal
						}
						fmt.Printf("DEBUG scope-merge (same-accum): var=%s preferring non-empty b over empty a (bTagged=%v, bAlloc=%s)\n", k, bTagged, bAlloc)
						continue
					}
					if isEmpty(bVal) && !isEmpty(aVal) {
						mergedScope[k] = aVal
						// Also update accumulator canonical if aVal is tagged
						if aTagged {
							merged.accum[aAlloc] = aVal
						}
						fmt.Printf("DEBUG scope-merge (same-accum): var=%s preferring non-empty a over empty b (aTagged=%v, aAlloc=%s)\n", k, aTagged, aAlloc)
						continue
					}

					// Case 1: Both tagged with same allocID - use canonical
					if aTagged && bTagged && aAlloc == bAlloc {
						if canonical, ok := merged.accum[aAlloc]; ok {
							if _, tagged := merged.schemaToAlloc[canonical]; !tagged {
								merged.schemaToAlloc[canonical] = aAlloc
							}
							mergedScope[k] = canonical
							// Alias both IDs to canonical (defensive, already same ID)
							if aTagged {
								merged.accum[aAlloc] = canonical
							}
							if bTagged {
								merged.accum[bAlloc] = canonical
							}
							continue
						}
					}

					// Case 2: One tagged canonical, other empty - prefer canonical
					if aTagged && isEmpty(bVal) {
						if canonical, ok := merged.accum[aAlloc]; ok {
							if _, tagged := merged.schemaToAlloc[canonical]; !tagged {
								merged.schemaToAlloc[canonical] = aAlloc
							}
							mergedScope[k] = canonical
							// Alias both IDs to canonical
							merged.accum[aAlloc] = canonical
							if bTagged {
								merged.accum[bAlloc] = canonical
							}
							continue
						}
					}
					if bTagged && isEmpty(aVal) {
						if canonical, ok := merged.accum[bAlloc]; ok {
							if _, tagged := merged.schemaToAlloc[canonical]; !tagged {
								merged.schemaToAlloc[canonical] = bAlloc
							}
							mergedScope[k] = canonical
							// Alias both IDs to canonical
							merged.accum[bAlloc] = canonical
							if aTagged {
								merged.accum[aAlloc] = canonical
							}
							continue
						}
					}

					// Case 3: Join arrays and tag the result
					joined := joinTwoSchemas(aVal, bVal)
					if getType(joined) == "array" {
						if _, tagged := merged.schemaToAlloc[joined]; !tagged {
							*merged.allocCounter++
							id := fmt.Sprintf("alloc%d", *merged.allocCounter)
							merged.accum[id] = joined
							merged.schemaToAlloc[joined] = id
							// Propagate alloc intent from sources to new joined alloc
							propagated := false
							if aTagged {
								if fp, ok := merged.allocDesiredFP[aAlloc]; ok && fp != "" {
									merged.allocDesiredFP[id] = fp
									propagated = true
								}
							}
							if bTagged && !propagated {
								if fp, ok := merged.allocDesiredFP[bAlloc]; ok && fp != "" {
									merged.allocDesiredFP[id] = fp
									propagated = true
								}
							}
							if propagated {
								fmt.Printf("DEBUG Case3 (same-accum): propagated intent to new alloc %s for var=%s\n", id, k)
							}
							// Propagate pointer-intent to the joined pointer
							if merged.schemaFPIntent == nil {
								merged.schemaFPIntent = make(map[*oas3.Schema]string)
							}
							if fp, ok := a.schemaFPIntent[aVal]; ok && fp != "" {
								merged.schemaFPIntent[joined] = fp
							} else if fp, ok := b.schemaFPIntent[bVal]; ok && fp != "" {
								merged.schemaFPIntent[joined] = fp
							} else if aTagged {
								// Fallback to alloc-intent
								if fp, ok := merged.allocDesiredFP[aAlloc]; ok && fp != "" {
									merged.schemaFPIntent[joined] = fp
								}
							} else if bTagged {
								if fp, ok := merged.allocDesiredFP[bAlloc]; ok && fp != "" {
									merged.schemaFPIntent[joined] = fp
								}
							}
						}
					}
					mergedScope[k] = joined
					// Alias both original IDs to the new joined canonical
					if aTagged {
						merged.accum[aAlloc] = joined
					}
					if bTagged {
						merged.accum[bAlloc] = joined
					}
					continue
				}

				// Non-array or only one side array: default join
				mergedScope[k] = joinTwoSchemas(aVal, bVal)
			} else if aHas {
				// Only a has it: keep a's value
				mergedScope[k] = aVal
			} else {
				// Only b has it: keep b's value
				mergedScope[k] = bVal
			}
		}

		merged.scopes[i] = mergedScope
	}

	return merged
}


// joinTwoSchemas performs schema-level join (LUB) using Union.
func joinTwoSchemas(a, b *oas3.Schema) *oas3.Schema {
	// CRITICAL FIX: If pointers are identical, return immediately to preserve pointer identity
	// This prevents unnecessary cloning and preserves tags in schemaToAlloc
	if a == b {
		return a
	}

	return Union([]*oas3.Schema{a, b}, SchemaExecOptions{})
}

// partitionByShape groups states with compatible layouts.
// If relaxScopeKeys is true, ignore scope variable names in the shape key
// to allow more aggressive merging at hot join points.
func partitionByShape(states []*execState, relaxScopeKeys bool) [][]*execState {
	groups := make(map[string][]*execState)
	for _, s := range states {
		key := shapeKey(s, relaxScopeKeys)
		groups[key] = append(groups[key], s)
	}

	result := make([][]*execState, 0, len(groups))
	for _, group := range groups {
		result = append(result, group)
	}
	return result
}

// shapeKey generates a deterministic key encoding the state's structural shape.
// If relaxScopeKeys is true, only include scope frame counts, not variable names.
func shapeKey(s *execState, relaxScopeKeys bool) string {
	var buf strings.Builder

	// Stack length
	buf.WriteString("stack:")
	buf.WriteString(strconv.Itoa(len(s.stack)))
	buf.WriteString(";")

	// Scope frame count and keys (conditionally)
	buf.WriteString("scopes:")
	buf.WriteString(strconv.Itoa(len(s.scopes)))
	buf.WriteString(";")

	if !relaxScopeKeys {
		// Include variable names for precise partitioning
		for i, scope := range s.scopes {
			keys := make([]string, 0, len(scope))
			for k := range scope {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			buf.WriteString("scope")
			buf.WriteString(strconv.Itoa(i))
			buf.WriteString(":[")
			buf.WriteString(strings.Join(keys, ","))
			buf.WriteString("];")
		}
	} else {
		// Just include counts for relaxed merging
		for i, scope := range s.scopes {
			buf.WriteString("scope")
			buf.WriteString(strconv.Itoa(i))
			buf.WriteString(":count=")
			buf.WriteString(strconv.Itoa(len(scope)))
			buf.WriteString(";")
		}
	}

	// Callstack
	buf.WriteString("callstack:")
	buf.WriteString(intSliceKey(s.callstack))

	return buf.String()
}

// intSliceKey generates a string key for an int slice.
func intSliceKey(ints []int) string {
	strs := make([]string, len(ints))
	for i, v := range ints {
		strs[i] = strconv.Itoa(v)
	}
	return "[" + strings.Join(strs, ",") + "]"
}

// maxInt returns the maximum of two integers.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
