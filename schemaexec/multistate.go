package schemaexec

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// execState represents a single execution state in the multi-state VM.
// jq's backtracking semantics require tracking multiple possible execution paths.
type execState struct {
	pc       int              // Program counter
	stack    []SValue         // Schema stack (copy for this state)
	scopes   []map[string]*oas3.Schema // Scope frames (copy for this state)
	depth    int              // Recursion depth (for limiting)
}

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

	return &execState{
		pc:     s.pc,
		stack:  stackCopy,
		scopes: scopesCopy,
		depth:  s.depth,
	}
}

// fingerprint computes a hash of this state for memoization.
// We hash: pc + stack top type + depth
func (s *execState) fingerprint() uint64 {
	h := sha256.New()

	// Hash PC
	binary.Write(h, binary.LittleEndian, uint64(s.pc))

	// Hash stack depth
	binary.Write(h, binary.LittleEndian, uint64(len(s.stack)))

	// Hash top of stack type (if non-empty)
	if len(s.stack) > 0 && s.stack[len(s.stack)-1].Schema != nil {
		topType := getType(s.stack[len(s.stack)-1].Schema)
		h.Write([]byte(topType))
	}

	// Hash recursion depth
	binary.Write(h, binary.LittleEndian, uint64(s.depth))

	// Return first 8 bytes as uint64
	sum := h.Sum(nil)
	return binary.LittleEndian.Uint64(sum[:8])
}

// push pushes a schema onto the stack.
func (s *execState) push(schema *oas3.Schema) {
	s.stack = append(s.stack, SValue{Schema: schema})
}

// pop removes and returns the top schema.
func (s *execState) pop() *oas3.Schema {
	if len(s.stack) == 0 {
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
	state := &execState{
		pc:     0,
		stack:  make([]SValue, 0, 16),
		scopes: make([]map[string]*oas3.Schema, 0, 4),
		depth:  0,
	}
	state.pushFrame() // Initial global frame
	state.push(input) // Push input onto stack
	return state
}

// stateWorklist manages the queue of states to execute.
type stateWorklist struct {
	states []*execState
	seen   map[uint64]bool // Memoization: fingerprint → visited
}

// newStateWorklist creates a new worklist.
func newStateWorklist() *stateWorklist {
	return &stateWorklist{
		states: make([]*execState, 0, 32),
		seen:   make(map[uint64]bool),
	}
}

// push adds a state to the worklist.
func (w *stateWorklist) push(state *execState) {
	w.states = append(w.states, state)
}

// pop removes and returns the next state (FIFO for breadth-first).
func (w *stateWorklist) pop() *execState {
	if len(w.states) == 0 {
		return nil
	}
	state := w.states[0]
	w.states = w.states[1:]
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

// Merge states at the same PC by unioning their top-of-stack schemas.
func mergeStates(states []*execState, opts SchemaExecOptions) *execState {
	if len(states) == 0 {
		return nil
	}
	if len(states) == 1 {
		return states[0]
	}

	// Use first state as base
	merged := states[0].clone()

	// Union top-of-stack from all states
	schemas := make([]*oas3.Schema, len(states))
	for i, state := range states {
		schemas[i] = state.top()
	}

	// Replace top with union
	merged.pop()
	merged.push(Union(schemas, opts))

	return merged
}

// Error types for multi-state VM
type haltError struct {
	message string
}

func (e *haltError) Error() string {
	return fmt.Sprintf("halt: %s", e.message)
}
