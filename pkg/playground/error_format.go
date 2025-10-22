// Package playground provides utilities for executing jq transformations on OpenAPI specifications.
package playground

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	atJSONPathRe = regexp.MustCompile(`\bat\s+(\$[^\s]+)`)
	bracketsRe   = regexp.MustCompile(`\[(.*?)\]`)
	hexPtrRe     = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	tokenRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)
)

// FormatTransformErrors turns low-level executor warnings into a user-facing message.
func FormatTransformErrors(warnings []string) string {
	if len(warnings) == 0 {
		return "Transformation failed, but no additional details were provided."
	}

	var b strings.Builder
	b.WriteString("Transformation failed (strict mode).\n")

	for _, w := range warnings {
		loc := deriveLocation(w)
		msg, hint := classifyAndHint(w)
		details := extractDetails(w)

		fmt.Fprintf(&b, "- %s\n", msg)
		if loc != "" {
			fmt.Fprintf(&b, "  Location: %s\n", loc)
		}
		if hint != "" {
			fmt.Fprintf(&b, "  How to fix: %s\n", hint)
		}
		if details != "" {
			fmt.Fprintf(&b, "  Details: %s\n", details)
		}
	}

	return b.String()
}

func deriveLocation(s string) string {
	// Prefer explicit JSONPath if present.
	if m := atJSONPathRe.FindStringSubmatch(s); len(m) == 2 {
		// Example: "$", "$.components.schemas" etc.
		return m[1]
	}

	// Otherwise, try to turn the bracketed prefix into a dot-path.
	// Example prefix: "[{0x.. components <nil> <nil>} {0x.. schemas 0x.. <nil>}]"
	br := bracketsRe.FindStringSubmatch(s)
	if len(br) != 2 {
		return ""
	}
	inside := br[1]
	parts := strings.Fields(inside)
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		// Strip braces and commas
		p = strings.Trim(p, "{}:,")
		if p == "" || p == "<nil>" || hexPtrRe.MatchString(p) {
			continue
		}
		if tokenRe.MatchString(p) {
			tokens = append(tokens, p)
		}
	}
	// Collapse consecutive duplicates and make a readable path.
	if len(tokens) == 0 {
		return ""
	}
	// A light dedupe of adjacent tokens.
	clean := tokens[:0]
	for _, t := range tokens {
		if len(clean) == 0 || clean[len(clean)-1] != t {
			clean = append(clean, t)
		}
	}
	return strings.Join(clean, ".")
}

func classifyAndHint(s string) (msg, hint string) {
	if strings.Contains(s, "contains Top") {
		msg = `Unknown type produced by the transformation ("Top"). This usually happens when a property is accessed on a non-object value.`
		hint = `Use optional access or guards, e.g. ".foo? // null" or "if type==\"object\" and has(\"foo\") then .foo else null end". Ensure both branches return the same type.`
		return
	}
	if strings.Contains(s, "contains Bottom") {
		msg = `Empty result produced by the transformation ("Bottom"). This usually happens with operations like "empty" or incompatible type filters.`
		hint = `Ensure your transformation produces a valid output. Avoid "empty" in strict mode.`
		return
	}
	if strings.Contains(s, "exceeded maximum execution depth") {
		msg = `Maximum execution depth exceeded. The transformation is too deeply nested or recursive.`
		hint = `Simplify your jq expression or reduce recursive operations.`
		return
	}
	// Future: add other classifications here.
	return "Transformation error.", ""
}

func extractDetails(s string) string {
	// Keep a concise tail; strip the noisy bracketed prefix.
	// Try to keep the essential executor message.
	if idx := strings.Index(s, "]:"); idx != -1 && idx+2 < len(s) {
		return strings.TrimSpace(s[idx+2:])
	}
	// Or return the whole string as a fallback (not ideal, but safe).
	return strings.TrimSpace(s)
}
