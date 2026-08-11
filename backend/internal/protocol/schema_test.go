package protocol

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
)

func TestSchemaValidationPathsAreDeterministic(t *testing.T) {
	t.Parallel()

	limits := DefaultLimits()
	for range 100 {
		_, violation := parseSchema(NewJSONSchema([]byte(`{
			"type":"object",
			"properties":{
				"z":{"type":false},
				"a":{"type":false}
			}
		}`)), limits, "schema")
		if violation == nil || violation.path != "schema.properties.a.type" {
			t.Fatalf("schema violation = %#v, want sorted property a", violation)
		}
	}

	schema, violation := parseSchema(NewJSONSchema([]byte(`{
		"type":"object",
		"properties":{"z":{"type":"integer"},"a":{"type":"integer"}}
	}`)), limits, "schema")
	if violation != nil {
		t.Fatalf("parseSchema() violation = %v", violation)
	}
	value := map[string]any{"z": "wrong", "a": "wrong"}
	for range 100 {
		violation = validateJSONAgainstSchema(value, schema, "value")
		if violation == nil || violation.path != "value.a" {
			t.Fatalf("value violation = %#v, want sorted property a", violation)
		}
	}
}

func TestUniqueItemsUsesCanonicalNumericKeys(t *testing.T) {
	t.Parallel()

	schema, violation := parseSchema(NewJSONSchema([]byte(`{"type":"array","uniqueItems":true}`)), DefaultLimits(), "schema")
	if violation != nil {
		t.Fatalf("parseSchema() violation = %v", violation)
	}
	duplicate := []any{json.Number("1"), json.Number("1.0")}
	if violation := validateJSONAgainstSchema(duplicate, schema, "value"); violation == nil || violation.rule != "must contain unique items" {
		t.Fatalf("duplicate violation = %#v", violation)
	}
	distinctTypes := []any{json.Number("1"), "1"}
	if violation := validateJSONAgainstSchema(distinctTypes, schema, "value"); violation != nil {
		t.Fatalf("distinct type violation = %v", violation)
	}

	large := make([]any, 10_000)
	for index := range large {
		large[index] = json.Number(fmt.Sprintf("%d", index))
	}
	if violation := validateJSONAgainstSchema(large, schema, "value"); violation != nil {
		t.Fatalf("large unique array violation = %v", violation)
	}
}

func TestParseSchemaCachesCompiledPatterns(t *testing.T) {
	t.Parallel()

	schema, violation := parseSchema(NewJSONSchema([]byte(`{"type":"string","pattern":"^a+$"}`)), DefaultLimits(), "schema")
	if violation != nil {
		t.Fatalf("parseSchema() violation = %v", violation)
	}
	if _, ok := schema["pattern"].(*regexp.Regexp); !ok {
		t.Fatalf("pattern type = %T, want *regexp.Regexp", schema["pattern"])
	}
	if violation := validateJSONAgainstSchema("aaa", schema, "value"); violation != nil {
		t.Fatalf("matching value violation = %v", violation)
	}
	if violation := validateJSONAgainstSchema("bbb", schema, "value"); violation == nil {
		t.Fatal("non-matching value accepted")
	}
}
