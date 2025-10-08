package schemaexec

import (
	"testing"

	"github.com/speakeasy-api/openapi/jsonschema/oas3"
	"gopkg.in/yaml.v3"
)

func TestUnionCollapse_ExactDuplicates(t *testing.T) {
	opts := DefaultOptions()

	// Test: anyOf with two identical {type: number} schemas
	s1 := NumberType()
	s2 := NumberType()

	result := Union([]*oas3.Schema{s1, s2}, opts)

	// Should collapse to a single {type: number}
	if result.AnyOf != nil {
		t.Errorf("Expected no anyOf, but got %d branches", len(result.AnyOf))
	}
	if getType(result) != "number" {
		t.Errorf("Expected type 'number', got '%s'", getType(result))
	}
}

func TestUnionCollapse_EnumSubsumedByType(t *testing.T) {
	opts := DefaultOptions()

	// Test: anyOf with {type: number, enum: [0]} and {type: number}
	enumZero := ConstNumber(0)
	numberType := NumberType()

	result := Union([]*oas3.Schema{enumZero, numberType}, opts)

	// Should collapse to {type: number} (enum is subsumed)
	if result.AnyOf != nil {
		t.Errorf("Expected no anyOf, but got %d branches", len(result.AnyOf))
	}
	if getType(result) != "number" {
		t.Errorf("Expected type 'number', got '%s'", getType(result))
	}
	if result.Enum != nil {
		t.Errorf("Expected no enum constraint after subsumption")
	}
}

func TestUnionCollapse_ThreeWay(t *testing.T) {
	opts := DefaultOptions()

	// Test: [{type: number, enum: [0]}, {type: number}, {type: number, enum: [1]}]
	enumZero := ConstNumber(0)
	enumOne := ConstNumber(1)
	numberType := NumberType()

	result := Union([]*oas3.Schema{enumZero, numberType, enumOne}, opts)

	// Should collapse to {type: number}
	if result.AnyOf != nil {
		t.Errorf("Expected no anyOf, but got %d branches", len(result.AnyOf))
	}
	if getType(result) != "number" {
		t.Errorf("Expected type 'number', got '%s'", getType(result))
	}
}

func TestUnionCollapse_DifferentTypes(t *testing.T) {
	opts := DefaultOptions()

	// Test: anyOf with {type: string} and {type: number}
	stringType := StringType()
	numberType := NumberType()

	result := Union([]*oas3.Schema{stringType, numberType}, opts)

	// Should keep anyOf (different types)
	if result.AnyOf == nil {
		t.Error("Expected anyOf to be preserved for different types")
	}
	if len(result.AnyOf) != 2 {
		t.Errorf("Expected 2 branches in anyOf, got %d", len(result.AnyOf))
	}
}

func TestUnionCollapse_EnumSubset(t *testing.T) {
	opts := DefaultOptions()

	// Create {type: string, enum: ["a"]}
	nodeA := &yaml.Node{Kind: yaml.ScalarNode, Value: "a", Tag: "!!str"}
	enumA := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{nodeA},
	}

	// Create {type: string, enum: ["a", "b"]}
	nodeB := &yaml.Node{Kind: yaml.ScalarNode, Value: "b", Tag: "!!str"}
	enumAB := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{nodeA, nodeB},
	}

	result := Union([]*oas3.Schema{enumA, enumAB}, opts)

	// Should collapse to {type: string, enum: ["a", "b"]}
	if result.AnyOf != nil {
		t.Errorf("Expected no anyOf, but got %d branches", len(result.AnyOf))
	}
	if getType(result) != "string" {
		t.Errorf("Expected type 'string', got '%s'", getType(result))
	}
	if len(result.Enum) != 2 {
		t.Errorf("Expected 2 enum values, got %d", len(result.Enum))
	}
}

func TestUnionCollapse_CartInputGrandTotal(t *testing.T) {
	opts := DefaultOptions()

	// This replicates the issue: anyOf with {type: number, enum: [0]} and {type: number}
	// After materialization from (.items | map(.price * .quantity) | add // 0)
	zero := ConstNumber(0)
	number := NumberType()

	result := Union([]*oas3.Schema{zero, number}, opts)

	// Should collapse to just {type: number}
	if result.AnyOf != nil {
		t.Errorf("Expected anyOf to collapse, but got %d branches", len(result.AnyOf))
		for i, branch := range result.AnyOf {
			if branch.Left != nil {
				t.Logf("  Branch %d: type=%s, enum=%v", i, getType(branch.Left), branch.Left.Enum)
			}
		}
	}

	if getType(result) != "number" {
		t.Errorf("Expected type 'number', got '%s'", getType(result))
	}

	if result.Enum != nil {
		t.Error("Expected enum to be removed after subsumption by unconstrained number type")
	}
}

func TestUnionCollapse_CartInputTotal(t *testing.T) {
	opts := DefaultOptions()

	// This replicates the CartInput items.total field issue
	// After materialization, we might get multiple {type: number} from different branches
	num1 := NumberType()
	num2 := NumberType()

	result := Union([]*oas3.Schema{num1, num2}, opts)

	// Should deduplicate to a single {type: number}
	if result.AnyOf != nil {
		t.Errorf("Expected exact duplicates to collapse, but got anyOf with %d branches", len(result.AnyOf))
	}

	if getType(result) != "number" {
		t.Errorf("Expected type 'number', got '%s'", getType(result))
	}
}

func TestFingerprinting(t *testing.T) {
	// Test that structurally identical schemas have the same fingerprint
	s1 := NumberType()
	s2 := NumberType()

	fp1 := schemaFingerprint(s1)
	fp2 := schemaFingerprint(s2)

	if fp1 != fp2 {
		t.Errorf("Expected identical schemas to have same fingerprint:\n  fp1=%s\n  fp2=%s", fp1, fp2)
	}

	// Test that different schemas have different fingerprints
	s3 := StringType()
	fp3 := schemaFingerprint(s3)

	if fp1 == fp3 {
		t.Error("Expected different schemas to have different fingerprints")
	}
}

func TestSubsumption_NumberEnum(t *testing.T) {
	enumZero := ConstNumber(0)
	numberType := NumberType()

	// enum [0] should be subsumed by unconstrained number
	if !isSubschemaOf(enumZero, numberType) {
		t.Error("Expected {type: number, enum: [0]} ⊆ {type: number}")
	}

	// But not the reverse
	if isSubschemaOf(numberType, enumZero) {
		t.Error("Did not expect {type: number} ⊆ {type: number, enum: [0]}")
	}
}

func TestSubsumption_StringEnum(t *testing.T) {
	nodeA := &yaml.Node{Kind: yaml.ScalarNode, Value: "a", Tag: "!!str"}
	nodeB := &yaml.Node{Kind: yaml.ScalarNode, Value: "b", Tag: "!!str"}

	enumA := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{nodeA},
	}

	enumAB := &oas3.Schema{
		Type: oas3.NewTypeFromString(oas3.SchemaTypeString),
		Enum: []*yaml.Node{nodeA, nodeB},
	}

	// ["a"] should be subsumed by ["a", "b"]
	if !isSubschemaOf(enumA, enumAB) {
		t.Error("Expected {enum: ['a']} ⊆ {enum: ['a', 'b']}")
	}

	// But not the reverse
	if isSubschemaOf(enumAB, enumA) {
		t.Error("Did not expect {enum: ['a', 'b']} ⊆ {enum: ['a']}")
	}
}

func TestSubsumption_Identical(t *testing.T) {
	s1 := NumberType()
	s2 := NumberType()

	// Identical schemas should be mutual subsets
	if !isSubschemaOf(s1, s2) {
		t.Error("Expected identical schemas to be subsets of each other")
	}
	if !isSubschemaOf(s2, s1) {
		t.Error("Expected identical schemas to be subsets of each other")
	}
}
