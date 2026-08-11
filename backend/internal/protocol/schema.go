package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf8"
)

type schemaViolation struct {
	path string
	rule string
}

func (v *schemaViolation) Error() string { return v.path + ": " + v.rule }

var supportedSchemaKeywords = map[string]struct{}{
	"$schema": {}, "title": {}, "description": {}, "default": {}, "examples": {},
	"type": {}, "properties": {}, "required": {}, "additionalProperties": {},
	"items": {}, "enum": {}, "const": {}, "anyOf": {}, "oneOf": {}, "allOf": {},
	"minimum": {}, "maximum": {}, "exclusiveMinimum": {}, "exclusiveMaximum": {},
	"minLength": {}, "maxLength": {}, "pattern": {}, "minItems": {}, "maxItems": {},
	"uniqueItems": {}, "minProperties": {}, "maxProperties": {},
}

var supportedJSONTypes = map[string]struct{}{
	"null": {}, "boolean": {}, "object": {}, "array": {},
	"number": {}, "integer": {}, "string": {},
}

func parseSchema(schema JSONSchema, limits Limits, path string) (map[string]any, *schemaViolation) {
	value, violation := decodeBoundedJSON(schema.raw, limits.MaxSchemaBytes, limits.MaxSchemaDepth, path)
	if violation != nil {
		return nil, violation
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, &schemaViolation{path: path, rule: "must be a JSON object"}
	}
	if violation := validateSchemaNode(object, path, 1, limits.MaxSchemaDepth); violation != nil {
		return nil, violation
	}
	return object, nil
}

func decodeBoundedJSON(raw []byte, maxBytes, maxDepth int, path string) (any, *schemaViolation) {
	if len(raw) == 0 {
		return nil, &schemaViolation{path: path, rule: "must not be empty"}
	}
	if len(raw) > maxBytes {
		return nil, &schemaViolation{path: path, rule: "exceeds the byte limit"}
	}
	if !utf8.Valid(raw) {
		return nil, &schemaViolation{path: path, rule: "must be valid UTF-8"}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, &schemaViolation{path: path, rule: "must contain valid JSON"}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, &schemaViolation{path: path, rule: "must contain exactly one JSON value"}
	}
	if jsonDepth(value, 1) > maxDepth {
		return nil, &schemaViolation{path: path, rule: "exceeds the nesting-depth limit"}
	}
	return value, nil
}

func jsonDepth(value any, depth int) int {
	maximum := depth
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			maximum = max(maximum, jsonDepth(child, depth+1))
		}
	case []any:
		for _, child := range typed {
			maximum = max(maximum, jsonDepth(child, depth+1))
		}
	}
	return maximum
}

func validateSchemaNode(schema map[string]any, path string, depth, maxDepth int) *schemaViolation {
	if depth > maxDepth {
		return &schemaViolation{path: path, rule: "exceeds the schema-depth limit"}
	}
	for keyword := range schema {
		if _, ok := supportedSchemaKeywords[keyword]; !ok {
			return &schemaViolation{path: path, rule: "contains unsupported keyword " + keyword}
		}
	}
	if value, ok := schema["type"]; ok {
		if violation := validateSchemaType(value, path+".type"); violation != nil {
			return violation
		}
	}
	properties, hasProperties := schema["properties"]
	if hasProperties {
		object, ok := properties.(map[string]any)
		if !ok {
			return &schemaViolation{path: path + ".properties", rule: "must be an object"}
		}
		for name, child := range object {
			childSchema, ok := child.(map[string]any)
			if !ok {
				return &schemaViolation{path: path + ".properties." + name, rule: "must be a schema object"}
			}
			if violation := validateSchemaNode(childSchema, path+".properties."+name, depth+1, maxDepth); violation != nil {
				return violation
			}
		}
	}
	if required, ok := schema["required"]; ok {
		values, ok := required.([]any)
		if !ok {
			return &schemaViolation{path: path + ".required", rule: "must be an array"}
		}
		seen := make(map[string]struct{}, len(values))
		propertyObject, _ := properties.(map[string]any)
		for _, value := range values {
			name, ok := value.(string)
			if !ok || name == "" {
				return &schemaViolation{path: path + ".required", rule: "must contain non-empty unique strings"}
			}
			if _, duplicate := seen[name]; duplicate {
				return &schemaViolation{path: path + ".required", rule: "must contain non-empty unique strings"}
			}
			if hasProperties {
				if _, exists := propertyObject[name]; !exists {
					return &schemaViolation{path: path + ".required", rule: "references an undefined property"}
				}
			}
			seen[name] = struct{}{}
		}
	}
	if additional, ok := schema["additionalProperties"]; ok {
		switch value := additional.(type) {
		case bool:
		case map[string]any:
			if violation := validateSchemaNode(value, path+".additionalProperties", depth+1, maxDepth); violation != nil {
				return violation
			}
		default:
			return &schemaViolation{path: path + ".additionalProperties", rule: "must be a boolean or schema object"}
		}
	}
	if items, ok := schema["items"]; ok {
		child, ok := items.(map[string]any)
		if !ok {
			return &schemaViolation{path: path + ".items", rule: "must be a schema object"}
		}
		if violation := validateSchemaNode(child, path+".items", depth+1, maxDepth); violation != nil {
			return violation
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		if choices, ok := schema[keyword]; ok {
			values, ok := choices.([]any)
			if !ok || len(values) == 0 {
				return &schemaViolation{path: path + "." + keyword, rule: "must be a non-empty schema array"}
			}
			for index, value := range values {
				child, ok := value.(map[string]any)
				if !ok {
					return &schemaViolation{path: fmt.Sprintf("%s.%s[%d]", path, keyword, index), rule: "must be a schema object"}
				}
				if violation := validateSchemaNode(child, fmt.Sprintf("%s.%s[%d]", path, keyword, index), depth+1, maxDepth); violation != nil {
					return violation
				}
			}
		}
	}
	if enum, ok := schema["enum"]; ok {
		values, ok := enum.([]any)
		if !ok || len(values) == 0 {
			return &schemaViolation{path: path + ".enum", rule: "must be a non-empty array"}
		}
	}
	for _, keyword := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum"} {
		if value, ok := schema[keyword]; ok && !isJSONNumber(value) {
			return &schemaViolation{path: path + "." + keyword, rule: "must be a number"}
		}
	}
	for _, keyword := range []string{"minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties"} {
		if value, ok := schema[keyword]; ok && !isNonNegativeInteger(value) {
			return &schemaViolation{path: path + "." + keyword, rule: "must be a non-negative integer"}
		}
	}
	if pattern, ok := schema["pattern"]; ok {
		text, ok := pattern.(string)
		if !ok {
			return &schemaViolation{path: path + ".pattern", rule: "must be a string"}
		}
		if _, err := regexp.Compile(text); err != nil {
			return &schemaViolation{path: path + ".pattern", rule: "must be a valid regular expression"}
		}
	}
	if unique, ok := schema["uniqueItems"]; ok {
		if _, ok := unique.(bool); !ok {
			return &schemaViolation{path: path + ".uniqueItems", rule: "must be a boolean"}
		}
	}
	return nil
}

func validateSchemaType(value any, path string) *schemaViolation {
	types := make([]string, 0, 1)
	switch typed := value.(type) {
	case string:
		types = append(types, typed)
	case []any:
		if len(typed) == 0 {
			return &schemaViolation{path: path, rule: "must not be empty"}
		}
		for _, item := range typed {
			name, ok := item.(string)
			if !ok {
				return &schemaViolation{path: path, rule: "must contain only type names"}
			}
			types = append(types, name)
		}
	default:
		return &schemaViolation{path: path, rule: "must be a type name or type-name array"}
	}
	seen := make(map[string]struct{}, len(types))
	for _, name := range types {
		if _, ok := supportedJSONTypes[name]; !ok {
			return &schemaViolation{path: path, rule: "contains an unsupported JSON type"}
		}
		if _, duplicate := seen[name]; duplicate {
			return &schemaViolation{path: path, rule: "must contain unique type names"}
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateJSONAgainstSchema(value any, schema map[string]any, path string) *schemaViolation {
	if choices, ok := schema["allOf"].([]any); ok {
		for _, choice := range choices {
			if violation := validateJSONAgainstSchema(value, choice.(map[string]any), path); violation != nil {
				return violation
			}
		}
	}
	if choices, ok := schema["anyOf"].([]any); ok {
		matched := false
		for _, choice := range choices {
			if validateJSONAgainstSchema(value, choice.(map[string]any), path) == nil {
				matched = true
				break
			}
		}
		if !matched {
			return &schemaViolation{path: path, rule: "does not match any allowed schema"}
		}
	}
	if choices, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, choice := range choices {
			if validateJSONAgainstSchema(value, choice.(map[string]any), path) == nil {
				matches++
			}
		}
		if matches != 1 {
			return &schemaViolation{path: path, rule: "must match exactly one allowed schema"}
		}
	}
	if expected, ok := schema["type"]; ok && !matchesSchemaType(value, expected) {
		return &schemaViolation{path: path, rule: "has the wrong JSON type"}
	}
	if expected, ok := schema["const"]; ok && !jsonValuesEqual(value, expected) {
		return &schemaViolation{path: path, rule: "does not match the required constant"}
	}
	if allowed, ok := schema["enum"].([]any); ok {
		matched := false
		for _, candidate := range allowed {
			if jsonValuesEqual(value, candidate) {
				matched = true
				break
			}
		}
		if !matched {
			return &schemaViolation{path: path, rule: "is not in the allowed enum"}
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		if violation := validateObjectConstraints(typed, schema, path); violation != nil {
			return violation
		}
	case []any:
		if violation := validateArrayConstraints(typed, schema, path); violation != nil {
			return violation
		}
	case string:
		if violation := validateStringConstraints(typed, schema, path); violation != nil {
			return violation
		}
	case json.Number:
		if violation := validateNumberConstraints(typed, schema, path); violation != nil {
			return violation
		}
	}
	return nil
}

func validateObjectConstraints(value map[string]any, schema map[string]any, path string) *schemaViolation {
	if minimum, ok := schemaInteger(schema, "minProperties"); ok && int64(len(value)) < minimum {
		return &schemaViolation{path: path, rule: "has too few properties"}
	}
	if maximum, ok := schemaInteger(schema, "maxProperties"); ok && int64(len(value)) > maximum {
		return &schemaViolation{path: path, rule: "has too many properties"}
	}
	if required, ok := schema["required"].([]any); ok {
		for _, item := range required {
			name := item.(string)
			if _, exists := value[name]; !exists {
				return &schemaViolation{path: path, rule: "is missing a required property"}
			}
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for name, child := range value {
		if propertySchema, ok := properties[name].(map[string]any); ok {
			if violation := validateJSONAgainstSchema(child, propertySchema, path+"."+name); violation != nil {
				return violation
			}
			continue
		}
		if additional, exists := schema["additionalProperties"]; exists {
			switch constraint := additional.(type) {
			case bool:
				if !constraint {
					return &schemaViolation{path: path, rule: "contains an additional property"}
				}
			case map[string]any:
				if violation := validateJSONAgainstSchema(child, constraint, path+"."+name); violation != nil {
					return violation
				}
			}
		}
	}
	return nil
}

func validateArrayConstraints(value []any, schema map[string]any, path string) *schemaViolation {
	if minimum, ok := schemaInteger(schema, "minItems"); ok && int64(len(value)) < minimum {
		return &schemaViolation{path: path, rule: "has too few items"}
	}
	if maximum, ok := schemaInteger(schema, "maxItems"); ok && int64(len(value)) > maximum {
		return &schemaViolation{path: path, rule: "has too many items"}
	}
	if unique, _ := schema["uniqueItems"].(bool); unique {
		for left := range value {
			for right := left + 1; right < len(value); right++ {
				if jsonValuesEqual(value[left], value[right]) {
					return &schemaViolation{path: path, rule: "must contain unique items"}
				}
			}
		}
	}
	if itemSchema, ok := schema["items"].(map[string]any); ok {
		for index, item := range value {
			if violation := validateJSONAgainstSchema(item, itemSchema, fmt.Sprintf("%s[%d]", path, index)); violation != nil {
				return violation
			}
		}
	}
	return nil
}

func validateStringConstraints(value string, schema map[string]any, path string) *schemaViolation {
	length := int64(utf8.RuneCountInString(value))
	if minimum, ok := schemaInteger(schema, "minLength"); ok && length < minimum {
		return &schemaViolation{path: path, rule: "is shorter than allowed"}
	}
	if maximum, ok := schemaInteger(schema, "maxLength"); ok && length > maximum {
		return &schemaViolation{path: path, rule: "is longer than allowed"}
	}
	if pattern, ok := schema["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(value) {
		return &schemaViolation{path: path, rule: "does not match the required pattern"}
	}
	return nil
}

func validateNumberConstraints(value json.Number, schema map[string]any, path string) *schemaViolation {
	actual, ok := numberRat(value)
	if !ok {
		return &schemaViolation{path: path, rule: "is not a finite number"}
	}
	checks := []struct {
		keyword string
		valid   func(int) bool
		rule    string
	}{
		{"minimum", func(cmp int) bool { return cmp >= 0 }, "is below the minimum"},
		{"maximum", func(cmp int) bool { return cmp <= 0 }, "is above the maximum"},
		{"exclusiveMinimum", func(cmp int) bool { return cmp > 0 }, "is not above the exclusive minimum"},
		{"exclusiveMaximum", func(cmp int) bool { return cmp < 0 }, "is not below the exclusive maximum"},
	}
	for _, check := range checks {
		if expected, ok := schema[check.keyword].(json.Number); ok {
			bound, valid := numberRat(expected)
			if !valid || !check.valid(actual.Cmp(bound)) {
				return &schemaViolation{path: path, rule: check.rule}
			}
		}
	}
	return nil
}

func matchesSchemaType(value, expected any) bool {
	types := []string{}
	switch typed := expected.(type) {
	case string:
		types = append(types, typed)
	case []any:
		for _, item := range typed {
			types = append(types, item.(string))
		}
	}
	actual := jsonType(value)
	if slices.Contains(types, actual) {
		return true
	}
	return actual == "integer" && slices.Contains(types, "number")
}

func jsonType(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case json.Number:
		if _, err := typed.Int64(); err == nil {
			return "integer"
		}
		floatValue, err := typed.Float64()
		if err == nil && math.Trunc(floatValue) == floatValue {
			return "integer"
		}
		return "number"
	default:
		return "unknown"
	}
}

func isJSONNumber(value any) bool {
	_, ok := value.(json.Number)
	return ok
}

func isNonNegativeInteger(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	integer, err := strconv.ParseInt(number.String(), 10, 64)
	return err == nil && integer >= 0
}

func schemaInteger(schema map[string]any, keyword string) (int64, bool) {
	number, ok := schema[keyword].(json.Number)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseInt(number.String(), 10, 64)
	return value, err == nil
}

func numberRat(number json.Number) (*big.Rat, bool) {
	value := new(big.Rat)
	if _, ok := value.SetString(number.String()); ok {
		return value, true
	}
	return nil, false
}

func jsonValuesEqual(left, right any) bool {
	leftNumber, leftIsNumber := left.(json.Number)
	rightNumber, rightIsNumber := right.(json.Number)
	if leftIsNumber && rightIsNumber {
		leftRat, leftOK := numberRat(leftNumber)
		rightRat, rightOK := numberRat(rightNumber)
		return leftOK && rightOK && leftRat.Cmp(rightRat) == 0
	}
	return reflect.DeepEqual(left, right)
}
