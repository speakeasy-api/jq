package schemaexec

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

