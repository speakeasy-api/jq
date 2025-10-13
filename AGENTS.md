# AGENTS.md - Symbolic Executor for JQ

## Purpose

This codebase builds a **KLEE-style symbolic executor for jq** in Go. It interprets gojq bytecode over **JSON Schemas** instead of concrete JSON values, producing output schemas that soundly overapproximate all possible jq outputs.

The goal is to answer: "Given an input JSON Schema, what schema describes all possible outputs of this JQ program?"

## High-Level Approach

### Bytecode-Level Symbolic Execution
- Execute compiled gojq bytecode with a **multi-state worklist VM** that models jq's backtracking semantics
- Evaluate builtins over JSON Schemas (not concrete values)
- Represent nondeterminism with union (anyOf)

### Soundness First
- **Prefer over-approximation** (Top/union) to under-approximation
- When unsure, widen
- The output schema must correctly represent ALL possible concrete values that could result from the JQ program

### Precision Where Cheap
- **Constant folding** for enum-singleton values
- Merge enum strings/integers and deduplicate unions structurally
- Filter out completely unconstrained "Top" when merging properties to preserve precision

## Key Files

### `schemaexec/log.go`
**Logging infrastructure for debug tracing.**

Provides comprehensive execution tracing at different log levels:
- **Logger interface**: Simple abstraction with Debugf/Infof/Warnf/Errorf methods
- **State tracking**: Each execution state has unique ID and lineage path for fork tracking
- **Schema summaries**: Compact representations like `object{name,age}`, `array[string]`, `integer(enum:(1,2,3))`
- **Helper functions**: `schemaTypeSummary()`, `schemaDelta()` for readable trace output

Usage:
```go
opts := DefaultOptions()
opts.LogLevel = "debug"  // Enable debug tracing
result, err := RunSchema(ctx, query, inputSchema, opts)
```

### `schemaexec/execute_schema.go`
**The core multi-state VM and bytecode interpreter.**

Key components:
- **Multi-state execution**: Maintains a worklist of execution states to handle JQ's fork/backtrack semantics
- **Accumulator-based arrays**: KLEE-style pointer tracking for array construction (lines 296-306)
- **Path mode**: Special mode for del/setpath/getpath operations (lines 437-448)
- **Object construction**: Sets required keys for all object literal keys (lines 1008-1016)

Important operations:
- `execIterMulti` (lines 940-978): Iteration over arrays/objects
- `execIndexMulti` (lines 887-938): Property access and array indexing
- `execObjectMulti` (lines 980-1027): Object literal construction
- `execAppendMulti` (lines 1029-1154): Array element accumulation
- `execFork` (lines 1156-1173): Fork operation creating parallel execution paths

### `schemaexec/builtins.go`
**Schema-level implementations of JQ builtins.**

Each builtin has signature:
```go
func(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error)
```

Key builtins:
- **Introspection**: `type`, `length`, `keys`, `values`, `has` (lines 124-268)
- **Type conversions**: `tonumber`, `tostring`, `toarray` (lines 274-291)
- **Array operations**: `add`, `reverse`, `sort`, `unique`, `min`, `max` (lines 297-378)
- **Object operations**: `to_entries`, `from_entries`, `with_entries` (lines 384-450)
- **Arithmetic**: `_plus`, `_minus`, `_multiply`, `_divide`, `_modulo` (lines 1504-1679)
- **Comparison**: `_equal`, `_less`, `_greater`, etc. (lines 1240-1286)
- **String operations**: `split`, `join`, `startswith`, `ascii_downcase`, etc. (lines 456-651)
- **Path operations**: `delpaths`, `getpath`, `setpath` (lines 1058-1165)

### `schemaexec/schemaops.go`
**Core schema algebra and manipulation.**

Key operations:
- **Schema constructors**: `Top()`, `Bottom()`, `ConstString()`, `NumberType()`, etc. (lines 17-141)
- **Union** (lines 192-277): Creates anyOf, with deduplication and widening
- **GetProperty** (lines 149-188): Property access from objects
- **BuildObject** (lines 771-784): Construct object schemas
- **Deduplication** (lines 433-469): Merges identical schemas, especially string/integer enums
- **Subsumption checking** (lines 1152-1221): Determines if one schema is a subset of another
- **Widening** (lines 693-767): Applied when unions exceed limits

Important for precision:
- String enum merging (lines 544-616): Combines multiple const strings into single enum
- Top filtering during property merge (lines 371-383): Preserves concrete schemas over unconstrained ones
- Object merging with identical required sets (lines 325-401)

### `schemaexec/errors.go`
**Error types for the symbolic executor.**

## Guiding Principles

### 1. Soundness > Precision
- When type/shape is unknown, yield `Top` or broader unions
- **Never discard possible outputs** - it's better to be imprecise than incorrect
- If you can't prove something won't happen, assume it can

### 2. Determinism/Stability
- Union-first, then materialize accumulators (to avoid "who writes last" effects)
- Deduplicate and merge wherever safe (enums, identical structure)
- Tests run symbolic execution twice to check schema stabilization

### 3. Widening Strategy
- Apply when anyOf exceeds `AnyOfLimit` (default: depends on config)
- `WideningLevel=1`: Keep per-type base schemas (e.g., separate number, string, array branches)
- `WideningLevel=2`: Collapse everything to Top

### 4. Object Literal Keys Are Always Required
- The VM marks all keys in object literals as required
- This reflects JQ semantics: `{a: .x, b: .y}` always produces both keys

## Common Patterns

### Implementing Builtins

```go
func builtinExample(input *oas3.Schema, args []*oas3.Schema, env *schemaEnv) ([]*oas3.Schema, error) {
    // 1. Check if inputs are concrete (enum with single value)
    if val, ok := extractConstValue(input); ok {
        // Perform concrete computation
        return []*oas3.Schema{ConstString(result)}, nil
    }

    // 2. Check type compatibility
    if !MightBeString(input) {
        return []*oas3.Schema{Bottom()}, nil
    }

    // 3. Return safe overapproximation
    return []*oas3.Schema{StringType()}, nil
}
```

Key points:
- Implement **const folding** for enum-singleton values
- Check type compatibility with `MightBeX()` helpers
- Return the **safest overapproximation** for symbolic inputs
- Can return **multiple schemas** to represent branching (VM forks states)

### Fork/Backtracking

JQ's `,` operator and many builtins can produce multiple outputs:
```jq
1, 2  # produces 1, then 2
.[]   # produces each array element
```

The VM handles this with:
- `opFork`: Creates two execution states (lines 1156-1173)
- `opBacktrack`: Terminates current path (line 352)
- Results from all paths are merged via `Union` at the end

### Array Construction via Accumulators

KLEE-style identity tracking:
1. Arrays stored in variables get an `allocID` (lines 296-306)
2. `opAppend` mutates the canonical array's items by unioning with new item type (lines 1105-1146)
3. After execution, `materializeArrays` resolves array references (lines 1549-1620)

This ensures all paths that append to the same array variable produce a single output schema with union of all appended types.

### Path Mode

For `del()`, `setpath()`, `getpath()`:
1. VM enters path-collection mode (`opPathBegin`, line 437)
2. Index operations collect path segments instead of navigating (lines 890-905)
3. `opPathEnd` builds a path tuple (array with prefixItems) (line 443-448)
4. Path tuples are preserved distinctly in accumulators (lines 1111-1136)

### Const Folding + Enum Merging

1. Builtins check for const inputs (enum with single value)
2. Perform concrete computation on const values
3. Multiple const results get merged by Union into enum sets
4. Example: `if .x then "a" else "b" end` → `{type: string, enum: ["a", "b"]}`

### Preserving Precision in Merges

When merging object properties from multiple execution paths:
- Filter out **unconstrained Top-like schemas** (empty schemas `{}`)
- Keep the more **concrete schemas** (lines 371-383)
- Example: merging `{id: {type: string}}` with `{id: {}}` keeps `{type: string}`

## Testing Strategy

### Golden Schema Tests

Tests in `pkg/playground/transform_test.go` validate:
1. **Iteration 1**: Execute JQ transform on input schema
2. **Iteration 2**: Feed iteration-1 output back into same transform
3. Compare against expected schemas in `testdata/{test}.out.1.yaml` and `testdata/{test}.out.2.yaml`

The schema may stabilize exactly or broaden to a consistent overapproximation.

### Test Structure

```
testdata/
  TestName.in.yaml       # Input schema
  TestName.out.1.yaml    # Expected output after iteration 1
  TestName.out.2.yaml    # Expected output after iteration 2
```

### Adding New Tests

1. Create input schema in `testdata/{TestName}.in.yaml`
2. Manually compute or run executor to generate `{TestName}.out.1.yaml`
3. Apply transform again to get `{TestName}.out.2.yaml`
4. Add test case in `transform_test.go` that loads and compares schemas

## Adding/Modifying Builtins

When implementing a new builtin:

1. **Start from similar builtins** (e.g., compare `builtinMultiply` for arithmetic)
2. **Implement const folding** against enum-singleton values
3. **Return safest overapproximation** for non-constant inputs
4. **Test arity variations**: Some builtins work with different argument counts
5. **Handle object-first calling convention**: Binary operators can receive args in different orders
6. **Prefer returning multiple branches** to encode possible outcomes

Example builtin locations:
- Arithmetic: lines 1504-1679 in `builtins.go`
- String operations: lines 456-651
- Array operations: lines 297-378

## Known Limitations and Edge Cases

### 1. Optional Property Nullability
- `GetProperty` doesn't union with null for non-required properties in Phase 1
- This keeps outputs simpler but may be optimistic
- Remains sound for overapproximation (consumers should assume properties may be absent)

### 2. Indexing Non-Indexable Types
- When indexing non-object/array (e.g., null), JQ returns null
- We return `Top` for overapproximation (less precise but sound)
- Location: `execute_schema.go` lines 931-934

### 3. Dynamic Object Keys
- `execObjectMulti` warns but doesn't deeply handle computed keys
- Location: `execute_schema.go` lines 766-768

### 4. Alternative Operator (`//`)
- Implemented by exploring both branches (execForkAlt)
- This overapproximates fallback semantics but is sound

### 5. Recursion
- `opCallRec` is widened to Top (not fully supported)
- Location: `execute_schema.go` lines 403-411

### 6. Regex Operations
- `test`, `match`: Conservative unless const inputs/patterns
- Location: `builtins.go` lines 988-1035

### 7. Array Slicing Bug
- Known issue with array slicing producing invalid EitherValue states
- Test disabled: `_TestSymbolicExecuteJQPipeline_ComputedFullName`
- See: `transform_test.go` lines 814-875

## Operational Practices

### Configuration
- Keep `WideningLevel` at 1 for practical unions
- Set `AnyOfLimit` and `EnumLimit` for tractable merges
- Enable `EnableWarnings` during development to collect warnings
- Set `LogLevel` to "debug" for detailed execution tracing (logs to stderr)
  - "error": Only critical failures
  - "warn": Warnings about precision loss and unsupported operations (default)
  - "info": High-level execution lifecycle
  - "debug": Per-opcode trace with state, stack, and schema changes

### Development Workflow
1. Write test with expected input/output schemas
2. Run symbolic executor
3. If output differs, investigate:
   - Is it a soundness issue? (Missing possible outputs)
   - Is it a precision issue? (Too wide but correct)
   - Is it a bug? (Incorrect transformation)

### Debugging Tips

**Use Debug Logging:**
- Set `opts.LogLevel = "debug"` to enable comprehensive execution tracing
- Each opcode execution is logged with state ID, lineage, PC, stack depth, and schema types
- Fork paths are tracked with lineage strings (e.g., "0" → "0.F" for fork branch, "0.C" for continue)
- Terminal states show final result schemas
- All warnings are automatically logged at WARN level

**When Things Don't Work:**
1. **Add debug logging immediately** when investigating issues - set `LogLevel: "debug"` in your test
2. **Leave the logs in** after fixing - they're useful for future debugging and understanding execution flow
3. **Watch for**:
   - Unexpected fork branches (lineage tracking shows execution paths)
   - Stack depth anomalies (might indicate missing pop/push operations)
   - Schema widening to Top (warnings show where precision is lost)
   - Accumulator issues for array construction (shows allocID and item type unions)

**Other Debugging:**
- Check worklist iterations (may indicate infinite loops)
- Verify accumulator materialization for array construction
- Use const folding tests to validate builtin logic
- Review terminal state logs to see what each execution path produced

### Performance Considerations
- Memoization currently disabled (fingerprint issues with nested properties)
- Watch for union explosion (adjust `AnyOfLimit`)
- Consider widening earlier if compilation times are too long

## Future Improvements

Areas for enhancement:
1. **Better nullability tracking**: Union optional properties with null
2. **More precise array slicing**: Fix the known bug
3. **Recursive function support**: Implement fixed-point iteration
4. **Better dynamic key handling**: Track key sets symbolically
5. **Memoization**: Fix fingerprinting to enable state deduplication
6. **Regex analysis**: More precise regex matching for common patterns

## Additional Resources

- JQ manual: https://jqlang.github.io/jq/manual/
- gojq (the JQ implementation we use): https://github.com/itchyny/gojq
- JSON Schema specification: https://json-schema.org/
- KLEE symbolic execution: https://klee.github.io/

## Questions or Issues?

When working on this codebase:
- **Check soundness first**: Does the change preserve overapproximation?
- **Test with iteration**: Does the schema stabilize or widen reasonably?
- **Document assumptions**: Add comments explaining non-obvious decisions
- **Ask for review**: Symbolic execution is subtle; peer review helps

Remember: **It's better to be imprecise than incorrect!**
