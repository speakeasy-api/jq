package schemaexec

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

// LogLevel represents the severity level for logs.
type LogLevel int

const (
	LevelError LogLevel = iota
	LevelWarn
	LevelInfo
	LevelDebug
)

func (l LogLevel) String() string {
	switch l {
	case LevelError:
		return "ERROR"
	case LevelWarn:
		return "WARN"
	case LevelInfo:
		return "INFO"
	case LevelDebug:
		return "DEBUG"
	default:
		return "UNKNOWN"
	}
}

// ParseLogLevel parses a string into a LogLevel.
func ParseLogLevel(s string) LogLevel {
	switch strings.ToUpper(s) {
	case "ERROR":
		return LevelError
	case "WARN", "WARNING":
		return LevelWarn
	case "INFO":
		return LevelInfo
	case "DEBUG":
		return LevelDebug
	default:
		return LevelWarn // default
	}
}

// Logger is the interface used by the executor for logging.
type Logger interface {
	// Debugf, Infof, Warnf, Errorf log formatted messages at respective levels.
	Debugf(format string, args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)

	// With returns a child logger augmented with the provided fields.
	With(fields map[string]any) Logger
}

// textFormatter emits compact single-line text logs.
// Format: [LEVEL] ts msg key1=val1 key2=val2 ...
type textFormatter struct {
	includeTimestamp bool
}

func newTextFormatter() *textFormatter {
	return &textFormatter{
		includeTimestamp: true,
	}
}

func (f *textFormatter) format(ts time.Time, level LogLevel, msg string, fields map[string]any) []byte {
	var b strings.Builder
	b.Grow(128)

	b.WriteByte('[')
	b.WriteString(level.String())
	b.WriteByte(']')
	b.WriteByte(' ')

	if f.includeTimestamp {
		b.WriteString(ts.UTC().Format(time.RFC3339Nano))
		b.WriteByte(' ')
	}

	// Message first for readability
	b.WriteString(msg)

	// Sort field keys for deterministic output
	if len(fields) > 0 {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteByte(' ')
			b.WriteString(k)
			b.WriteByte('=')
			b.WriteString(safeSprint(fields[k]))
		}
	}

	b.WriteByte('\n')
	return []byte(b.String())
}

func safeSprint(v any) string {
	switch t := v.(type) {
	case string:
		// Quote if contains whitespace
		if strings.IndexFunc(t, func(r rune) bool { return r <= ' ' }) >= 0 {
			return fmt.Sprintf("%q", t)
		}
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprint(v)
	}
}

// defaultLogger is a thread-safe logger implementation supporting With() context.
type defaultLogger struct {
	out       io.Writer
	level     LogLevel
	formatter *textFormatter

	// baseFields are the context fields attached to this logger.
	baseFields map[string]any

	// mu serializes writes to the writer and protects baseFields during write.
	mu *sync.Mutex
}

// NewLogger creates a default logger with the given level.
// If w is nil, os.Stderr is used.
func NewLogger(level LogLevel, w io.Writer) Logger {
	if w == nil {
		w = os.Stderr
	}
	return &defaultLogger{
		out:        w,
		level:      level,
		formatter:  newTextFormatter(),
		baseFields: make(map[string]any),
		mu:         &sync.Mutex{},
	}
}

// noopLogger is a logger that discards all output.
type noopLogger struct{}

func (l *noopLogger) IsEnabled(level LogLevel) bool     { return false }
func (l *noopLogger) Debugf(format string, args ...any) {}
func (l *noopLogger) Infof(format string, args ...any)  {}
func (l *noopLogger) Warnf(format string, args ...any)  {}
func (l *noopLogger) Errorf(format string, args ...any) {}
func (l *noopLogger) With(fields map[string]any) Logger { return l }

// newNoopLogger returns a logger that discards all output.
func newNoopLogger() Logger {
	return &noopLogger{}
}

func (l *defaultLogger) IsEnabled(level LogLevel) bool {
	return level <= l.level
}

func (l *defaultLogger) With(fields map[string]any) Logger {
	if len(fields) == 0 {
		return l
	}
	// Shallow copy of base fields to avoid parent mutation
	newFields := make(map[string]any, len(l.baseFields)+len(fields))
	for k, v := range l.baseFields {
		newFields[k] = v
	}
	for k, v := range fields {
		newFields[k] = v
	}
	return &defaultLogger{
		out:        l.out,
		level:      l.level,
		formatter:  l.formatter,
		baseFields: newFields,
		mu:         l.mu, // share same lock and writer
	}
}

func (l *defaultLogger) Debugf(format string, args ...any) {
	l.logf(LevelDebug, format, args...)
}

func (l *defaultLogger) Infof(format string, args ...any) {
	l.logf(LevelInfo, format, args...)
}

func (l *defaultLogger) Warnf(format string, args ...any) {
	l.logf(LevelWarn, format, args...)
}

func (l *defaultLogger) Errorf(format string, args ...any) {
	l.logf(LevelError, format, args...)
}

func (l *defaultLogger) logf(level LogLevel, format string, args ...any) {
	if !l.IsEnabled(level) {
		return
	}
	// Format message only when enabled
	msg := fmt.Sprintf(format, args...)

	// Snapshot fields to avoid mutation races by callers
	fields := make(map[string]any, len(l.baseFields))
	for k, v := range l.baseFields {
		fields[k] = v
	}

	ts := time.Now()
	line := l.formatter.format(ts, level, msg, fields)

	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.out.Write(line)
}

// ----------------------------------------------------------------------------
// Helpers: schema summaries and deltas, truncation
// ----------------------------------------------------------------------------

// SchemaLogOptions control verbosity for schema summaries/deltas.
type SchemaLogOptions struct {
	LogMaxEnumValues     int // default 5
	LogMaxProps          int // default 5
	LogMaxAnyOfBranches  int // default 5
	LogStackPreviewDepth int // default 3 (not used here, but kept for parity)
}

// schemaTypeSummary returns a compact one-line representation of a schema's shape.
// It avoids heavy traversal and truncates collections to keep output small.
func schemaTypeSummary(s *oas3.Schema, maxDepth int) string {
	if s == nil {
		return "Bottom"
	}
	typ := getType(s)
	if typ == "" {
		// Unknown/Top-like
		// Try to hint at structure
		if len(s.AnyOf) > 0 {
			return fmt.Sprintf("anyOf(%d)", len(s.AnyOf))
		}
		if s.Properties != nil {
			count := 0
			for range s.Properties.All() {
				count++
			}
			return fmt.Sprintf("object{~%d props}", count)
		}
		if s.Items != nil || s.PrefixItems != nil {
			return "array"
		}
		return "Top"
	}

	switch typ {
	case "string", "number", "integer", "boolean", "null":
		if len(s.Enum) > 0 {
			values := previewEnumStrings(s.Enum, 5)
			return fmt.Sprintf("%s(enum:%s)", typ, values)
		}
		if s.Format != nil && *s.Format != "" && typ == "string" {
			return fmt.Sprintf("string(%s)", *s.Format)
		}
		return typ

	case "object":
		props := previewPropertyKeys(s, 5)
		if props == "" {
			return "object"
		}
		return "object{" + props + "}"

	case "array":
		// tuple?
		if len(s.PrefixItems) > 0 {
			if maxDepth <= 0 {
				return fmt.Sprintf("tuple[len=%d]", len(s.PrefixItems))
			}
			// summarize first element and count
			head := s.PrefixItems[0]
			headSum := "Top"
			if head != nil && head.Left != nil {
				headSum = schemaTypeSummary(head.Left, maxDepth-1)
			}
			return fmt.Sprintf("tuple[len=%d, head=%s]", len(s.PrefixItems), headSum)
		}
		// homogenous items
		if s.Items != nil && s.Items.Left != nil {
			if maxDepth <= 0 {
				return "array[...]"
			}
			return "array[" + schemaTypeSummary(s.Items.Left, maxDepth-1) + "]"
		}
		return "array"

	default:
		// anyOf etc
		if len(s.AnyOf) > 0 {
			branches := make([]string, 0, min(3, len(s.AnyOf)))
			limit := min(3, len(s.AnyOf))
			for i := 0; i < limit; i++ {
				if s.AnyOf[i] != nil && s.AnyOf[i].Left != nil {
					branches = append(branches, schemaTypeSummary(s.AnyOf[i].Left, max(maxDepth-1, 0)))
				}
			}
			extra := ""
			if len(s.AnyOf) > limit {
				extra = fmt.Sprintf("+%d", len(s.AnyOf)-limit)
			}
			if extra != "" {
				return "anyOf(" + strings.Join(branches, "|") + "," + extra + ")"
			}
			return "anyOf(" + strings.Join(branches, "|") + ")"
		}
		return typ
	}
}

// schemaDelta returns a compact delta from before -> after, focusing on type,
// object props presence, array item shape, and anyOf size. It is intentionally
// shallow to keep logs readable and low-cost.
// truncateList joins items with "," and appends +N if truncated.
func truncateList(items []string, max int) string {
	if max <= 0 || len(items) <= max {
		return strings.Join(items, ",")
	}
	head := items[:max]
	return strings.Join(head, ",") + fmt.Sprintf(",+%d", len(items)-max)
}

// Internal helpers ------------------------------------------------------------

func previewPropertyKeys(s *oas3.Schema, limit int) string {
	if s == nil || s.Properties == nil {
		return ""
	}
	keys := make([]string, 0)
	for k := range s.Properties.All() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return truncateList(keys, limit)
}

func previewEnumStrings(enum []*yaml.Node, limit int) string {
	if len(enum) == 0 {
		return ""
	}
	vals := make([]string, 0, min(limit, len(enum)))
	l := min(limit, len(enum))
	for i := 0; i < l; i++ {
		vals = append(vals, prettyYAMLScalar(enum[i]))
	}
	if len(enum) > limit {
		return fmt.Sprintf("(%s,+%d)", strings.Join(vals, ","), len(enum)-limit)
	}
	return "(" + strings.Join(vals, ",") + ")"
}

func prettyYAMLScalar(n *yaml.Node) string {
	if n == nil {
		return "null"
	}
	switch n.Kind {
	case yaml.ScalarNode:
		// Try to preserve basic scalar types
		switch n.Tag {
		case "!!str":
			// Quote strings containing spaces or punctuation
			if needsQuote(n.Value) {
				return fmt.Sprintf("%q", n.Value)
			}
			return n.Value
		case "!!int", "!!float", "!!bool", "!!null":
			return n.Value
		default:
			// Fallback to canonical format from multistate helpers if available
			return canonicalizeYAMLNode(n)
		}
	case yaml.SequenceNode, yaml.MappingNode:
		return canonicalizeYAMLNode(n)
	default:
		return canonicalizeYAMLNode(n)
	}
}

func needsQuote(s string) bool {
	for _, r := range s {
		if r <= ' ' || r == ',' || r == '"' || r == '\'' || r == '\\' {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
