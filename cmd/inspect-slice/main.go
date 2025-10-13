package main

import (
	"fmt"
	gojq "github.com/speakeasy-api/jq"
)

func main() {
	queries := []string{
		".[0]",     // Index
		".[1:]",    // Slice from index
		".[:2]",    // Slice to index
		".[1:3]",   // Slice range
		".[]",      // Iterate
	}

	for _, q := range queries {
		fmt.Printf("\n=== %s ===\n", q)
		query, err := gojq.Parse(q)
		if err != nil {
			fmt.Printf("Parse error: %v\n", err)
			continue
		}

		code, err := gojq.Compile(query)
		if err != nil {
			fmt.Printf("Compile error: %v\n", err)
			continue
		}

		rawCodes := code.GetCodes()
		for i, rc := range rawCodes {
			op := getCodeOp(rc)
			val := getCodeValue(rc)
			fmt.Printf("%3d: %-15s %v\n", i, opcodeToString(op), val)
		}
	}
}

func getCodeOp(c any) int {
	if code, ok := c.(interface{ GetOp() int }); ok {
		return code.GetOp()
	}
	return -1
}

func getCodeValue(c any) any {
	if code, ok := c.(interface{ GetValue() any }); ok {
		return code.GetValue()
	}
	return nil
}

func opcodeToString(op int) string {
	names := map[int]string{
		0: "opNop", 1: "opPush", 2: "opPop", 3: "opDup", 4: "opConst",
		5: "opLoad", 6: "opStore", 7: "opObject", 8: "opAppend", 9: "opFork",
		10: "opForkTryBegin", 11: "opForkTryEnd", 12: "opForkAlt", 13: "opForkLabel",
		14: "opBacktrack", 15: "opJump", 16: "opJumpIfNot", 17: "opIndex",
		18: "opIndexArray", 19: "opCall", 20: "opCallRec", 21: "opPushPC",
		22: "opCallPC", 23: "opScope", 24: "opRet", 25: "opIter",
		26: "opExpBegin", 27: "opExpEnd", 28: "opPathBegin", 29: "opPathEnd",
	}
	if name, ok := names[op]; ok {
		return name
	}
	return fmt.Sprintf("op%d", op)
}
