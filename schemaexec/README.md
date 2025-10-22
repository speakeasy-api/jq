# schemaexec — JSON Schema Symbolic Execution for jq

Production-ready symbolic executor for jq over OpenAPI/JSON Schema. Infer the output schema from an input schema and a jq program. KLEE-style execution with allocation-site abstraction. Used in production as part of Speakeasy's Transformation Extensions.

## Install

```sh
go get github.com/speakeasy-api/jq
```

## Quick start (symbolic)

```go
import (
  "context"
  "github.com/speakeasy-api/jq"
  "github.com/speakeasy-api/jq/schemaexec"
)

input := schemaexec.BuildObject(map[string]*oas3.Schema{
  "items": schemaexec.ArrayType(schemaexec.ObjectType()),
}, []string{"items"})
q, _ := jq.Parse(".items | map({id, total: (.price * .qty)})")
out, _ := schemaexec.RunSchema(context.Background(), q, input)
// out.Schema is the inferred output schema
```

## Quick examples

- Map: `.items | map({id, total: (.price * .qty)})` → `array<{id: string, total: number}>`
- Select with narrowing: `.items | map(select(.qty > 0) | {id})` → `array<{id: string}>`
- Conditionals to enum: `if .score >= 90 then "gold" else "silver"` → `enum["gold","silver"]`

## Symbolic capabilities

- Property access and navigation: `.foo`, `.[]`, `.[0]`
- Object build/merge: `{a: .x}`, `{a} + {b: 1}`
- Array ops: `map`, `select`, `any`, `all`, `reduce`, `foreach`
- Conditionals and error handling: `if/then/else`, `try/catch`, alternative `//`
- Arithmetic and comparisons with type propagation
- Variables and closures: `as $x | ...`
- Schema inference: required tracking, anyOf unions, enum synthesis
- OpenAPI aware: designed for `x-speakeasy-transform-from-api` workflows

## Why this exists

Use jq to describe JSON transformations while preserving type information. schemaexec symbolically evaluates jq to infer precise output schemas, keeping downstream codegen and tooling type-safe.

## Architecture (brief)

- KLEE-style multi-state execution with path joining
- Allocation-site abstraction for arrays/objects; lattice-based union
- Termination guards and memoization for fast, safe execution

## Testing

Run: `go test ./schemaexec/...`

## Thanks

 - Cristian Cadar / KLEE authors. Thanks for the research paving the golden path towards symbolic executors and teaching it to your undergrad students!
 - itchyny. Thanks for the base project!