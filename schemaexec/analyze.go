package schemaexec

import (
	"context"
	"fmt"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// Verdict classifies the outcome of symbolically executing a jq query against
// an input schema. The library is best-effort: it cannot prove everything, but
// when it CAN prove a query is broken, consumers may hard-error.
type Verdict int

const (
	// VerdictProven: a concrete output schema was inferred; it contains no
	// Top (unknown) anywhere and is not provably empty. Safe to use as the
	// projected output shape.
	VerdictProven Verdict = iota

	// VerdictProvenBroken: the output is provably null or empty for EVERY
	// valid input — e.g. a typo'd leaf yielding const null, or a query whose
	// execution paths are all dead (Bottom). Safe to fail a build on.
	VerdictProvenBroken

	// VerdictUnverifiable: the output contains Top (unknown) somewhere — the
	// library could not decide. This is NOT an error; consumers should warn
	// at most.
	VerdictUnverifiable
)

// String returns a human-readable name for the verdict.
func (v Verdict) String() string {
	switch v {
	case VerdictProven:
		return "proven"
	case VerdictProvenBroken:
		return "proven-broken"
	case VerdictUnverifiable:
		return "unverifiable"
	default:
		return fmt.Sprintf("verdict(%d)", int(v))
	}
}

// Analysis is the result of Analyze: the inferred output schema plus a
// classification of how trustworthy that inference is.
type Analysis struct {
	// Output is the inferred output schema (usable as the projected response
	// shape). Nil when the query provably produces no output (Bottom).
	Output *oas3.Schema

	// Verdict classifies the result; see the Verdict constants.
	Verdict Verdict

	// Causes holds human-readable explanations with schema locations for
	// VerdictUnverifiable and VerdictProvenBroken, e.g.
	// "property access on non-object type at $.anyOf[0]".
	Causes []string

	// Semantics records the schema interpretation mode the analysis ran
	// under. Verdicts are only meaningful relative to it: under the default
	// SchemaSemanticsSpeakeasy, "valid input" means a value as modeled by
	// Speakeasy's generators (closed objects, implied types), NOT an
	// arbitrary payload the wire could carry. Under SchemaSemanticsRaw a
	// missing property is never provably broken without an explicit
	// additionalProperties: false.
	Semantics SchemaSemantics
}

// Analyze symbolically executes a jq query against an input schema and
// classifies the result.
//
// Contract for consumers (e.g. generators linting authored jq projections
// against response schemas):
//
//   - VerdictProvenBroken is safe to fail a build on: the query provably
//     produces null (or nothing) for every valid input.
//   - VerdictUnverifiable is NOT a failure — the library could not decide
//     (open schemas, oneOf, unsupported operations legitimately widen to
//     unknown). Consumers should warn at most.
//   - VerdictProven means the returned Output schema is a sound, fully
//     concrete over-approximation of all possible outputs.
//
// Analyze always runs in lenient mode (StrictMode is ignored): strict mode
// aborts on the first widening, whereas classification needs the completed
// output schema. The verdict is computed by deep-walking the output —
// including array items, object properties, and union branches — so a Top
// buried inside a container makes the result Unverifiable, not Proven.
//
// jq missing-key semantics are respected: optional property access yields
// null UNIONED with the real types, which stays Proven. Only an output that
// is null for every input (or has no output at all) is ProvenBroken.
//
// Verdicts are relative to opts.Semantics (echoed on Analysis.Semantics).
// Under the default SchemaSemanticsSpeakeasy, "valid input" means a value as
// modeled by Speakeasy's generators — objects are closed, so access to an
// undeclared property is provably null (typo detection). Under
// SchemaSemanticsRaw, objects without additionalProperties are open and such
// access is merely Unverifiable.
func Analyze(ctx context.Context, q *gojq.Query, input *oas3.Schema, opts ...SchemaExecOptions) (*Analysis, error) {
	opt := DefaultOptions()
	if len(opts) > 0 {
		opt = opts[0]
	}
	// Classification requires the completed lenient output schema.
	opt.StrictMode = false

	code, err := gojq.Compile(q, gojq.WithSkipLibraryExpansion("gsub", "sub", "test"))
	if err != nil {
		return nil, fmt.Errorf("failed to compile query: %w", err)
	}

	if err := validateSchema(input); err != nil {
		return nil, fmt.Errorf("invalid input schema: %w", err)
	}

	// Same root normalization as ExecSchema: collapse combinators and follow
	// resolved $refs so builtins never observe bare $ref shells.
	if collapsed, err := collapseAllOf(input); err == nil && collapsed != nil {
		if collapsed2, err2 := collapseAnyOf(collapsed); err2 == nil && collapsed2 != nil {
			collapsed = collapsed2
		}
		input = collapsed
	}

	env := newSchemaEnv(ctx, opt)
	result, err := env.execute(code, input)
	if err != nil {
		return nil, err
	}

	analysis := &Analysis{Output: result.Schema, Semantics: opt.Semantics}
	analysis.Verdict, analysis.Causes = classifyOutput(result.Schema, env.topCauses)
	return analysis, nil
}

// classifyOutput derives a verdict from an output schema produced by lenient
// symbolic execution.
func classifyOutput(schema *oas3.Schema, topCauses map[*oas3.Schema]string) (Verdict, []string) {
	// No output at all: every execution path was dead.
	if isBottomSchema(schema) {
		return VerdictProvenBroken, []string{"query produces no output for any valid input"}
	}

	// Null-only output: every execution path produced null. Note this is only
	// broken when null is the ONLY possibility — null unioned with real types
	// (jq's optional-property access) is fine and handled below.
	if isProvablyNullOnly(schema) {
		return VerdictProvenBroken, []string{"query output is null for every valid input"}
	}

	issues := collectSchemaIssues(schema, topCauses)
	if len(issues) == 0 {
		return VerdictProven, nil
	}

	causes := make([]string, 0, len(issues))
	for _, issue := range issues {
		causes = append(causes, issue.describe())
	}
	return VerdictUnverifiable, causes
}

// isProvablyNullOnly reports whether a schema admits null and nothing else.
func isProvablyNullOnly(s *oas3.Schema) bool {
	if s == nil {
		return false
	}
	// getType already folds anyOf-of-identical-types, so a union of nulls
	// reports "null" here too.
	if getType(s) == "null" {
		return true
	}
	// oneOf is not folded by getType; check branches explicitly.
	if len(s.OneOf) > 0 && len(s.GetType()) == 0 && len(s.AnyOf) == 0 && len(s.AllOf) == 0 {
		for _, br := range s.OneOf {
			if br == nil || !isProvablyNullOnly(resolvedLeft(br)) {
				return false
			}
		}
		return true
	}
	return false
}

// schemaIssue is one Top/Bottom occurrence found while walking an output
// schema. Used by both strict-mode validation (first issue → error) and
// Analyze (all issues → causes).
type schemaIssue struct {
	path     string
	cause    string // recorded Top cause, empty if none
	isBottom bool
}

func (i schemaIssue) describe() string {
	if i.isBottom {
		return fmt.Sprintf("impossible (never) sub-schema at %s", i.path)
	}
	if i.cause != "" {
		return fmt.Sprintf("%s at %s", i.cause, i.path)
	}
	return fmt.Sprintf("unknown (unconstrained) schema at %s", i.path)
}

// collectSchemaIssues deep-walks a schema and returns every Top or Bottom
// occurrence, annotated with the recorded cause when available. The walk is
// cycle-safe and follows resolved $refs (via resolvedLeft) so pass-through
// referenced schemas are inspected rather than misread as unconstrained
// shells.
func collectSchemaIssues(schema *oas3.Schema, topCauses map[*oas3.Schema]string) []schemaIssue {
	var issues []schemaIssue
	collectSchemaIssuesPath(schema, "$", topCauses, make(map[*oas3.Schema]bool), &issues)
	return issues
}

func collectSchemaIssuesPath(schema *oas3.Schema, path string, topCauses map[*oas3.Schema]string, seen map[*oas3.Schema]bool, issues *[]schemaIssue) {
	// Bottom is represented as nil.
	if isBottomSchema(schema) {
		*issues = append(*issues, schemaIssue{path: path, isBottom: true})
		return
	}
	if schema == nil {
		return
	}
	if seen[schema] {
		return
	}
	seen[schema] = true

	if isTopSchema(schema) {
		issue := schemaIssue{path: path}
		if topCauses != nil {
			issue.cause = topCauses[schema]
		}
		*issues = append(*issues, issue)
		return
	}

	// Child walk order mirrors the historical strict-mode validation order so
	// the first reported issue (and thus strict-mode error text) is stable.
	if left := resolvedLeft(schema.Items); left != nil {
		collectSchemaIssuesPath(left, path+".items", topCauses, seen, issues)
	}

	for i, item := range schema.PrefixItems {
		if left := resolvedLeft(item); left != nil {
			collectSchemaIssuesPath(left, fmt.Sprintf("%s.prefixItems[%d]", path, i), topCauses, seen, issues)
		}
	}

	if schema.Properties != nil {
		for k, prop := range schema.Properties.All() {
			if left := resolvedLeft(prop); left != nil {
				collectSchemaIssuesPath(left, fmt.Sprintf("%s.properties.%s", path, k), topCauses, seen, issues)
			}
		}
	}

	if left := resolvedLeft(schema.AdditionalProperties); left != nil {
		collectSchemaIssuesPath(left, path+".additionalProperties", topCauses, seen, issues)
	}

	for i, branch := range schema.AnyOf {
		if left := resolvedLeft(branch); left != nil {
			collectSchemaIssuesPath(left, fmt.Sprintf("%s.anyOf[%d]", path, i), topCauses, seen, issues)
		}
	}

	for i, branch := range schema.OneOf {
		if left := resolvedLeft(branch); left != nil {
			collectSchemaIssuesPath(left, fmt.Sprintf("%s.oneOf[%d]", path, i), topCauses, seen, issues)
		}
	}

	for i, branch := range schema.AllOf {
		if left := resolvedLeft(branch); left != nil {
			collectSchemaIssuesPath(left, fmt.Sprintf("%s.allOf[%d]", path, i), topCauses, seen, issues)
		}
	}

	if left := resolvedLeft(schema.Not); left != nil {
		collectSchemaIssuesPath(left, path+".not", topCauses, seen, issues)
	}

	if left := resolvedLeft(schema.Contains); left != nil {
		collectSchemaIssuesPath(left, path+".contains", topCauses, seen, issues)
	}

	if left := resolvedLeft(schema.UnevaluatedProperties); left != nil {
		collectSchemaIssuesPath(left, path+".unevaluatedProperties", topCauses, seen, issues)
	}

	if left := resolvedLeft(schema.UnevaluatedItems); left != nil {
		collectSchemaIssuesPath(left, path+".unevaluatedItems", topCauses, seen, issues)
	}

	if schema.PatternProperties != nil {
		for k, prop := range schema.PatternProperties.All() {
			if left := resolvedLeft(prop); left != nil {
				collectSchemaIssuesPath(left, fmt.Sprintf("%s.patternProperties.%s", path, k), topCauses, seen, issues)
			}
		}
	}

	if left := resolvedLeft(schema.PropertyNames); left != nil {
		collectSchemaIssuesPath(left, path+".propertyNames", topCauses, seen, issues)
	}

	if schema.DependentSchemas != nil {
		for k, dep := range schema.DependentSchemas.All() {
			if left := resolvedLeft(dep); left != nil {
				collectSchemaIssuesPath(left, fmt.Sprintf("%s.dependentSchemas.%s", path, k), topCauses, seen, issues)
			}
		}
	}

	if left := resolvedLeft(schema.If); left != nil {
		collectSchemaIssuesPath(left, path+".if", topCauses, seen, issues)
	}
	if left := resolvedLeft(schema.Then); left != nil {
		collectSchemaIssuesPath(left, path+".then", topCauses, seen, issues)
	}
	if left := resolvedLeft(schema.Else); left != nil {
		collectSchemaIssuesPath(left, path+".else", topCauses, seen, issues)
	}

	if left := resolvedLeft(schema.ContentSchema); left != nil {
		collectSchemaIssuesPath(left, path+".contentSchema", topCauses, seen, issues)
	}

	if schema.Defs != nil {
		for k, def := range schema.Defs.All() {
			if left := resolvedLeft(def); left != nil {
				collectSchemaIssuesPath(left, fmt.Sprintf("%s.$defs.%s", path, k), topCauses, seen, issues)
			}
		}
	}
}
