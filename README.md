# jq - Symbolic Execution Engine for JQ

A Go library for symbolic execution of [jq](https://github.com/jqlang/jq) expressions, built on top of [gojq](https://github.com/itchyny/gojq). This library helps you write type-safe transformers from JSON that matches a given JSON Schema to code in multiple languages.

## Purpose

This library extends gojq to provide symbolic execution capabilities, enabling:

- **Type-safe transformations**: Validate JQ expressions against JSON Schemas to ensure type safety before execution
- **Code generation**: Generate type-safe transformer code in multiple target languages
- **Static analysis**: Analyze JQ expressions to understand input/output type relationships
- **Schema validation**: Verify that JQ transformations preserve type contracts

## Installation

```sh
go get github.com/speakeasy-api/jq
```

## Usage as a library

```go
package main

import (
	"fmt"
	"log"

	"github.com/speakeasy-api/jq"
)

func main() {
	query, err := jq.Parse(".foo | ..")
	if err != nil {
		log.Fatalln(err)
	}
	input := map[string]any{"foo": []any{1, 2, 3}}
	iter := query.Run(input)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			log.Fatalln(err)
		}
		fmt.Printf("%#v\n", v)
	}
}
```

## Built on gojq

This library builds on the excellent [gojq](https://github.com/itchyny/gojq) implementation:

- Pure Go implementation of jq
- Arbitrary-precision integer calculation
- Better error messages
- YAML input/output support
- Fully portable with no C dependencies

See the [gojq documentation](https://github.com/itchyny/gojq) for more details on the underlying jq implementation.

## License

This software is released under the MIT License, see LICENSE.
