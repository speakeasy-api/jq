package schemaexec

import (
	"context"
	"fmt"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
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
	op    int // Opcode as int (from GetOp())
	value any // Opcode value
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

	return &schemaEnv{
		ctx:      ctx,
		opts:     opts,
		stack:    newSchemaStack(),
		warnings: make([]string, 0),
		scopes:   scopes,
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
			op:    getCodeOp(rc),
			value: getCodeValue(rc),
		}
	}

	// Create initial state
	initialState := newExecState(input)

	// Create worklist
	worklist := newStateWorklist()
	worklist.push(initialState)

	// Outputs accumulator
	outputs := make([]*oas3.Schema, 0)

	// Multi-state execution loop
	for !worklist.isEmpty() {
		// Check context cancellation
		select {
		case <-env.ctx.Done():
			return nil, env.ctx.Err()
		default:
		}

		// Get next state
		state := worklist.pop()

		// Check if we've seen this state (memoization)
		if env.opts.EnableMemo && worklist.hasSeen(state) {
			continue
		}
		if env.opts.EnableMemo {
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
			if state.top() != nil {
				outputs = append(outputs, state.top())
			}
			continue
		}

		code := env.codes[state.pc]

		// Execute opcode on this state
		newStates, err := env.executeOpMultiState(state, &code)
		if err != nil {
			return nil, fmt.Errorf("error at pc=%d op=%d: %w", state.pc, code.op, err)
		}

		// Add successor states to worklist
		for _, newState := range newStates {
			worklist.push(newState)
		}
	}

	// Union all outputs
	var result *oas3.Schema
	if len(outputs) == 0 {
		result = Bottom()
	} else if len(outputs) == 1 {
		result = outputs[0]
	} else {
		result = Union(outputs, env.opts)
	}

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
		}
		return []*execState{next}, nil

	case opLoad:
		key := fmt.Sprintf("%v", c.value)
		if val, ok := next.loadVar(key); ok {
			next.push(val)
		} else {
			next.push(Top())
			env.addWarning("variable %s not found", key)
		}
		return []*execState{next}, nil

	case opRet:
		next.popFrame()
		return []*execState{next}, nil

	case opDup:
		if top := next.top(); top != nil {
			next.push(top)
		}
		return []*execState{next}, nil

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
		// Push PC for function call - we can ignore this for schema execution
		// It pushes [pc, scopeIndex] but we don't track PC values symbolically
		next.push(Top()) // Push a generic value as placeholder
		return []*execState{next}, nil

	case opCallPC:
		// Call saved PC - pop the value and widen conservatively
		// In concrete execution this jumps to a saved PC, but we can't do that symbolically
		if len(next.stack) > 0 {
			next.pop() // Pop the [pc, scopeIndex] value
		}
		// Conservative: unknown function result
		next.push(Top())
		env.addWarning("opCallPC not fully supported, widening result to Top")
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

		errorState := state.clone()
		errorState.pc = targetPC

		return []*execState{continueState, errorState}, nil

	case opForkTryEnd:
		// try-catch end - marks end of try block
		// For schema execution, just continue normally
		return []*execState{next}, nil

	case opExpBegin, opExpEnd:
		// Expression boundary markers - used for error messages
		// For schema execution, these are no-ops
		return []*execState{next}, nil

	case opPathBegin, opPathEnd:
		// Path operation markers - used for getpath/setpath
		// For schema execution, these are no-ops
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
		schema = ConstNumber(val)
	case int:
		schema = ConstNumber(float64(val))
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
		schema = ConstNumber(val)
	case int:
		schema = ConstNumber(float64(val))
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
		// Array indexing
		result = getArrayElement(base, indexKey, env.opts)
		if result == nil {
			result = Top()
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
	if env.opts.EnableWarnings {
		env.warnings = append(env.warnings, fmt.Sprintf(format, args...))
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
		result = getArrayElement(base, indexKey, env.opts)
	default:
		// Unknown type - conservative
		result = Top()
	}

	state.push(result)
	return []*execState{state}, nil
}

// execIterMulti handles iteration in multi-state mode.
func (env *schemaEnv) execIterMulti(state *execState, c *codeOp) ([]*execState, error) {
	val := state.pop()
	if val == nil {
		return []*execState{state}, nil
	}

	baseType := getType(val)
	var itemSchema *oas3.Schema

	switch baseType {
	case "array":
		if val.Items != nil && val.Items.Left != nil {
			itemSchema = val.Items.Left
		} else {
			itemSchema = Top()
		}
	case "object":
		itemSchema = unionAllObjectValues(val, env.opts)
	default:
		itemSchema = Bottom()
	}

	state.push(itemSchema)
	return []*execState{state}, nil
}

// execObjectMulti handles object construction in multi-state mode.
func (env *schemaEnv) execObjectMulti(state *execState, c *codeOp) ([]*execState, error) {
	n := c.value.(int)
	props := make(map[string]*oas3.Schema)
	required := make([]string, 0, n)

	for i := 0; i < n; i++ {
		if len(state.stack) < 2 {
			return nil, fmt.Errorf("stack underflow on object construction")
		}

		val := state.pop()
		key := state.pop()

		if getType(key) == "string" && key.Enum != nil && len(key.Enum) > 0 {
			keyNode := key.Enum[0]
			if keyNode.Kind == yaml.ScalarNode {
				props[keyNode.Value] = val
				required = append(required, keyNode.Value)
			}
		}
	}

	obj := BuildObject(props, required)
	state.push(obj)
	return []*execState{state}, nil
}

// execFork handles fork opcode - creates two execution paths.
func (env *schemaEnv) execFork(state *execState, c *codeOp) ([]*execState, error) {
	// Fork to target PC
	targetPC := c.value.(int)

	// Create two states: one continues, one jumps to target
	continueState := state.clone()
	continueState.pc++

	forkState := state.clone()
	forkState.pc = targetPC
	forkState.depth++

	return []*execState{continueState, forkState}, nil
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

	continueState := state.clone()
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

		// Pop arguments
		args := make([]*oas3.Schema, argCount)
		for i := argCount - 1; i >= 0; i-- {
			if len(state.stack) == 0 {
				return nil, fmt.Errorf("stack underflow on call")
			}
			args[i] = state.pop()
		}

		// Pop input
		if len(state.stack) == 0 {
			return nil, fmt.Errorf("stack underflow on call input")
		}
		input := state.pop()

		// Call builtin
		results, err := env.callBuiltin(funcName, input, args)
		if err != nil {
			env.addWarning("builtin %s: %v", funcName, err)
			state.push(Top())
			return []*execState{state}, nil
		}

		// For single result, push and continue
		if len(results) == 1 {
			state.push(results[0])
			return []*execState{state}, nil
		}

		// For multiple results, create separate states for each
		// This handles builtins that can return different schemas
		states := make([]*execState, len(results))
		for i, result := range results {
			s := state.clone()
			s.push(result)
			states[i] = s
		}
		return states, nil

	case int:
		// User-defined function (PC to jump to)
		// For symbolic execution, we can't easily inline the function body
		// Be conservative to avoid false negatives: widen to Top
		if len(state.stack) > 0 {
			_ = state.pop() // Discard input
		}
		env.addWarning("user-defined function call approximated; widened to Top")
		state.push(Top())
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
		return ConstNumber(val)
	case int:
		return ConstNumber(float64(val))
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
		return ArrayType(Top()) // Empty array - any items
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
		return ConstNumber(val)
	case int:
		return ConstNumber(float64(val))
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
