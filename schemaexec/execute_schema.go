package schemaexec

import (
	"context"
	"crypto/sha256"
	"errors"
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
	warnings []string
	logger   Logger // Logger for debug tracing
	execID   string // Unique execution ID
	strict   bool   // If true, fail on unsupported ops and Top/Bottom results

	// Tracks why a Top schema was created during execution.
	// Keyed by the exact Top() schema pointer identity.
	topCauses    map[*oas3.Schema]string
	norm         *normCtx
	loopHeads    map[string]*loopHeadState
	foreachSeeds map[int][]foreachMarkerLocation
}

type loopHeadState struct {
	state   *execState
	rounds  int
	widened bool
}

type foreachMarkerLocation struct {
	pc     int
	marker gojq.SchemaForeachMarker
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

	// Attach the resolved logger to the options copy so package-level schema
	// operations (Union, merges, ...) can log through it.
	opts.logger = logger
	if opts.norm == nil {
		opts.norm = newNormCtxForSemantics(ctx, opts.Semantics)
	}
	opts.norm.semantics = opts.Semantics
	opts.norm.logger = logger

	return &schemaEnv{
		ctx:      ctx,
		opts:     opts,
		warnings: make([]string, 0),
		logger:   logger,
		execID:   execID,
		strict:   opts.StrictMode,
		norm:     opts.norm,
	}
}

func (env *schemaEnv) normalizationContext() context.Context {
	return withNormCtx(env.ctx, env.norm)
}

// resolvedLeft returns the concrete schema a wrapper denotes, following the
// wrapper's resolved $ref (populated by openapi.ResolveAllReferences) before
// falling back to the inline Left schema.
//
// IMPORTANT: reference resolution state lives on the WRAPPER
// (JSONSchema[Referenceable]), not on the inline Left schema. For a $ref, Left
// is a bare "$ref shell" (only Ref set). Rebuilding wrappers with
// NewJSONSchemaFromSchema(child.Left) discards the wrapper's resolution caches,
// so any code that reconstructs child wrappers MUST look through the resolved
// schema first, or $ref children degrade into shells that read as untyped
// downstream (widening to Top, or worse, "definitely missing" in property
// lookups).
func resolvedLeft(js *oas3.JSONSchema[oas3.Referenceable]) *oas3.Schema {
	if js == nil {
		return nil
	}
	if resolved := js.GetResolvedSchema(); resolved != nil {
		if s := resolved.GetLeft(); s != nil {
			return s
		}
	}
	if value, ok := resolvedBooleanSchema(js); ok {
		if value {
			return Top()
		}
		return Bottom()
	}
	return js.Left
}

func resolvedBooleanSchema(js *oas3.JSONSchema[oas3.Referenceable]) (bool, bool) {
	if js == nil {
		return false, false
	}
	if resolved := js.GetResolvedSchema(); resolved != nil {
		if value := resolved.GetRight(); value != nil {
			return *value, true
		}
	}
	if js.Right != nil {
		return *js.Right, true
	}
	return false, false
}

// dispatchType returns the type used to dispatch navigation (property access,
// iteration, indexing) for a schema, honoring the configured SchemaSemantics:
// Speakeasy mode consults structural inference for untyped schemas, Raw mode
// requires an explicit type (untyped schemas conservatively widen at the
// dispatch site).
func (env *schemaEnv) dispatchType(s *oas3.Schema) string {
	if env.opts.Semantics == SchemaSemanticsRaw {
		return getTypeExplicit(s)
	}
	return getType(s)
}

// derefJSONSchema attempts to dereference a JSONSchema wrapper to get the actual Schema.
// Tries GetResolvedSchema() first for $ref cases, then falls back to inline Left schemas.
// Uses the provided normCtx for cycle-aware collapsing.
// Returns (schema, true) if successful, (nil, false) if unresolved or invalid.
func derefJSONSchema(ctx context.Context, js *oas3.JSONSchema[oas3.Referenceable]) (*oas3.Schema, bool) {
	if js == nil {
		return nil, false
	}
	if value, ok := resolvedBooleanSchema(js); ok {
		if value {
			return Top(), true
		}
		return Bottom(), true
	}

	// 1) Try GetResolvedSchema() first (handles $refs after ResolveAllReferences).
	if resolved := js.GetResolvedSchema(); resolved != nil {
		if schema := resolved.GetLeft(); schema != nil {
			collapsed, err := normalizeSchema(ctx, schema)
			if err != nil {
				return nil, false
			}
			return collapsed, true
		}
	}

	// 2) Fall back to inline Left if GetResolvedSchema didn't work.
	// This handles schemas created by wrapping with NewJSONSchemaFromSchema.
	if js.Left != nil {
		s := js.Left
		if s.IsReference() {
			// Unresolved $ref shell: the wrapper has no resolution state and
			// the inline schema is just a pointer (Ref set, no structure).
			// Treating it as an inline schema would make it read as untyped —
			// or as "definitely missing" in property lookups, which is
			// UNSOUND (discards the referenced schema's values). Report
			// failure so callers widen to Top instead.
			return nil, false
		}
		collapsed, err := normalizeSchema(ctx, s)
		if err != nil {
			return nil, false
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
	ctx        context.Context
	memo       map[*oas3.Schema]*oas3.Schema
	normalized map[*oas3.Schema]struct{}
	logger     Logger
	semantics  SchemaSemantics
}

func newNormCtx(ctx context.Context) *normCtx {
	if ctx == nil {
		ctx = context.Background()
	}
	return &normCtx{
		ctx:        ctx,
		memo:       make(map[*oas3.Schema]*oas3.Schema, 256),
		normalized: make(map[*oas3.Schema]struct{}, 256),
	}
}

func newNormCtxForSemantics(ctx context.Context, semantics SchemaSemantics) *normCtx {
	nctx := newNormCtx(ctx)
	nctx.semantics = semantics
	return nctx
}

func (nctx *normCtx) schemaType(schema *oas3.Schema) string {
	if nctx != nil && nctx.semantics == SchemaSemanticsRaw {
		return getTypeExplicit(schema)
	}
	return getType(schema)
}

func (nctx *normCtx) debugf(format string, args ...any) {
	if nctx != nil && nctx.logger != nil {
		nctx.logger.Debugf(format, args...)
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
	ctx := context.Background()
	return withNormCtx(ctx, newNormCtx(ctx))
}

func collapseContextForOptions(opts SchemaExecOptions) context.Context {
	if opts.norm == nil {
		ctx := context.Background()
		return withNormCtx(ctx, newNormCtxForSemantics(ctx, opts.Semantics))
	}
	return withNormCtx(opts.norm.ctx, opts.norm)
}

// normalizeSchema collapses allOf and anyOf while rebuilding every reachable
// child wrapper in a single memoized traversal. The result shell is registered
// before descent, so recursive inputs become recursive normalized graphs.
func normalizeSchema(ctx context.Context, schema *oas3.Schema) (*oas3.Schema, error) {
	nctx := getNormCtx(ctx)
	if nctx == nil {
		nctx = newNormCtx(ctx)
		ctx = withNormCtx(ctx, nctx)
	}
	if schema == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, ok := nctx.normalized[schema]; ok {
		return schema, nil
	}
	if result, ok := nctx.memo[schema]; ok {
		return result, nil
	}

	result := new(oas3.Schema)
	nctx.memo[schema] = result
	nctx.normalized[result] = struct{}{}
	*result = *schema

	if err := normalizeSchemaChildren(ctx, result); err != nil {
		return nil, err
	}
	if expandSchemaTypeArray(result) {
		bottom, err := normalizeExpandedTypeBranches(ctx, result, nctx)
		if err != nil {
			return nil, err
		}
		if bottom {
			nctx.memo[schema] = nil
			return nil, nil
		}
		return result, nil
	}
	bottom, err := collapseNormalizedCombinators(ctx, result, nctx)
	if err != nil {
		return nil, err
	}
	if bottom {
		nctx.memo[schema] = nil
		return nil, nil
	}
	return result, nil
}

// expandSchemaTypeArray represents a JSON Schema type union using the same
// disjunctive shape the executor already distributes over. Each branch keeps
// the original facets: JSON Schema keywords only constrain instances of the
// types to which they apply, so retaining (for example) items on the null
// branch is semantically neutral while preserving it on the array branch.
func expandSchemaTypeArray(schema *oas3.Schema) bool {
	types := append([]oas3.SchemaType(nil), schema.GetType()...)
	if len(types) <= 1 {
		return false
	}
	sort.Slice(types, func(i, j int) bool { return types[i] < types[j] })
	unique := types[:0]
	for _, typ := range types {
		if len(unique) == 0 || unique[len(unique)-1] != typ {
			unique = append(unique, typ)
		}
	}
	if len(unique) == 1 {
		schema.Type = oas3.NewTypeFromString(unique[0])
		return false
	}

	base := cloneSchema(schema)
	branches := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(unique))
	for _, typ := range unique {
		branch := cloneSchema(base)
		branch.Type = oas3.NewTypeFromString(typ)
		branches = append(branches, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branch))
	}
	*schema = oas3.Schema{AnyOf: branches}
	return true
}

// normalizeExpandedTypeBranches collapses the original schema's combinators
// inside each single-type branch. The generated outer anyOf intentionally
// remains intact: flattening it through primary-type helpers would turn mixed
// types back into an unknown type and could drop a listed alternative.
func normalizeExpandedTypeBranches(ctx context.Context, schema *oas3.Schema, nctx *normCtx) (bool, error) {
	branches := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(schema.AnyOf))
	for _, wrapper := range schema.AnyOf {
		branch := resolvedLeft(wrapper)
		if branch == nil {
			continue
		}
		bottom, err := collapseNormalizedCombinators(ctx, branch, nctx)
		if err != nil {
			return false, err
		}
		if bottom {
			continue
		}
		if err := markNormalizedGraph(ctx, branch, nctx, make(map[*oas3.Schema]bool)); err != nil {
			return false, err
		}
		branches = append(branches, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branch))
	}
	if len(branches) == 0 {
		return true, nil
	}
	schema.AnyOf = branches
	return false, nil
}

func normalizeSchemaChildren(ctx context.Context, schema *oas3.Schema) error {
	var err error
	normalize := func(js *oas3.JSONSchema[oas3.Referenceable]) *oas3.JSONSchema[oas3.Referenceable] {
		if err != nil || js == nil {
			return js
		}
		var normalized *oas3.JSONSchema[oas3.Referenceable]
		normalized, err = normalizeJSONSchema(ctx, js)
		return normalized
	}

	normalizeSlice := func(src []*oas3.JSONSchema[oas3.Referenceable]) []*oas3.JSONSchema[oas3.Referenceable] {
		if src == nil {
			return nil
		}
		result := make([]*oas3.JSONSchema[oas3.Referenceable], len(src))
		for i, js := range src {
			result[i] = normalize(js)
		}
		return result
	}

	normalizeMap := func(src *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]]) *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]] {
		if src == nil {
			return nil
		}
		result := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for key, js := range src.All() {
			result.Set(key, normalize(js))
		}
		return result
	}

	schema.Properties = normalizeMap(schema.Properties)
	schema.PatternProperties = normalizeMap(schema.PatternProperties)
	schema.DependentSchemas = normalizeMap(schema.DependentSchemas)
	schema.Defs = normalizeMap(schema.Defs)
	schema.Items = normalize(schema.Items)
	schema.PrefixItems = normalizeSlice(schema.PrefixItems)
	schema.Contains = normalize(schema.Contains)
	schema.AdditionalProperties = normalize(schema.AdditionalProperties)
	schema.PropertyNames = normalize(schema.PropertyNames)
	schema.UnevaluatedItems = normalize(schema.UnevaluatedItems)
	schema.UnevaluatedProperties = normalize(schema.UnevaluatedProperties)
	schema.ContentSchema = normalize(schema.ContentSchema)
	schema.AllOf = normalizeSlice(schema.AllOf)
	schema.AnyOf = normalizeSlice(schema.AnyOf)
	// oneOf and not remain combinators: their children are resolved and
	// normalized branch-wise, but their semantics are not collapsed here.
	// TODO: Collapse oneOf and not if the executor needs that precision.
	schema.OneOf = normalizeSlice(schema.OneOf)
	schema.Not = normalize(schema.Not)
	schema.If = normalize(schema.If)
	schema.Then = normalize(schema.Then)
	schema.Else = normalize(schema.Else)
	return err
}

func normalizeJSONSchema(ctx context.Context, js *oas3.JSONSchema[oas3.Referenceable]) (*oas3.JSONSchema[oas3.Referenceable], error) {
	if value, ok := resolvedBooleanSchema(js); ok {
		return oas3.NewJSONSchemaFromBool(value), nil
	}
	left := resolvedLeft(js)
	if left == nil {
		return js, nil
	}
	if left.IsReference() && (js == nil || js.GetResolvedSchema() == nil) {
		return js, nil
	}
	normalized, err := normalizeSchema(ctx, left)
	if err != nil {
		return nil, err
	}
	if normalized == nil {
		return oas3.NewJSONSchemaFromBool(false), nil
	}
	return oas3.NewJSONSchemaFromSchema[oas3.Referenceable](normalized), nil
}

func collapseNormalizedCombinators(ctx context.Context, schema *oas3.Schema, nctx *normCtx) (bool, error) {
	if len(schema.AllOf) > 0 {
		base := cloneSchema(schema)
		base.AllOf = nil
		merged := base
		for _, branch := range schema.AllOf {
			if value, ok := resolvedBooleanSchema(branch); ok {
				if !value {
					return true, nil
				}
				continue
			}
			left := resolvedLeft(branch)
			if left == nil {
				continue
			}
			var err error
			merged, err = mergeSchemas(merged, left)
			if err != nil {
				nctx.debugf("normalization: allOf merge widened to Top: %v", err)
				*schema = oas3.Schema{}
				return false, nil
			}
			if err := markNormalizedGraph(ctx, merged, nctx, make(map[*oas3.Schema]bool)); err != nil {
				return false, err
			}
		}
		*schema = *merged
	}

	if len(schema.AnyOf) == 0 {
		return false, nil
	}
	base := cloneSchema(schema)
	base.AnyOf = nil
	branches := make([]*oas3.Schema, 0, len(schema.AnyOf))
	for _, branch := range schema.AnyOf {
		if value, ok := resolvedBooleanSchema(branch); ok {
			if !value {
				continue
			}
			branch = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](Top())
		}
		left := resolvedLeft(branch)
		if left == nil {
			continue
		}
		merged, err := mergeSchemas(base, left)
		if err != nil {
			nctx.debugf("normalization: conjunctive anyOf branch merge widened to Top: %v", err)
			*schema = oas3.Schema{}
			return false, nil
		}
		if err := markNormalizedGraph(ctx, merged, nctx, make(map[*oas3.Schema]bool)); err != nil {
			return false, err
		}
		branches = append(branches, merged)
	}
	if len(branches) == 0 {
		return true, nil
	}
	for _, branch := range branches {
		if isTopSchema(branch) {
			*schema = oas3.Schema{}
			return false, nil
		}
	}
	if len(branches) == 1 {
		*schema = *branches[0]
		return false, nil
	}

	commonType := nctx.schemaType(branches[0])
	allSameType := commonType != ""
	for _, branch := range branches[1:] {
		if nctx.schemaType(branch) != commonType {
			allSameType = false
			break
		}
	}
	if !allSameType {
		setAnyOfBranches(schema, base, branches)
		return false, nil
	}

	merged := branches[0]
	flattened := true
	for i := 1; i < len(branches); i++ {
		var err error
		merged, err = mergeSchemasModeWithSemantics(merged, branches[i], MergeDisjunctive, nctx.semantics)
		if err != nil {
			if !errors.Is(err, errCannotFlatten) {
				nctx.debugf("normalization: disjunctive merge kept anyOf branches: %v", err)
			}
			flattened = false
			break
		}
		if err := markNormalizedGraph(ctx, merged, nctx, make(map[*oas3.Schema]bool)); err != nil {
			return false, err
		}
	}
	if !flattened {
		setAnyOfBranches(schema, base, branches)
		return false, nil
	}
	*schema = *merged
	return false, nil
}

func markNormalizedGraph(ctx context.Context, schema *oas3.Schema, nctx *normCtx, seen map[*oas3.Schema]bool) error {
	if schema == nil || seen[schema] {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := nctx.normalized[schema]; ok {
		return nil
	}
	seen[schema] = true
	nctx.normalized[schema] = struct{}{}

	visit := func(js *oas3.JSONSchema[oas3.Referenceable]) error {
		if js == nil || js.Left == nil {
			return nil
		}
		return markNormalizedGraph(ctx, js.Left, nctx, seen)
	}
	visitSlice := func(schemas []*oas3.JSONSchema[oas3.Referenceable]) error {
		for _, js := range schemas {
			if err := visit(js); err != nil {
				return err
			}
		}
		return nil
	}
	visitMap := func(schemas *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]]) error {
		if schemas == nil {
			return nil
		}
		for _, js := range schemas.All() {
			if err := visit(js); err != nil {
				return err
			}
		}
		return nil
	}

	for _, schemas := range []*sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]]{
		schema.Properties, schema.PatternProperties, schema.DependentSchemas, schema.Defs,
	} {
		if err := visitMap(schemas); err != nil {
			return err
		}
	}
	for _, schemas := range [][]*oas3.JSONSchema[oas3.Referenceable]{
		schema.PrefixItems, schema.AllOf, schema.AnyOf, schema.OneOf,
	} {
		if err := visitSlice(schemas); err != nil {
			return err
		}
	}
	for _, js := range []*oas3.JSONSchema[oas3.Referenceable]{
		schema.Items, schema.Contains, schema.AdditionalProperties, schema.PropertyNames,
		schema.UnevaluatedItems, schema.UnevaluatedProperties, schema.ContentSchema,
		schema.Not, schema.If, schema.Then, schema.Else,
	} {
		if err := visit(js); err != nil {
			return err
		}
	}
	return nil
}

func setAnyOfBranches(schema, base *oas3.Schema, branches []*oas3.Schema) {
	*schema = *base
	schema.AnyOf = make([]*oas3.JSONSchema[oas3.Referenceable], len(branches))
	for i, branch := range branches {
		schema.AnyOf[i] = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](branch)
	}
}

// errCannotFlatten signals that two schemas cannot be soundly merged into a
// single schema disjunctively (e.g. differing enums/consts/patterns/bounds,
// for which no facet-level union is implemented). Callers should keep the
// anyOf structure instead of flattening.
var errCannotFlatten = errors.New("schemas cannot be flattened disjunctively")

// disjunctiveFacetsMergeable reports whether flattening s1 ∨ s2 into a single
// schema would preserve all values of both. Facets without an implemented
// disjunctive union (enum, const, pattern, format, numeric/length/item
// bounds) must be identical (or absent on both sides) to flatten.
func disjunctiveFacetsMergeable(s1, s2 *oas3.Schema) bool {
	// Enum and const have a sound disjunctive union implemented in
	// mergeDisjunctiveValueFacets; they never force keeping anyOf.
	eqStr := func(a, b *string) bool {
		if (a == nil) != (b == nil) {
			return false
		}
		return a == nil || *a == *b
	}
	eqF := func(a, b *float64) bool {
		if (a == nil) != (b == nil) {
			return false
		}
		return a == nil || *a == *b
	}
	eqI := func(a, b *int64) bool {
		if (a == nil) != (b == nil) {
			return false
		}
		return a == nil || *a == *b
	}
	if !eqStr(s1.Pattern, s2.Pattern) || !eqStr(s1.Format, s2.Format) {
		return false
	}
	if !eqF(s1.Minimum, s2.Minimum) || !eqF(s1.Maximum, s2.Maximum) || !eqF(s1.MultipleOf, s2.MultipleOf) {
		return false
	}
	// Exclusive bounds: no disjunctive union implemented — require identical
	// wrappers (both nil, or the same wrapper) to flatten.
	if s1.ExclusiveMinimum != s2.ExclusiveMinimum || s1.ExclusiveMaximum != s2.ExclusiveMaximum {
		return false
	}
	if !eqI(s1.MinLength, s2.MinLength) || !eqI(s1.MaxLength, s2.MaxLength) {
		return false
	}
	mm := func(a, b *int64) bool {
		if (a == nil) != (b == nil) {
			return false
		}
		return a == nil || *a == *b
	}
	if !mm(s1.MinItems, s2.MinItems) || !mm(s1.MaxItems, s2.MaxItems) {
		return false
	}
	// Facets with no disjunctive union implemented must be identical (or
	// absent on both sides) to flatten; otherwise the anyOf structure is
	// kept. Pointer identity is the conservative equality for wrapper-typed
	// facets.
	if !mm(s1.MinProperties, s2.MinProperties) || !mm(s1.MaxProperties, s2.MaxProperties) {
		return false
	}
	if !mm(s1.MinContains, s2.MinContains) || !mm(s1.MaxContains, s2.MaxContains) {
		return false
	}
	if s1.Not != s2.Not || s1.If != s2.If || s1.Then != s2.Then || s1.Else != s2.Else {
		return false
	}
	if s1.PatternProperties != s2.PatternProperties || s1.PropertyNames != s2.PropertyNames ||
		s1.DependentSchemas != s2.DependentSchemas {
		return false
	}
	if s1.UnevaluatedProperties != s2.UnevaluatedProperties || s1.UnevaluatedItems != s2.UnevaluatedItems ||
		s1.ContentSchema != s2.ContentSchema {
		return false
	}

	// additionalProperties: no disjunctive union implemented — flattening two
	// object shapes with different AP configurations would impose one
	// branch's AP on the other. Require equivalence.
	apEqual := func(a, b *oas3.JSONSchema[oas3.Referenceable]) bool {
		if (a == nil) != (b == nil) {
			return false
		}
		if a == nil {
			return true
		}
		if (a.Right == nil) != (b.Right == nil) || (a.Left == nil) != (b.Left == nil) {
			return false
		}
		if a.Right != nil && *a.Right != *b.Right {
			return false
		}
		if a.Left != nil && a.Left != b.Left {
			return false
		}
		return true
	}
	if !apEqual(s1.AdditionalProperties, s2.AdditionalProperties) {
		return false
	}
	// prefixItems (tuples): no disjunctive union implemented.
	if len(s1.PrefixItems) != len(s2.PrefixItems) {
		return false
	}
	for i := range s1.PrefixItems {
		if s1.PrefixItems[i] != s2.PrefixItems[i] {
			return false
		}
	}
	// contains: no disjunctive union implemented.
	if (s1.Contains == nil) != (s2.Contains == nil) {
		return false
	}
	if s1.Contains != nil && s1.Contains != s2.Contains {
		return false
	}
	return true
}

type schemaFacetKind uint8

const (
	facetAbsent schemaFacetKind = iota
	facetTrue
	facetFalse
	facetSchema
	facetUnresolved
)

func classifySchemaFacet(wrapper *oas3.JSONSchema[oas3.Referenceable]) (schemaFacetKind, *oas3.Schema) {
	if wrapper == nil {
		return facetAbsent, nil
	}
	if value, ok := resolvedBooleanSchema(wrapper); ok {
		if value {
			return facetTrue, nil
		}
		return facetFalse, nil
	}
	if schema := resolvedLeft(wrapper); schema != nil {
		return facetSchema, schema
	}
	return facetUnresolved, nil
}

func mergeSchemaFacets(a, b *oas3.JSONSchema[oas3.Referenceable], mode MergeMode, absentIsTrue bool, semantics SchemaSemantics, inProgress map[mergePair]bool) (*oas3.JSONSchema[oas3.Referenceable], error) {
	ka, sa := classifySchemaFacet(a)
	kb, sb := classifySchemaFacet(b)
	if absentIsTrue {
		if ka == facetAbsent {
			ka = facetTrue
		}
		if kb == facetAbsent {
			kb = facetTrue
		}
	}
	// An unresolved reference could denote any schema. Treating it as true is
	// the conservative choice for both union and intersection.
	if ka == facetUnresolved {
		ka = facetTrue
	}
	if kb == facetUnresolved {
		kb = facetTrue
	}

	if mode == MergeDisjunctive {
		switch {
		case ka == facetTrue || kb == facetTrue:
			return oas3.NewJSONSchemaFromBool(true), nil
		case ka == facetFalse:
			return cloneSchemaFacet(b, kb, sb), nil
		case kb == facetFalse:
			return cloneSchemaFacet(a, ka, sa), nil
		}
	} else {
		switch {
		case ka == facetFalse || kb == facetFalse:
			return oas3.NewJSONSchemaFromBool(false), nil
		case ka == facetTrue:
			return cloneSchemaFacet(b, kb, sb), nil
		case kb == facetTrue:
			return cloneSchemaFacet(a, ka, sa), nil
		}
	}

	if ka == facetAbsent {
		return cloneSchemaFacet(b, kb, sb), nil
	}
	if kb == facetAbsent {
		return cloneSchemaFacet(a, ka, sa), nil
	}
	merged, err := mergeSchemasModeGuarded(sa, sb, mode, semantics, inProgress)
	if err != nil {
		return nil, err
	}
	if merged == nil {
		return oas3.NewJSONSchemaFromBool(false), nil
	}
	return oas3.NewJSONSchemaFromSchema[oas3.Referenceable](merged), nil
}

func cloneSchemaFacet(original *oas3.JSONSchema[oas3.Referenceable], kind schemaFacetKind, schema *oas3.Schema) *oas3.JSONSchema[oas3.Referenceable] {
	switch kind {
	case facetTrue, facetUnresolved:
		return oas3.NewJSONSchemaFromBool(true)
	case facetFalse:
		return oas3.NewJSONSchemaFromBool(false)
	case facetSchema:
		return oas3.NewJSONSchemaFromSchema[oas3.Referenceable](cloneSchema(schema))
	default:
		return original
	}
}

// mergeDisjunctiveValueFacets computes the union of the enum/const value
// facets for a disjunctive (anyOf) merge and applies it to result:
//   - both sides constrained (enum or const): union of the value sets;
//   - one side unconstrained: it admits a superset, so the merged schema
//     carries NO value constraint.
func mergeDisjunctiveValueFacets(result, s1, s2 *oas3.Schema) {
	valuesOf := func(s *oas3.Schema) []*yaml.Node {
		if len(s.Enum) > 0 {
			return s.Enum
		}
		if s.Const != nil {
			return []*yaml.Node{s.Const}
		}
		return nil
	}
	v1, v2 := valuesOf(s1), valuesOf(s2)
	result.Const = nil
	if len(v1) == 0 || len(v2) == 0 {
		// One branch is value-unconstrained: the union is unconstrained.
		result.Enum = nil
		return
	}
	merged := make([]*yaml.Node, 0, len(v1)+len(v2))
	seen := make(map[string]bool, len(v1)+len(v2))
	for _, n := range append(append([]*yaml.Node{}, v1...), v2...) {
		if n == nil {
			continue
		}
		key := n.Tag + "\x00" + n.Value
		if !seen[key] {
			seen[key] = true
			merged = append(merged, n)
		}
	}
	result.Enum = merged
}

// mergePair identifies an in-progress (s1, s2, mode) merge for cycle detection.
type mergePair struct {
	a, b *oas3.Schema
	mode MergeMode
}

// mergeSchemasMode deep merges two schemas according to the specified mode.
// Returns an error if the schemas have incompatible constraints.
func mergeSchemasMode(s1, s2 *oas3.Schema, mode MergeMode) (*oas3.Schema, error) {
	return mergeSchemasModeWithSemantics(s1, s2, mode, SchemaSemanticsSpeakeasy)
}

func mergeSchemasModeWithSemantics(s1, s2 *oas3.Schema, mode MergeMode, semantics SchemaSemantics) (*oas3.Schema, error) {
	return mergeSchemasModeGuarded(s1, s2, mode, semantics, make(map[mergePair]bool))
}

// mergeSchemasModeGuarded is mergeSchemasMode with a pair-keyed in-progress
// set. Following resolved $refs means merge inputs can be cyclic (recursive
// component schemas); re-entering the same (s1, s2, mode) pair must terminate.
// On recurrence we return a conservative over-approximation instead of
// recursing:
//   - conjunctive (allOf): s1 without s2's constraints — a superset of the
//     intersection, hence sound;
//   - disjunctive (anyOf): an anyOf of both sides, representing the union
//     without further merging.
func mergeSchemasModeGuarded(s1, s2 *oas3.Schema, mode MergeMode, semantics SchemaSemantics, inProgress map[mergePair]bool) (*oas3.Schema, error) {
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

	// Lattice identities for Top. These come FIRST: routing Top through the
	// field-by-field merger would narrow it (disjunctive Top ∨ X previously
	// picked up X's type/facets), and conjunctive Top ∧ X must preserve ALL
	// of X's facets — including ones the merger does not handle (nullable,
	// additionalProperties: true), which an "empty base" merge used to drop.
	if mode == MergeDisjunctive {
		if isTopSchema(s1) || isTopSchema(s2) {
			return Top(), nil
		}
	} else {
		if isTopSchema(s1) {
			return cloneSchema(s2), nil
		}
		if isTopSchema(s2) {
			return cloneSchema(s1), nil
		}
	}

	if mode == MergeDisjunctive && !disjunctiveFacetsMergeable(s1, s2) {
		return nil, errCannotFlatten
	}

	pair := mergePair{a: s1, b: s2, mode: mode}
	if inProgress[pair] {
		if mode == MergeConjunctive {
			return cloneSchema(s1), nil
		}
		return &oas3.Schema{
			AnyOf: []*oas3.JSONSchema[oas3.Referenceable]{
				oas3.NewJSONSchemaFromSchema[oas3.Referenceable](s1),
				oas3.NewJSONSchemaFromSchema[oas3.Referenceable](s2),
			},
		}, nil
	}
	inProgress[pair] = true
	defer delete(inProgress, pair)

	result := cloneSchema(s1)

	if mode == MergeDisjunctive {
		// Union the enum/const value facets (or clear them when one branch
		// is unconstrained). Without this, flattening anyOf[{const "a"},
		// {const "b"}] would keep only s1's value — discarding outputs.
		mergeDisjunctiveValueFacets(result, s1, s2)

		// Nullable union: if either branch admits null, the flattened
		// schema must too.
		if (s1.Nullable != nil && *s1.Nullable) || (s2.Nullable != nil && *s2.Nullable) {
			nb := true
			result.Nullable = &nb
		}
	}

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
					return nil, errCannotFlatten
				} else if t1 == "" {
					result.Type = s2.Type
				}
			}
		}
	}

	// Merge properties (union of keys, recursively merge overlapping)
	if (s1.Properties != nil && s1.Properties.Len() > 0) || (s2.Properties != nil && s2.Properties.Len() > 0) {
		// COPY-ON-WRITE: cloneSchema is shallow, so result.Properties is the
		// SAME map as s1.Properties. s1 may be a resolved component schema
		// shared across the document; mutating it in place would corrupt the
		// input and make results depend on merge order. Build a fresh map.
		mergedProps := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		keys := make([]string, 0)
		seenKeys := make(map[string]bool)
		collectKeys := func(properties *sequencedmap.Map[string, *oas3.JSONSchema[oas3.Referenceable]]) {
			if properties == nil {
				return
			}
			for key := range properties.All() {
				if !seenKeys[key] {
					seenKeys[key] = true
					keys = append(keys, key)
				}
			}
		}
		collectKeys(s1.Properties)
		collectKeys(s2.Properties)
		result.Properties = mergedProps

		mergeOneSided := func(declared *oas3.JSONSchema[oas3.Referenceable], other *oas3.Schema, key string) *oas3.JSONSchema[oas3.Referenceable] {
			if mode == MergeConjunctive {
				return declared
			}
			opts := SchemaExecOptions{Semantics: semantics}
			parts := make([]*oas3.Schema, 0, 2)
			if value, possible := schemaFacetValue(declared, opts); possible {
				parts = append(parts, value)
			}
			if value, possible := objectUndeclaredValueForKey(other, key, opts); possible {
				parts = append(parts, value)
			}
			value := Union(parts, opts)
			if value == nil {
				return oas3.NewJSONSchemaFromBool(false)
			}
			if isTopSchema(value) {
				return oas3.NewJSONSchemaFromBool(true)
			}
			return oas3.NewJSONSchemaFromSchema[oas3.Referenceable](value)
		}

		for _, key := range keys {
			prop1, exists1 := objectDeclaredProperty(s1, key)
			prop2, exists2 := objectDeclaredProperty(s2, key)
			switch {
			case exists1 && exists2:
				merged, err := mergeSchemaFacets(prop1, prop2, mode, false, semantics, inProgress)
				if err != nil {
					return nil, fmt.Errorf("incompatible property %q: %w", key, err)
				}
				result.Properties.Set(key, merged)
			case exists1:
				result.Properties.Set(key, mergeOneSided(prop1, s2, key))
			case exists2:
				result.Properties.Set(key, mergeOneSided(prop2, s1, key))
			}
		}
	}

	// Merge required fields based on mode
	if mode == MergeConjunctive {
		// allOf: union of required fields (field required in ANY subschema)
		if len(s2.Required) > 0 {
			requiredSet := make(map[string]bool)
			// Copy before append: the shallow clone shares s1's backing array.
			merged := make([]string, 0, len(result.Required)+len(s2.Required))
			for _, r := range result.Required {
				merged = append(merged, r)
				requiredSet[r] = true
			}
			for _, r := range s2.Required {
				if !requiredSet[r] {
					merged = append(merged, r)
					requiredSet[r] = true
				}
			}
			// Sort for determinism
			sort.Strings(merged)
			result.Required = merged
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

	// An absent items facet is the boolean true schema in JSON Schema.
	mergedItems, err := mergeSchemaFacets(s1.Items, s2.Items, mode, true, semantics, inProgress)
	if err != nil {
		return nil, fmt.Errorf("incompatible array items: %w", err)
	}
	if kind, _ := classifySchemaFacet(mergedItems); kind == facetTrue {
		result.Items = nil
	} else {
		result.Items = mergedItems
	}
	// uniqueItems: disjunctive union of unique and non-unique is non-unique.
	if mode == MergeDisjunctive {
		u1 := s1.UniqueItems != nil && *s1.UniqueItems
		u2 := s2.UniqueItems != nil && *s2.UniqueItems
		if u1 != u2 {
			result.UniqueItems = nil
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
	env.indexForeachMarkers()

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

	// Track all terminal states to merge accumulators across all terminal paths
	terminalStates := make([]*execState, 0, 32)

	// Multi-state execution loop
	maxIterations := env.opts.MaxDepth * 1000 // Safeguard against infinite loops
	iterations := 0
	mergeThreshold := 8
	for !worklist.isEmpty() {
		// Check context cancellation
		select {
		case <-env.ctx.Done():
			return nil, env.ctx.Err()
		default:
		}

		// Merge the execution frontier periodically to prevent exponential branch explosion.
		// Amortize frontier partitioning while still bounding branch growth.
		if len(worklist.states) >= mergeThreshold {
			frontier := make([]*execState, 0, len(worklist.states))
			for !worklist.isEmpty() {
				candidate := worklist.pop()
				candidate.applyForkUpdates()
				frontier = append(frontier, candidate)
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
		state.applyForkUpdates()

		// Check if we've seen this state (memoization) — currently disabled (see code comments)
		_ = worklist

		// Check depth limit
		if state.depth > env.opts.MaxDepth {
			if env.strict {
				return nil, fmt.Errorf("strict mode: exceeded maximum execution depth (MaxDepth=%d)", env.opts.MaxDepth)
			}
			if env.widenActiveAccumulators(state) {
				env.addWarning("max depth exceeded, widening active array accumulators")
				// Once recursive generators have reached their abstraction bound,
				// batch their remaining frontier states more coarsely. The active
				// accumulator has already been widened, so frequent joins add cost
				// without recovering precision.
				mergeThreshold = 256
			} else {
				env.addWarning("max depth exceeded, widening to Top")
				outputs = append(outputs, Top())
			}
			continue
		}

		// Count states that execute bytecode. Depth-pruned states are terminal
		// abstractions, not evidence of an infinite VM loop.
		iterations++
		if iterations > maxIterations {
			return nil, fmt.Errorf("exceeded maximum iterations (%d) - possible infinite loop", maxIterations)
		}

		// Execute one step
		if state.pc >= len(env.codes) {
			// Terminal state - collect output
			outputSchema := state.top()
			if outputSchema != nil {
				// DEBUG: Show stack state at terminal
				if env.opts.EnableWarnings {
					env.logger.Debugf("Terminal state: stack length=%d, top ptr=%p, top type=%s",
						len(state.stack), outputSchema, getType(outputSchema))
					if getType(outputSchema) == "object" {
						hasAP := outputSchema.AdditionalProperties != nil && outputSchema.AdditionalProperties.Left != nil
						var apType string
						if hasAP {
							apType = getType(outputSchema.AdditionalProperties.Left)
						}
						env.logger.Debugf("Terminal object: hasAP=%v, apType=%s", hasAP, apType)
					}
					for i, sv := range state.stack {
						env.logger.Debugf("Terminal stack[%d]: ptr=%p, type=%s", i, sv.Schema, getType(sv.Schema))
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
			// Collect every terminal state for accumulator merging
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

	// Materialize arrays using a MERGED accumulator built from ALL terminal states
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
	}
	result = stripInternalSchemaMarkers(result)

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

func (env *schemaEnv) indexForeachMarkers() {
	env.foreachSeeds = make(map[int][]foreachMarkerLocation)
	for pc, code := range env.codes {
		marker, ok := code.value.(gojq.SchemaForeachMarker)
		if !ok {
			continue
		}
		env.foreachSeeds[marker.InitialStorePC] = append(env.foreachSeeds[marker.InitialStorePC], foreachMarkerLocation{
			pc:     pc,
			marker: marker,
		})
	}
}

// widenActiveAccumulators preserves an enclosing array result only when the
// exhausted state is provably confined to compileArray generators. Merely
// finding an array in the shared accumulator map is insufficient: it may have
// been built by an unrelated earlier expression while this state still emits
// directly to the query output.
func (env *schemaEnv) widenActiveAccumulators(state *execState) bool {
	if state == nil || state.depthWidenBlocked || state.labels.len() != 0 ||
		state.tryDepth != 0 || state.forks.len() == 0 {
		return false
	}
	if state.forks.tail == nil || !state.forks.tail.allPlain {
		return false
	}

	allocIDs := make(map[string]struct{})
	foundGenerator := false
	for node := state.forks.tail; node != nil; node = node.prev {
		fork := node.value
		if fork.arrayAppendKey == "" {
			continue
		}

		foundGenerator = true
		array, ok := state.loadVar(fork.arrayAppendKey)
		if !ok {
			return false
		}
		allocID, ok := state.schemaToAlloc[array]
		if !ok {
			return false
		}
		canonical, ok := state.accum[allocID]
		if !ok || canonical == nil || getType(canonical) != "array" {
			return false
		}
		allocIDs[allocID] = struct{}{}
	}
	if !foundGenerator || len(allocIDs) == 0 {
		return false
	}

	for _, allocID := range sortedAccumKeys(allocIDs) {
		array := state.accum[allocID]
		if items := resolvedLeft(array.Items); items != nil && isTopSchema(items) &&
			len(array.PrefixItems) == 0 && array.MinItems == nil && array.MaxItems == nil {
			continue
		}
		widened := cloneSchema(array)
		widened.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](env.NewTopWithCause("array generator exceeded maximum execution depth"))
		widened.PrefixItems = nil
		widened.MinItems = nil
		widened.MaxItems = nil
		state.accum[allocID] = widened
		state.schemaToAlloc[widened] = allocID
	}
	return true
}

// sortedAccumKeys returns the keys of an accumulator map in sorted order.
// Accumulator merging, tagging, and redirect selection MUST iterate
// deterministically: several of these loops are first-wins or mutate shared
// entries, and Go map order would otherwise make outputs (and flaky tests)
// depend on the run. See AGENTS.md "Determinism/Stability".
func sortedAccumKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
		for _, id := range sortedAccumKeys(mergedAccum) {
			if canon := mergedAccum[id]; canon != nil {
				if _, ok := mergedTags[canon]; !ok {
					mergedTags[canon] = id
				}
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
		for _, id := range sortedAccumKeys(mergedAccum) {
			if canon := mergedAccum[id]; canon != nil {
				if _, ok := mergedTags[canon]; !ok {
					mergedTags[canon] = id
				}
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
			if existing == v {
				continue
			}
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
	for _, id := range sortedAccumKeys(mergedAccum) {
		if canon := mergedAccum[id]; canon != nil {
			if _, ok := mergedTags[canon]; !ok {
				mergedTags[canon] = id
			}
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
			for _, id := range sortedAccumKeys(mergedAccum) {
				if canon := mergedAccum[id]; canon != nil {
					if _, ok := mergedTags[canon]; !ok {
						mergedTags[canon] = id
					}
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
	// DSU gating: a redirect between two allocIDs is legitimate when SOME
	// terminal state links them — equivalences are path-local facts, so we
	// must neither look at only states[0] (order-dependent: a link recorded
	// in a sibling state was invisible) nor take the transitive closure
	// ACROSS states (which conflates arrays that are distinct on every
	// concrete path).
	anyStateLinks := func(a, b string) bool {
		linked := false
		for _, st := range states {
			if st == nil || st.dsu == nil {
				continue
			}
			linked = st.dsu.Find(a) == st.dsu.Find(b)
			if linked {
				break
			}
		}
		return linked
	}
	haveDSU := false
	for _, st := range states {
		if st != nil && st.dsu != nil {
			haveDSU = true
			break
		}
	}

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
		// Union all vars that co-occur in this state (sorted for determinism)
		sort.Strings(varsInState)
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
	for _, id := range sortedAccumKeys(mergedAccum) {
		arr := mergedAccum[id]
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
		for _, varKey := range sortedAccumKeys(s.varDesiredItemFP) {
			varFP := s.varDesiredItemFP[varKey]
			target, ok := bestByFP[varFP]
			if !ok || target == "" {
				if env.opts.EnableWarnings {
					env.logger.Debugf("computeAllocRedirect(intent): no rank-2 alloc for fp=%s... (var=%s)",
						varFP[:min(16, len(varFP))], varKey)
				}
				continue
			}
			// Gather all allocIDs this var ever held
			hist := s.varAllocHistory[varKey]
			for _, allocID := range sortedAccumKeys(hist) {
				if allocID != target {
					// Theory 10: Only redirect within the same DSU class
					if haveDSU && !anyStateLinks(allocID, target) {
						continue
					}
					redirect[allocID] = target
					if env.opts.EnableWarnings {
						env.logger.Debugf("computeAllocRedirect(intent): var=%s fp=%s... redirect %s -> %s",
							varKey, varFP[:min(16, len(varFP))], allocID, target)
					}
				}
			}
		}

		// FALLBACK: If var has no intent, check if any of its allocIDs have allocDesiredFP
		for _, varKey := range sortedAccumKeys(s.varAllocHistory) {
			hist := s.varAllocHistory[varKey]
			if _, hasVarIntent := s.varDesiredItemFP[varKey]; hasVarIntent {
				continue // Already handled above
			}
			// Check if any allocID in history has a known FP
			for _, allocID := range sortedAccumKeys(hist) {
				if fp, ok := s.allocDesiredFP[allocID]; ok {
					if target, ok := bestByFP[fp]; ok && target != "" && allocID != target {
						// Theory 10: Only redirect within the same DSU class
						if haveDSU && !anyStateLinks(allocID, target) {
							continue
						}
						redirect[allocID] = target
						if env.opts.EnableWarnings {
							env.logger.Debugf("computeAllocRedirect(alloc-intent): var=%s alloc=%s fp=%s... redirect %s -> %s",
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
	for _, v := range sortedAccumKeys(parent) {
		r := find(v)
		groups[r] = append(groups[r], v)
	}

	// For each co-occurrence group, pick group-best alloc by rank
	for _, groupRoot := range sortedAccumKeys(groups) {
		members := groups[groupRoot]
		// Gather all candidate allocIDs across group members
		candidates := make([]string, 0, 16)
		candidateSet := make(map[string]struct{})
		for _, varKey := range members {
			for _, allocID := range sortedAccumKeys(varToAllocs[varKey]) {
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

		// If multiple rank-2, union their items into best — but ONLY allocs
		// that some state's DSU actually links to best. Variable
		// co-occurrence alone groups DISTINCT arrays (e.g. an entries array
		// and a string-values array from the same pipeline); unioning their
		// items conflates array types that are never merged on any path.
		if len(rank2IDs) > 1 {
			items := make([]*oas3.Schema, 0, len(rank2IDs))
			for _, id := range rank2IDs {
				if id != best && haveDSU && !anyStateLinks(id, best) {
					continue
				}
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
			for _, allocID := range sortedAccumKeys(varToAllocs[varKey]) {
				if allocID != best {
					// Theory 10: Only redirect within the same DSU class
					if haveDSU && !anyStateLinks(allocID, best) {
						continue
					}
					redirect[allocID] = best
					if env.opts.EnableWarnings {
						env.logger.Debugf("computeAllocRedirect(group): var=%s redirecting %s -> %s (groupBestRank=%d, group=%s)",
							varKey, allocID, best, bestRank, groupRoot)
					}
				}
			}
		}
	}

	// Theory 10: Overlay DSU canonicalization and ensure mergedAccum has
	// class roots. Applied PER STATE (sorted, first-wins) so path-local
	// equivalence classes never merge transitively across alternative paths.
	if haveDSU {
		for _, st := range states {
			if st == nil || st.dsu == nil {
				continue
			}
			d := st.dsu
			for _, id := range sortedAccumKeys(mergedAccum) {
				if _, done := redirect[id]; done {
					continue
				}
				root := d.Find(id)
				if root == "" || root == id {
					continue
				}
				env.logger.Debugf("DSU computeAllocRedirect: redirect %s -> %s (DSU root)", id, root)
				redirect[id] = root
				// Ensure we have an entry at the root and union siblings there
				if rootArr, ok := mergedAccum[root]; ok && rootArr != mergedAccum[id] {
					mergedAccum[root] = joinTwoSchemas(rootArr, mergedAccum[id], env.opts)
					env.logger.Debugf("DSU computeAllocRedirect: merged %s into root %s", id, root)
				} else if _, ok := mergedAccum[root]; !ok {
					mergedAccum[root] = mergedAccum[id]
					env.logger.Debugf("DSU computeAllocRedirect: created root entry %s from %s", root, id)
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
		if marker, ok := c.value.(gojq.SchemaForeachMarker); ok {
			return env.execForeachMarker(next, state.pc, marker), nil
		}
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
		if next.callstack.len() > 0 {
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
							env.logger.Debugf("opStore: var='%s', accumKey=%s - storing canonical instead of stale (valEmpty=%v, canonEmpty=%v, canonHasItems=%v, itemType=%s)",
								key, accumKey, valEmpty, canonEmpty, canonHasItems, itemType)
						}
						finalVal = canonical // Store canonical, not stale reference!
					}
				}
			}

			next.storeVar(key, finalVal)
			env.captureForeachSeed(next, key, finalVal, next.pc-1)

			// DEBUG: Log variable storage for arrays
			if getType(finalVal) == "array" && env.opts.EnableWarnings {
				isEmpty := finalVal.MaxItems != nil && *finalVal.MaxItems == 0
				hasItems := finalVal.Items != nil && finalVal.Items.Left != nil
				var itemType string
				if hasItems {
					itemType = getType(finalVal.Items.Left)
				}
				accumPtr := fmt.Sprintf("%p", next.accum)
				env.logger.Debugf("opStore: STORED var='%s' - empty=%v, hasItems=%v, itemType=%s, accumPtr=%s",
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
							env.logger.Debugf("opStore: lifted pointer-intent to new alloc %s for var=%s, fp=%s...",
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
								env.logger.Debugf("opStore: lifted pointer-intent to existing alloc %s for var=%s, fp=%s...",
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
							if sameOrigin(prevOrigin, currOrigin) &&
								accumulationCompatible(next.accum[priorID], next.accum[currID]) {

								// Union classes
								env.logger.Debugf("DSU opStore: var=%s union %s with %s (same origin PC=%d, ctx=%s)",
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
									merged = joinTwoSchemas(arr1, arr2, env.opts)
								}
								if merged != nil {
									next.accum[root] = merged
									// Alias both ids to the merged canonical to be robust
									next.accum[priorID] = merged
									next.accum[currID] = merged
									next.schemaToAlloc[merged] = root
									// Store canonical back to the variable (preserve pointer identity)
									next.storeVar(key, merged)
									env.logger.Debugf("DSU opStore: merged arrays to root=%s", root)
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
										env.logger.Debugf("DSU opStore: joined cardinality to root=%s MinItems=%d",
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
							env.logger.Debugf("opLoad: var='%s', accumKey=%s - origEmpty=%v, canonEmpty=%v, hasItems=%v, itemType=%s, samePtr=%v, accumPtr=%s, valPtr=%s, canonPtr=%s",
								key, accumKey, origEmpty, canonEmpty, canonHasItems, itemType, samePtr, accumPtr, valPtr, canonPtr)
						}
						// Use the canonical (mutated) version, not the original
						finalVal = canonical
						if env.opts.EnableWarnings && canonHasItems {
							finalPtr := fmt.Sprintf("%p", finalVal)
							env.logger.Debugf("opLoad: PUSHING non-empty canonical to stack, ptr=%s", finalPtr)
						}
					} else if env.opts.EnableWarnings {
						env.logger.Debugf("opLoad: var='%s' tagged as %s but NOT in accum", key, accumKey)
					}
				} else if env.opts.EnableWarnings {
					isEmpty := val.MaxItems != nil && *val.MaxItems == 0
					hasItems := val.Items != nil && val.Items.Left != nil
					var itemType string
					if hasItems {
						itemType = getType(val.Items.Left)
					}
					env.logger.Debugf("opLoad: loading var='%s' (not tagged) - empty=%v, hasItems=%v, itemType=%s",
						key, isEmpty, hasItems, itemType)
				}
			}
			next.pushLoadedVar(finalVal, key)
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
		if next.pathMode && len(next.pathEvalBases) > 0 {
			last := len(next.pathEvalBases) - 1
			base := next.pathEvalBases[last]
			if base < len(next.currentPath) {
				next.currentPath = next.currentPath[:base]
			}
			next.pathEvalBases = next.pathEvalBases[:last]
		}
		next.popFrame()
		if retPC, callstack, ok := next.callstack.pop(); ok {
			// Pop return address and jump back
			next.callstack = callstack
			next.forks = next.forks.pruneCallDepth(callstack.len())
			next.labels = next.labels.pruneCallDepth(callstack.len())
			next.invalidateShapeKey()
			next.pc = retPC
			// NOTE: Accumulator changes are preserved in next.accum
			// When multiple returns merge, Union will handle the lattice join
			return []*execState{next}, nil
		}
		// No caller - terminate this path
		next.pc = len(env.codes)
		return []*execState{next}, nil

	case opDup:
		if top := next.topValue(); top.Schema != nil {
			next.pushValue(top)
		}
		return []*execState{next}, nil

	case opAppend:
		return env.execAppendMulti(next, c)

	case opFork:
		// Fork creates two execution paths
		return env.execFork(state, c)

	case opBacktrack:
		return env.execBacktrack(state.clone())

	case opJump:
		// Unconditional jump
		targetPC := c.value.(int)
		next.pc = targetPC
		if targetPC <= state.pc {
			return env.execBackwardJump(next), nil
		}
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
			if len(next.stack) > 0 {
				next.pop()
			}
			next.push(Top())
			return []*execState{next}, nil
		}
		if pc, ok := getClosurePC(clos); ok {
			input := next.top()
			if env.shouldWidenRecursiveCall(next.callstack, pc, input) {
				next.pop()
				next.push(env.NewTopWithCause("recursion depth limit for closure call"))
				return []*execState{next}, nil
			}
			// Push return address (next.pc is already incremented by executeOpMultiState)
			if next.pathMode && env.returnsToDynamicIndex(next.pc) {
				next.pathEvalBases = append(next.pathEvalBases[:len(next.pathEvalBases):len(next.pathEvalBases)], len(next.currentPath))
			}
			next.callstack = next.callstack.append(next.pc, pc, input)
			next.invalidateShapeKey()

			// Jump to closure PC
			next.pc = pc
			return []*execState{next}, nil
		}
		// Unknown closure
		if env.strict {
			return nil, fmt.Errorf("strict mode: attempted to call unknown closure at pc=%d", state.pc)
		}
		if len(next.stack) > 0 {
			next.pop()
		}
		next.push(env.NewTopWithCause("attempted to call unknown closure"))
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
		continueState.tryDepth++
		continueState.invalidateShapeKey()
		continueState.lineage = state.lineage + ".S" // Success branch

		errorState := state.clone()
		errorState.pc = targetPC
		errorState.lineage = state.lineage + ".E" // Error branch
		if len(errorState.stack) > 0 {
			errorState.pop()
		}
		errorState.push(env.NewTopWithCause("value caught by try/catch is unknown"))

		return []*execState{continueState, errorState}, nil

	case opForkTryEnd:
		// try-catch end - marks end of try block
		if next.tryDepth > 0 {
			next.tryDepth--
			next.invalidateShapeKey()
		}
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
		next.pathEvalBases = nil
		return []*execState{next}, nil

	case opForkLabel:
		key := fmt.Sprintf("%v", c.value)
		next.storeVar(key, ConstString("jq-label:"+key))
		next.labels = next.labels.append(labelContinuation{
			key:            key,
			forkDepth:      next.forks.len(),
			callstackDepth: next.callstack.len(),
		})
		next.invalidateShapeKey()
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

	// Warnings are logged at warn level, but the DEFAULT LogLevel is ""
	// (no logger): the library is silent on stdout/stderr unless a consumer
	// opts into logging. Warnings always remain available programmatically
	// on SchemaExecResult.Warnings.
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
		var elem *oas3.Schema

		// Check prefixItems for tuple access
		if arr.PrefixItems != nil && idx >= 0 && idx < len(arr.PrefixItems) {
			if schema, ok := derefJSONSchema(collapseContextForOptions(opts), arr.PrefixItems[idx]); ok {
				elem = schema
			}
		}

		// Fall through to items for indices beyond prefixItems
		if elem == nil && arr.Items != nil {
			if schema, ok := derefJSONSchema(collapseContextForOptions(opts), arr.Items); ok {
				elem = schema
			}
		}

		if elem == nil {
			// No schema for this index
			return Top()
		}

		// jq semantics: indexing out of bounds yields null. Only when
		// minItems PROVES the index exists can null be excluded.
		if idx >= 0 && arr.MinItems != nil && *arr.MinItems > int64(idx) {
			return elem
		}
		return Union([]*oas3.Schema{elem, ConstNull()}, opts)
	}

	// Non-constant or unknown index - union all possible element types
	schemas := make([]*oas3.Schema, 0)

	// Add all prefixItems
	if arr.PrefixItems != nil {
		for _, item := range arr.PrefixItems {
			if schema, ok := derefJSONSchema(collapseContextForOptions(opts), item); ok {
				schemas = append(schemas, schema)
			} else {
				// Unresolved reference in tuple element - widen conservatively
				schemas = append(schemas, Top())
			}
		}
	}

	// Add items schema
	if arr.Items != nil {
		if schema, ok := derefJSONSchema(collapseContextForOptions(opts), arr.Items); ok {
			schemas = append(schemas, schema)
		} else {
			// Unresolved reference in items - widen conservatively
			schemas = append(schemas, Top())
		}
	}

	if len(schemas) == 0 {
		return Top()
	}

	// Unknown index: may be out of bounds — jq yields null then.
	schemas = append(schemas, ConstNull())

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
			state.currentPath = append(state.currentPath[:len(state.currentPath):len(state.currentPath)], PathSegment{
				Key:        PathWildcard{},
				IsSymbolic: true,
			})
			return []*execState{state}, nil
		}
		state.currentPath = append(state.currentPath[:len(state.currentPath):len(state.currentPath)], PathSegment{
			Key:        indexKey,
			IsSymbolic: false,
		})
		return []*execState{state}, nil
	}

	// NORMAL MODE: Navigate schema
	baseValue := state.popValue()
	base := baseValue.Schema
	if base == nil {
		return []*execState{state}, nil
	}

	result := env.indexSchema(base, c.value, make(map[*oas3.Schema]bool))
	state.pushDerived(result, baseValue, PathSegment{Key: c.value})
	return []*execState{state}, nil
}

// indexSchema distributes jq indexing over disjunctive receiver schemas.
// The individual branches retain the existing per-type behavior: null indexes
// to null, arrays yield elements/slices, and unsupported receivers widen.
func (env *schemaEnv) indexSchema(base *oas3.Schema, indexKey any, seen map[*oas3.Schema]bool) *oas3.Schema {
	if base == nil {
		return Bottom()
	}
	if seen[base] {
		return env.NewTopWithCause("indexing recursive disjunctive schema")
	}
	if branches, ok := disjunctiveBranches(base); ok {
		seen[base] = true
		results := make([]*oas3.Schema, 0, len(branches))
		for _, branch := range branches {
			results = append(results, env.indexSchema(branch, indexKey, seen))
		}
		delete(seen, base)
		return Union(results, env.opts)
	}

	baseNullable := base.Nullable != nil && *base.Nullable
	var result *oas3.Schema
	switch baseType := env.dispatchType(base); baseType {
	case "object":
		if key, ok := indexKey.(string); ok {
			// Union-aware property lookup with jq semantics: missing → null
			result = accessPropertyUnion(base, key, env.opts)
		} else {
			result = Top()
		}
	case "null":
		// jq indexes null without erroring: `null | .foo`, `null | .[0]`,
		// and `null | .[a:b]` all yield null. Widening here to Top loses
		// the null-ness that `//` and truthiness checks rely on.
		result = ConstNull()
	case "array":
		// Array slicing: .[start:end] returns the same item type, but the
		// slice may be shorter (or empty) than the source — a lower bound on
		// length does not survive slicing.
		if isSliceIndex(indexKey) {
			sliced := eraseArrayPositions(base, env.opts)
			sliced.MinItems = nil
			result = sliced
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

	// jq: indexing/property access on null yields null. A nullable base can
	// BE null, so null joins the result.
	if baseNullable && result != nil && !isTopSchema(result) {
		result = Union([]*oas3.Schema{result, ConstNull()}, env.opts)
	}
	return result
}

// execIterMulti handles iteration in multi-state mode.
func (env *schemaEnv) execIterMulti(state *execState, c *codeOp) ([]*execState, error) {
	// PATH MODE: Add wildcard segment for symbolic iteration
	if state.pathMode {
		// Unlike an unknown single index, .[] selects every element. Preserve
		// that distinction so update assignments can replace the item schema
		// without adding padding or retaining the old item type.
		state.currentPath = append(state.currentPath[:len(state.currentPath):len(state.currentPath)], PathSegment{
			Key:        PathAllElements{},
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

	itemSchema := env.iterationSchema(val, make(map[*oas3.Schema]bool))

	// Don't push Bottom - it should terminate paths earlier
	if itemSchema == Bottom() {
		return []*execState{}, nil
	}

	state.push(itemSchema)
	return []*execState{state}, nil
}

// iterationSchema distributes jq iteration across anyOf/oneOf branches.
// Proven scalar and null alternatives produce no values; only a genuinely
// untyped alternative widens the result to a caused Top.
func (env *schemaEnv) iterationSchema(val *oas3.Schema, seen map[*oas3.Schema]bool) *oas3.Schema {
	if val == nil {
		return Bottom()
	}
	if seen[val] {
		return env.NewTopWithCause("iteration over recursive disjunctive schema")
	}
	if branches, ok := disjunctiveBranches(val); ok {
		seen[val] = true
		items := make([]*oas3.Schema, 0, len(branches))
		for _, branch := range branches {
			items = append(items, env.iterationSchema(branch, seen))
		}
		delete(seen, val)
		return Union(items, env.opts)
	}

	switch baseType := env.dispatchType(val); baseType {
	case "array":
		// Check for empty array sentinel first, regardless of Items being set
		// Empty arrays are represented with MaxItems=0 and Items may be nil
		if val.MaxItems != nil && *val.MaxItems == 0 {
			// Empty array sentinel: produce no iteration values
			if env.opts.EnableWarnings {
				env.addWarning("ITER: empty array sentinel detected, producing no iterations")
			}
			return Bottom()
		}

		return arrayElementUnion(val, env.opts)
	case "object":
		return unionAllObjectValues(val, env.opts)
	default:
		if baseType == "" {
			return env.NewTopWithCause("iteration over unknown type")
		}
		return Bottom()
	}
}

// disjunctiveBranches resolves the alternatives of oneOf/anyOf, including
// boolean schemas. A true branch is Top and a false branch is Bottom. Object
// and array constraints alongside the combinator are also retained as a
// conservative alternative; they constrain every branch conjunctively, and
// omitting them would lose values declared only on the receiver itself.
func disjunctiveBranches(schema *oas3.Schema) ([]*oas3.Schema, bool) {
	if schema == nil {
		return nil, false
	}
	branches := schema.OneOf
	if len(branches) == 0 {
		branches = schema.AnyOf
	}
	if len(branches) == 0 {
		return nil, false
	}
	resolved := make([]*oas3.Schema, 0, len(branches))
	for _, branch := range branches {
		if value, ok := resolvedBooleanSchema(branch); ok {
			if value {
				resolved = append(resolved, Top())
			}
			continue
		}
		if left := resolvedLeft(branch); left != nil {
			resolved = append(resolved, left)
		}
	}
	if base := disjunctiveNavigationBase(schema); base != nil {
		resolved = append(resolved, base)
	}
	if schema.Nullable != nil && *schema.Nullable {
		resolved = append(resolved, ConstNull())
	}
	return resolved, true
}

func disjunctiveNavigationBase(schema *oas3.Schema) *oas3.Schema {
	base := cloneSchema(schema)
	base.OneOf = nil
	base.AnyOf = nil
	base.Nullable = nil

	hasObjectShape := base.Properties != nil || base.AdditionalProperties != nil ||
		(base.PatternProperties != nil && base.PatternProperties.Len() > 0)
	hasArrayShape := base.Items != nil || len(base.PrefixItems) > 0
	typeName := getType(base)
	if !hasObjectShape && !hasArrayShape && typeName != "object" && typeName != "array" {
		return nil
	}
	return base
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
				env.logger.Debugf("opObject: materialized array for property; before(empty=%v,hasItems=%v) → after(empty=%v,hasItems=%v)",
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
				env.logger.Debugf("opObject: configs POPPED - empty=%v, hasItems=%v, itemType=%s, ptr=%s",
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

// accumulationCompatible reports whether rebinding a variable from array
// `prior` to array `curr` looks like genuine ACCUMULATION of the same logical
// array (loop iterations: `. + [x]` always folds the prior items into the new
// array, so one side's items subsume the other's) rather than a SEPARATE
// construction reusing the same bytecode temp (e.g. two map() calls in one
// pipeline, whose item types are unrelated). Unioning the latter conflates
// distinct arrays' item types.
func accumulationCompatible(prior, curr *oas3.Schema) bool {
	if prior == nil || curr == nil {
		return true
	}
	pItems := resolvedLeft(prior.Items)
	cItems := resolvedLeft(curr.Items)
	currFreshEmpty := curr.MaxItems != nil && *curr.MaxItems == 0 && cItems == nil
	if currFreshEmpty && pItems != nil {
		// A brand-new empty array rebinding over a finished, populated array
		// is a NEW construction starting from scratch, not an accumulation
		// step (loop steps rebind with the grown concat result, never with a
		// fresh empty). Merging here would leak the finished array's items
		// into the new construction.
		return false
	}
	if pItems == nil || cItems == nil {
		return true // otherwise unconstrained: nothing to conflate
	}
	return isSubschemaOf(pItems, cItems) || isSubschemaOf(cItems, pItems)
}

// execAppendMulti handles array element appending in multi-state mode.
// This is used for array construction: [.[] | f]
// Each state has its own accumulator map. Merging happens via lattice join when paths converge.
// allocateArrayWithOrigin creates a new allocID with origin tracking
func (s *execState) allocateArrayWithOrigin(pc int, context string) string {
	*s.allocCounter++
	allocID := fmt.Sprintf("alloc%d", *s.allocCounter)

	// Track origin for DSU equivalence. The callsite (return address of the
	// enclosing frame) distinguishes allocations made by different
	// invocations of the same library function.
	callSite := -1
	if s.callstack.tail != nil {
		callSite = s.callstack.tail.returnPC
	}
	if s.allocOrigin == nil {
		s.allocOrigin = make(map[string]*AllocOrigin)
	}
	s.allocOrigin[allocID] = &AllocOrigin{
		PC:       pc,
		Context:  context,
		CallSite: callSite,
	}

	// Initialize cardinality as empty (MinItems=0, MaxItems=0)
	if s.allocCardinality == nil {
		s.allocCardinality = make(map[string]*ArrayCardinality)
	}
	zero := 0
	s.allocCardinality[allocID] = &ArrayCardinality{
		MinItems: &zero,
		MaxItems: &zero,
	}

	// Theory 10: Initialize DSU parent for this new allocID
	if s.dsu == nil {
		s.dsu = NewDSU()
	}
	s.dsu.Find(allocID) // seeds parent[allocID] = allocID

	return allocID
}

// setArrayNonEmpty marks an array as non-empty (MinItems=1)
func (s *execState) setArrayNonEmpty(allocID string) {
	if allocID == "" {
		return
	}
	if s.allocCardinality == nil {
		s.allocCardinality = make(map[string]*ArrayCardinality)
	}

	// Get or create cardinality
	card := s.allocCardinality[allocID]
	if card == nil {
		card = &ArrayCardinality{}
		s.allocCardinality[allocID] = card
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
					env.logger.Debugf("execAppendMulti: resolved stack array to variable '%s' by pointer", key)
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
		env.logger.Debugf("execAppendMulti: accumKey=%s, wasEmpty=%v, appending type=%s, accumPtr=%s, canonPtr=%s",
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
		// Normal array accumulation - union items. A previous heuristic
		// dropped a nested-array prior item when the next item was a scalar
		// ("likely a bytecode artifact"); that discarded real outputs
		// ([[.], 1] lost its array element). Always union.
		unionedItems = Union([]*oas3.Schema{priorItems, val}, env.opts)
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
			env.logger.Debugf("execAppendMulti: setting items on %s - unionedItems type=%s, isNil=%v",
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
			env.logger.Debugf("execAppendMulti: recorded allocDesiredFP[%s] = %s...",
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
			env.logger.Debugf("execAppendMulti: AFTER mutation %s - empty=%v, hasItems=%v, itemType=%s",
				accumKey, afterEmpty, afterItems, afterItemType)
		}
	}

	// CRITICAL FIX: Update the variable frame with the canonical pointer
	// This ensures that when opLoad retrieves the variable, it gets the mutated canonical
	if fromVar && key != "" && accumKey != "" {
		state.storeVar(key, canonicalArr)
		if env.opts.EnableWarnings {
			env.logger.Debugf("execAppendMulti: updated variable frame %s with canonical ptr=%p", key, canonicalArr)
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
	control := &forkControl{}

	// Create two states: one continues, one jumps to target
	continueState := state.clone()
	continueState.pc++
	continueState.lineage = state.lineage + ".C" // Continue branch
	arrayAppendKey, _ := env.arrayGeneratorAppendKey(targetPC)
	continueState.forks = continueState.forks.append(forkContinuation{
		kind:           c.op,
		continuePC:     state.pc + 1,
		targetPC:       targetPC,
		arrayAppendKey: arrayAppendKey,
		stack:          state.stack,
		scopeDepth:     len(state.scopes),
		scopes:         append([]map[string]*oas3.Schema(nil), state.scopes...),
		callstackLen:   state.callstack.len(),
		callstack:      state.callstack,
		depth:          state.depth,
		pathMode:       state.pathMode,
		currentPath:    state.currentPath,
		pathEvalBases:  state.pathEvalBases,
		tryDepth:       state.tryDepth,
		control:        control,
	})
	continueState.invalidateShapeKey()

	forkState := state.clone()
	forkState.pc = targetPC
	forkState.depth++
	forkState.lineage = state.lineage + ".F" // Fork branch
	forkState.forkUpdates = append(forkState.forkUpdates, control)

	// Return fork target FIRST, continue SECOND
	// This ensures LIFO worklist processes continue state first,
	// which is critical for accumulator mutations (e.g., path collection)
	return []*execState{forkState, continueState}, nil
}

const loopFixpointRounds = 4

type backtrackLoop struct {
	forkDepth   int
	fork        forkContinuation
	accumulator string
	bodyPC      int
}

// execBacktrack recognizes compiler-owned reduce loop tails. The
// concrete VM resumes the pending fork with variables updated by the body;
// the eager symbolic fork cannot do that for scalar or freshly allocated
// values, because its exit state was snapshotted before the first update.
// Iterate the abstract body to a bounded fixpoint and restore the exit with
// the joined accumulator instead of silently retaining only acc_0.
func (env *schemaEnv) execBacktrack(state *execState) ([]*execState, error) {
	loop, ok := env.backtrackAccumulatorLoop(state)
	if !ok {
		return nil, nil
	}
	if forkControlUpdatesVariable(loop.fork.control, loop.accumulator) {
		// setpath-style accumulators already propagate immutable replacement
		// values into the eagerly queued exit through forkControl. Keep that
		// more precise identity-aware path instead of joining it with the
		// deliberately conservative current-state placeholder.
		return nil, nil
	}

	previous, previousOK := loadVarFromScopes(loop.fork.scopes, loop.accumulator)
	current, currentOK := state.loadVar(loop.accumulator)
	if !previousOK || !currentOK {
		unknown := env.NewTopWithCause("reduce accumulator could not be modeled")
		return []*execState{env.restoreBacktrackLoopExit(state, loop, unknown)}, nil
	}

	joined := Union([]*oas3.Schema{previous, current}, env.opts)
	stable := schemaSubsumes(previous, current) || schemaFingerprint(joined) == schemaFingerprint(previous)
	round := loop.fork.loopRound + 1
	env.logger.Debugf("backtrack loop: accumulator=%s round=%d previous=%s current=%s joined=%s stable=%v",
		loop.accumulator, round, schemaTypeSummary(previous, 2), schemaTypeSummary(current, 2),
		schemaTypeSummary(joined, 2), stable)
	if !stable && round > loopFixpointRounds+1 {
		unknown := env.NewTopWithCause("reduce fixpoint did not stabilize after widening")
		return []*execState{env.restoreBacktrackLoopExit(state, loop, unknown)}, nil
	}
	if !stable && round > loopFixpointRounds {
		joined = env.widenLoopValue(joined, make(map[*oas3.Schema]*oas3.Schema))
	}

	if stable {
		return []*execState{env.restoreBacktrackLoopExit(state, loop, joined)}, nil
	}

	reentered := state.clone()
	restoreForkContinuation(reentered, loop.fork)
	reentered.pc = loop.bodyPC
	reentered.lineage += ".L"
	reentered.storeExistingVar(loop.accumulator, joined)

	updatedFork := cloneForkContinuation(loop.fork)
	updatedFork.loopRound = round
	updatedFork.scopes = storeVarInScopes(updatedFork.scopes, loop.accumulator, joined)
	values := state.forks.values()
	values[loop.forkDepth-1] = updatedFork
	reentered.forks = forkContinuationsFromValues(values[:loop.forkDepth])
	reentered.invalidateShapeKey()
	return []*execState{reentered}, nil
}

func forkControlUpdatesVariable(control *forkControl, key string) bool {
	if control == nil {
		return false
	}
	for _, replacement := range control.replacements {
		if replacement.key == key {
			return true
		}
	}
	return false
}

func (env *schemaEnv) backtrackAccumulatorLoop(state *execState) (backtrackLoop, bool) {
	if state == nil || state.pc <= 0 || state.pc >= len(env.codes) || env.codes[state.pc].op != opBacktrack {
		return backtrackLoop{}, false
	}
	for node := state.forks.tail; node != nil; node = node.prev {
		fork := node.value
		if fork.targetPC != state.pc+1 || fork.targetPC < 2 || fork.targetPC > len(env.codes) {
			continue
		}

		// compileReduce: ... store v; backtrack; L_end: pop; load v
		if store := env.codes[fork.targetPC-2]; store.op == opStore {
			return backtrackLoop{
				forkDepth:   node.depth,
				fork:        fork,
				accumulator: fmt.Sprintf("%v", store.value),
				bodyPC:      fork.continuePC,
			}, true
		}

	}
	return backtrackLoop{}, false
}

// captureForeachSeed snapshots the control state immediately after acc_0 is
// stored. The compiler's no-op marker later restores this snapshot for every
// abstract next round, independent of how the extract or downstream filter
// backtracks.
func (env *schemaEnv) captureForeachSeed(state *execState, accumulator string, value *oas3.Schema, storePC int) {
	if state == nil {
		return
	}
	locations := env.foreachSeeds[storePC]
	if len(locations) == 0 {
		return
	}
	loops := cloneForeachLoops(state.foreachLoops)
	for _, location := range locations {
		if fmt.Sprintf("%v", location.marker.Accumulator) != accumulator {
			continue
		}
		key := foreachLoopKey{markerPC: location.pc, callDepth: state.callstack.len()}
		loops[key] = foreachLoopState{
			accumulator: accumulator,
			previous:    value,
			forks:       state.forks,
			labels:      state.labels,
			forkUpdates: append([]*forkControl(nil), state.forkUpdates...),
			continuation: forkContinuation{
				kind:          opNop,
				continuePC:    location.marker.ContinuePC,
				targetPC:      location.marker.ContinuePC,
				stack:         append([]SValue(nil), state.stack...),
				scopeDepth:    len(state.scopes),
				scopes:        cloneScopeMaps(state.scopes),
				callstackLen:  state.callstack.len(),
				callstack:     state.callstack,
				depth:         state.depth,
				pathMode:      state.pathMode,
				currentPath:   append([]PathSegment(nil), state.currentPath...),
				pathEvalBases: append([]int(nil), state.pathEvalBases...),
				tryDepth:      state.tryDepth,
			},
		}
	}
	state.foreachLoops = loops
	state.invalidateShapeKey()
}

// execForeachMarker emits the current round into the extract/downstream code
// and independently schedules the next abstract round from the initialization
// snapshot. This mirrors jq's generator backtracking without relying on a
// particular downstream opbacktrack layout.
func (env *schemaEnv) execForeachMarker(state *execState, markerPC int, marker gojq.SchemaForeachMarker) []*execState {
	key := foreachLoopKey{markerPC: markerPC, callDepth: state.callstack.len()}
	loop, ok := state.foreachLoops[key]
	if !ok {
		// The marker is compiler-owned, so a missing seed means the control
		// state could not be reconstructed. Widen the emitted value instead of
		// silently representing only the first concrete round.
		unknown := env.NewTopWithCause("foreach continuation could not be modeled")
		state.storeExistingVar(fmt.Sprintf("%v", marker.Accumulator), unknown)
		if len(state.stack) > 0 {
			state.stack[len(state.stack)-1] = SValue{Schema: unknown}
			state.invalidateShapeKey()
		}
		return []*execState{state}
	}

	current, currentOK := state.loadVar(loop.accumulator)
	if !currentOK || loop.previous == nil {
		unknown := env.NewTopWithCause("foreach accumulator could not be modeled")
		state.storeExistingVar(loop.accumulator, unknown)
		if len(state.stack) > 0 {
			state.stack[len(state.stack)-1] = SValue{Schema: unknown}
			state.invalidateShapeKey()
		}
		state.foreachLoops = withoutForeachLoop(state.foreachLoops, key)
		return []*execState{state}
	}

	joined := Union([]*oas3.Schema{loop.previous, current}, env.opts)
	stable := schemaSubsumes(loop.previous, current) ||
		schemaFingerprint(joined) == schemaFingerprint(loop.previous)
	round := loop.round + 1
	forcedWiden := false
	env.logger.Debugf("foreach loop: accumulator=%s round=%d previous=%s current=%s joined=%s stable=%v",
		loop.accumulator, round, schemaTypeSummary(loop.previous, 2), schemaTypeSummary(current, 2),
		schemaTypeSummary(joined, 2), stable)
	if !stable && round > loopFixpointRounds+1 {
		joined = env.NewTopWithCause("foreach fixpoint did not stabilize after widening")
		stable = true
		forcedWiden = true
	} else if !stable && round > loopFixpointRounds {
		joined = env.widenLoopValue(joined, make(map[*oas3.Schema]*oas3.Schema))
	}

	emitted := state.clone()
	if forcedWiden {
		emitted.storeExistingVar(loop.accumulator, joined)
		if len(emitted.stack) > 0 {
			emitted.stack[len(emitted.stack)-1] = SValue{Schema: joined}
		}
	}
	emitted.foreachLoops = withoutForeachLoop(emitted.foreachLoops, key)
	emitted.lineage += ".O"
	emitted.invalidateShapeKey()
	if stable {
		return []*execState{emitted}
	}

	reentered := state.clone()
	restoreForkContinuation(reentered, loop.continuation)
	reentered.forks = loop.forks
	reentered.labels = loop.labels
	reentered.forkUpdates = append([]*forkControl(nil), loop.forkUpdates...)
	reentered.storeExistingVar(loop.accumulator, joined)
	reentered.lineage += ".L"
	updated := loop
	updated.previous = joined
	updated.round = round
	updated.continuation = cloneForkContinuation(loop.continuation)
	updated.continuation.scopes = storeVarInScopes(updated.continuation.scopes, loop.accumulator, joined)
	reentered.foreachLoops = cloneForeachLoops(reentered.foreachLoops)
	reentered.foreachLoops[key] = updated
	reentered.invalidateShapeKey()
	return []*execState{emitted, reentered}
}

func cloneForeachLoops(loops map[foreachLoopKey]foreachLoopState) map[foreachLoopKey]foreachLoopState {
	cloned := make(map[foreachLoopKey]foreachLoopState, len(loops)+1)
	for key, loop := range loops {
		cloned[key] = loop
	}
	return cloned
}

func withoutForeachLoop(loops map[foreachLoopKey]foreachLoopState, remove foreachLoopKey) map[foreachLoopKey]foreachLoopState {
	cloned := make(map[foreachLoopKey]foreachLoopState, maxInt(0, len(loops)-1))
	for key, loop := range loops {
		if key != remove {
			cloned[key] = loop
		}
	}
	return cloned
}

func (env *schemaEnv) restoreBacktrackLoopExit(state *execState, loop backtrackLoop, accumulator *oas3.Schema) *execState {
	exit := state.clone()
	restoreForkContinuation(exit, loop.fork)
	exit.storeExistingVar(loop.accumulator, accumulator)
	exit.forks = state.forks.truncate(loop.forkDepth - 1)
	exit.lineage += ".E"
	exit.invalidateShapeKey()
	return exit
}

func (env *schemaEnv) shouldWidenRecursiveCall(stack callStack, targetPC int, input *oas3.Schema) bool {
	if stack.targetInputSubsumes(targetPC, input) {
		return true
	}
	limit := env.opts.MaxDepth / 12
	if limit < 2 {
		limit = 2
	}
	if limit > 8 {
		limit = 8
	}
	return stack.countTarget(targetPC) >= limit
}

// execBackwardJump is a small abstract loop-header cache for compiler-emitted
// while/until/repeat bodies. A repeated state already subsumed by a processed
// header cannot add outputs; growing states are joined for a few rounds and
// then widened once so a typed-but-unknown value cannot run to the global VM
// iteration guard.
func (env *schemaEnv) execBackwardJump(state *execState) []*execState {
	if state == nil {
		return nil
	}
	if env.loopHeads == nil {
		env.loopHeads = make(map[string]*loopHeadState)
	}
	callHash := state.callstack.shapeHash()
	key := fmt.Sprintf("%d:%x:%s", state.pc, callHash, shapeKey(state, false))
	previous := env.loopHeads[key]
	if previous == nil {
		env.loopHeads[key] = &loopHeadState{state: state.clone(), rounds: 1}
		return []*execState{state}
	}
	if loopStateSubsumes(previous.state, state) {
		if !previous.widened && state.pc >= 0 && state.pc < len(env.codes) && env.codes[state.pc].op == opFork {
			widened := env.widenRecursiveLoopState(state)
			env.widenLoopGeneratorArrays(widened)
			env.loopHeads[key] = &loopHeadState{state: widened.clone(), rounds: previous.rounds + 1, widened: true}
			return []*execState{widened}
		}
		env.logger.Debugf("backward loop fixpoint at pc=%d after %d rounds", state.pc, previous.rounds)
		return nil
	}
	if !compatibleStateShapes(previous.state, state, false) {
		// The same bytecode header can be invoked with multiple structural
		// layouts. Do not combine them; call-depth bounding remains the safe
		// fallback for this uncommon case.
		return []*execState{state}
	}

	joined := joinState(previous.state, state, env.opts)
	rounds := previous.rounds + 1
	if rounds > loopFixpointRounds {
		joined = env.widenRecursiveLoopState(joined)
	}
	env.loopHeads[key] = &loopHeadState{state: joined.clone(), rounds: rounds, widened: rounds > loopFixpointRounds}
	return []*execState{joined}
}

func (env *schemaEnv) widenLoopGeneratorArrays(state *execState) bool {
	changed := false
	env.logger.Debugf("widen recursive loop generators: forks=%d", state.forks.len())
	for node := state.forks.tail; node != nil; node = node.prev {
		key := node.value.arrayAppendKey
		env.logger.Debugf("widen recursive loop generator continuation: target=%d key=%q", node.value.targetPC, key)
		if key == "" {
			continue
		}
		array, ok := state.loadVar(key)
		if !ok {
			continue
		}
		allocID, ok := state.schemaToAlloc[array]
		if !ok {
			continue
		}
		canonical := state.accum[allocID]
		if canonical == nil || getType(canonical) != "array" {
			continue
		}
		widened := cloneSchema(canonical)
		widened.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](
			env.NewTopWithCause("recursive array generator reached an abstract fixpoint"))
		widened.PrefixItems = nil
		widened.MinItems = nil
		widened.MaxItems = nil
		state.accum[allocID] = widened
		state.schemaToAlloc[widened] = allocID
		state.storeExistingVar(key, widened)
		changed = true
	}
	return changed
}

func loopStateSubsumes(dom, sub *execState) bool {
	if dom == nil || sub == nil || dom.pc != sub.pc || len(dom.stack) != len(sub.stack) ||
		len(dom.scopes) != len(sub.scopes) || !compatibleStateShapes(dom, sub, false) {
		return false
	}
	for i := range dom.stack {
		if !schemaSubsumes(dom.stack[i].Schema, sub.stack[i].Schema) {
			return false
		}
	}
	for i := range dom.scopes {
		for key, subValue := range sub.scopes[i] {
			domValue, ok := dom.scopes[i][key]
			if !ok || !schemaSubsumes(domValue, subValue) {
				return false
			}
		}
	}
	return true
}

func (env *schemaEnv) widenRecursiveLoopState(state *execState) *execState {
	widened := state.clone()
	activeArrays := make(map[string]struct{})
	for node := state.forks.tail; node != nil; node = node.prev {
		if node.value.arrayAppendKey != "" {
			activeArrays[node.value.arrayAppendKey] = struct{}{}
		}
	}
	for i := range widened.stack {
		if _, ok := getClosurePC(widened.stack[i].Schema); ok {
			continue
		}
		widened.stack[i] = SValue{Schema: env.NewTopWithCause("recursive loop fixpoint exceeded widening budget")}
	}
	for i, scope := range widened.scopes {
		frame := make(map[string]*oas3.Schema, len(scope))
		for key, value := range scope {
			if _, ok := activeArrays[key]; ok {
				frame[key] = value
				continue
			}
			if _, ok := getClosurePC(value); ok {
				frame[key] = value
				continue
			}
			frame[key] = env.NewTopWithCause("recursive loop variable exceeded widening budget")
		}
		widened.scopes[i] = frame
	}
	widened.scopeShapes = nil
	widened.depthWidenBlocked = true
	widened.invalidateShapeKey()
	return widened
}

func loadVarFromScopes(scopes []map[string]*oas3.Schema, key string) (*oas3.Schema, bool) {
	for i := len(scopes) - 1; i >= 0; i-- {
		if value, ok := scopes[i][key]; ok {
			return value, true
		}
	}
	return nil, false
}

func storeVarInScopes(scopes []map[string]*oas3.Schema, key string, value *oas3.Schema) []map[string]*oas3.Schema {
	result := append([]map[string]*oas3.Schema(nil), scopes...)
	for i := len(result) - 1; i >= 0; i-- {
		if _, ok := result[i][key]; !ok {
			continue
		}
		result[i] = cloneScopeMap(result[i])
		result[i][key] = value
		return result
	}
	if len(result) > 0 {
		result[len(result)-1] = cloneScopeMap(result[len(result)-1])
		result[len(result)-1][key] = value
	}
	return result
}

func (s *execState) storeExistingVar(key string, value *oas3.Schema) {
	s.scopes = storeVarInScopes(s.scopes, key, value)
	s.scopeShapes = nil
	s.invalidateShapeKey()
}

// widenLoopValue removes value/count facets after bounded growth while
// retaining cheap container shape. It is applied only after repeated strict
// growth; one additional body pass then proves the widened fixpoint stable.
func (env *schemaEnv) widenLoopValue(schema *oas3.Schema, seen map[*oas3.Schema]*oas3.Schema) *oas3.Schema {
	if schema == nil {
		return Bottom()
	}
	if isTopSchema(schema) {
		return env.NewTopWithCause("reduce/foreach fixpoint exceeded widening budget")
	}
	if widened, ok := seen[schema]; ok {
		return widened
	}
	if branches, ok := disjunctiveBranches(schema); ok {
		widened := make([]*oas3.Schema, 0, len(branches))
		for _, branch := range branches {
			widened = append(widened, env.widenLoopValue(branch, seen))
		}
		return Union(widened, env.opts)
	}

	switch getType(schema) {
	case "string":
		return StringType()
	case "integer":
		return IntegerType()
	case "number":
		return NumberType()
	case "boolean":
		return BoolType()
	case "null":
		return NullType()
	case "array":
		result := ArrayType(Top())
		seen[schema] = result
		if items := arrayElementUnion(schema, env.opts); items != nil {
			result.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](env.widenLoopValue(items, seen))
		}
		return result
	case "object":
		result := cloneSchema(schema)
		seen[schema] = result
		result.Enum = nil
		result.Const = nil
		result.MinProperties = nil
		result.MaxProperties = nil
		if schema.Properties != nil {
			result.Properties = cloneSchemaMap(schema.Properties)
			for key, property := range schema.Properties.All() {
				if child := resolvedLeft(property); child != nil {
					result.Properties.Set(key, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](env.widenLoopValue(child, seen)))
				}
			}
		}
		if child := resolvedLeft(schema.AdditionalProperties); child != nil {
			result.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](env.widenLoopValue(child, seen))
		}
		return result
	default:
		return env.NewTopWithCause("reduce/foreach accumulator type is unknown")
	}
}

func (env *schemaEnv) arrayGeneratorAppendKey(targetPC int) (string, bool) {
	appendPC := targetPC - 2
	backtrackPC := targetPC - 1
	if appendPC < 0 || backtrackPC < 0 || backtrackPC >= len(env.codes) ||
		env.codes[appendPC].op != opAppend || env.codes[backtrackPC].op != opBacktrack {
		return "", false
	}
	return fmt.Sprintf("%v", env.codes[appendPC].value), true
}

func (env *schemaEnv) execBreak(state *execState, token *oas3.Schema) ([]*execState, error) {
	value, ok := extractConstString(token)
	if !ok || !strings.HasPrefix(value, "jq-label:") {
		return []*execState{}, nil
	}
	key := strings.TrimPrefix(value, "jq-label:")
	labelNode := state.labels.find(key)
	if labelNode == nil {
		return []*execState{}, nil
	}

	label := labelNode.value
	if label.forkDepth == 0 {
		return []*execState{}, nil
	}
	if label.forkDepth > state.forks.len() {
		return []*execState{}, nil
	}
	forkIndex := label.forkDepth - 1
	fork, ok := state.forks.atDepth(label.forkDepth)
	if !ok {
		return []*execState{}, nil
	}
	restoreForkContinuation(state, fork)
	state.forks = state.forks.truncate(forkIndex)
	state.labels = labelContinuations{tail: labelNode.prev}
	state.invalidateShapeKey()
	state.lineage += ".B"
	return []*execState{state}, nil
}

func restoreForkContinuation(state *execState, fork forkContinuation) {
	state.pc = fork.targetPC
	state.stack = append([]SValue(nil), fork.stack...)
	currentScopes := state.scopes
	state.scopes = cloneScopeMaps(fork.scopes)
	for i := 0; i < len(currentScopes) && i < len(state.scopes); i++ {
		for key, value := range currentScopes[i] {
			state.scopes[i][key] = value
		}
	}
	state.scopeShapes = nil
	state.callstack = fork.callstack
	state.depth = fork.depth
	state.pathMode = fork.pathMode
	state.currentPath = fork.currentPath
	state.pathEvalBases = fork.pathEvalBases
	state.tryDepth = fork.tryDepth
	state.invalidateShapeKey()
}

// execForkAlt handles alternative fork (// operator).
func (env *schemaEnv) execForkAlt(state *execState, c *codeOp) ([]*execState, error) {
	// Similar to fork but with alt semantics
	// For now, treat same as fork
	return env.execFork(state, c)
}

// execJumpIfNot handles conditional jump.
func (env *schemaEnv) execJumpIfNot(state *execState, c *codeOp) ([]*execState, error) {
	tested := state.popValue()
	val := tested.Schema

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
			refineTestedValue(continueState, tested, nn, env.opts)
			// Else-branch: value is null
			refineTestedValue(jumpState, tested, ConstNull(), env.opts)
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
		inputValue := state.popValue()
		input := inputValue.Schema

		// Then pop args in left-to-right order (matching the push order from compiler)
		args := make([]*oas3.Schema, argCount)
		for i := 0; i < argCount; i++ {
			if len(state.stack) == 0 {
				return nil, fmt.Errorf("stack underflow on call args")
			}
			args[i] = state.pop()
		}
		if funcName == "_break" {
			return env.execBreak(state, input)
		}
		if state.pathMode && funcName == "_index" {
			state.currentPath = append(state.currentPath[:len(state.currentPath):len(state.currentPath)], dynamicPathSegment(input, args))
			state.push(Top())
			return []*execState{state}, nil
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

		// Propagate compiler-owned accumulator updates to queued iterations.
		if (funcName == "setpath" || funcName == "_setpath") && len(results) == 1 && results[0] != nil {
			for _, accumulatorKey := range env.setpathAccumulatorKeys(state, inputValue) {
				refineForkVarRefs(state, accumulatorKey, input, results[0])
			}
			if env.opts.EnableWarnings {
				env.logger.Debugf("execCallMulti: processed %s result (old ptr=%p, new ptr=%p)",
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
		input := state.top()
		if env.shouldWidenRecursiveCall(state.callstack, targetPC, input) {
			state.pop()
			state.push(env.NewTopWithCause("recursion depth limit for function call"))
			return []*execState{state}, nil
		}
		// Record call site PC (for accumulator disambiguation)
		// removed callSitePC = state.pc
		// Push return PC (state.pc is already incremented by dispatcher)
		state.callstack = state.callstack.append(state.pc, targetPC, input)
		state.invalidateShapeKey()
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

func (env *schemaEnv) setpathAccumulatorKeys(state *execState, input SValue) []string {
	if state == nil || input.rootVar == "" || state.pc < 0 || state.pc >= len(env.codes) {
		return nil
	}
	next := env.codes[state.pc]
	if next.op != opStore || fmt.Sprintf("%v", next.value) != input.rootVar {
		return nil
	}
	keys := []string{input.rootVar}
	seen := map[string]struct{}{input.rootVar: {}}
	for call := state.callstack.tail; call != nil; call = call.prev {
		if call.returnPC < 0 || call.returnPC >= len(env.codes) {
			continue
		}
		store := env.codes[call.returnPC]
		if store.op != opStore {
			continue
		}
		key := fmt.Sprintf("%v", store.value)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	return keys
}

func (env *schemaEnv) returnsToDynamicIndex(returnPC int) bool {
	for pc := returnPC; pc < len(env.codes) && pc < returnPC+8; pc++ {
		code := env.codes[pc]
		if code.op == opRet || code.op == opCallPC {
			return false
		}
		if code.op != opCall {
			continue
		}
		if value, ok := code.value.([3]any); ok {
			name, _ := value[2].(string)
			return name == "_index"
		}
		return false
	}
	return false
}

func dynamicPathSegment(input *oas3.Schema, args []*oas3.Schema) PathSegment {
	candidates := append(append(make([]*oas3.Schema, 0, len(args)+1), args...), input)
	for _, candidate := range candidates {
		if key, ok := extractConstString(candidate); ok {
			return PathSegment{Key: key}
		}
		if value, ok := extractConstValue(candidate); ok {
			if number, ok := value.(float64); ok && number == math.Trunc(number) {
				return PathSegment{Key: int(number)}
			}
		}
	}
	for _, candidate := range candidates {
		if getType(candidate) == "string" || getType(candidate) == "integer" || getType(candidate) == "number" {
			return PathSegment{Key: PathWildcard{}, IsSymbolic: true}
		}
	}
	return PathSegment{Key: PathWildcard{}, IsSymbolic: true}
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
	case symbolicEnvironment:
		return OpenObjectType(StringType())
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
			if schema, ok := derefJSONSchema(collapseContextForOptions(opts), v); ok {
				schemas = append(schemas, schema)
				if opts.EnableWarnings {
					opts.debugf("unionAllObjectValues: property %s type=%s", k, getType(schema))
				}
			} else {
				// Unresolved reference in property - widen conservatively
				schemas = append(schemas, Top())
				if opts.EnableWarnings {
					opts.debugf("unionAllObjectValues: property %s UNRESOLVED -> Top", k)
				}
			}
		}
	}

	// Add additionalProperties
	if obj.AdditionalProperties != nil {
		if schema, ok := derefJSONSchema(collapseContextForOptions(opts), obj.AdditionalProperties); ok {
			schemas = append(schemas, schema)
			if opts.EnableWarnings {
				opts.debugf("unionAllObjectValues: additionalProperties type=%s, unconstrained=%v",
					getType(schema), isUnconstrainedSchema(schema))
			}
		} else {
			// Unresolved reference in additionalProperties - widen conservatively
			schemas = append(schemas, Top())
			if opts.EnableWarnings {
				opts.debugf("unionAllObjectValues: additionalProperties UNRESOLVED -> Top")
			}
		}
	} else if opts.Semantics == SchemaSemanticsRaw {
		// Raw JSON Schema semantics: absent additionalProperties means the
		// object is OPEN — iteration may yield values of any type beyond the
		// declared properties.
		schemas = append(schemas, Top())
	}

	if obj.PatternProperties != nil {
		for pattern, wrapper := range obj.PatternProperties.All() {
			if schema, ok := derefJSONSchema(collapseContextForOptions(opts), wrapper); ok {
				schemas = append(schemas, schema)
				if opts.EnableWarnings {
					opts.debugf("unionAllObjectValues: patternProperty %s type=%s", pattern, getType(schema))
				}
			} else {
				schemas = append(schemas, Top())
			}
		}
	}

	if len(schemas) == 0 {
		if opts.EnableWarnings {
			opts.debugf("unionAllObjectValues: no schemas found")
		}
		if obj.PatternProperties != nil && obj.PatternProperties.Len() > 0 {
			return Top()
		}
		return Bottom() // closed object with no possible values
	}

	result := Union(schemas, opts)
	if opts.EnableWarnings {
		opts.debugf("unionAllObjectValues: union result type=%s, unconstrained=%v (from %d schemas)",
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

	// Constant array literals have exact positional contents.
	prefixItems := make([]*oas3.Schema, len(arr))

	for i, v := range arr {
		prefixItems[i] = valueToSchemaStatic(v)
	}
	result := BuildArray(nil, prefixItems)
	length := int64(len(arr))
	result.MinItems = &length
	result.MaxItems = &length
	result.Items = oas3.NewJSONSchemaFromBool(false)
	return result
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

	// An unknown-depth tail admits any number of further segments, so the
	// path has no faithful tuple form. Emitting the known prefix as
	// prefixItems would let setpath perform a strong write at the wrong
	// depth; a homogeneous unknown-length key array routes every consumer to
	// its conservative dynamic-path handling instead.
	for _, seg := range segments {
		if _, ok := seg.Key.(PathUnknownSubtree); ok {
			result := ArrayType(Union([]*oas3.Schema{StringType(), IntegerType()}, SchemaExecOptions{}))
			if known := int64(len(segments) - 1); known > 0 {
				result.MinItems = &known
			}
			return result
		}
	}

	prefixItems := make([]*oas3.Schema, len(segments))
	for i, seg := range segments {
		if _, ok := seg.Key.(PathAllElements); ok {
			prefixItems[i] = allElementsPathSchema()
		} else if _, ok := seg.Key.(PathUnknownStringKey); ok {
			// Unknown single object key from a state join: non-const string.
			prefixItems[i] = StringType()
		} else if _, ok := seg.Key.(PathUnknownKey); ok {
			// Unknown single key or index from a state join.
			prefixItems[i] = Union([]*oas3.Schema{StringType(), IntegerType()}, SchemaExecOptions{})
		} else if seg.IsSymbolic {
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
// The walk itself is shared with the Analyze API (collectSchemaIssues).
func (env *schemaEnv) validateStrictResult(schema *oas3.Schema) error {
	issues := collectSchemaIssues(schema, env.topCauses)
	if len(issues) == 0 {
		return nil
	}
	first := issues[0]
	if first.isBottom {
		return fmt.Errorf("strict mode: result contains Bottom at %s", first.path)
	}
	if first.cause != "" {
		return fmt.Errorf("strict mode: result contains Top at %s; cause: %s", first.path, first.cause)
	}
	return fmt.Errorf("strict mode: result contains Top at %s", first.path)
}

// isTopSchema checks if a schema is Top using both pointer identity and structural checks.
func isTopSchema(s *oas3.Schema) bool {
	if s == nil {
		return false
	}
	// Pointer identity check
	if s == Top() {
		return true
	}

	// Structural check: empty type with no constraints. Any validation facet
	// below makes the schema non-universal — treating a constraint-bearing
	// schema as Top would let subsumption drop branches it does not subsume.
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
		s.ExclusiveMinimum == nil &&
		s.ExclusiveMaximum == nil &&
		s.MultipleOf == nil &&
		s.Items == nil &&
		s.PrefixItems == nil &&
		s.MinItems == nil &&
		s.MaxItems == nil &&
		s.UniqueItems == nil &&
		s.Contains == nil &&
		s.MinProperties == nil &&
		s.MaxProperties == nil &&
		s.Required == nil &&
		s.PatternProperties == nil &&
		s.PropertyNames == nil &&
		s.DependentSchemas == nil &&
		s.If == nil &&
		s.Then == nil &&
		s.Else == nil &&
		s.UnevaluatedProperties == nil &&
		s.UnevaluatedItems == nil &&
		s.ContentSchema == nil &&
		s.Ref == nil {
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
	return env.materializeArraysSeen(schema, accum, schemaToAlloc, make(map[*oas3.Schema]bool), redirect...)
}

func (env *schemaEnv) materializeArraysSeen(schema *oas3.Schema, accum map[string]*oas3.Schema, schemaToAlloc map[*oas3.Schema]string, seen map[*oas3.Schema]bool, redirect ...map[string]string) *oas3.Schema {
	if schema == nil {
		return nil
	}
	if seen[schema] {
		return schema
	}
	seen[schema] = true
	defer delete(seen, schema)

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
						env.logger.Debugf("materialize: redirecting %s -> %s for array lookup", allocID, redirectTo)
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
				// Recurse into the canonical array’s items/prefixItems before returning.
				// Children are read via resolvedLeft and original wrappers are kept
				// when unchanged, so resolved $ref children keep their resolution.
				arr := *canonical
				changed := false
				// Items
				if left := resolvedLeft(arr.Items); left != nil {
					newItems := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
					if newItems != left {
						arr.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newItems)
						changed = true
					}
				}
				// PrefixItems
				if len(arr.PrefixItems) > 0 {
					newPrefix := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(arr.PrefixItems))
					prefixChanged := false
					for _, pi := range arr.PrefixItems {
						if left := resolvedLeft(pi); left != nil {
							newPi := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
							if newPi != left {
								newPrefix = append(newPrefix, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newPi))
								prefixChanged = true
								continue
							}
						}
						newPrefix = append(newPrefix, pi)
					}
					if prefixChanged {
						arr.PrefixItems = newPrefix
						changed = true
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
		itemsLeft := resolvedLeft(schema.Items)
		hasEmptyItems := (itemsLeft == nil || getType(itemsLeft) == "")
		if hasEmptyItems {
			for allocID, canonical := range accum {
				if canonical == schema {
					// This IS a canonical array - recurse into its internals
					arr := *canonical
					changed := false
					if left := resolvedLeft(arr.Items); left != nil {
						newItems := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
						if newItems != left {
							arr.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newItems)
							changed = true
						}
					}
					if len(arr.PrefixItems) > 0 {
						newPrefix := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(arr.PrefixItems))
						prefixChanged := false
						for _, pi := range arr.PrefixItems {
							if left := resolvedLeft(pi); left != nil {
								newPi := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
								if newPi != left {
									newPrefix = append(newPrefix, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newPi))
									prefixChanged = true
									continue
								}
							}
							newPrefix = append(newPrefix, pi)
						}
						if prefixChanged {
							arr.PrefixItems = newPrefix
							changed = true
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
		if left := resolvedLeft(clone.Items); left != nil {
			newItems := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
			if newItems != left {
				clone.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newItems)
				changed = true
			}
		}
		if len(clone.PrefixItems) > 0 {
			newPrefix := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(clone.PrefixItems))
			prefixChanged := false
			for _, pi := range clone.PrefixItems {
				if left := resolvedLeft(pi); left != nil {
					newPi := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
					if newPi != left {
						newPrefix = append(newPrefix, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newPi))
						prefixChanged = true
						continue
					}
				}
				newPrefix = append(newPrefix, pi)
			}
			if prefixChanged {
				clone.PrefixItems = newPrefix
				changed = true
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
			if left := resolvedLeft(propSchema); left != nil {
				materialized := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
				if materialized != left {
					modified = true
					newProps.Set(k, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](materialized))
				} else {
					// Unchanged: keep the original wrapper (and its $ref resolution)
					newProps.Set(k, propSchema)
				}
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
		if left := resolvedLeft(clone.AdditionalProperties); left != nil {
			newAP := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
			if newAP != left {
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
	if len(schema.AnyOf) > 0 {
		changed := false
		newAny := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(schema.AnyOf))
		for _, br := range schema.AnyOf {
			if left := resolvedLeft(br); left != nil {
				newBr := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
				if newBr != left {
					newAny = append(newAny, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newBr))
					changed = true
					continue
				}
			}
			newAny = append(newAny, br)
		}
		if changed {
			clone := *schema
			clone.AnyOf = newAny
			return &clone
		}
	}
	// oneOf
	if len(schema.OneOf) > 0 {
		changed := false
		newOne := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(schema.OneOf))
		for _, br := range schema.OneOf {
			if left := resolvedLeft(br); left != nil {
				newBr := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
				if newBr != left {
					newOne = append(newOne, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newBr))
					changed = true
					continue
				}
			}
			newOne = append(newOne, br)
		}
		if changed {
			clone := *schema
			clone.OneOf = newOne
			return &clone
		}
	}
	// allOf
	if len(schema.AllOf) > 0 {
		changed := false
		newAll := make([]*oas3.JSONSchema[oas3.Referenceable], 0, len(schema.AllOf))
		for _, br := range schema.AllOf {
			if left := resolvedLeft(br); left != nil {
				newBr := env.materializeArraysSeen(left, accum, schemaToAlloc, seen, allocRedirect)
				if newBr != left {
					newAll = append(newAll, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](newBr))
					changed = true
					continue
				}
			}
			newAll = append(newAll, br)
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

	// Iterate PCs in sorted order: the concatenation order of merged states
	// feeds the worklist, and downstream accumulator joins are
	// who-writes-last sensitive. Map order here made outputs flaky.
	pcs := make([]int, 0, len(byPC))
	for pc := range byPC {
		pcs = append(pcs, pc)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(pcs)))

	out := make([]*execState, 0, len(in))
	for _, pc := range pcs {
		group := byPC[pc]
		// Only merge at hot PCs (opIter or high fan-in)
		if !env.shouldMergeAtPC(pc, len(group)) {
			out = append(out, group...)
			continue
		}

		// Partition by shape
		// At hot merge points, we relax scope-key matching to allow more aggressive merging
		relaxScopeKeys := env.codes[pc].op != opIter || len(group) >= 8
		partitions := partitionByShape(group, relaxScopeKeys)
		if env.codes[pc].op == opJumpIfNot || pc+1 < len(env.codes) && env.codes[pc+1].op == opJumpIfNot {
			partitions = partitionByConstantBoolean(partitions)
		}

		for _, partition := range partitions {
			if len(partition) == 1 {
				out = append(out, partition[0])
				continue
			}

			// Large partitions are cheaper to fold directly than to compare
			// pairwise for subsumption.
			const mergeCap = 16
			if len(partition) > mergeCap {
				merged := partition[0]
				for i := 1; i < len(partition); i++ {
					merged = joinState(merged, partition[i], env.opts)
				}
				out = append(out, merged)
				continue
			}

			kept := make([]*execState, 0, len(partition))
			for _, candidate := range partition {
				subsumed := false
				for _, other := range partition {
					if candidate != other && subsumesState(other, candidate) {
						subsumed = true
						break
					}
				}
				if !subsumed {
					kept = append(kept, candidate)
				}
			}
			if len(kept) == 0 {
				kept = append(kept, partition[0])
			}
			merged := partition[0]
			if len(kept) > 0 {
				merged = kept[0]
			}
			for i := 1; i < len(kept); i++ {
				merged = joinState(merged, kept[i], env.opts)
			}
			out = append(out, merged)
		}
	}

	return out
}

// subsumesState checks if state 'dom' subsumes state 'sub' (dom ⊒ sub).
// This means dom's abstract values are at least as general as sub's.
func subsumesState(dom, sub *execState) bool {
	// Subsumption discards sub entirely, so dom must cover more than sub's
	// abstract values: a state's collected write path and its pending update
	// controls are obligations, not values. Discarding a state that collected
	// a different path deletes that write; discarding one with different
	// pending controls starves its eager fork alternatives of updates.
	if dom.pathMode != sub.pathMode || !reflect.DeepEqual(dom.currentPath, sub.currentPath) {
		return false
	}
	if !equalForkUpdates(dom.forkUpdates, sub.forkUpdates) {
		return false
	}
	if dom.pc != sub.pc {
		return false
	}
	if len(dom.stack) != len(sub.stack) {
		return false
	}
	if len(dom.scopes) != len(sub.scopes) {
		return false
	}
	if !depthWidenContextSubsumes(dom, sub) {
		return false
	}

	// Check stack subsumption
	for i := range dom.stack {
		if !sameValueProvenance(dom.stack[i], sub.stack[i]) {
			return false
		}
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

func depthWidenContextSubsumes(dom, sub *execState) bool {
	if dom.depthWidenBlocked {
		return true
	}
	if sub.depthWidenBlocked {
		return false
	}
	return equalArrayGeneratorContexts(dom.forks, sub.forks)
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

// unionForkUpdates merges the pending update controls of two joined states so
// writes that happen after the merge reach the eager alternatives of both.
func unionForkUpdates(a, b []*forkControl) []*forkControl {
	if len(b) == 0 {
		return a
	}
	if len(a) == 0 {
		return b
	}
	seen := make(map[*forkControl]struct{}, len(a)+len(b))
	out := make([]*forkControl, 0, len(a)+len(b))
	for _, controls := range [2][]*forkControl{a, b} {
		for _, control := range controls {
			if control == nil {
				continue
			}
			if _, ok := seen[control]; ok {
				continue
			}
			seen[control] = struct{}{}
			out = append(out, control)
		}
	}
	return out
}

// joinState merges two states using lattice join (LUB).
// When scope keys differ, union them and treat missing keys as if they were present
// with the value from the other state (join with implicit "undefined" = keep existing).
func joinState(a, b *execState, opts SchemaExecOptions) *execState {
	if a.pc != b.pc {
		panic("joinState: PC mismatch")
	}
	if len(a.stack) != len(b.stack) {
		panic("joinState: stack length mismatch")
	}
	if len(a.scopes) != len(b.scopes) {
		panic("joinState: scope length mismatch")
	}

	// Joined states always share the accumulator maps: accum/schemaToAlloc are
	// created once in newExecState and propagated by reference through clones
	// and joins, so the join keeps the shared maps.
	mergedForks := joinForkContinuations(a.forks, b.forks, opts)
	merged := &execState{
		pc:            a.pc,
		stack:         make([]SValue, len(a.stack)),
		scopes:        make([]map[string]*oas3.Schema, len(a.scopes)),
		depth:         maxInt(a.depth, b.depth),
		callstack:     a.callstack, // Assume same callstack in partition
		forks:         mergedForks,
		labels:        joinLabelContinuations(a.labels, b.labels, mergedForks.len()),
		foreachLoops:  joinForeachLoops(a.foreachLoops, b.foreachLoops, opts),
		tryDepth:      a.tryDepth,
		pathMode:      a.pathMode,
		currentPath:   joinCurrentPaths(a.currentPath, b.currentPath),
		pathEvalBases: append([]int(nil), a.pathEvalBases...),
		depthWidenBlocked: a.depthWidenBlocked || b.depthWidenBlocked ||
			!equalArrayGeneratorContexts(a.forks, b.forks),
		id:       a.id,
		parentID: a.parentID,
		lineage:  a.lineage,
		// Preserve shared fields from first state
		accum:         a.accum,
		schemaToAlloc: a.schemaToAlloc,
		allocCounter:  a.allocCounter,
		// Theory 10: Hybrid Origin-Lattice (SHARED since accum is same)
		allocOrigin:      a.allocOrigin,
		allocCardinality: a.allocCardinality,
		dsu:              a.dsu,
		// Post-merge writes must reach the eager alternatives of both
		// joined states.
		forkUpdates: unionForkUpdates(a.forkUpdates, b.forkUpdates),
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
						opts.debugf("joinState: preserving canonical ptr for allocID=%s (stack pos %d), canonical IS tagged as %s", aAlloc, i, taggedAlloc)
					} else {
						opts.debugf("joinState: preserving canonical ptr for allocID=%s (stack pos %d), canonical NOT TAGGED! Re-tagging now.", aAlloc, i)
						merged.schemaToAlloc[canonical] = aAlloc
					}
					merged.stack[i] = joinedStackValue(a.stack[i], b.stack[i], opts)
					merged.stack[i].Schema = canonical
					continue
				}
			}
		}

		// Default: join via union
		merged.stack[i] = joinedStackValue(a.stack[i], b.stack[i], opts)
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
				opts.debugf("scope-merge (same-accum): var=%s aHas=%v bHas=%v aType=%s bType=%s aEmpty=%v bEmpty=%v",
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
						if sameOrigin(oa, ob) {
							// Same origin => union classes
							opts.debugf("DSU joinState(same-accum): var=%s union %s with %s (same origin PC=%d, ctx=%s)",
								k, aAlloc, bAlloc, oa.PC, oa.Context)
							a.dsu.Union(aAlloc, bAlloc)
							root := a.dsu.Find(aAlloc)

							// Merge canonical arrays and bind canonical to merged scope
							joined := joinTwoSchemas(aVal, bVal, opts)
							if joined != nil {
								merged.accum[root] = joined
								merged.schemaToAlloc[joined] = root
								mergedScope[k] = joined
								opts.debugf("DSU joinState(same-accum): merged to root=%s", root)
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
									opts.debugf("DSU joinState(same-accum): cardinality root=%s MinItems=%d",
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
						opts.debugf("scope-merge: var=%s isEmpty(a)=%v isEmpty(b)=%v", k, isEmpty(aVal), isEmpty(bVal))
					}

					// Prefer non-empty over empty, regardless of tagging
					// This prevents empty arrays from clobbering real data during merges
					if isEmpty(aVal) && !isEmpty(bVal) {
						mergedScope[k] = bVal
						// Also update accumulator canonical if bVal is tagged
						if bTagged {
							merged.accum[bAlloc] = bVal
						}
						opts.debugf("scope-merge (same-accum): var=%s preferring non-empty b over empty a (bTagged=%v, bAlloc=%s)", k, bTagged, bAlloc)
						continue
					}
					if isEmpty(bVal) && !isEmpty(aVal) {
						mergedScope[k] = aVal
						// Also update accumulator canonical if aVal is tagged
						if aTagged {
							merged.accum[aAlloc] = aVal
						}
						opts.debugf("scope-merge (same-accum): var=%s preferring non-empty a over empty b (aTagged=%v, aAlloc=%s)", k, aTagged, aAlloc)
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
					joined := joinTwoSchemas(aVal, bVal, opts)
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
								opts.debugf("Case3 (same-accum): propagated intent to new alloc %s for var=%s", id, k)
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
				mergedScope[k] = joinTwoSchemas(aVal, bVal, opts)
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

func joinForeachLoops(a, b map[foreachLoopKey]foreachLoopState, opts SchemaExecOptions) map[foreachLoopKey]foreachLoopState {
	if len(a) == 0 {
		return b
	}
	if len(b) == 0 {
		return a
	}
	joined := make(map[foreachLoopKey]foreachLoopState, len(a))
	for key, left := range a {
		right, ok := b[key]
		if !ok {
			continue
		}
		loop := left
		loop.previous = Union([]*oas3.Schema{left.previous, right.previous}, opts)
		loop.round = maxInt(left.round, right.round)
		loop.continuation = cloneForkContinuation(left.continuation)
		loop.forks = joinForkContinuations(left.forks, right.forks, opts)
		loop.labels = joinLabelContinuations(left.labels, right.labels, loop.forks.len())
		loop.forkUpdates = append([]*forkControl(nil), left.forkUpdates...)
		for index := range loop.continuation.stack {
			loop.continuation.stack[index] = joinedStackValue(
				left.continuation.stack[index], right.continuation.stack[index], opts)
			loop.continuation.stack[index].Schema = Union([]*oas3.Schema{
				left.continuation.stack[index].Schema,
				right.continuation.stack[index].Schema,
			}, opts)
		}
		for index := range loop.continuation.scopes {
			frame := cloneScopeMap(left.continuation.scopes[index])
			for name, value := range right.continuation.scopes[index] {
				if existing, exists := frame[name]; exists {
					frame[name] = Union([]*oas3.Schema{existing, value}, opts)
				} else {
					frame[name] = value
				}
			}
			loop.continuation.scopes[index] = frame
		}
		joined[key] = loop
	}
	return joined
}

// joinTwoSchemas performs schema-level join (LUB) using Union.
func joinTwoSchemas(a, b *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	// CRITICAL FIX: If pointers are identical, return immediately to preserve pointer identity
	// This prevents unnecessary cloning and preserves tags in schemaToAlloc
	if a == b {
		return a
	}

	return Union([]*oas3.Schema{a, b}, opts)
}

// partitionByShape groups states with compatible layouts.
// If relaxScopeKeys is true, ignore scope variable names in the shape key
// to allow more aggressive merging at hot join points.
func partitionByShape(states []*execState, relaxScopeKeys bool) [][]*execState {
	buckets := make(map[string][][]*execState)
	for _, s := range states {
		key := shapeKey(s, relaxScopeKeys)
		groups := buckets[key]
		matched := false
		for i := range groups {
			if compatibleStateShapes(groups[i][0], s, relaxScopeKeys) {
				groups[i] = append(groups[i], s)
				matched = true
				break
			}
		}
		if !matched {
			groups = append(groups, []*execState{s})
		}
		buckets[key] = groups
	}

	result := make([][]*execState, 0, len(buckets))
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		groups := buckets[key]
		result = append(result, groups...)
	}
	return result
}

func partitionByConstantBoolean(partitions [][]*execState) [][]*execState {
	result := make([][]*execState, 0, len(partitions))
	for _, partition := range partitions {
		var buckets [3][]*execState
		var seen [3]bool
		order := make([]int, 0, len(buckets))
		for _, state := range partition {
			bucket := 0
			if value, ok := extractConstValue(state.top()); ok {
				if boolean, ok := value.(bool); ok {
					if boolean {
						bucket = 2
					} else {
						bucket = 1
					}
				}
			}
			if !seen[bucket] {
				seen[bucket] = true
				order = append(order, bucket)
			}
			buckets[bucket] = append(buckets[bucket], state)
		}
		for _, bucket := range order {
			result = append(result, buckets[bucket])
		}
	}
	return result
}

// compatibleStateShapes verifies the hashed persistent-stack components used
// by shapeKey. Pointer equality makes the common shared case O(1); the exact
// fallback prevents hash collisions from merging incompatible control states.
func compatibleStateShapes(a, b *execState, relaxScopeKeys bool) bool {
	if len(a.stack) != len(b.stack) || len(a.scopes) != len(b.scopes) ||
		a.tryDepth != b.tryDepth || !equalCallStacks(a.callstack, b.callstack) ||
		!equalLabelContinuations(a.labels, b.labels) || !compatibleForeachLoops(a.foreachLoops, b.foreachLoops) {
		return false
	}
	if a.scopeShapes != b.scopeShapes {
		for i := range a.scopes {
			if len(a.scopes[i]) != len(b.scopes[i]) {
				return false
			}
			if !relaxScopeKeys {
				for key := range a.scopes[i] {
					if _, ok := b.scopes[i][key]; !ok {
						return false
					}
				}
			}
		}
	}
	if !reflect.DeepEqual(a.pathEvalBases, b.pathEvalBases) {
		return false
	}
	// Merged states must agree on path-collection mode: joinState joins the
	// collected paths themselves (segmentwise weak symbolic segments, with an
	// unknown-depth tail for cross-depth joins), so differing path contents
	// are mergeable — and must stay mergeable, because path enumeration over
	// recursive inputs relies on these joins for termination.
	if a.pathMode != b.pathMode {
		return false
	}
	labelForkDepth := 0
	if a.labels.tail != nil {
		labelForkDepth = a.labels.tail.maxForkDepth
	}
	aDepth := min(labelForkDepth, a.forks.len())
	bDepth := min(labelForkDepth, b.forks.len())
	if aDepth != bDepth {
		return false
	}
	depth := aDepth
	left, right := a.forks.nodeAtDepth(depth), b.forks.nodeAtDepth(depth)
	if left == right {
		return true
	}
	for left != nil && right != nil {
		if !compatibleForkContinuations(left.value, right.value) {
			return false
		}
		left, right = left.prev, right.prev
	}
	return left == nil && right == nil
}

// shapeKey generates a deterministic key encoding the state's structural shape.
// If relaxScopeKeys is true, only include scope frame counts, not variable names.
func shapeKey(s *execState, relaxScopeKeys bool) string {
	if relaxScopeKeys && s.relaxedShapeKeyValid {
		return s.relaxedShapeKey
	}
	if !relaxScopeKeys && s.shapeKeyValid {
		return s.shapeKey
	}
	var buf strings.Builder

	// Stack length
	buf.WriteString("stack:")
	buf.WriteString(strconv.Itoa(len(s.stack)))
	buf.WriteString(";")

	scopeHash := scopeShapeHash(s, relaxScopeKeys)
	buf.Write(scopeHash[:])

	// Callstack
	buf.WriteString("callstack:")
	callstackHash := s.callstack.shapeHash()
	buf.Write(callstackHash[:])
	buf.WriteString(";labels:")
	labelForkDepth := 0
	if s.labels.tail != nil {
		buf.Write(s.labels.tail.shape[:])
		labelForkDepth = s.labels.tail.maxForkDepth
	}
	buf.WriteString("label-forks:")
	if node := s.forks.nodeAtDepth(min(labelForkDepth, s.forks.len())); node != nil {
		buf.Write(node.shape[:])
	}
	buf.WriteString(";try:")
	buf.WriteString(strconv.Itoa(s.tryDepth))
	buf.WriteString(";foreach:")
	for _, key := range sortedForeachLoopKeys(s.foreachLoops) {
		buf.WriteString(strconv.Itoa(key.markerPC))
		buf.WriteByte('/')
		buf.WriteString(strconv.Itoa(key.callDepth))
		buf.WriteByte(',')
	}

	key := buf.String()
	if relaxScopeKeys {
		s.relaxedShapeKey = key
		s.relaxedShapeKeyValid = true
	} else {
		s.shapeKey = key
		s.shapeKeyValid = true
	}
	return key
}

func sortedForeachLoopKeys(loops map[foreachLoopKey]foreachLoopState) []foreachLoopKey {
	keys := make([]foreachLoopKey, 0, len(loops))
	for key := range loops {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].markerPC != keys[j].markerPC {
			return keys[i].markerPC < keys[j].markerPC
		}
		return keys[i].callDepth < keys[j].callDepth
	})
	return keys
}

func compatibleForeachLoops(a, b map[foreachLoopKey]foreachLoopState) bool {
	if len(a) != len(b) {
		return false
	}
	for key, left := range a {
		right, ok := b[key]
		if !ok || left.accumulator != right.accumulator ||
			!compatibleForkContinuations(left.continuation, right.continuation) ||
			!compatibleForkStacks(left.forks, right.forks) ||
			!equalLabelContinuations(left.labels, right.labels) ||
			!equalForkUpdates(left.forkUpdates, right.forkUpdates) {
			return false
		}
	}
	return true
}

func equalForkUpdates(a, b []*forkControl) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

func compatibleForkStacks(a, b forkContinuations) bool {
	if a.tail == b.tail {
		return true
	}
	if a.len() != b.len() {
		return false
	}
	left, right := a.tail, b.tail
	for left != nil && right != nil {
		if !compatibleForkContinuations(left.value, right.value) {
			return false
		}
		left, right = left.prev, right.prev
	}
	return left == nil && right == nil
}

func scopeShapeHash(s *execState, relaxed bool) [sha256.Size]byte {
	if s.scopeShapes == nil || s.scopeShapes.depth != len(s.scopes) {
		var shapes *scopeShapeNode
		for _, frame := range s.scopes {
			shapes = appendScopeShape(shapes, frame)
		}
		s.scopeShapes = shapes
	}
	if s.scopeShapes == nil {
		return [sha256.Size]byte{}
	}
	if relaxed {
		return s.scopeShapes.relaxed
	}
	return s.scopeShapes.exact
}

func pathSegmentsKey(segments []PathSegment) string {
	var buf strings.Builder
	buf.WriteByte('[')
	for _, segment := range segments {
		switch key := segment.Key.(type) {
		case string:
			buf.WriteString("s:")
			buf.WriteString(strconv.Quote(key))
		case int:
			buf.WriteString("i:")
			buf.WriteString(strconv.Itoa(key))
		case PathWildcard:
			buf.WriteString("wildcard")
		case PathAllElements:
			buf.WriteString("all")
		case map[string]any:
			keys := make([]string, 0, len(key))
			for name := range key {
				keys = append(keys, name)
			}
			sort.Strings(keys)
			buf.WriteString("map:")
			for _, name := range keys {
				buf.WriteString(name)
				buf.WriteByte('=')
				buf.WriteString(fmt.Sprintf("%#v", key[name]))
				buf.WriteByte('/')
			}
		default:
			buf.WriteString(fmt.Sprintf("%T:%v", key, key))
		}
		if segment.IsSymbolic {
			buf.WriteByte('*')
		}
		buf.WriteByte(',')
	}
	buf.WriteByte(']')
	return buf.String()
}

// maxInt returns the maximum of two integers.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
