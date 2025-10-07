package jqfmt

import (
	"strings"
	"testing"
)

func TestFormat(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		config JqFmtCfg
		want   string
	}{
		// Array formatting
		{
			name:  "array_formatting",
			input: `[1,2,{"a":1,"b":[2,3]},.foo,(.a+.b)]`,
			config: JqFmtCfg{Arr: true},
			want: "[" + `
  1, 
  2, 
  { "a": 1, "b": [
    2, 
    3
  ] }, 
  .foo, 
  (.a + .b)
]`,
		},

		// Object formatting
		{
			name:  "object_formatting",
			input: `{"a":1,"b":{"c":2,"d":[1,2,3]},"e":.x|length,"f":(.a+.b)}`,
			config: JqFmtCfg{Obj: true},
			want: "{ " + `
  "a": 1, 
  "b": { 
    "c": 2, 
    "d": [1, 2, 3]
  }, 
  "e": .x | length, 
  "f": (.a + .b)
}`,
		},

		// Operator: pipe
		{
			name:   "operator_pipe",
			input:  `.a|.b|length`,
			config: JqFmtCfg{Ops: []string{"pipe"}},
			want: `.a | 
  .b | 
  length`,
		},

		// Operator: comma
		{
			name:   "operator_comma",
			input:  `.a,.b,.c`,
			config: JqFmtCfg{Ops: []string{"comma"}},
			want: `.a, 
  .b, 
  .c`,
		},

		// Operator: add
		{
			name:   "operator_add",
			input:  `.a+.b+(.c*2)`,
			config: JqFmtCfg{Ops: []string{"add"}},
			want: `.a + 
  .b + 
  (.c * 2)`,
		},

		// Operator: sub
		{
			name:   "operator_sub",
			input:  `.a-.b-1`,
			config: JqFmtCfg{Ops: []string{"sub"}},
			want: `.a - 
  .b - 
  1`,
		},

		// Operator: mul
		{
			name:   "operator_mul",
			input:  `.a*.b*2`,
			config: JqFmtCfg{Ops: []string{"mul"}},
			want: `.a * 
  .b * 
  2`,
		},

		// Operator: div
		{
			name:   "operator_div",
			input:  `.a/.b/2`,
			config: JqFmtCfg{Ops: []string{"div"}},
			want: `.a / 
  .b / 
  2`,
		},

		// Operator: mod
		{
			name:   "operator_mod",
			input:  `.a%.b%2`,
			config: JqFmtCfg{Ops: []string{"mod"}},
			want: `.a % 
  .b % 
  2`,
		},

		// Operator: eq
		{
			name:   "operator_eq",
			input:  `(.a+1)==(.b|length)`,
			config: JqFmtCfg{Ops: []string{"eq"}},
			want: `(.a + 1) == 
  (.b | length)`,
		},

		// Operator: ne
		{
			name:   "operator_ne",
			input:  `.a!=.b`,
			config: JqFmtCfg{Ops: []string{"ne"}},
			want: `.a != 
  .b`,
		},

		// Operator: gt
		{
			name:   "operator_gt",
			input:  `(.a+1)>.b`,
			config: JqFmtCfg{Ops: []string{"gt"}},
			want: `(.a + 1) > 
  .b`,
		},

		// Operator: lt
		{
			name:   "operator_lt",
			input:  `.a<(.b|length)`,
			config: JqFmtCfg{Ops: []string{"lt"}},
			want: `.a < 
  (.b | length)`,
		},

		// Operator: ge
		{
			name:   "operator_ge",
			input:  `(.a|length)>=10`,
			config: JqFmtCfg{Ops: []string{"ge"}},
			want: `(.a | length) >= 
  10`,
		},

		// Operator: le
		{
			name:   "operator_le",
			input:  `.b<=.a`,
			config: JqFmtCfg{Ops: []string{"le"}},
			want: `.b <= 
  .a`,
		},

		// Operator: and
		{
			name:   "operator_and",
			input:  `(.a>0)and(.b|length>1)`,
			config: JqFmtCfg{Ops: []string{"and"}},
			want: `(.a > 0) and 
  (.b | length > 1)`,
		},

		// Operator: or
		{
			name:   "operator_or",
			input:  `.a==1 or .b==2 or .c`,
			config: JqFmtCfg{Ops: []string{"or"}},
			want: `.a == 1 or 
  .b == 2 or 
  .c`,
		},

		// Operator: alt
		{
			name:   "operator_alt",
			input:  `.a//.b//0`,
			config: JqFmtCfg{Ops: []string{"alt"}},
			want: `.a // 
  .b // 
  0`,
		},

		// Operator: assign
		{
			name:   "operator_assign",
			input:  `.foo.bar=.a+.b`,
			config: JqFmtCfg{Ops: []string{"assign"}},
			want: `.foo.bar = 
  .a + .b`,
		},

		// Operator: modify
		{
			name:   "operator_modify",
			input:  `.a |= (. + 1)`,
			config: JqFmtCfg{Ops: []string{"modify"}},
			want: `.a |= 
  (. + 1)`,
		},

		// Operator: updateAdd
		{
			name:   "operator_updateAdd",
			input:  `.a+=1+.b`,
			config: JqFmtCfg{Ops: []string{"updateAdd"}},
			want: `.a += 
  1 + .b`,
		},

		// Operator: updateSub
		{
			name:   "operator_updateSub",
			input:  `.a-=2`,
			config: JqFmtCfg{Ops: []string{"updateSub"}},
			want: `.a -= 
  2`,
		},

		// Operator: updateMul
		{
			name:   "operator_updateMul",
			input:  `.a*=10`,
			config: JqFmtCfg{Ops: []string{"updateMul"}},
			want: `.a *= 
  10`,
		},

		// Operator: updateDiv
		{
			name:   "operator_updateDiv",
			input:  `.a/=3`,
			config: JqFmtCfg{Ops: []string{"updateDiv"}},
			want: `.a /= 
  3`,
		},

		// Operator: updateMod
		{
			name:   "operator_updateMod",
			input:  `.a%=2`,
			config: JqFmtCfg{Ops: []string{"updateMod"}},
			want: `.a %= 
  2`,
		},

		// Operator: updateAlt
		{
			name:   "operator_updateAlt",
			input:  `.a//=0`,
			config: JqFmtCfg{Ops: []string{"updateAlt"}},
			want: `.a //= 
  0`,
		},

		// Multi-feature: pipe + array
		{
			name:  "multi_pipe_and_array",
			input: `[.a,.b,.c]|map(.+1)|length`,
			config: JqFmtCfg{
				Ops: []string{"pipe"},
				Arr: true,
			},
			want: "[" + `
  .a, 
  .b, 
  .c
] | 
  map(. + 1) | 
  length`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DoThing(tt.input, tt.config)
			if err != nil {
				t.Fatalf("DoThing() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("DoThing() mismatch:\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name    string
		config  JqFmtCfg
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid_operators",
			config:  JqFmtCfg{Ops: []string{"pipe", "comma"}},
			wantErr: false,
		},
		{
			name:    "invalid_operator",
			config:  JqFmtCfg{Ops: []string{"invalid"}},
			wantErr: true,
			errMsg:  "invalid operator",
		},
		{
			name:    "case_insensitive",
			config:  JqFmtCfg{Ops: []string{"PIPE", "Comma"}},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ValidateConfig(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Error("ValidateConfig() expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("ValidateConfig() error = %v, want substring %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("ValidateConfig() unexpected error = %v", err)
				}
			}
		})
	}
}
