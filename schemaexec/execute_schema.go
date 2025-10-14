package schemaexec

import (
	"context"
	"fmt"
	"math"
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
	pc       int
	warnings []string
	scopes   *scopeFrames // Scope frame stack for variable management
	logger   Logger       // Logger for debug tracing
	execID   string       // Unique execution ID
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

// popFrame removes the current variable scope.
func (sf *scopeFrames) popFrame() {
	if len(sf.frames) > 0 {
		sf.frames = sf.frames[:len(sf.frames)-1]
	}
}

// currentFrame returns the current variable frame (or nil if no frames).
func (sf *scopeFrames) currentFrame() map[string]*oas3.Schema {
	if len(sf.frames) == 0 {
		return nil
	}
	return sf.frames[len(sf.frames)-1]
}

// store saves a schema to the current frame.
func (sf *scopeFrames) store(key string, schema *oas3.Schema) {
	if frame := sf.currentFrame(); frame != nil {
		frame[key] = schema
	}
}

// load retrieves a schema from the current or outer frames.
func (sf *scopeFrames) load(key string) (*oas3.Schema, bool) {
	// Search from innermost to outermost frame
	for i := len(sf.frames) - 1; i >= 0; i-- {
		if schema, ok := sf.frames[i][key]; ok {
			return schema, true
		}
	}
	return nil, false
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
	}
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

	// Track accumulator for array construction results
	var sharedAccum map[string]*oas3.Schema
	var sharedSchemaToAlloc map[*oas3.Schema]string

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

		// Get next state
		state := worklist.pop()

		// Check if we've seen this state (memoization)
		// TODO: Memoization currently disabled because fingerprint doesn't recursively
		// hash nested property schemas, causing incorrect state deduplication
		// when properties have different enum values (e.g., tier: "gold" vs "silver")
		_ = worklist // Suppress unused warning
		if false && env.opts.EnableMemo && worklist.hasSeen(state) {
			fmt.Printf("DEBUG: Skipping memoized state at pc=%d\n", state.pc)
			continue
		}
		if false && env.opts.EnableMemo {
			worklist.markSeen(state)
		}

		// Check depth limit
		if state.depth > env.opts.MaxDepth {
			env.addWarning("max depth exceeded, widening to Top")
			outputs = append(outputs, Top())
			continue
		}

		// Execute one step
		if state.pc >= len(env.codes) {
			// Terminal state - collect output
			outputSchema := state.top()
			if outputSchema != nil {
				outputs = append(outputs, outputSchema)

				// Log terminal state
				env.logger.With(map[string]any{
					"exec":    env.execID,
					"state":   fmt.Sprintf("s%d", state.id),
					"lineage": state.lineage,
					"result":  schemaTypeSummary(outputSchema, 1),
				}).Debugf("Terminal state reached")
			}
			// Save reference to shared maps (all states share the same maps)
			if sharedAccum == nil && state.accum != nil {
				sharedAccum = state.accum
				sharedSchemaToAlloc = state.schemaToAlloc
			}
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
					"exec":     env.execID,
					"state":    fmt.Sprintf("s%d", newState.id),
					"parent":   fmt.Sprintf("s%d", newState.parentID),
					"lineage":  newState.lineage,
					"pc":       newState.pc,
					"op":       code.opName,
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

	// Materialize arrays from accumulators AFTER Union
	// This ensures arrays in merged objects are also resolved
	if sharedAccum != nil && sharedSchemaToAlloc != nil {
		result = env.materializeArrays(result, sharedAccum, sharedSchemaToAlloc)
	}

	// Log execution completion
	env.logger.With(map[string]any{
		"exec":        env.execID,
		"outputs":     len(outputs),
		"result_type": schemaTypeSummary(result, 1),
		"warnings":    len(env.warnings),
	}).Infof("Execution completed")

	return &SchemaExecResult{
		Schema:   result,
		Warnings: env.warnings,
	}, nil
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
		next.pushFrame()
		return []*execState{next}, nil

	case opStore:
		if len(next.stack) > 0 {
			val := next.pop()
			key := fmt.Sprintf("%v", c.value)
			next.storeVar(key, val)
			// If storing an array, assign unique allocID and tag the schema
			// KLEE-style: arrays carry their identity via schema pointer → allocID mapping
			if getType(val) == "array" {
				*next.allocCounter++
				allocID := *next.allocCounter
				accumKey := fmt.Sprintf("alloc%d", allocID)
				// Initialize canonical array
				if _, exists := next.accum[accumKey]; !exists {
					next.accum[accumKey] = val
				}
				// Tag this schema with its allocID
				next.schemaToAlloc[val] = accumKey
			}
		}
		return []*execState{next}, nil

	case opLoad:
		key := fmt.Sprintf("%v", c.value)
		// Load from normal variable frames (arrays are tagged with allocID)
		if val, ok := next.loadVar(key); ok {
			next.push(val)
		} else {
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
		next.push(Top())
		return []*execState{next}, nil

	case opCallRec:
		// Recursive call - similar to CallPC
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
		if env.opts.StrictMode {
			return nil, fmt.Errorf("unsupported opcode: %d", c.op)
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
func (env *schemaEnv) executeOp(c *codeOp) error {
	switch c.op {
	case opNop:
		return nil

	case opPush:
		return env.execPush(c)

	case opPop:
		return env.execPop(c)

	case opConst:
		return env.execConst(c)

	case opIndex:
		return env.execIndex(c)

	case opIndexArray:
		return env.execIndexArray(c)

	case opIter:
		return env.execIter(c)

	case opObject:
		return env.execObject(c)

	case opRet:
		// Return from function - pop scope frame
		env.scopes.popFrame()
		return nil

	case opScope:
		// Enter new variable scope
		env.scopes.pushFrame()
		return nil

	case opStore:
		// Store variable in current scope frame
		if !env.stack.empty() {
			val := env.stack.popSchema()
			// The value contains [scopeID, varIndex]
			key := fmt.Sprintf("%v", c.value)
			env.scopes.store(key, val)
		}
		return nil

	case opLoad:
		// Load variable from scope frames (searches inner to outer)
		key := fmt.Sprintf("%v", c.value)
		if val, ok := env.scopes.load(key); ok {
			env.stack.pushSchema(val)
		} else {
			// Variable not found - push Top
			env.stack.pushSchema(Top())
			env.addWarning("variable %s not found, using Top", key)
		}
		return nil

	case opDup:
		// Duplicate top of stack
		if env.stack.empty() {
			return fmt.Errorf("stack underflow on dup")
		}
		top := env.stack.topSchema()
		env.stack.pushSchema(top)
		return nil

	case opCall:
		// Function call
		return env.execCall(c)

	// Unsupported opcodes - handle gracefully
	default:
		if env.opts.StrictMode {
			return fmt.Errorf("unsupported opcode: %d", c.op)
		}

		// Permissive mode: widen to Top
		env.addWarning("unsupported opcode %d, widening to Top", c.op)
		if !env.stack.empty() {
			env.stack.popSchema()
		}
		env.stack.pushSchema(Top())
		return nil
	}
}

// ============================================================================
// OPCODE HANDLERS
// ============================================================================

// execPush handles oppush - push a constant value as a schema.
func (env *schemaEnv) execPush(c *codeOp) error {
	v := c.value

	var schema *oas3.Schema
	switch val := v.(type) {
	case string:
		schema = ConstString(val)
	case float64:
		if val == math.Trunc(val) {
			schema = ConstInteger(int64(val))
		} else {
			schema = ConstNumber(val)
		}
	case int:
		schema = ConstInteger(int64(val))
	case bool:
		schema = ConstBool(val)
	case nil:
		schema = ConstNull()
	case map[string]any:
		// Object literal - build schema from the literal values
		schema = buildObjectFromLiteral(val)
	case []any:
		// Array literal - build schema from the literal values
		schema = buildArrayFromLiteral(val)
	default:
		schema = Top()
		env.addWarning("unknown constant type: %T", v)
	}

	env.stack.pushSchema(schema)
	return nil
}

// execPop handles oppop - remove top value from stack.
func (env *schemaEnv) execPop(c *codeOp) error {
	if env.stack.empty() {
		return fmt.Errorf("stack underflow on pop")
	}
	env.stack.popSchema()
	return nil
}

// execConst handles opconst - replace top of stack with constant.
func (env *schemaEnv) execConst(c *codeOp) error {
	if env.stack.empty() {
		return fmt.Errorf("stack underflow on const")
	}

	env.stack.popSchema()

	// Push the constant value
	v := c.value
	var schema *oas3.Schema
	switch val := v.(type) {
	case string:
		schema = ConstString(val)
	case float64:
		if val == math.Trunc(val) {
			schema = ConstInteger(int64(val))
		} else {
			schema = ConstNumber(val)
		}
	case int:
		schema = ConstInteger(int64(val))
	case bool:
		schema = ConstBool(val)
	case nil:
		schema = ConstNull()
	default:
		schema = Top()
	}

	env.stack.pushSchema(schema)
	return nil
}

// execIndex handles opindex - index into object or array with a constant key.
func (env *schemaEnv) execIndex(c *codeOp) error {
	if env.stack.empty() {
		return fmt.Errorf("stack underflow on index")
	}

	base := env.stack.popSchema()
	indexKey := c.value // The key is stored in the code

	var result *oas3.Schema

	// Determine what we're indexing
	baseType := getType(base)

	switch baseType {
	case "object":
		// Object property access
		if key, ok := indexKey.(string); ok {
			result = GetProperty(base, key, env.opts)
		} else {
			result = Top()
			env.addWarning("non-string object index")
		}

	case "array":
		// Array slicing or indexing
		if isSliceIndex(indexKey) {
			// Slicing preserves array schema
			result = base
		} else {
			result = getArrayElement(base, indexKey, env.opts)
			if result == nil {
				result = Top()
			}
		}

	case "":
		// Unknown type - could be object or array
		// Try both and union
		if key, ok := indexKey.(string); ok {
			objResult := GetProperty(base, key, env.opts)
			arrResult := Top() // Conservative for array case
			result = Union([]*oas3.Schema{objResult, arrResult}, env.opts)
		} else {
			result = Top()
		}

	default:
		// Indexing other types returns null in jq
		result = ConstNull()
	}

	env.stack.pushSchema(result)
	return nil
}

// execIndexArray handles opindexarray - index that requires array type.
func (env *schemaEnv) execIndexArray(c *codeOp) error {
	// Similar to execIndex but enforces array type
	return env.execIndex(c)
}

// execIter handles opiter - iterate array or object values.
func (env *schemaEnv) execIter(c *codeOp) error {
	if env.stack.empty() {
		return fmt.Errorf("stack underflow on iter")
	}

	val := env.stack.popSchema()
	baseType := getType(val)

	var itemSchema *oas3.Schema

	switch baseType {
	case "array":
		// Iterate array items
		if val.Items != nil && val.Items.Left != nil {
			itemSchema = val.Items.Left
		} else {
			itemSchema = Top()
		}

	case "object":
		// Iterate object values - union of all property schemas
		itemSchema = unionAllObjectValues(val, env.opts)

	case "":
		// Unknown type - could be either
		itemSchema = Top()

	default:
		// Non-iterable returns nothing
		itemSchema = Bottom()
	}

	env.stack.pushSchema(itemSchema)
	return nil
}

// execObject handles opobject - construct object from key-value pairs.
func (env *schemaEnv) execObject(c *codeOp) error {
	n := c.value.(int) // Number of key-value pairs

	props := make(map[string]*oas3.Schema)
	required := make([]string, 0, n)

	for i := 0; i < n; i++ {
		if env.stack.len() < 2 {
			return fmt.Errorf("stack underflow on object construction (need %d pairs, stack has %d)", n, env.stack.len()/2)
		}

		val := env.stack.popSchema()
		key := env.stack.popSchema()

		// Check if key is constant string
		if getType(key) == "string" && key.Enum != nil && len(key.Enum) > 0 {
			// Extract constant string from enum
			keyNode := key.Enum[0]
			if keyNode.Kind == yaml.ScalarNode {
				keyStr := keyNode.Value
				props[keyStr] = val
				// In jq object construction, keys are always present
				required = append(required, keyStr)
			}
		} else {
			// Dynamic key - not fully supported yet
			env.addWarning("dynamic object key not fully supported")
		}
	}
	obj := BuildObject(props, required)
	env.stack.pushSchema(obj)
	return nil
}

// ============================================================================
// HELPERS
// ============================================================================

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
			if arr.PrefixItems[idx].Left != nil {
				return arr.PrefixItems[idx].Left
			}
		}

		// Fall through to items for indices beyond prefixItems
		if arr.Items != nil && arr.Items.Left != nil {
			return arr.Items.Left
		}

		// No schema for this index
		return Top()
	}

	// Non-constant or unknown index - union all possible element types
	schemas := make([]*oas3.Schema, 0)

	// Add all prefixItems
	if arr.PrefixItems != nil {
		for _, item := range arr.PrefixItems {
			if item.Left != nil {
				schemas = append(schemas, item.Left)
			}
		}
	}

	// Add items schema
	if arr.Items != nil && arr.Items.Left != nil {
		schemas = append(schemas, arr.Items.Left)
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
			result = GetProperty(base, key, env.opts)
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
		// Unknown type - conservative
		result = Top()
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
	if val == nil {
		return []*execState{state}, nil
	}

	baseType := getType(val)
	var itemSchema *oas3.Schema

	switch baseType {
	case "array":
		if val.Items != nil {
			// Items field is set
			if val.Items.Left != nil {
				// Items.Left is set to actual schema
				itemSchema = val.Items.Left
			} else {
				// Items.Left is nil, meaning Bottom (empty array with 0 elements)
				// Iterating an empty array produces no values, terminate this execution path
				return []*execState{}, nil
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

		if env.opts.EnableWarnings {
			keyStr := "<?>"
			if getType(key) == "string" && key.Enum != nil && len(key.Enum) > 0 && key.Enum[0].Kind == yaml.ScalarNode {
				keyStr = key.Enum[0].Value
			}
			env.addWarning("opObject: pair %d: key=%s, valType=%s", i, keyStr, getType(val))
		}

		if getType(key) == "string" && key.Enum != nil && len(key.Enum) > 0 {
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
	// If no variable target, pop the array from the stack
	if targetArray == nil && len(state.stack) > 0 {
		candidate := state.pop()
		if getType(candidate) == "array" {
			targetArray = candidate
			fromVar = false
		} else {
			// Not an array; push it back
			state.push(candidate)
		}
	}

	// Look up or assign allocID for this array
	var accumKey string
	if targetArray != nil {
		if ak, ok := state.schemaToAlloc[targetArray]; ok {
			accumKey = ak
		} else {
			// Not tagged yet - assign new allocID
			*state.allocCounter++
			accumKey = fmt.Sprintf("alloc%d", *state.allocCounter)
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
			canonicalArr = ArrayType(Bottom())
			state.accum[accumKey] = canonicalArr
		}
	} else {
		// No target found - create standalone
		canonicalArr = ArrayType(Bottom())
	}

	// Get prior items
	priorItems := Bottom()
	if getType(canonicalArr) == "array" && canonicalArr.Items != nil && canonicalArr.Items.Left != nil {
		priorItems = canonicalArr.Items.Left
	}

	// For path tuples (arrays with prefixItems), don't union - collect multiple paths
	// This preserves the path structure for delpaths/getpath/setpath
	var unionedItems *oas3.Schema
	valType := getType(val)
	hasTuplePrefixItems := val != nil && val.PrefixItems != nil && len(val.PrefixItems) > 0

	if valType == "array" && hasTuplePrefixItems {
		// This is a path tuple - don't union, but collect multiple paths
		priorIsTuple := getType(priorItems) == "array" && priorItems.PrefixItems != nil && len(priorItems.PrefixItems) > 0

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
		unionedItems = Union([]*oas3.Schema{priorItems, val}, env.opts)
	}

	// MUTATE canonical array in-place - safe with unique keys!
	// All states/references to this array will see the update
	if getType(canonicalArr) == "array" {
		canonicalArr.Items = oas3.NewJSONSchemaFromSchema[oas3.Referenceable](unionedItems)
	}

	// Push the canonical array back ONLY if we took it from the stack
	// For variable-backed accumulation, the array stays in the variable
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
		val.Enum != nil && len(val.Enum) == 1 &&
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

		// Pop arguments FIRST (right-to-left), then input
		args := make([]*oas3.Schema, argCount)
		for i := argCount - 1; i >= 0; i-- {
			if len(state.stack) == 0 {
				return nil, fmt.Errorf("stack underflow on call args")
			}
			args[i] = state.pop()
		}

		// Pop input (from bottom of stack)
		if len(state.stack) == 0 {
			return nil, fmt.Errorf("stack underflow on call input")
		}
		input := state.pop()

		// SPECIAL CASE: Path builtins have inverted calling convention
		// For delpaths/getpath/setpath: input should be the object, not the paths array
		if (funcName == "delpaths" || funcName == "getpath" || funcName == "setpath") && len(args) >= 1 {
			// Swap input and first arg
			input, args[0] = args[0], input
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
			env.addWarning("builtin %s: %v", funcName, err)
			state.push(Top())
			return []*execState{state}, nil
		}

		// For single result, push and continue
		if len(results) == 1 {
			r := results[0]
			if r == nil {
				env.addWarning("builtin %s returned nil; widening to Top", funcName)
				r = Top()
			}
			state.push(r)
			return []*execState{state}, nil
		}

		// For multiple results, create separate states for each
		// This handles builtins that can return different schemas
		states := make([]*execState, 0, len(results))
		for _, result := range results {
			s := state.clone()
			if result == nil {
				env.addWarning("builtin %s produced nil result; widening to Top", funcName)
				result = Top()
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

// execCall handles function calls (opcall).
func (env *schemaEnv) execCall(c *codeOp) error {
	// The value can be different things:
	// - [3]any{func, argCount, name} for simple builtins
	// - int for user-defined functions
	// - string for simple builtins

	switch v := c.value.(type) {
	case [3]any:
		// Builtin function call
		// v[0] = function pointer (ignore for symbolic)
		// v[1] = arg count
		// v[2] = function name

		argCount := 0
		if ac, ok := v[1].(int); ok {
			argCount = ac
		}

		funcName := ""
		if fn, ok := v[2].(string); ok {
			funcName = fn
		}

		// Pop arguments and input from stack
		args := make([]*oas3.Schema, argCount)
		for i := argCount - 1; i >= 0; i-- {
			if env.stack.empty() {
				return fmt.Errorf("stack underflow on call args")
			}
			args[i] = env.stack.popSchema()
		}

		// Pop input
		if env.stack.empty() {
			return fmt.Errorf("stack underflow on call input")
		}
		input := env.stack.popSchema()

		// Call builtin
		results, err := env.callBuiltin(funcName, input, args)
		if err != nil {
			// Builtin not implemented - widen to Top
			env.addWarning("builtin %s not implemented: %v", funcName, err)
			env.stack.pushSchema(Top())
			return nil
		}

		// Push result (union if multiple)
		if len(results) == 0 {
			env.stack.pushSchema(Bottom())
		} else if len(results) == 1 {
			env.stack.pushSchema(results[0])
		} else {
			env.stack.pushSchema(Union(results, env.opts))
		}

		return nil

	default:
		// User-defined function or unknown
		env.addWarning("user-defined function call not yet supported")
		if !env.stack.empty() {
			env.stack.popSchema()
		}
		env.stack.pushSchema(Top())
		return nil
	}
}

// unionAllObjectValues creates union of all property and additionalProperty schemas.
func unionAllObjectValues(obj *oas3.Schema, opts SchemaExecOptions) *oas3.Schema {
	schemas := make([]*oas3.Schema, 0)

	// Add all property values
	if obj.Properties != nil {
		for _, v := range obj.Properties.All() {
			if v.Left != nil {
				schemas = append(schemas, v.Left)
			}
		}
	}

	// Add additionalProperties
	if obj.AdditionalProperties != nil && obj.AdditionalProperties.Left != nil {
		schemas = append(schemas, obj.AdditionalProperties.Left)
	}

	// TODO: Add patternProperties

	if len(schemas) == 0 {
		return Top() // Unknown object values
	}

	return Union(schemas, opts)
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
		// Empty array constant: Use Bottom() as items to signify "no elements".
		// Items.Left = nil allows iteration to detect and skip empty arrays.
		// Also set maxItems=0 for schema validation clarity.
		emptyArray := ArrayType(Bottom())
		maxItems := int64(0)
		emptyArray.MaxItems = &maxItems
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

// materializeArrays recursively walks a schema and replaces arrays with
// their final accumulated versions using schema pointer tagging
func (env *schemaEnv) materializeArrays(schema *oas3.Schema, accum map[string]*oas3.Schema, schemaToAlloc map[*oas3.Schema]string) *oas3.Schema {
	if schema == nil {
		return nil
	}

	// If this is an array, check if it's tagged OR if it IS a canonical array
	if getType(schema) == "array" {
		// First try: check if tagged
		if allocID, ok := schemaToAlloc[schema]; ok {
			if canonical, ok2 := accum[allocID]; ok2 {
				if env.opts.EnableWarnings {
					canonicalItems := ""
					if canonical.Items != nil && canonical.Items.Left != nil {
						canonicalItems = getType(canonical.Items.Left)
					}
					env.addWarning("materialize: tagged array → canonical %s (items=%s)", allocID, canonicalItems)
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
					// This IS a canonical array - already has accumulated items
					if env.opts.EnableWarnings {
						env.addWarning("materialize: array is canonical %s (no replacement needed)", allocID)
					}
					return canonical
				}
			}
			// Empty array with no tag and not in accumulator - leave as-is
			// Don't guess which accumulator to use (that would be heuristic)
		}
	}

	// Recursively materialize object properties
	if getType(schema) == "object" && schema.Properties != nil {
		modified := false
		newProps := sequencedmap.New[string, *oas3.JSONSchema[oas3.Referenceable]]()
		for k, propSchema := range schema.Properties.All() {
			if propSchema.Left != nil {
				materialized := env.materializeArrays(propSchema.Left, accum, schemaToAlloc)
				if materialized != propSchema.Left {
					modified = true
				}
				newProps.Set(k, oas3.NewJSONSchemaFromSchema[oas3.Referenceable](materialized))
			} else {
				newProps.Set(k, propSchema)
			}
		}
		if modified {
			if env.opts.EnableWarnings {
				propCount := 0
				for range newProps.All() {
					propCount++
				}
				env.addWarning("materialize: reconstructed object with %d properties", propCount)
			}
			result := *schema
			result.Properties = newProps
			return &result
		}
	}

	return schema
}
