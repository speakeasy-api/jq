# jq — Symbolic Execution for jq

Run jq on schemas, not just data. Infer the output JSON Schema from an input schema and a jq expression. Built on gojq. Used in production to keep transformations type-safe.

## Install

```sh
go get github.com/speakeasy-api/jq
```

## Playground

Run `make build-wasm && cd web && npm install && npm run dev`, then visit http://localhost:5174/. Execute: run jq on JSON data. Symbolic: transform OpenAPI schemas with `x-speakeasy-transform-from-api: 'jq {id, total: (.price * .qty)}'`.

## Quick example (symbolic)

```go
import (
  "context"
  "github.com/speakeasy-api/jq"
  "github.com/speakeasy-api/jq/schemaexec"
)

input := schemaexec.BuildObject(map[string]*oas3.Schema{
  "items": schemaexec.ArrayType(schemaexec.ObjectType()),
}, []string{"items"})
q, _ := jq.Parse(".items[] | {name: .product}")
out, _ := schemaexec.RunSchema(context.Background(), q, input)
// out.Schema is the inferred output schema
```

## Quick example (data)

```go
package main

import (
  "fmt"
  "log"
  "github.com/speakeasy-api/jq"
)

func main() {
  q, err := jq.Parse(`.foo | ..`)
  if err != nil { log.Fatalln(err) }
  input := map[string]any{"foo": []any{1, 2, 3}}
  iter := q.Run(input)
  for {
    v, ok := iter.Next(); if !ok { break }
    if err, ok := v.(error); ok { log.Fatalln(err) }
    fmt.Printf("%#v\n", v)
  }
}
```

## Symbolic Execution Capabilities

- Schema inference for common jq constructs: property access (`.foo`, `.foo.bar`), arrays (`.[0]`, `.[]`), object construction (`{name: .x}`), type narrowing and constraints
- Conditionals: `if .score >= 90 then "gold" else "silver"` → `enum: ["gold", "silver"]`
- Object merging: reconcile compatible branches into unions
- OpenAPI aware: designed for `x-speakeasy-transform-from-api` workflows

## Why this exists

Use jq to describe JSON transformations while preserving type information. Infers output schemas to keep downstream codegen and tooling type-safe. Powers Speakeasy's SDK generation.

## Built on gojq

Fork of [itchyny/gojq](https://github.com/itchyny/gojq) (pure Go jq). Adds bytecode introspection (`GetCodes`) for symbolic execution. No C dependencies. YAML support.
