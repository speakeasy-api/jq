package schemaexec

import "github.com/speakeasy-api/openapi/jsonschema/oas3"

// ValueKind classifies entries on the abstract stack.
type ValueKind uint8

const (
	VSchema ValueKind = iota
	VClosure
)

// Closure abstracts a function value captured by pushpc.
// PC is the entry address, ScopeIndex is the captured lexical scope index.
type Closure struct {
	PC         int
	ScopeIndex int
}

// AValue is the abstract value stored on the symbolic VM stack.
// Phase 1a: only VSchema is used by the existing code. VClosure will be used in Phase 1b+.
type AValue struct {
	Kind    ValueKind
	Schema  *oas3.Schema
	Closure *Closure
}

// Constructors and accessors.
func NewSchemaValue(s *oas3.Schema) AValue {
	return AValue{Kind: VSchema, Schema: s}
}

func NewClosureValue(pc, scopeIdx int) AValue {
	return AValue{Kind: VClosure, Closure: &Closure{PC: pc, ScopeIndex: scopeIdx}}
}

func (v AValue) AsSchema() (*oas3.Schema, bool) {
	if v.Kind == VSchema {
		return v.Schema, true
	}
	return nil, false
}

func (v AValue) AsClosure() (*Closure, bool) {
	if v.Kind == VClosure {
		return v.Closure, true
	}
	return nil, false
}
