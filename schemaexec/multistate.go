package schemaexec

import (
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
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

	// Variable intent tracking (to solve orphaned-allocID issue)
	varAllocHistory  map[string]map[string]struct{} // varKey -> set(allocID) ever assigned
	varDesiredItemFP map[string]string              // varKey -> schemaFingerprint(items)
	allocDesiredFP   map[string]string              // SHARED: allocID -> schemaFingerprint(items)
	schemaFPIntent   map[*oas3.Schema]string        // Per-state: schema pointer → items fingerprint

	// Theory 10: Hybrid Origin-Lattice
	allocOrigin      map[string]*AllocOrigin       // SHARED: allocID -> origin (where it was created)
	allocCardinality map[string]*ArrayCardinality  // SHARED: allocID -> cardinality bounds
	dsu              *DSU                          // SHARED: Disjoint Set Union for allocID equivalence
}

// AllocOrigin tracks where an allocID was created in the AST/execution
type AllocOrigin struct {
	PC      int    // Program counter where allocation occurred
	Context string // Semantic context (e.g., "reduce_accumulator", "map_accumulator")
}

// ArrayCardinality tracks bounds on array size for lattice-based merging
type ArrayCardinality struct {
	MinItems *int // Lower bound: 0 = maybe-empty, 1+ = must-be-non-empty
	MaxItems *int // Upper bound: nil = unbounded
}

// Join performs lattice join (LUB) on two cardinality bounds
// This is the mathematically sound merge operation for the cardinality lattice
func (a *ArrayCardinality) Join(other *ArrayCardinality) *ArrayCardinality {
	if a == nil && other == nil {
		return nil
	}
	if a == nil {
		return other
	}
	if other == nil {
		return a
	}

	// Join MinItems: take minimum (most permissive lower bound)
	var minItems *int
	if a.MinItems == nil && other.MinItems == nil {
		minItems = nil
	} else if a.MinItems == nil {
		minItems = other.MinItems
	} else if other.MinItems == nil {
		minItems = a.MinItems
	} else {
		min := *a.MinItems
		if *other.MinItems < min {
			min = *other.MinItems
		}
		minItems = &min
	}

	// Join MaxItems: take maximum (most permissive upper bound)
	var maxItems *int
	if a.MaxItems == nil || other.MaxItems == nil {
		// nil means unbounded, which dominates any finite bound
		maxItems = nil
	} else {
		max := *a.MaxItems
		if *other.MaxItems > max {
			max = *other.MaxItems
		}
		maxItems = &max
	}

	return &ArrayCardinality{
		MinItems: minItems,
		MaxItems: maxItems,
	}
}

// DSU implements Disjoint Set Union (Union-Find) for allocID equivalence classes
type DSU struct {
	parent map[string]string // allocID -> parent allocID
}

// NewDSU creates a new Disjoint Set Union structure
func NewDSU() *DSU {
	return &DSU{
		parent: make(map[string]string),
	}
}

// Find returns the canonical allocID for an equivalence class (with path compression)
func (d *DSU) Find(allocID string) string {
	if allocID == "" {
		return ""
	}
	if _, exists := d.parent[allocID]; !exists {
		// First time seeing this allocID, it's its own parent
		d.parent[allocID] = allocID
		return allocID
	}
	// Path compression
	if d.parent[allocID] != allocID {
		d.parent[allocID] = d.Find(d.parent[allocID])
	}
	return d.parent[allocID]
}

// Union merges two equivalence classes
func (d *DSU) Union(allocID1, allocID2 string) {
	if allocID1 == "" || allocID2 == "" {
		return
	}
	root1 := d.Find(allocID1)
	root2 := d.Find(allocID2)
	if root1 != root2 {
		// Union by making root1 the parent of root2
		d.parent[root2] = root1
	}
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

	// Clone intent/history maps (per-state tracking)
	histCopy := make(map[string]map[string]struct{}, len(s.varAllocHistory))
	for k, set := range s.varAllocHistory {
		setCopy := make(map[string]struct{}, len(set))
		for id := range set {
			setCopy[id] = struct{}{}
		}
		histCopy[k] = setCopy
	}
	fpCopy := make(map[string]string, len(s.varDesiredItemFP))
	for k, fp := range s.varDesiredItemFP {
		fpCopy[k] = fp
	}
	schemaFPCopy := make(map[*oas3.Schema]string, len(s.schemaFPIntent))
	for ptr, fp := range s.schemaFPIntent {
		schemaFPCopy[ptr] = fp
	}

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
		varAllocHistory:  histCopy,
		varDesiredItemFP: fpCopy,
		allocDesiredFP:   s.allocDesiredFP, // SHARED
		schemaFPIntent:   schemaFPCopy,
		// Theory 10: Hybrid Origin-Lattice (SHARED)
		allocOrigin:      s.allocOrigin,      // SHARED
		allocCardinality: s.allocCardinality, // SHARED
		dsu:              s.dsu,              // SHARED
	}
}

// recordSchemaFP records intended items fingerprint for a schema pointer
func (s *execState) recordSchemaFP(arr, items *oas3.Schema) {
	if arr == nil || items == nil {
		return
	}
	if s.schemaFPIntent == nil {
		s.schemaFPIntent = make(map[*oas3.Schema]string)
	}
	s.schemaFPIntent[arr] = schemaFingerprint(items)
}

// recordVarAlloc records that a variable has been assigned an allocID
func (s *execState) recordVarAlloc(key, allocID string) {
	if key == "" || allocID == "" {
		return
	}
	set, ok := s.varAllocHistory[key]
	if !ok {
		set = make(map[string]struct{}, 4)
		s.varAllocHistory[key] = set
	}
	set[allocID] = struct{}{}
}

// recordDesiredFP records the intended items schema fingerprint for a variable and its allocID
func (s *execState) recordDesiredFP(key string, items *oas3.Schema) {
	if key == "" || items == nil {
		return
	}
	fp := schemaFingerprint(items)
	s.varDesiredItemFP[key] = fp
	// Also record on the allocID for cross-variable propagation
	// Check what allocID this variable currently uses
	if v, ok := s.loadVar(key); ok {
		if allocID, ok := s.schemaToAlloc[v]; ok && allocID != "" {
			if s.allocDesiredFP == nil {
				s.allocDesiredFP = make(map[string]string)
			}
			s.allocDesiredFP[allocID] = fp
		}
	}
}

// fingerprint computes a hash of this state for memoization.
// Uses proper schema fingerprinting to handle nested structures, enums, and circular references.
// Includes: pc, depth, callstack, full stack with schema fingerprints, scope bindings with schema fingerprints, and path mode.
func (s *execState) fingerprint() uint64 {
	// Use the new fingerprinting helper from fingerprint.go
	return fingerprintStateHelper(s, defaultFingerprinter)
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
		varAllocHistory:  make(map[string]map[string]struct{}),
		varDesiredItemFP: make(map[string]string),
		allocDesiredFP:   make(map[string]string), // Shared
		schemaFPIntent:   make(map[*oas3.Schema]string),
		// Theory 10: Hybrid Origin-Lattice
		allocOrigin:      make(map[string]*AllocOrigin),
		allocCardinality: make(map[string]*ArrayCardinality),
		dsu:              NewDSU(),
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

