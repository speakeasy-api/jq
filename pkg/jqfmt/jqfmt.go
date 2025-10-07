package jqfmt

import (
	"encoding/json"
	"fmt"
	"strings"

	gojq "github.com/speakeasy-api/jq"
)

type JqFmtCfg struct {
	Ops []string
	Obj bool
	Arr bool
}

var cfg JqFmtCfg

var line int
var node string
var nodeIdts map[string][]string
var indented map[int]int

func ValidateConfig(cfg JqFmtCfg) (JqFmtCfg, error) {
	validOps := []string{
		"pipe",
		"comma",
		"add",
		"sub",
		"mul",
		"div",
		"mod",
		"eq",
		"ne",
		"gt",
		"lt",
		"ge",
		"le",
		"and",
		"or",
		"alt",
		"assign",
		"modify",
		"updateAdd",
		"updateSub",
		"updateMul",
		"updateDiv",
		"updateMod",
		"updateAlt",
	}

	ops := cfg.Ops
	for o, op := range ops {
		valid := false
		for _, vop := range validOps {
			if strings.ToLower(op) == strings.ToLower(vop) {
				ops[o] = vop
				valid = true
			}
		}
		if !valid {
			return cfg, fmt.Errorf("invalid operator \"%s\"; valid operators: %s\n", op, strings.Join(validOps[:], ", "))
		}
	}
	cfg.Ops = ops

	return cfg, nil

}

func strToQuery(jqStr string) (Query, error) {

	jqAstQ := Query{}

	// Parse into AST.
	jqAst, err := gojq.Parse(jqStr)
	// TODO: print jq pretty errors
	if err != nil {
		return jqAstQ, fmt.Errorf("could not parse jq: %w", err)
	}

	// Initially format jq to give us something consistent to start with.
	jqAstPty, err := gojq.Parse(jqAst.String())
	if err != nil {
		return jqAstQ, fmt.Errorf("could not parse jq: %w", err)
	}

	// Convert from jq.Query to Query.
	jqAstJson, err := json.Marshal(jqAstPty)
	if err != nil {
		return jqAstQ, fmt.Errorf("could not convert query: %w", err)
	}
	json.Unmarshal([]byte(jqAstJson), &jqAstQ)

	return jqAstQ, nil
}

func DoThing(jqStr string, cfg_ JqFmtCfg) (string, error) {
	cfg = cfg_

	// Initialize state
	line = 1
	node = ""
	nodeIdts = map[string][]string{}
	indented = map[int]int{}

	// Parse and format
	initQ, err := strToQuery(jqStr)
	if err != nil {
		return "", fmt.Errorf("could not convert jq to query: %w", err)
	}

	// Generate formatted string
	temp := initQ.String()

	fnlQ, err := strToQuery(temp)
	if err != nil {
		return "", fmt.Errorf("could not convert jq to query: %w", err)
	}

	return fnlQ.String(), nil
}

