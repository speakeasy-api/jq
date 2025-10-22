package schemaexec

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// Register additional string and utility builtins without modifying builtins.go.
func init() {
	// String regex substitution
	builtinRegistry["gsub"] = builtinGsub
	builtinRegistry["test"] = builtinTest

	// Trimming family
	builtinRegistry["trim"] = builtinTrim
	builtinRegistry["ltrim"] = builtinLtrim
	builtinRegistry["rtrim"] = builtinRtrim
	builtinRegistry["trimstr"] = builtinTrimstr

	// String metrics
	builtinRegistry["utf8bytelength"] = builtinUtf8ByteLength

	// Array/string slicing and regex operations
	builtinRegistry["_slice"] = builtinSlice
	builtinRegistry["_capture"] = builtinCapture
	builtinRegistry["_match"] = builtinMatchArray // override default to never return nil
}

// ============================================================================
// gsub implementation
// ============================================================================

// builtinGsub applies a global regex substitution.
// Signature: gsub(pattern; replacement; [flags])
// - Input: string schema
// - Args: 2 or 3 string schemas (pattern, replacement, optional flags)
// - Returns: string schema
// Behavior:
// - If input and args are all const strings and regex compiles, const-fold to ConstString
// - If input is a small enum (<= EnumLimit), map each through and return merged enum
// - Otherwise return StringType()
// - If regex compilation fails on const args, return Bottom()
func builtinGsub(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		// Conservative: return string type
		return []*oas3.Schema{StringType()}, nil
	}

	// Default output when symbolic
	defaultOut := StringType()

	// Parse args
	if len(args) < 2 {
		// Missing args → symbolic string
		return []*oas3.Schema{defaultOut}, nil
	}
	pat, patOK := extractConstString(args[0])
	repl, replOK := extractConstString(args[1])

	flags := ""
	if len(args) >= 3 {
		if f, ok := extractConstString(args[2]); ok {
			flags = f
		} else {
			// Non-const flags => cannot fold; return symbolic
			return []*oas3.Schema{defaultOut}, nil
		}
	}

	// Helper: compile pattern with flags when available
	compile := func(re string, fl string) (*regexp.Regexp, error) {
		return compileRegexpWithFlags(re, fl)
	}

	// Const folding for const input+args
	if inStr, inOK := extractConstString(input); inOK && patOK && replOK {
		re, err := compile(pat, flags)
		if err != nil {
			// Invalid regex on fully-const => Bottom (per instruction)
			return []*oas3.Schema{Bottom()}, nil
		}
		out := re.ReplaceAllString(inStr, repl)
		return []*oas3.Schema{ConstString(out)}, nil
	}

	// Enum preservation for small enums on input
	if len(input.Enum) > 0 && (env == nil || env.opts.EnumLimit == 0 || len(input.Enum) <= env.opts.EnumLimit) && patOK && replOK {
		re, err := compile(pat, flags)
		if err != nil {
			// Invalid regex: cannot evaluate => Bottom
			return []*oas3.Schema{Bottom()}, nil
		}
		seen := map[string]struct{}{}
		var outEnum []*yaml.Node
		for _, node := range input.Enum {
			if node == nil || node.Kind != yaml.ScalarNode {
				// Non-scalar: widen to string
				return []*oas3.Schema{defaultOut}, nil
			}
			// Apply replacement
			res := re.ReplaceAllString(node.Value, repl)
			if _, ok := seen[res]; ok {
				continue
			}
			seen[res] = struct{}{}
			outEnum = append(outEnum, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: res,
			})
		}
		// Return string enum with mapped values
		result := StringType()
		result.Enum = outEnum
		return []*oas3.Schema{result}, nil
	}

	// For symbolic inputs or non-const args, return string type
	return []*oas3.Schema{defaultOut}, nil
}

// compileRegexpWithFlags adds minimal support for 'i' (ignore case) and 'm' (dotall) flags.
// Any unsupported flag leads to best-effort behavior: ignore unknown flags rather than failing.
// Note: jq also supports 'g' but Go's regex ReplaceAllString is global by default.
func compileRegexpWithFlags(pattern, flags string) (*regexp.Regexp, error) {
	if flags != "" {
		// Unsupported flags (not 'g', 'i', or 'm') are ignored rather than failing for symbolic safety.
		// We preserve 'i' and 'm' below.
		if strings.ContainsRune(flags, 'i') {
			pattern = "(?i)" + pattern
		}
		if strings.ContainsRune(flags, 'm') {
			pattern = "(?s)" + pattern
		}
	}
	return regexp.Compile(pattern)
}

// ============================================================================
// Trim family
// ============================================================================

// builtinTrim removes leading and trailing whitespace.
func builtinTrim(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding
	if inStr, ok := extractConstString(input); ok {
		return []*oas3.Schema{ConstString(strings.TrimSpace(inStr))}, nil
	}

	// Enum preservation
	if len(input.Enum) > 0 && (env == nil || env.opts.EnumLimit == 0 || len(input.Enum) <= env.opts.EnumLimit) {
		newEnum := make([]*yaml.Node, 0, len(input.Enum))
		seen := map[string]struct{}{}
		for _, node := range input.Enum {
			if node.Kind != yaml.ScalarNode {
				continue
			}
			val := strings.TrimSpace(node.Value)
			if _, exists := seen[val]; exists {
				continue
			}
			seen[val] = struct{}{}
			newEnum = append(newEnum, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
		}
		out := StringType()
		out.Enum = newEnum
		return []*oas3.Schema{out}, nil
	}

	return []*oas3.Schema{StringType()}, nil
}

// builtinLtrim removes leading whitespace.
func builtinLtrim(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}
	if inStr, ok := extractConstString(input); ok {
		return []*oas3.Schema{ConstString(strings.TrimLeftFunc(inStr, unicode.IsSpace))}, nil
	}
	// Enum preservation
	if len(input.Enum) > 0 && (env == nil || env.opts.EnumLimit == 0 || len(input.Enum) <= env.opts.EnumLimit) {
		newEnum := make([]*yaml.Node, 0, len(input.Enum))
		seen := map[string]struct{}{}
		for _, node := range input.Enum {
			if node.Kind != yaml.ScalarNode {
				continue
			}
			val := strings.TrimLeftFunc(node.Value, unicode.IsSpace)
			if _, exists := seen[val]; exists {
				continue
			}
			seen[val] = struct{}{}
			newEnum = append(newEnum, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
		}
		out := StringType()
		out.Enum = newEnum
		return []*oas3.Schema{out}, nil
	}
	return []*oas3.Schema{StringType()}, nil
}

// builtinRtrim removes trailing whitespace.
func builtinRtrim(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}
	if inStr, ok := extractConstString(input); ok {
		return []*oas3.Schema{ConstString(strings.TrimRightFunc(inStr, unicode.IsSpace))}, nil
	}
	// Enum preservation
	if len(input.Enum) > 0 && (env == nil || env.opts.EnumLimit == 0 || len(input.Enum) <= env.opts.EnumLimit) {
		newEnum := make([]*yaml.Node, 0, len(input.Enum))
		seen := map[string]struct{}{}
		for _, node := range input.Enum {
			if node.Kind != yaml.ScalarNode {
				continue
			}
			val := strings.TrimRightFunc(node.Value, unicode.IsSpace)
			if _, exists := seen[val]; exists {
				continue
			}
			seen[val] = struct{}{}
			newEnum = append(newEnum, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
		}
		out := StringType()
		out.Enum = newEnum
		return []*oas3.Schema{out}, nil
	}
	return []*oas3.Schema{StringType()}, nil
}

// builtinTrimstr removes a specific prefix and suffix (literal match) when present.
func builtinTrimstr(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}
	if len(args) != 1 {
		return []*oas3.Schema{StringType()}, nil
	}
	token, ok := extractConstString(args[0])
	if !ok {
		return []*oas3.Schema{StringType()}, nil
	}

	// Const folding
	if inStr, inOK := extractConstString(input); inOK {
		out := strings.TrimSuffix(strings.TrimPrefix(inStr, token), token)
		return []*oas3.Schema{ConstString(out)}, nil
	}

	// Enum preservation
	if len(input.Enum) > 0 && (env == nil || env.opts.EnumLimit == 0 || len(input.Enum) <= env.opts.EnumLimit) {
		newEnum := make([]*yaml.Node, 0, len(input.Enum))
		seen := map[string]struct{}{}
		for _, node := range input.Enum {
			if node.Kind != yaml.ScalarNode {
				continue
			}
			val := strings.TrimSuffix(strings.TrimPrefix(node.Value, token), token)
			if _, exists := seen[val]; exists {
				continue
			}
			seen[val] = struct{}{}
			newEnum = append(newEnum, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
		}
		out := StringType()
		out.Enum = newEnum
		return []*oas3.Schema{out}, nil
	}

	return []*oas3.Schema{StringType()}, nil
}

// ============================================================================
// utf8bytelength implementation
// ============================================================================

// builtinUtf8ByteLength returns the number of bytes in the UTF-8 encoding of the string.
// - Const folding to ConstInteger
// - Otherwise return number with minimum 0
func builtinUtf8ByteLength(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	if !MightBeString(input) {
		return []*oas3.Schema{Bottom()}, nil
	}

	// Const folding
	if inStr, ok := extractConstString(input); ok {
		return []*oas3.Schema{ConstInteger(int64(len(inStr)))}, nil
	}

	// Generic: non-negative number
	out := NumberType()
	zero := 0.0
	out.Minimum = &zero
	return []*oas3.Schema{out}, nil
}

// ============================================================================
// _slice implementation (arrays and strings)
// ============================================================================

// builtinSlice implements the internal _slice(v; end; start)
// Note: gojq calls funcSlice(_, v, e, s). To be robust across emitters, this supports:
// - args len==3: args[0]=v, args[1]=end, args[2]=start
// - args len==2: container comes from input, args[0]=end, args[1]=start
func builtinSlice(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Normalize operands
	var container, endArg, startArg *oas3.Schema
	switch len(args) {
	case 3:
		container, endArg, startArg = args[0], args[1], args[2]
	case 2:
		container, endArg, startArg = input, args[0], args[1]
	default:
		// Arity mismatch - conservatively return based on input type
		return sliceFallbackByType(input), nil
	}

	// Decide by container type
	ctype := getType(container)
	switch ctype {
	case "array":
		return []*oas3.Schema{sliceArray(container, endArg, startArg, env)}, nil
	case "string":
		return []*oas3.Schema{sliceStringSchema(container, endArg, startArg)}, nil
	case "":
		// Unknown type; attempt best-effort by might-be checks
		if MightBeArray(container) {
			return []*oas3.Schema{sliceArray(container, endArg, startArg, env)}, nil
		}
		if MightBeString(container) {
			return []*oas3.Schema{sliceStringSchema(container, endArg, startArg)}, nil
		}
		// Conservative: return string type (most common case for slicing in string contexts)
		return []*oas3.Schema{StringType()}, nil
	default:
		// Not array or string - conservative fallback (likely type guard failure)
		return []*oas3.Schema{StringType()}, nil
	}
}

// sliceFallbackByType returns something sensible when arity was unexpected.
func sliceFallbackByType(s *oas3.Schema) []*oas3.Schema {
	if MightBeArray(s) {
		// Preserve array items
		if s.Items != nil && s.Items.Left != nil {
			return []*oas3.Schema{ArrayType(s.Items.Left)}
		}
		// Tuple-only: widen to union-of-prefix items
		if len(s.PrefixItems) > 0 {
			items := make([]*oas3.Schema, 0, len(s.PrefixItems))
			for _, pi := range s.PrefixItems {
				if pi.Left != nil {
					items = append(items, pi.Left)
				}
			}
			return []*oas3.Schema{ArrayType(Union(items, sExecOptsOrDefault()))}
		}
		return []*oas3.Schema{ArrayType(Top())}
	}
	if MightBeString(s) {
		return []*oas3.Schema{StringType()}
	}
	return []*oas3.Schema{Bottom()}
}

// Utility to fetch env.opts safely if env is nil
func sExecOptsOrDefault() SchemaExecOptions {
	return SchemaExecOptions{
		EnumLimit:     32,
		AnyOfLimit:    64,
		WideningLevel: 1,
	}
}

// sliceArray returns an array schema representing the sliced view.
func sliceArray(arr, endArg, startArg *oas3.Schema, env *schemaEnv) *oas3.Schema {
	// If we can const-evaluate indices and tuple length, refine prefixItems slice
	startIdx, hasStart := extractConstSliceStart(startArg)
	endIdx, hasEnd := extractConstSliceEnd(endArg)

	// When we have tuple prefixItems and const indices, slice them precisely
	if len(arr.PrefixItems) > 0 && hasStart && hasEnd {
		n := len(arr.PrefixItems)
		s, e := clampSliceBounds(startIdx, endIdx, n)

		// Build sliced prefixItems
		elements := make([]*oas3.Schema, 0, max(0, e-s))
		for i := s; i < e; i++ {
			if arr.PrefixItems[i].Left != nil {
				elements = append(elements, arr.PrefixItems[i].Left)
			}
		}
		// Preserve items for "additional" if any
		var items *oas3.Schema
		if arr.Items != nil && arr.Items.Left != nil {
			items = arr.Items.Left
		}
		return BuildArray(items, elements)
	}

	// Otherwise: homogeneous element schema
	// Prefer arr.Items if present; else union all prefix items; else Top
	var itemSchema *oas3.Schema
	switch {
	case arr.Items != nil && arr.Items.Left != nil:
		itemSchema = arr.Items.Left
	case len(arr.PrefixItems) > 0:
		cands := make([]*oas3.Schema, 0, len(arr.PrefixItems))
		for _, pi := range arr.PrefixItems {
			if pi.Left != nil {
				cands = append(cands, pi.Left)
			}
		}
		itemSchema = Union(cands, pickOpts(env))
	default:
		itemSchema = Top()
	}
	return ArrayType(itemSchema)
}

func pickOpts(env *schemaEnv) SchemaExecOptions {
	if env == nil {
		return sExecOptsOrDefault()
	}
	return env.opts
}

// sliceStringSchema attempts const-folding for string slices; else returns string type
func sliceStringSchema(str, endArg, startArg *oas3.Schema) *oas3.Schema {
	// If we can const-fold string and indices, evaluate precisely
	if s, ok := extractConstString(str); ok {
		startIdx, hasStart := extractConstSliceStart(startArg)
		endIdx, hasEnd := extractConstSliceEnd(endArg)

		// If indices missing, we can't compute precise result safely → return string type
		if !hasStart && !hasEnd {
			return StringType()
		}

		// Compute slice by rune indices with clamp semantics matching gojq
		runes := []rune(s)
		n := len(runes)

		// Defaults: start=0, end=n
		si := 0
		if hasStart {
			if startIdx < 0 {
				si = startIdx + n
			} else {
				si = startIdx
			}
			if si < 0 {
				si = 0
			}
			if si > n {
				si = n
			}
		}

		ei := n
		if hasEnd {
			ei = endIdx
			if endIdx < 0 {
				ei = endIdx + n
			}
			if ei < si {
				ei = si
			}
			if ei > n {
				ei = n
			}
		}

		if si < 0 {
			si = 0
		}
		if ei > n {
			ei = n
		}
		if si > ei {
			si = ei
		}

		return ConstString(string(runes[si:ei]))
	}

	// Symbolic fallback
	return StringType()
}

// Index helpers
func extractConstSliceStart(s *oas3.Schema) (int, bool) {
	if s == nil {
		return 0, false
	}
	if v, ok := extractConstValue(s); ok {
		if f, ok := v.(float64); ok {
			// start uses integer conversion (truncate)
			return int(f), true
		}
	}
	return 0, false
}

func extractConstSliceEnd(e *oas3.Schema) (int, bool) {
	if e == nil {
		return 0, false
	}
	if v, ok := extractConstValue(e); ok {
		if f, ok := v.(float64); ok {
			// end uses ceil semantics in gojq; model as ceil for const-folding
			if f != float64(int(f)) {
				return int(f) + 1, true
			}
			return int(f), true
		}
	}
	return 0, false
}

func clampSliceBounds(startIdx, endIdx, n int) (int, int) {
	// Negative indexing relative to n
	s := startIdx
	if s < 0 {
		s = s + n
	}
	if s < 0 {
		s = 0
	}
	if s > n {
		s = n
	}

	e := endIdx
	if e < 0 {
		e = e + n
	}
	if e < s {
		e = s
	}
	if e > n {
		e = n
	}

	return s, e
}

// ============================================================================
// _capture implementation
// ============================================================================

// builtinCapture turns a single match-object into an object of named capture groups.
// We conservatively return: object with additionalProperties: string|null
// (Regex names are unknown symbolically.)
func builtinCapture(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// If input might be object, return capture object shape; else Bottom
	if !MightBeObject(input) {
		return []*oas3.Schema{Bottom()}, nil
	}
	valueSchema := Union([]*oas3.Schema{StringType(), ConstNull()}, pickOpts(env))
	obj := ObjectType()
	obj.AdditionalProperties = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](valueSchema)
	return []*oas3.Schema{obj}, nil
}

// ============================================================================
// _match override: always return array<MatchObject>
// ============================================================================

// builtinMatchArray overrides default _match to always return array of match objects (possibly empty).
// This avoids returning nil and prevents Top-widening in pipelines like: _match | _capture | ...
func builtinMatchArray(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
	// Input must be string for meaningful regex matching
	// Conservative: return empty array (don't completely prune this execution path)
	if !MightBeString(input) {
		// Return empty array of match objects (conservative fallback)
		matchObj := BuildObject(map[string]*oas3.Schema{
			"offset": IntegerType(),
			"length": IntegerType(),
			"string": StringType(),
			"captures": ArrayType(BuildObject(map[string]*oas3.Schema{
				"offset": Union([]*oas3.Schema{IntegerType(), ConstNull()}, pickOpts(env)),
				"length": Union([]*oas3.Schema{IntegerType(), ConstNull()}, pickOpts(env)),
				"string": Union([]*oas3.Schema{StringType(), ConstNull()}, pickOpts(env)),
				"name":   Union([]*oas3.Schema{StringType(), ConstNull()}, pickOpts(env)),
			}, []string{})),
		}, []string{"offset", "length", "string", "captures"})
		return []*oas3.Schema{ArrayType(matchObj)}, nil
	}

	// Build the canonical match object schema (same fields as existing implementation)
	matchObj := BuildObject(map[string]*oas3.Schema{
		"offset": IntegerType(),
		"length": IntegerType(),
		"string": StringType(),
		"captures": ArrayType(BuildObject(map[string]*oas3.Schema{
			"offset": Union([]*oas3.Schema{IntegerType(), ConstNull()}, pickOpts(env)),
			"length": Union([]*oas3.Schema{IntegerType(), ConstNull()}, pickOpts(env)),
			"string": Union([]*oas3.Schema{StringType(), ConstNull()}, pickOpts(env)),
			"name":   Union([]*oas3.Schema{StringType(), ConstNull()}, pickOpts(env)),
		}, []string{})),
	}, []string{"offset", "length", "string", "captures"})

	// Always return an array of match objects
	return []*oas3.Schema{ArrayType(matchObj)}, nil
}
