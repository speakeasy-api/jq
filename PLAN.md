# JSON Schema Symbolic Execution for JQ - Implementation Plan

## Current Status: Phase 1 - Foundation (COMPLETED ✅)

✅ Research completed
✅ Detailed plan approved
✅ Package structure created
✅ Core types implemented
✅ Basic schema operations working
✅ Tests passing (16/16)
⏳ **READY FOR PHASE 2** - Schema VM Implementation

---

## Overview
Extend the jq library to perform **symbolic execution** over JSON Schemas using the Speakeasy openapi library's JSON Schema types. The system will take a JSON Schema as input and compute the output JSON Schema after applying a jq transformation.

## Architecture

### Core Approach
- **Treat JSON Schema as an abstract value domain** for abstract interpretation of jq
- **Reuse existing parser, AST, and compiler** to generate the same bytecode
- **Create a parallel Schema VM** that executes the same bytecode but operates on schemas
- **Union-based semantics**: Multiple possible outputs become `anyOf` unions
- **Conservative approximation**: When precise analysis is impossible, widen to supertype

### Component Diagram
```
┌─────────────────────────────────────────────────────────────┐
│                    User API Layer                            │
│  query.RunSchema(schema) / code.ExecSchema(schema)          │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│              Schema Virtual Machine                          │
│  (execute_schema.go) - Executes bytecode on schemas         │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│           Schema Operations Library                          │
│  (schemaops.go) - Union, Intersect, GetProperty, etc.       │
└─────────────────────────────────────────────────────────────┘
                            ↓
┌─────────────────────────────────────────────────────────────┐
│          Normalization & Widening                            │
│  (schemanorm.go) - Simplify, bound anyOf, prevent explosion │
└─────────────────────────────────────────────────────────────┘
```

---

## Implementation Phases

### Phase 1: Foundation (Week 1) - **COMPLETED ✅**
**Status**: ✅ Complete (2025-10-07)

**Files Created**:
- ✅ `schemaexec/types.go` (52 lines)
- ✅ `schemaexec/api.go` (86 lines)
- ✅ `schemaexec/schemaops.go` (220 lines)
- ✅ `schemaexec/schemaops_test.go` (205 lines)
- ✅ `schemaexec/api_test.go` (126 lines)

**Completed Tasks**:
- [x] Set up `schemaexec` package structure
- [x] Add Speakeasy OpenAPI dependency to go.mod (v1.7.8)
- [x] Implement basic schema constructors (Top, Bottom, ConstString, ConstNumber, ConstBool, ConstNull)
- [x] Implement type constructors (StringType, NumberType, BoolType, NullType, ArrayType, ObjectType)
- [x] Implement `GetProperty` for object property access
- [x] Implement `Union` with basic flattening and widening
- [x] Add public API: `RunSchema` and `ExecSchema` (placeholder for Phase 2)
- [x] Write comprehensive tests (16 tests, all passing)
- [x] Implement `BuildObject` helper for test construction

**Key Deliverables**:
- ✅ Package compiles successfully
- ✅ All tests pass (16/16)
- ✅ Public API defined and documented
- ✅ Core types with sensible defaults
- ✅ Basic schema constructors working correctly
- ✅ Property access operation functional
- ✅ Union operation with widening support
- ✅ Test coverage for all basic operations

**Next**: Phase 2 - Implement Schema VM and bytecode execution

### Phase 2: Schema VM Core (Week 2)
**Status**: ⏳ Not Started

**Files**: `execute_schema.go`, `stack.go`

- [ ] Create `schemaEnv` and schema stack
- [ ] Implement VM loop with opcode dispatch
- [ ] Implement core opcodes:
  - `oppush` (constants)
  - `oppop`
  - `opindex` (property/array access)
  - `opconst`
- [ ] Add memoization infrastructure
- [ ] Write tests: `.foo`, `.foo.bar`, `.[0]`

### Phase 3: Complex Operations (Week 3)
**Status**: ⏳ Not Started

**Files**: Continue `execute_schema.go`, add `schemaops.go` helpers

- [ ] Implement `opiter` (array/object iteration)
- [ ] Implement `opobject` (object construction)
- [ ] Implement array operations
- [ ] Add `Intersect` operation
- [ ] Add `RequireType` and `HasProperty`
- [ ] Write tests: `.[]`, `{a:.x}`, `map`, iterations

### Phase 4: Normalization (Week 4)
**Status**: ⏳ Not Started

**Files**: `schemanorm.go`

- [ ] Implement `flattenAnyOf`
- [ ] Implement `deduplicateAnyOf`
- [ ] Implement widening heuristics
- [ ] Add enum simplification
- [ ] Add anyOf limit enforcement
- [ ] Write tests for explosion prevention

### Phase 5: Built-ins (Week 5-6)
**Status**: ⏳ Not Started

**Files**: `builtins.go`

Priority 1 (High-use builtins):
- [ ] `keys`, `values`, `length`, `type`, `has`
- [ ] `map`, `select`, `any`, `all`
- [ ] `add`, `min`, `max`
- [ ] Type conversions: `tonumber`, `tostring`

Priority 2 (Medium-use builtins):
- [ ] `to_entries`, `from_entries`, `with_entries`
- [ ] `group_by`, `unique`, `sort`
- [ ] String functions: `split`, `join`

### Phase 6: Advanced Features (Week 7)
**Status**: ⏳ Not Started

- [ ] Handle optional access (`.foo?`)
- [ ] Implement `try-catch` semantics
- [ ] Add `select` predicate narrowing
- [ ] Handle recursive schemas
- [ ] Add `$ref` resolution
- [ ] Performance optimization

### Phase 7: Testing & Documentation (Week 8)
**Status**: ⏳ Not Started

- [ ] Golden test suite (input schema + jq → expected output schema)
- [ ] Differential testing against concrete execution
- [ ] Benchmark suite
- [ ] Update README with examples
- [ ] Write API documentation
- [ ] Create example programs

---

## File Structure

### New Files to Create

#### Package: schemaexec/
- `types.go` - Core types and options (~200 LOC)
- `api.go` - Public API (~100 LOC)
- `schemaops.go` - Schema algebra (~1000 LOC)
- `schemanorm.go` - Normalization (~500 LOC)
- `execute_schema.go` - Schema VM (~1500 LOC)
- `builtins.go` - Built-in functions (~1000 LOC)
- `helpers.go` - Utility functions (~300 LOC)
- `stack.go` - Schema stack implementation (~100 LOC)

#### Test Files
- `schemaops_test.go`
- `execute_test.go`
- `builtins_test.go`
- `integration_test.go`
- `golden_test.go`
- `testdata/*.json` - Golden test cases

### Modified Files
- `go.mod` - Add dependency on `github.com/speakeasy-api/openapi`
- `query.go` - Add `RunSchema` method (optional, can use package-level func)

---

## Key Technical Decisions

### 1. Schema Representation
**Decision**: Use `github.com/speakeasy-api/openapi/jsonschema/oas3/core.Schema`

**Rationale**:
- Production-tested library
- Full JSON Schema support (properties, additionalProperties, anyOf, allOf, etc.)
- Already in Speakeasy ecosystem

**Key Fields**:
```go
type Schema struct {
    Type                 marshaller.Node[*values.EitherValue[[]marshaller.Node[string], string]]
    Properties           marshaller.Node[*sequencedmap.Map[string, JSONSchema]]
    AdditionalProperties marshaller.Node[JSONSchema]
    Items                marshaller.Node[JSONSchema]
    PrefixItems          marshaller.Node[[]JSONSchema]
    AnyOf                marshaller.Node[[]JSONSchema]
    AllOf                marshaller.Node[[]JSONSchema]
    OneOf                marshaller.Node[[]JSONSchema]
    Not                  marshaller.Node[JSONSchema]
    Enum                 marshaller.Node[[]marshaller.Node[values.Value]]
    Required             marshaller.Node[[]marshaller.Node[string]]
    // ... many more fields
}
```

### 2. Optional Access (`.foo?` vs `.foo`)
**Decision**: Distinguish in opcode or track in execution context

**Rationale**:
- `.foo` returns null if property missing
- `.foo?` returns empty (no output) if property missing
- Schema VM needs to handle differently: `.foo` → union with null, `.foo?` → no null

**Implementation**: Check if gojq bytecode distinguishes; if not, may need compiler modification

### 3. Bottom/Never Schema
**Decision**: Use `nil` pointer as convention for "never" schema

**Rationale**:
- Simpler than creating `false` boolean schemas
- Easy to check: `if schema == nil`
- Clear semantic meaning

### 4. Union Explosion Prevention
**Decision**: Three-level widening with configurable limits

**Levels**:
- Level 0 (none): Keep all precision
- Level 1 (conservative): Keep types, drop facets when limit exceeded
- Level 2 (aggressive): Collapse to Top when limit exceeded

**Limits**:
- `AnyOfLimit`: Max 10 branches (default)
- `EnumLimit`: Max 50 enum values (default)
- `MaxDepth`: Max 100 recursion depth (default)

### 5. Memoization Strategy
**Decision**: Key by (PC, schema-hash)

**Rationale**:
- Avoid re-computing same transformation on same schema
- PC (program counter) identifies location in bytecode
- Schema hash identifies input shape
- Hash only type-level structure, not deep content

### 6. Error Handling Modes
**Decision**: Support both strict and permissive modes

**Strict Mode** (`StrictMode: true`):
- Fail immediately on unsupported operations
- Best for validation and ensuring complete support

**Permissive Mode** (`StrictMode: false`, default):
- Widen to Top with warning on unsupported operations
- Best for gradual adoption and robustness

---

## Example Transformations

### Example 1: Simple Property Access
```
Input:  {type: "object", properties: {name: {type: "string"}}, required: ["name"]}
JQ:     .name
Output: {type: "string"}
```

### Example 2: Optional Property
```
Input:  {type: "object", properties: {age: {type: "number"}}}
JQ:     .age
Output: {anyOf: [{type: "number"}, {type: "null"}]}
```
(Property is not required, so it might not exist → null)

### Example 3: Array Map
```
Input:  {type: "array", items: {type: "number"}}
JQ:     map(. * 2)
Output: {type: "array", items: {type: "number"}}
```
(Arithmetic on numbers yields numbers)

### Example 4: Object Construction
```
Input:  {type: "object", properties: {x: {type: "string"}, y: {type: "number"}}}
JQ:     {name: .x, value: .y}
Output: {
  type: "object",
  properties: {
    name: {anyOf: [{type: "string"}, {type: "null"}]},
    value: {anyOf: [{type: "number"}, {type: "null"}]}
  },
  required: ["name", "value"]
}
```
(Object construction makes keys required, but values might be null if input properties not required)

### Example 5: Array Iteration
```
Input:  {type: "array", items: {type: "number"}}
JQ:     .[]
Output: {type: "number"}
```
(Iterating array yields item schema)

### Example 6: Keys Function
```
Input:  {type: "object", properties: {a: {type: "number"}, b: {type: "string"}}}
JQ:     keys
Output: {type: "array", items: {type: "string", enum: ["a", "b"]}}
```
(Known properties become string enum)

### Example 7: Union Input
```
Input:  {anyOf: [{type: "array", items: {type: "number"}}, {type: "object", properties: {x: {type: "string"}}}]}
JQ:     .[]
Output: {anyOf: [{type: "number"}, {type: "string"}]}
```
(Array branch yields numbers, object branch yields property values)

---

## Testing Strategy

### 1. Unit Tests
Test individual schema operations in isolation:
```go
func TestGetProperty(t *testing.T) {
    obj := BuildObject(map[string]*core.Schema{
        "name": ConstString("Alice"),
        "age": ConstNumber(30),
    }, []string{"name"}, nil)

    result := GetProperty(obj, "name", DefaultOptions())
    assert.Equal(t, "string", getType(result))

    result = GetProperty(obj, "age", DefaultOptions())
    // Should be number | null (age not required)
    assert.True(t, hasAnyOf(result))
}
```

### 2. Integration Tests
Test complete jq expressions end-to-end:
```go
func TestRunSchema_PropertyAccess(t *testing.T) {
    query, _ := gojq.Parse(".foo.bar")

    input := BuildObject(map[string]*core.Schema{
        "foo": BuildObject(map[string]*core.Schema{
            "bar": ConstString("value"),
        }, []string{"bar"}, nil),
    }, []string{"foo"}, nil)

    result, err := query.RunSchema(context.Background(), input)
    assert.NoError(t, err)
    assert.Equal(t, "string", getType(result.Schema))
}
```

### 3. Golden Tests
JSON files with input schema, jq, expected output:
```json
{
  "name": "array_map_select",
  "description": "Test map with select filter on array of objects",
  "input": {
    "type": "array",
    "items": {
      "type": "object",
      "properties": {
        "x": {"type": "number"}
      }
    }
  },
  "jq": "map(select(.x > 0))",
  "expected": {
    "type": "array",
    "items": {
      "type": "object",
      "properties": {
        "x": {"type": "number"}
      }
    }
  }
}
```

### 4. Differential Testing
Generate random JSON instances from schema, execute concretely, verify symbolic result is supertype:
```go
func TestDifferential(t *testing.T) {
    schema := randomSchema()
    query := randomJQQuery()

    // Generate 100 instances from schema
    instances := generateInstances(schema, 100)

    // Execute concretely
    concreteResults := make([]any, 0)
    for _, inst := range instances {
        result := query.Run(inst)
        concreteResults = append(concreteResults, result)
    }

    // Execute symbolically
    symbolicResult, _ := query.RunSchema(context.Background(), schema)

    // Verify all concrete results match symbolic schema
    for _, cr := range concreteResults {
        assert.True(t, validates(cr, symbolicResult.Schema))
    }
}
```

---

## Performance Considerations

### Memoization
- Cache schema transformations by (PC, schema-hash)
- Prevents redundant computation on repeated patterns
- Especially important for loops and recursive operations

### Widening
- Prevents combinatorial explosion in anyOf unions
- Configurable limits allow precision/performance trade-off
- Progressive widening: try precise first, widen if exceeded

### Schema Hashing
- Fast structural hashing for memoization keys
- Hash only type-level structure, not full content
- Collision-resistant but not cryptographic

### Normalization
- Simplify schemas after each operation
- Flatten nested anyOf/allOf
- Deduplicate branches
- Remove redundant constraints

---

## Open Questions & Future Work

### 1. Compiler Modifications
**Question**: Does gojq bytecode distinguish `.foo` vs `.foo?`?

**Investigation needed**:
- Examine opcode definitions
- Check if optional flag exists
- May need to modify compiler if not present

### 2. Recursive Schemas
**Question**: How to handle `$ref` and recursive schema definitions?

**Approaches**:
- Bound recursion depth and widen beyond limit
- Detect cycles and use fixpoint iteration
- Represent recursion symbolically (advanced)

### 3. Predicate Analysis
**Question**: How precisely can we narrow schemas in `select` predicates?

**Examples**:
- `select(.x == "foo")` → narrow `.x` to `{type: "string", enum: ["foo"]}`
- `select(.x > 0)` → narrow `.x` to `{type: "number", minimum: 0}` (exclusive)
- `select(type == "array")` → narrow to `{type: "array"}`

**Implementation**: Pattern matching on predicate AST

### 4. Performance Benchmarks
**Question**: What is the overhead vs concrete execution?

**Measurements needed**:
- Simple queries (`.foo.bar`)
- Complex queries (`map(select(...))`)
- Large schemas (100+ properties)
- Deep nesting (10+ levels)

---

## Dependencies

### Required
- `github.com/speakeasy-api/openapi` - JSON Schema types
- `github.com/itchyny/gojq` - Base jq implementation (already present)

### Testing
- Standard library `testing`
- `github.com/google/go-cmp` - Already in go.mod

---

## Estimated Effort

- **Total**: ~8 weeks for full implementation
- **MVP** (Phases 1-3): ~3 weeks
- **Production-ready** (through Phase 7): ~8 weeks

**Lines of Code**: ~5000-6000 LOC (excluding tests)

---

## Success Criteria

### MVP (Phase 3 Complete)
- ✅ Property access (`.foo`, `.foo.bar`)
- ✅ Array indexing (`.[0]`, `.[]`)
- ✅ Object construction (`{a:.x}`)
- ✅ Basic iteration
- ✅ Union handling
- ✅ Test coverage > 80%

### Production Ready (Phase 7 Complete)
- ✅ All common jq operations supported
- ✅ Comprehensive builtin library
- ✅ Normalization prevents explosion
- ✅ Performance acceptable (< 100x slowdown vs concrete)
- ✅ Golden test suite validates correctness
- ✅ Documentation complete
- ✅ Real-world examples work

---

## References

- [jq Manual](https://jqlang.github.io/jq/manual/)
- [JSON Schema Specification](https://json-schema.org/specification)
- [Speakeasy OpenAPI Library](https://github.com/speakeasy-api/openapi)
- [gojq Implementation](https://github.com/itchyny/gojq)
- [Abstract Interpretation](https://en.wikipedia.org/wiki/Abstract_interpretation)

---

**Last Updated**: 2025-10-07
**Current Phase**: Phase 1 - Foundation
**Next Milestone**: Complete basic schema operations and public API
