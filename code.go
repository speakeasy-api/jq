package gojq

type code struct {
	v  any
	op opcode
}

// GetOp returns the opcode for schema execution.
func (c *code) GetOp() int {
	return int(c.op)
}

// GetValue returns the opcode value for schema execution.
func (c *code) GetValue() any {
	return c.v
}

// OpString returns the string representation of the opcode.
func (c *code) OpString() string {
	return c.op.String()
}

type opcode int

const (
	opnop opcode = iota
	oppush
	oppop
	opdup
	opconst
	opload
	opstore
	opobject
	opappend
	opfork
	opforktrybegin
	opforktryend
	opforkalt
	opforklabel
	opbacktrack
	opjump
	opjumpifnot
	opindex
	opindexarray
	opcall
	opcallrec
	oppushpc
	opcallpc
	opscope
	opret
	opiter
	opexpbegin
	opexpend
	oppathbegin
	oppathend
)

func (op opcode) String() string {
	switch op {
	case opnop:
		return "nop"
	case oppush:
		return "push"
	case oppop:
		return "pop"
	case opdup:
		return "dup"
	case opconst:
		return "const"
	case opload:
		return "load"
	case opstore:
		return "store"
	case opobject:
		return "object"
	case opappend:
		return "append"
	case opfork:
		return "fork"
	case opforktrybegin:
		return "forktrybegin"
	case opforktryend:
		return "forktryend"
	case opforkalt:
		return "forkalt"
	case opforklabel:
		return "forklabel"
	case opbacktrack:
		return "backtrack"
	case opjump:
		return "jump"
	case opjumpifnot:
		return "jumpifnot"
	case opindex:
		return "index"
	case opindexarray:
		return "indexarray"
	case opcall:
		return "call"
	case opcallrec:
		return "callrec"
	case oppushpc:
		return "pushpc"
	case opcallpc:
		return "callpc"
	case opscope:
		return "scope"
	case opret:
		return "ret"
	case opiter:
		return "iter"
	case opexpbegin:
		return "expbegin"
	case opexpend:
		return "expend"
	case oppathbegin:
		return "pathbegin"
	case oppathend:
		return "pathend"
	default:
		panic(op)
	}
}
