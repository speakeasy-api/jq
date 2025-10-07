# schemaexec - JSON Schema Symbolic Execution

This package provides symbolic execution of jq expressions over JSON Schemas.

## Status: Phase 1 Complete ✅

**Current capabilities:**
- ✅ Basic schema type constructors
- ✅ Property access operations
- ✅ Union operations with widening
- ✅ Public API defined
- ✅ Comprehensive test coverage

**Not yet implemented (Phase 2+):**
- ⏳ Full bytecode execution (Schema VM)
- ⏳ Complete opcode support
- ⏳ Built-in function transformations
- ⏳ Normalization and optimization

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/itchyny/gojq"
    "github.com/itchyny/gojq/schemaexec"
)

func main() {
    // Parse a jq query
    query, err := gojq.Parse(".foo.bar")
    if err != nil {
        log.Fatal(err)
    }

    // Create an input schema
    inputSchema := schemaexec.BuildObject(map[string]*oas3.Schema{
        "foo": schemaexec.BuildObject(map[string]*oas3.Schema{
            "bar": schemaexec.StringType(),
        }, []string{"bar"}),
    }, []string{"foo"})

    // Run symbolic execution
    result, err := schemaexec.RunSchema(context.Background(), query, inputSchema)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Output schema type: %v\n", result.Schema.GetType())
    fmt.Printf("Warnings: %v\n", result.Warnings)
}
```

## API Reference

### Types

#### `SchemaExecOptions`
Configuration for symbolic execution:
- `AnyOfLimit` (int): Max anyOf branches before widening (default: 10)
- `EnumLimit` (int): Max enum values before widening (default: 50)
- `MaxDepth` (int): Max recursion depth (default: 100)
- `StrictMode` (bool): Fail on unsupported ops vs widen to Top (default: false)
- `EnableWarnings` (bool): Collect precision-loss warnings (default: true)
- `EnableMemo` (bool): Enable memoization (default: true)
- `WideningLevel` (int): 0=none, 1=conservative, 2=aggressive (default: 1)

#### `SchemaExecResult`
Result of symbolic execution:
- `Schema` (*oas3.Schema): The output schema
- `Warnings` ([]string): Diagnostic warnings

### Functions

#### `RunSchema(ctx, query, input, opts...) (*SchemaExecResult, error)`
Execute a jq query symbolically on an input schema.

#### `ExecSchema(ctx, code, input, opts) (*SchemaExecResult, error)`
Execute compiled jq bytecode symbolically.

### Schema Constructors

- `Top()` - Schema matching any value
- `Bottom()` - Schema matching nothing (nil)
- `ConstString(s)` - String literal schema
- `ConstNumber(n)` - Number literal schema
- `ConstBool(b)` - Boolean literal schema
- `ConstNull()` - Null schema
- `StringType()` - Basic string schema
- `NumberType()` - Basic number schema
- `BoolType()` - Basic boolean schema
- `NullType()` - Null type schema
- `ArrayType(items)` - Array schema with items
- `ObjectType()` - Basic object schema
- `BuildObject(props, required)` - Object with properties

### Schema Operations

- `GetProperty(obj, key, opts)` - Extract property schema from object
- `Union(schemas, opts)` - Create anyOf union of schemas

## Testing

Run tests:
```bash
go test ./schemaexec/...
```

Run with verbose output:
```bash
go test ./schemaexec/... -v
```

Current test coverage: **16 tests, all passing**

## Implementation Roadmap

See [../PLAN.md](../PLAN.md) for the full 7-phase implementation plan.

**Completed:** Phase 1
**Current:** Ready for Phase 2 - Schema VM
**Remaining:** Phases 2-7 (~7 weeks estimated)

## License

MIT License (same as parent gojq project)
