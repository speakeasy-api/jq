package schemaexec

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"sort"
	"strings"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// execState represents a single execution state in the multi-state VM.
// jq's backtracking semantics require tracking multiple possible execution paths.
type execState struct {
	pc        int                          // Program counter
	stack     []SValue                     // Schema stack (copy for this state)
	scopes    []map[string]*oas3.Schema    // Scope frames (copy for this state)
	depth     int                          // Recursion depth (for limiting)
	accum         map[string]*oas3.Schema // Shared: allocID → canonical array
	schemaToAlloc map[*oas3.Schema]string // Shared: schema pointer → allocID (for opAppend)
	allocCounter  *int                     // Shared: Monotonic counter for unique allocation IDs
	callstack     []int                    // Per-state: Return address stack for closures

	// Path collection (for del/getpath/setpath operations)
	pathMode    bool          // Are we collecting a path (between opPathBegin/opPathEnd)?
	currentPath []PathSegment // Current path segments being collected

	// State tracking for logging
	id       int    // Unique state ID
	parentID int    // Parent state ID (0 for root)
	lineage  string // Lineage string (e.g., "0", "0.F", "0.F.C")
}

// PathSegment represents one segment of a path expression
type PathSegment struct {
	Key        interface{} // string (property), int (array index), or PathWildcard (symbolic)
	IsSymbolic bool        // True if this is a wildcard from .[] iteration
}

// PathWildcard represents a symbolic array index (from .[])
type PathWildcard struct{}

// clone creates a deep copy of this state for forking.
func (s *execState) clone() *execState {
	// Clone stack
	stackCopy := make([]SValue, len(s.stack))
	copy(stackCopy, s.stack)

	// Clone scope frames
	scopesCopy := make([]map[string]*oas3.Schema, len(s.scopes))
	for i, frame := range s.scopes {
		frameCopy := make(map[string]*oas3.Schema, len(frame))
		for k, v := range frame {
			frameCopy[k] = v // Schemas are immutable, so shallow copy OK
		}
		scopesCopy[i] = frameCopy
	}

	// Accumulator maps are SHARED across states (for array construction)
	// Call stack is per-state

	callstackCopy := make([]int, len(s.callstack))
	copy(callstackCopy, s.callstack)

	// Clone currentPath for path collection
	pathCopy := make([]PathSegment, len(s.currentPath))
	copy(pathCopy, s.currentPath)

	return &execState{
		pc:         s.pc,
		stack:      stackCopy,
		scopes:     scopesCopy,
		depth:      s.depth,
		accum:         s.accum,         // SHARED
		schemaToAlloc: s.schemaToAlloc, // SHARED
		allocCounter:  s.allocCounter,  // SHARED pointer
		callstack:     callstackCopy,
		pathMode:      s.pathMode,
		currentPath:   pathCopy,
		id:            s.id,       // Clone inherits ID initially, will be reassigned
		parentID:      s.parentID, // Clone inherits parent
		lineage:       s.lineage,  // Clone inherits lineage, will be extended
	}
}

// fingerprint computes a hash of this state for memoization.
// Includes: pc, depth, full stack shape, and scope bindings
func (s *execState) fingerprint() uint64 {
	h := sha256.New()

	// Hash PC
	binary.Write(h, binary.LittleEndian, uint64(s.pc))

	// Hash recursion depth
	binary.Write(h, binary.LittleEndian, uint64(s.depth))

	// Hash entire stack: depth + type and shape of each element
	binary.Write(h, binary.LittleEndian, uint64(len(s.stack)))
	for _, sv := range s.stack {
		schema := sv.Schema
		if schema == nil {
			h.Write([]byte("nil"))
			continue
		}

		// Hash type
		h.Write([]byte(getType(schema)))

		// Hash structural markers for better precision
		if schema.Enum != nil {
			hashEnumValues(h, schema.Enum)
		}
		if schema.Items != nil {
			h.Write([]byte("arr"))
		}
		if schema.Properties != nil {
			// Count properties
			propCount := 0
			for range schema.Properties.All() {
				propCount++
			}
			binary.Write(h, binary.LittleEndian, uint64(propCount))
		}
		if schema.AnyOf != nil {
			binary.Write(h, binary.LittleEndian, uint64(len(schema.AnyOf)))
		}
	}

	// Hash scope frames: frame count, variables per frame
	binary.Write(h, binary.LittleEndian, uint64(len(s.scopes)))
	for _, frame := range s.scopes {
		binary.Write(h, binary.LittleEndian, uint64(len(frame)))
		// Hash each variable name and its type
		for k, v := range frame {
			h.Write([]byte(k))
			h.Write([]byte(getType(v)))
		}
	}

	// Return first 8 bytes as uint64
	sum := h.Sum(nil)
	return binary.LittleEndian.Uint64(sum[:8])
}

// canonicalizeYAMLNode converts a yaml.Node to a canonical string for fingerprinting.
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

// hashEnumValues hashes enum values in a deterministic way.
func hashEnumValues(h hash.Hash, enum []*yaml.Node) {
	if len(enum) == 0 {
		return
	}
	// Canonicalize each value, sort so order doesn't affect fingerprint
	enc := make([]string, 0, len(enum))
	for _, node := range enum {
		enc = append(enc, canonicalizeYAMLNode(node))
	}
	sort.Strings(enc)

	// Include length
	binary.Write(h, binary.LittleEndian, uint64(len(enc)))

	// Write all canonicalized values
	for _, s := range enc {
		h.Write([]byte(s))
		h.Write([]byte{0}) // Separator
	}
}

// push pushes a schema onto the stack.
func (s *execState) push(schema *oas3.Schema) {
	s.stack = append(s.stack, SValue{Schema: schema})
}

// pop removes and returns the top schema.
func (s *execState) pop() *oas3.Schema {
	if len(s.stack) == 0 {
		// Stack underflow - should not happen in correct bytecode
		return nil
	}
	top := s.stack[len(s.stack)-1].Schema
	s.stack = s.stack[:len(s.stack)-1]
	return top
}

// top returns the top schema without removing it.
func (s *execState) top() *oas3.Schema {
	if len(s.stack) == 0 {
		return nil
	}
	return s.stack[len(s.stack)-1].Schema
}

// currentFrame returns the current scope frame.
func (s *execState) currentFrame() map[string]*oas3.Schema {
	if len(s.scopes) == 0 {
		return nil
	}
	return s.scopes[len(s.scopes)-1]
}

// pushFrame creates a new scope.
func (s *execState) pushFrame() {
	s.scopes = append(s.scopes, make(map[string]*oas3.Schema))
}

// popFrame removes the current scope.
func (s *execState) popFrame() {
	if len(s.scopes) > 0 {
		s.scopes = s.scopes[:len(s.scopes)-1]
	}
}

// storeVar saves a schema to the current frame.
func (s *execState) storeVar(key string, schema *oas3.Schema) {
	if frame := s.currentFrame(); frame != nil {
		frame[key] = schema
	}
}

// loadVar retrieves a schema from frames (inner to outer).
func (s *execState) loadVar(key string) (*oas3.Schema, bool) {
	for i := len(s.scopes) - 1; i >= 0; i-- {
		if schema, ok := s.scopes[i][key]; ok {
			return schema, true
		}
	}
	return nil, false
}

// newExecState creates an initial execution state.
func newExecState(input *oas3.Schema) *execState {
	counter := 0
	state := &execState{
		pc:           0,
		stack:        make([]SValue, 0, 16),
		scopes:       make([]map[string]*oas3.Schema, 0, 4),
		depth:        0,
		accum:         make(map[string]*oas3.Schema), // Shared accumulator
		schemaToAlloc: make(map[*oas3.Schema]string), // Schema → allocID mapping
		allocCounter:  &counter,                       // Shared counter (pointer)
		callstack:     make([]int, 0, 8),
		id:            0,    // Initial state ID
		parentID:      0,    // Root has no parent
		lineage:       "0",  // Root lineage
	}
	state.pushFrame() // Initial global frame
	state.push(input) // Push input onto stack
	return state
}

// stateWorklist manages the queue of states to execute.
type stateWorklist struct {
	states       []*execState
	seen         map[uint64]bool // Memoization: fingerprint → visited
	nextStateID  int             // Monotonic counter for state IDs
}

// newStateWorklist creates a new worklist.
func newStateWorklist() *stateWorklist {
	return &stateWorklist{
		states:      make([]*execState, 0, 32),
		seen:        make(map[uint64]bool),
		nextStateID: 1, // Start from 1 (0 is reserved for root)
	}
}

// push adds a state to the worklist.
func (w *stateWorklist) push(state *execState) {
	w.states = append(w.states, state)
}

// pop removes and returns the next state (LIFO/stack for depth-first).
// This ensures iteration paths complete before done paths, which is critical
// for array construction where the done path loads the accumulated result.
func (w *stateWorklist) pop() *execState {
	if len(w.states) == 0 {
		return nil
	}
	// Pop from end (LIFO) instead of beginning (FIFO)
	state := w.states[len(w.states)-1]
	w.states = w.states[:len(w.states)-1]
	return state
}

// isEmpty checks if worklist is empty.
func (w *stateWorklist) isEmpty() bool {
	return len(w.states) == 0
}

// hasSeen checks if we've seen this state before (memoization).
func (w *stateWorklist) hasSeen(state *execState) bool {
	fp := state.fingerprint()
	return w.seen[fp]
}

// markSeen marks a state as visited.
func (w *stateWorklist) markSeen(state *execState) {
	fp := state.fingerprint()
	w.seen[fp] = true
}

