//go:build js && wasm

package main

import (
	"encoding/json"
	"fmt"
	"syscall/js"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/jq/pkg/jqfmt"
	"github.com/speakeasy-api/jq/pkg/playground"
)

// ExecuteJQ runs a JQ query against JSON input and returns the result
func ExecuteJQ(query, jsonInput string) (string, error) {
	// Parse the JQ query
	q, err := gojq.Parse(query)
	if err != nil {
		return "", fmt.Errorf("failed to parse jq query: %w", err)
	}

	// Parse the JSON input
	var input any
	if err := json.Unmarshal([]byte(jsonInput), &input); err != nil {
		return "", fmt.Errorf("failed to parse JSON input: %w", err)
	}

	// Compile the query
	code, err := gojq.Compile(q)
	if err != nil {
		return "", fmt.Errorf("failed to compile jq query: %w", err)
	}

	// Execute the query
	iter := code.Run(input)
	var results []any
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			return "", fmt.Errorf("execution error: %w", err)
		}
		results = append(results, v)
	}

	// If there's only one result, return it directly
	// Otherwise return an array of results
	var output any
	if len(results) == 1 {
		output = results[0]
	} else {
		output = results
	}

	// Marshal the result back to JSON
	outBytes, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %w", err)
	}

	return string(outBytes), nil
}

// Note: SymbolicExecuteJQ and transformSchema are now in transform.go (shared between WASM and tests)

// FormatJQ formats a JQ query using jqfmt
func FormatJQ(query string) (string, error) {
	cfg := jqfmt.JqFmtCfg{
		Obj: true,
		Arr: true,
		Ops: []string{"pipe"},
	}

	formatted, err := jqfmt.DoThing(query, cfg)
	if err != nil {
		return "", fmt.Errorf("failed to format jq query: %w", err)
	}

	return formatted, nil
}

// promisify wraps a Go function to return a JavaScript Promise
func promisify(fn func(args []js.Value) (string, error)) js.Func {
	return js.FuncOf(func(this js.Value, args []js.Value) any {
		// Handler for the Promise
		handler := js.FuncOf(func(this js.Value, promiseArgs []js.Value) interface{} {
			resolve := promiseArgs[0]
			reject := promiseArgs[1]

			// Run this code asynchronously
			go func() {
				result, err := fn(args)
				if err != nil {
					errorConstructor := js.Global().Get("Error")
					errorObject := errorConstructor.New(err.Error())
					reject.Invoke(errorObject)
					return
				}

				resolve.Invoke(result)
			}()

			// The handler of a Promise doesn't return any value
			return nil
		})

		// Create and return the Promise object
		promiseConstructor := js.Global().Get("Promise")
		return promiseConstructor.New(handler)
	})
}

func main() {
	// Register ExecuteJQ function
	js.Global().Set("ExecuteJQ", promisify(func(args []js.Value) (string, error) {
		if len(args) != 2 {
			return "", fmt.Errorf("ExecuteJQ: expected 2 args (query, jsonInput), got %v", len(args))
		}

		return ExecuteJQ(args[0].String(), args[1].String())
	}))

	// Register SymbolicExecuteJQ function
	js.Global().Set("SymbolicExecuteJQ", promisify(func(args []js.Value) (string, error) {
		if len(args) != 1 {
			return "", fmt.Errorf("SymbolicExecuteJQ: expected 1 arg (oasYAML), got %v", len(args))
		}

		return playground.SymbolicExecuteJQ(args[0].String())
	}))

	// Register FormatJQ function
	js.Global().Set("FormatJQ", promisify(func(args []js.Value) (string, error) {
		if len(args) != 1 {
			return "", fmt.Errorf("FormatJQ: expected 1 arg (query), got %v", len(args))
		}

		return FormatJQ(args[0].String())
	}))

	// Register SymbolicExecuteJQPipeline function
	js.Global().Set("SymbolicExecuteJQPipeline", promisify(func(args []js.Value) (string, error) {
		if len(args) != 1 {
			return "", fmt.Errorf("SymbolicExecuteJQPipeline: expected 1 arg (oasYAML), got %v", len(args))
		}

		result, err := playground.SymbolicExecuteJQPipeline(args[0].String())
		if err != nil {
			return "", err
		}

		// Serialize the result to JSON
		jsonBytes, err := json.Marshal(result)
		if err != nil {
			return "", fmt.Errorf("failed to marshal pipeline result: %w", err)
		}

		return string(jsonBytes), nil
	}))

	// Keep the program running
	<-make(chan bool)
}
