package schemaexec

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// Fingerprinter provides schema canonicalization and hashing with caching
type Fingerprinter struct {
	mu       sync.RWMutex
	cache    map[*oas3.Schema]string // Persistent: schema pointer → fingerprint hex
	maxDepth int                     // Guardrail for pathological cycles
}

// NewFingerprinter creates a new fingerprinter
func NewFingerprinter() *Fingerprinter {
	return &Fingerprinter{
		cache:    make(map[*oas3.Schema]string, 1024),
		maxDepth: 1000, // Depth limit for pathological cases
	}
}

// FingerprintSchema returns a deterministic hex fingerprint for a schema
// Uses persistent caching for performance
func (fp *Fingerprinter) FingerprintSchema(s *oas3.Schema) string {
	if s == nil {
		return "bottom"
	}

	// Check persistent cache
	fp.mu.RLock()
	if sum, ok := fp.cache[s]; ok {
		fp.mu.RUnlock()
		return sum
	}
	fp.mu.RUnlock()

	// Canonicalize and hash
	ctx := newCanonCtx(fp.maxDepth, nil)
	data := canonicalize(s, ctx)
	sum := sha256.Sum256(data)
	hex := fmt.Sprintf("%x", sum[:])

	// Store in persistent cache
	fp.mu.Lock()
	fp.cache[s] = hex
	fp.mu.Unlock()

	return hex
}

// FingerprintSchemaWithExclusions computes fingerprint but skips persistent cache
// for schemas in the exclusion set (used for mutable schemas)
func (fp *Fingerprinter) FingerprintSchemaWithExclusions(s *oas3.Schema, excl map[*oas3.Schema]struct{}) string {
	if s == nil {
		return "bottom"
	}

	// Skip persistent cache for excluded schemas
	if excl != nil {
		if _, skip := excl[s]; !skip {
			fp.mu.RLock()
			if sum, ok := fp.cache[s]; ok {
				fp.mu.RUnlock()
				return sum
			}
			fp.mu.RUnlock()
		}
	} else {
		fp.mu.RLock()
		if sum, ok := fp.cache[s]; ok {
			fp.mu.RUnlock()
			return sum
		}
		fp.mu.RUnlock()
	}

	// Canonicalize and hash
	ctx := newCanonCtx(fp.maxDepth, excl)
	data := canonicalize(s, ctx)
	sum := sha256.Sum256(data)
	hex := fmt.Sprintf("%x", sum[:])

	// Store in persistent cache only if not excluded
	if excl == nil || func() bool {
		_, skip := excl[s]
		return !skip
	}() {
		fp.mu.Lock()
		fp.cache[s] = hex
		fp.mu.Unlock()
	}

	return hex
}

// Reset clears the persistent cache
func (fp *Fingerprinter) Reset() {
	fp.mu.Lock()
	fp.cache = make(map[*oas3.Schema]string, 1024)
	fp.mu.Unlock()
}

// canonCtx holds state for a single canonicalization traversal
type canonCtx struct {
	inProgress map[*oas3.Schema]int         // Cycle detection: schema → cycle ID
	nextID     int                          // Next cycle ID to assign
	localMemo  map[*oas3.Schema][]byte      // Per-call memoization for DAGs
	excl       map[*oas3.Schema]struct{}    // Exclusions from persistent cache
	depth      int                          // Current recursion depth
	maxDepth   int                          // Maximum recursion depth guard
}

func newCanonCtx(maxDepth int, excl map[*oas3.Schema]struct{}) *canonCtx {
	return &canonCtx{
		inProgress: make(map[*oas3.Schema]int, 64),
		localMemo:  make(map[*oas3.Schema][]byte, 256),
		nextID:     1,
		excl:       excl,
		depth:      0,
		maxDepth:   maxDepth,
	}
}

// derefJSONSchemaForFingerprint dereferences a JSONSchema without collapsing
// Used during fingerprinting to avoid infinite recursion in circular schemas
func derefJSONSchemaForFingerprint(js *oas3.JSONSchema[oas3.Referenceable]) (*oas3.Schema, bool) {
	if js == nil {
		return nil, false
	}

	// Try GetResolvedSchema() first (handles $refs after ResolveAllReferences)
	if resolved := js.GetResolvedSchema(); resolved != nil {
		if schema := resolved.GetLeft(); schema != nil {
			return schema, true
		}
	}

	// Fall back to inline Left (handles NewJSONSchemaFromSchema wrappers)
	if js.Left != nil {
		return js.Left, true
	}

	// Unresolved or unknown wrapper
	return nil, false
}

// canonicalize produces a canonical byte representation of a schema
func canonicalize(s *oas3.Schema, ctx *canonCtx) []byte {
	// Collapsing is now working with context-aware derefJSONSchema
	// 3. Moving collapse logic outside of fingerprinting entirely
	//
	// For now, we rely on encodeSchema's own cycle detection which works correctly.

	// Encode with cycle detection and local memoization
	w := newCanonWriter()
	encodeSchema(s, ctx, w)
	return w.Bytes()
}

// encodeSchema recursively encodes a schema into canonical form
func encodeSchema(s *oas3.Schema, ctx *canonCtx, w *canonWriter) {
	// Depth guard
	ctx.depth++
	if ctx.depth > ctx.maxDepth {
		w.WriteString("{\"$max_depth\":true}")
		ctx.depth--
		return
	}
	defer func() { ctx.depth-- }()

	// Handle nil (Bottom)
	if s == nil {
		w.WriteString("{\"$bottom\":true}")
		return
	}

	// Handle Top
	if isTopSchema(s) {
		w.WriteString("{\"$top\":true}")
		return
	}

	// Check local memo (for DAG sharing within this traversal)
	if cached, ok := ctx.localMemo[s]; ok {
		w.Write(cached)
		return
	}

	// Cycle detection
	if id, inProgress := ctx.inProgress[s]; inProgress {
		w.WriteString(fmt.Sprintf("{\"$cycle\":%d}", id))
		return
	}

	// Mark in progress
	id := ctx.nextID
	ctx.nextID++
	ctx.inProgress[s] = id

	// Build canonical representation
	startPos := w.Len()
	w.WriteByte('{')

	first := true
	writeField := func(key string, fn func()) {
		if !first {
			w.WriteByte(',')
		}
		first = false
		w.WriteString(fmt.Sprintf("%q:", key))
		fn()
	}

	// Type (always first for consistency)
	typ := getType(s)
	if typ != "" {
		writeField("type", func() { w.WriteString(fmt.Sprintf("%q", typ)) })
	}

	// Nullable
	if s.Nullable != nil && *s.Nullable {
		writeField("nullable", func() { w.WriteString("true") })
	}

	// Enum (canonicalized and sorted)
	if len(s.Enum) > 0 {
		writeField("enum", func() { encodeEnums(s.Enum, w) })
	}

	// Const
	if s.Const != nil {
		writeField("const", func() { w.WriteString(canonicalizeYAMLNode(s.Const)) })
	}

	// Numeric constraints
	if s.Minimum != nil {
		writeField("minimum", func() { w.WriteString(fmt.Sprintf("%v", *s.Minimum)) })
	}
	if s.Maximum != nil {
		writeField("maximum", func() { w.WriteString(fmt.Sprintf("%v", *s.Maximum)) })
	}
	if s.MultipleOf != nil {
		writeField("multipleOf", func() { w.WriteString(fmt.Sprintf("%v", *s.MultipleOf)) })
	}

	// String constraints
	if s.MinLength != nil {
		writeField("minLength", func() { w.WriteString(fmt.Sprintf("%d", *s.MinLength)) })
	}
	if s.MaxLength != nil {
		writeField("maxLength", func() { w.WriteString(fmt.Sprintf("%d", *s.MaxLength)) })
	}
	if s.Pattern != nil {
		writeField("pattern", func() { w.WriteString(fmt.Sprintf("%q", *s.Pattern)) })
	}
	if s.Format != nil {
		writeField("format", func() { w.WriteString(fmt.Sprintf("%q", *s.Format)) })
	}

	// Array constraints
	if s.MinItems != nil {
		writeField("minItems", func() { w.WriteString(fmt.Sprintf("%d", *s.MinItems)) })
	}
	if s.MaxItems != nil {
		writeField("maxItems", func() { w.WriteString(fmt.Sprintf("%d", *s.MaxItems)) })
	}
	if s.UniqueItems != nil && *s.UniqueItems {
		writeField("uniqueItems", func() { w.WriteString("true") })
	}

	// Array items (single schema for all items)
	if s.Items != nil {
		writeField("items", func() {
			if itemSchema, ok := derefJSONSchemaForFingerprint(s.Items); ok {
				encodeSchema(itemSchema, ctx, w)
			} else {
				w.WriteString("{\"$unresolved\":true}")
			}
		})
	}

	// Array prefixItems (tuple types)
	if len(s.PrefixItems) > 0 {
		writeField("prefixItems", func() {
			w.WriteByte('[')
			for i, item := range s.PrefixItems {
				if i > 0 {
					w.WriteByte(',')
				}
				if itemSchema, ok := derefJSONSchemaForFingerprint(item); ok {
					encodeSchema(itemSchema, ctx, w)
				} else {
					w.WriteString("{\"$unresolved\":true}")
				}
			}
			w.WriteByte(']')
		})
	}

	// Object properties (sorted by key)
	if s.Properties != nil && s.Properties.Len() > 0 {
		writeField("properties", func() {
			// Collect and sort keys
			type propEntry struct {
				key    string
				schema *oas3.Schema
			}
			props := make([]propEntry, 0, s.Properties.Len())
			for k, v := range s.Properties.All() {
				if schema, ok := derefJSONSchemaForFingerprint(v); ok {
					props = append(props, propEntry{k, schema})
				}
			}
			sort.Slice(props, func(i, j int) bool { return props[i].key < props[j].key })

			w.WriteByte('{')
			for i, p := range props {
				if i > 0 {
					w.WriteByte(',')
				}
				w.WriteString(fmt.Sprintf("%q:", p.key))
				encodeSchema(p.schema, ctx, w)
			}
			w.WriteByte('}')
		})
	}

	// Required fields (sorted)
	if len(s.Required) > 0 {
		writeField("required", func() {
			sorted := make([]string, len(s.Required))
			copy(sorted, s.Required)
			sort.Strings(sorted)
			w.WriteByte('[')
			for i, r := range sorted {
				if i > 0 {
					w.WriteByte(',')
				}
				w.WriteString(fmt.Sprintf("%q", r))
			}
			w.WriteByte(']')
		})
	}

	// AdditionalProperties
	if s.AdditionalProperties != nil {
		writeField("additionalProperties", func() {
			// Check if it's a boolean (Right)
			if s.AdditionalProperties.Right != nil {
				w.WriteString(fmt.Sprintf("%t", *s.AdditionalProperties.Right))
			} else if schema, ok := derefJSONSchemaForFingerprint(s.AdditionalProperties); ok {
				encodeSchema(schema, ctx, w)
			} else {
				w.WriteString("{\"$unresolved\":true}")
			}
		})
	}

	// Combinators: allOf, anyOf, oneOf
	// Note: allOf should be collapsed if opts.Collapse is true, but handle it if present
	if len(s.AllOf) > 0 {
		writeField("allOf", func() { encodeCombinator(s.AllOf, ctx, w) })
	}
	if len(s.AnyOf) > 0 {
		writeField("anyOf", func() { encodeCombinator(s.AnyOf, ctx, w) })
	}
	if len(s.OneOf) > 0 {
		writeField("oneOf", func() { encodeCombinator(s.OneOf, ctx, w) })
	}

	// Not
	if s.Not != nil {
		writeField("not", func() {
			if notSchema, ok := derefJSONSchemaForFingerprint(s.Not); ok {
				encodeSchema(notSchema, ctx, w)
			} else {
				w.WriteString("{\"$unresolved\":true}")
			}
		})
	}

	w.WriteByte('}')

	// Unmark in progress
	delete(ctx.inProgress, s)

	// Save to local memo
	encoded := w.BytesFrom(startPos)
	ctx.localMemo[s] = encoded
}

// encodeCombinator encodes allOf/anyOf/oneOf branches
// Canonicalizes each branch, dedups by content, and sorts lexicographically
func encodeCombinator(branches []*oas3.JSONSchema[oas3.Referenceable], ctx *canonCtx, w *canonWriter) {
	// Canonicalize and collect unique branches
	seen := make(map[string][]byte)
	var order []string

	for _, branch := range branches {
		if branchSchema, ok := derefJSONSchemaForFingerprint(branch); ok {
			branchW := newCanonWriter()
			encodeSchema(branchSchema, ctx, branchW)
			branchBytes := branchW.Bytes()
			branchStr := string(branchBytes)

			if _, exists := seen[branchStr]; !exists {
				seen[branchStr] = branchBytes
				order = append(order, branchStr)
			}
		}
	}

	// Sort branches lexicographically for determinism
	sort.Strings(order)

	// Emit as array
	w.WriteByte('[')
	for i, key := range order {
		if i > 0 {
			w.WriteByte(',')
		}
		w.Write(seen[key])
	}
	w.WriteByte(']')
}

// encodeEnums canonicalizes and encodes enum values
func encodeEnums(enums []*yaml.Node, w *canonWriter) {
	// Canonicalize each enum value
	canonical := make([]string, 0, len(enums))
	for _, node := range enums {
		canonical = append(canonical, canonicalizeYAMLNode(node))
	}

	// Sort for determinism
	sort.Strings(canonical)

	// Emit as array
	w.WriteByte('[')
	for i, val := range canonical {
		if i > 0 {
			w.WriteByte(',')
		}
		w.WriteString(val)
	}
	w.WriteByte(']')
}

// canonWriter is a simple buffer for building canonical representations
type canonWriter struct {
	buf []byte
}

func newCanonWriter() *canonWriter {
	return &canonWriter{buf: make([]byte, 0, 1024)}
}

func (w *canonWriter) Write(p []byte) {
	w.buf = append(w.buf, p...)
}

func (w *canonWriter) WriteByte(b byte) {
	w.buf = append(w.buf, b)
}

func (w *canonWriter) WriteString(s string) {
	w.buf = append(w.buf, []byte(s)...)
}

func (w *canonWriter) Bytes() []byte {
	return w.buf
}

func (w *canonWriter) BytesFrom(start int) []byte {
	return w.buf[start:]
}

func (w *canonWriter) Len() int {
	return len(w.buf)
}

// Package-level default fingerprinter for convenience
var defaultFingerprinter = NewFingerprinter()

// FingerprintSchema is a convenience function using the default fingerprinter
func FingerprintSchema(s *oas3.Schema) string {
	return defaultFingerprinter.FingerprintSchema(s)
}

// fingerprintStateHelper computes a fingerprint for an execState using schema fingerprints
// This is used by execState.fingerprint() in multistate.go
func fingerprintStateHelper(s *execState, fp *Fingerprinter) uint64 {
	h := sha256.New()

	// Scalar state
	binary.Write(h, binary.LittleEndian, uint64(s.pc))
	binary.Write(h, binary.LittleEndian, uint64(s.depth))

	// Callstack
	binary.Write(h, binary.LittleEndian, uint64(len(s.callstack)))
	for _, pc := range s.callstack {
		binary.Write(h, binary.LittleEndian, uint64(pc))
	}

	// Build exclusion set for mutable schemas (accumulators)
	excl := make(map[*oas3.Schema]struct{}, len(s.accum)+len(s.schemaToAlloc))
	for _, arr := range s.accum {
		if arr != nil {
			excl[arr] = struct{}{}
		}
	}
	for arr := range s.schemaToAlloc {
		if arr != nil {
			excl[arr] = struct{}{}
		}
	}

	// Stack schemas
	binary.Write(h, binary.LittleEndian, uint64(len(s.stack)))
	for _, sv := range s.stack {
		var sfp string
		if sv.Schema == nil {
			sfp = "bottom"
		} else {
			sfp = fp.FingerprintSchemaWithExclusions(sv.Schema, excl)
		}
		h.Write([]byte(sfp))
		h.Write([]byte{0})
	}

	// Scopes (frames in order, variables sorted within each frame)
	binary.Write(h, binary.LittleEndian, uint64(len(s.scopes)))
	for _, frame := range s.scopes {
		// Sort variable names for determinism
		names := make([]string, 0, len(frame))
		for k := range frame {
			names = append(names, k)
		}
		sort.Strings(names)

		binary.Write(h, binary.LittleEndian, uint64(len(names)))
		for _, name := range names {
			h.Write([]byte(name))
			h.Write([]byte{0})
			v := frame[name]
			var vfp string
			if v == nil {
				vfp = "bottom"
			} else {
				vfp = fp.FingerprintSchemaWithExclusions(v, excl)
			}
			h.Write([]byte(vfp))
			h.Write([]byte{0})
		}
	}

	// Path mode and current path
	if s.pathMode {
		h.Write([]byte("pm:1"))
		binary.Write(h, binary.LittleEndian, uint64(len(s.currentPath)))
		for _, seg := range s.currentPath {
			if seg.IsSymbolic {
				h.Write([]byte("*"))
			} else {
				switch k := seg.Key.(type) {
				case string:
					h.Write([]byte("s:"))
					h.Write([]byte(k))
				case int:
					h.Write([]byte(fmt.Sprintf("i:%d", k)))
				default:
					h.Write([]byte("u"))
				}
			}
			h.Write([]byte{0})
		}
	} else {
		h.Write([]byte("pm:0"))
	}

	sum := h.Sum(nil)
	return binary.LittleEndian.Uint64(sum[:8])
}

// canonicalizeYAMLNode is moved here from multistate.go for reuse
// It converts a yaml.Node to a canonical string for fingerprinting
func canonicalizeYAMLNode(node *yaml.Node) string {
	if node == nil {
		return "null"
	}

	switch node.Kind {
	case yaml.ScalarNode:
		return "s:" + node.Value
	case yaml.SequenceNode:
		var b strings.Builder
		b.WriteString("[")
		for i, n := range node.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(canonicalizeYAMLNode(n))
		}
		b.WriteByte(']')
		return b.String()
	case yaml.MappingNode:
		// Sort keys for deterministic encoding
		pairs := make([]struct{ key, val string }, 0, len(node.Content)/2)
		for i := 0; i < len(node.Content); i += 2 {
			if i+1 < len(node.Content) {
				key := canonicalizeYAMLNode(node.Content[i])
				val := canonicalizeYAMLNode(node.Content[i+1])
				pairs = append(pairs, struct{ key, val string }{key, val})
			}
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
		var b strings.Builder
		b.WriteString("{")
		for i, p := range pairs {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(p.key)
			b.WriteByte(':')
			b.WriteString(p.val)
		}
		b.WriteByte('}')
		return b.String()
	default:
		return fmt.Sprintf("kind%d", node.Kind)
	}
}
