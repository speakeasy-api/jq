# schemaexec — JSON Schema Symbolic Execution for jq

Production-ready symbolic executor for jq over JSON Schema. Infers output OpenAPI schemas from jq programs. KLEE-style architecture with allocation-site abstraction. **75/75 tests passing (100%)**.

## Install

```sh
go get github.com/speakeasy-api/jq
```

## Quick Start

```go
package main

import (
  "context"
  "fmt"
  "log"

  "github.com/speakeasy-api/jq"
  "github.com/speakeasy-api/jq/schemaexec"
  "github.com/speakeasy-api/openapi/jsonschema/oas3"
)

func main() {
  // Parse a jq program
  q, err := jq.Parse(".items | map({id, total: (.price * .qty)})")
  if err != nil { log.Fatal(err) }

  // Build an input schema
  input := schemaexec.BuildObject(map[string]*oas3.Schema{
    "items": schemaexec.ArrayType(schemaexec.BuildObject(map[string]*oas3.Schema{
      "id":    schemaexec.StringType(),
      "price": schemaexec.NumberType(),
      "qty":   schemaexec.NumberType(),
    }, []string{"id", "price", "qty"})),
  }, []string{"items"})

  // Run symbolic execution
  res, err := schemaexec.RunSchema(context.Background(), q, input)
  if err != nil { log.Fatal(err) }

  fmt.Printf("Output schema type: %v\n", res.Schema.GetType())
  for _, w := range res.Warnings {
    fmt.Println("warning:", w)
  }
}
```

## Key Capabilities

**Full jq Symbolic Execution:**
- Property access and navigation: `.foo`, `.[]`, `.[0]`
- Object construction and merge: `{name: .x}`, `{a} + {b: 1}`
- Array operations: `map(f)`, `select(pred)`, `any`, `all`
- Conditionals: `if/then/else`, `try/catch`, alternative (`//`)
- Arithmetic and comparisons: `+`, `-`, `*`, `/`, `==`, `>`, `<=`
- Variables and closures: `as $x | ...`, scoped bindings
- Reductions: `reduce`, `foreach`

**Schema Inference:**
- Type propagation through transformations
- Enum synthesis from branches (`if x then "a" else "b"` → enum)
- anyOf unions with configurable widening
- Required properties tracking
- Array item schemas with proper merging

**Production Features:**
- Designed for `x-speakeasy-transform-from-api` workflows
- Keeps codegen type-safe
- Comprehensive termination safeguards
- Fast execution (~0.4s for full test suite)

## Examples

**Map (transform collections):**
```jq
.items | map({id, total: (.price * .qty)})
```
Result: `array<{id: string, total: number}>`

**Select (filtering with type narrowing):**
```jq
.items | map(select(.qty > 0) | {id})
```
Result: `array<{id: string}>`

**Nested transformations:**
```jq
{
  grandTotal: (.items | map(.price * .qty) | add),
  items: (.items | map({sku, total: (.price * .qty)}))
}
```
Result: `{grandTotal: number, items: array<{sku: string, total: number}>}`

## API

### Core Functions

**`RunSchema(ctx, query, input, opts...) (*SchemaExecResult, error)`**
- Execute a jq query symbolically on an input schema

**`ExecSchema(ctx, code, input, opts) (*SchemaExecResult, error)`**
- Execute compiled jq bytecode

### Schema Constructors

- `Top()` — Any value
- `Bottom()` — No value (empty/impossible)
- `ConstString(s)`, `ConstNumber(n)`, `ConstBool(b)`, `ConstNull()` — Literals
- `StringType()`, `NumberType()`, `BoolType()`, `NullType()` — Basic types
- `ArrayType(items)` — Array with item schema
- `ObjectType()` — Basic object
- `BuildObject(props, required)` — Object with properties

### Schema Operations

- `GetProperty(obj, key, opts)` — Extract property from object
- `Union(schemas, opts)` — Merge schemas (lattice join)

### Configuration

**`SchemaExecOptions`:**
- `AnyOfLimit` (default: 10) — Max anyOf branches before widening
- `EnumLimit` (default: 50) — Max enum values before widening
- `MaxDepth` (default: 100) — Max recursion depth
- `StrictMode` (default: false) — Fail on unsupported ops vs widen to Top
- `EnableWarnings` (default: true) — Collect diagnostic warnings
- `EnableMemo` (default: true) — State deduplication
- `WideningLevel` (0-2, default: 1) — Precision vs performance trade-off

## Architecture

**KLEE-Style Symbolic Execution:**
- Multi-state execution with backtracking via worklist
- Path exploration and join at merge points
- Allocation-site abstraction for heap objects

**Key Components:**
- **Monotonic allocation IDs** — Unique identity per array construction site
- **Schema pointer tagging** — Track array references across execution paths
- **Shared accumulators** — Arrays built incrementally with in-place mutation
- **Lattice-based merging** — Union as least upper bound (LUB) operation
- **Materialization pass** — Resolve array references after path merging
- **Recursive array/object merging** — Preserve nested properties through Union

**Correctness Guarantees:**
- Sound type inference (all possible outputs covered)
- Termination guarantees (iteration limits, depth bounds, timeouts)
- Proper closure semantics with call stack
- No infinite loops (100K iteration limit)

## Testing

**75/75 tests passing (100%)**

Run tests:
```bash
go test ./schemaexec/...
```

Verbose output:
```bash
go test ./schemaexec/... -v
```

Test coverage includes:
- Basic types and constructors
- Property access and navigation
- Map/select/filter operations
- Object construction with shorthand syntax
- Nested transformations (ProductInput, CartInput, UserInput)
- Edge cases (select(false), try/catch, alternative operator)
- Arithmetic and comparison operations
- Closure and variable scoping
- Array accumulation across backtracking

## License

MIT License (same as parent jq project)
