package schemaexec

import (
	"bytes"
	"context"
	"strings"
	"testing"

	gojq "github.com/speakeasy-api/jq"
	"github.com/speakeasy-api/openapi/jsonschema/oas3"
)

// TestDebugLogging demonstrates the debug tracing functionality
func TestDebugLogging(t *testing.T) {
	// Create a simple object schema
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"name":  ConstString("John"),
		"age":   ConstInteger(30),
		"city":  ConstString("NYC"),
		"score": ConstInteger(100),
	}, []string{"name", "age", "city", "score"})

	// Parse a jq query that does some interesting operations
	query, err := gojq.Parse(".name as $n | {name: $n, age: .age, doubled: (.score * 2)}")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	// Test with DEBUG level logging
	t.Run("debug_logging", func(t *testing.T) {
		var logBuf bytes.Buffer

		opts := DefaultOptions()
		opts.LogLevel = "debug"

		// Create logger manually to capture output
		logger := NewLogger(LevelDebug, &logBuf)

		// Create env with logger
		env := newSchemaEnv(context.Background(), opts)
		env.logger = logger

		// Compile and execute
		code, err := gojq.Compile(query)
		if err != nil {
			t.Fatalf("Failed to compile query: %v", err)
		}

		result, err := env.execute(code, inputSchema)
		if err != nil {
			t.Fatalf("Execution failed: %v", err)
		}

		if result.Schema == nil {
			t.Fatal("Expected result schema")
		}

		// Check that logs were generated
		logs := logBuf.String()
		if logs == "" {
			t.Fatal("Expected debug logs to be generated")
		}

		// Verify some key log entries
		if !strings.Contains(logs, "Starting symbolic execution") {
			t.Error("Expected 'Starting symbolic execution' log")
		}
		if !strings.Contains(logs, "Executing") {
			t.Error("Expected 'Executing' logs for opcodes")
		}
		if !strings.Contains(logs, "Terminal state reached") {
			t.Error("Expected 'Terminal state reached' log")
		}
		if !strings.Contains(logs, "Execution completed") {
			t.Error("Expected 'Execution completed' log")
		}

		// Print sample logs for demonstration
		t.Log("Sample debug logs (first 20 lines):")
		lines := strings.Split(logs, "\n")
		for i := 0; i < min(20, len(lines)); i++ {
			if lines[i] != "" {
				t.Log(lines[i])
			}
		}
	})

	// Test with WARN level (should only show warnings, not debug)
	t.Run("warn_level", func(t *testing.T) {
		var logBuf bytes.Buffer

		opts := DefaultOptions()
		opts.LogLevel = "warn"

		logger := NewLogger(LevelWarn, &logBuf)

		env := newSchemaEnv(context.Background(), opts)
		env.logger = logger

		code, err := gojq.Compile(query)
		if err != nil {
			t.Fatalf("Failed to compile query: %v", err)
		}

		_, err = env.execute(code, inputSchema)
		if err != nil {
			t.Fatalf("Execution failed: %v", err)
		}

		logs := logBuf.String()

		// Should NOT contain DEBUG logs
		if strings.Contains(logs, "[DEBUG]") {
			t.Error("Expected no DEBUG logs with WARN level")
		}

		// Should NOT contain INFO logs
		if strings.Contains(logs, "[INFO]") {
			t.Error("Expected no INFO logs with WARN level")
		}

		// May contain WARN logs if any warnings occurred
		t.Logf("WARN level logs: %d bytes", len(logs))
	})
}

// TestDebugLogForkTracing tests that fork lineage is properly tracked
func TestDebugLogForkTracing(t *testing.T) {
	var logBuf bytes.Buffer

	// Create schema
	inputSchema := BuildObject(map[string]*oas3.Schema{
		"x": ConstInteger(1),
		"y": ConstInteger(2),
	}, []string{"x", "y"})

	// Query with conditional that creates forks
	query, err := gojq.Parse("if .x > 0 then .y else .x end")
	if err != nil {
		t.Fatalf("Failed to parse query: %v", err)
	}

	opts := DefaultOptions()
	opts.LogLevel = "debug"

	logger := NewLogger(LevelDebug, &logBuf)

	env := newSchemaEnv(context.Background(), opts)
	env.logger = logger

	code, err := gojq.Compile(query)
	if err != nil {
		t.Fatalf("Failed to compile query: %v", err)
	}

	result, err := env.execute(code, inputSchema)
	if err != nil {
		t.Fatalf("Execution failed: %v", err)
	}

	if result.Schema == nil {
		t.Fatal("Expected result schema")
	}

	logs := logBuf.String()

	// Check for fork-related logs
	if !strings.Contains(logs, "lineage=") {
		t.Error("Expected lineage tracking in logs")
	}

	// Print logs showing fork tracing
	t.Log("Fork tracing logs:")
	lines := strings.Split(logs, "\n")
	for _, line := range lines {
		if strings.Contains(line, "lineage=") && line != "" {
			t.Log(line)
		}
	}
}
