package schemaexec

import "github.com/speakeasy-api/openapi/jsonschema/oas3"

// schemaStack implements a simple stack for schema values.
type schemaStack struct {
	data []SValue
}

// newSchemaStack creates a new schema stack.
func newSchemaStack() *schemaStack {
	return &schemaStack{
		data: make([]SValue, 0, 64),
	}
}

// push adds a schema to the top of the stack.
func (s *schemaStack) push(v SValue) {
	s.data = append(s.data, v)
}

// pushSchema adds a schema to the stack (convenience wrapper).
func (s *schemaStack) pushSchema(schema *oas3.Schema) {
	s.data = append(s.data, SValue{Schema: schema})
}

// pop removes and returns the top schema from the stack.
// Panics if stack is empty.
func (s *schemaStack) pop() SValue {
	if len(s.data) == 0 {
		panic("schema stack underflow")
	}
	v := s.data[len(s.data)-1]
	s.data = s.data[:len(s.data)-1]
	return v
}

// popSchema removes and returns the top schema (convenience wrapper).
func (s *schemaStack) popSchema() *oas3.Schema {
	return s.pop().Schema
}

// top returns the top schema without removing it.
func (s *schemaStack) top() SValue {
	if len(s.data) == 0 {
		panic("schema stack underflow")
	}
	return s.data[len(s.data)-1]
}

// topSchema returns the top schema without removing it (convenience wrapper).
func (s *schemaStack) topSchema() *oas3.Schema {
	return s.top().Schema
}

// empty checks if the stack is empty.
func (s *schemaStack) empty() bool {
	return len(s.data) == 0
}

// len returns the number of elements on the stack.
func (s *schemaStack) len() int {
	return len(s.data)
}
