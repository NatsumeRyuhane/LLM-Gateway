package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
)

func validateJSONDocument(data []byte) *protocolError {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := walkJSONValue(decoder, "$", 0); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return &protocolError{path: "$", rule: fmt.Sprintf("contains trailing JSON after %v", token)}
		}
		return &protocolError{path: "$", rule: "contains invalid trailing JSON"}
	}
	return nil
}

type protocolError struct {
	path string
	rule string
}

func walkJSONValue(decoder *json.Decoder, path string, depth int) *protocolError {
	if depth > 256 {
		return &protocolError{path: path, rule: "exceeds the JSON nesting bound"}
	}
	token, err := decoder.Token()
	if err != nil {
		return &protocolError{path: path, rule: "contains invalid JSON"}
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return &protocolError{path: path, rule: "contains invalid JSON"}
			}
			key, ok := keyToken.(string)
			if !ok {
				return &protocolError{path: path, rule: "contains a non-string object key"}
			}
			childPath := joinPath(path, key)
			if _, duplicate := seen[key]; duplicate {
				return &protocolError{path: childPath, rule: "must not be repeated"}
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder, childPath, depth+1); err != nil {
				return err
			}
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
			return &protocolError{path: path, rule: "contains an unterminated object"}
		}
	case '[':
		index := 0
		for decoder.More() {
			if err := walkJSONValue(decoder, fmt.Sprintf("%s[%d]", path, index), depth+1); err != nil {
				return err
			}
			index++
		}
		if token, err := decoder.Token(); err != nil || token != json.Delim(']') {
			return &protocolError{path: path, rule: "contains an unterminated array"}
		}
	default:
		return &protocolError{path: path, rule: "contains an invalid JSON delimiter"}
	}
	return nil
}

func decodeObject(raw []byte, path string) (map[string]json.RawMessage, *protocolError) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, &protocolError{path: path, rule: "must be a JSON object"}
	}
	return object, nil
}

func decodeArray(raw []byte, path string) ([]json.RawMessage, *protocolError) {
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, &protocolError{path: path, rule: "must be a JSON array"}
	}
	return values, nil
}

func decodeRequiredString(object map[string]json.RawMessage, key, path string) (string, *protocolError) {
	raw, ok := object[key]
	if !ok {
		return "", &protocolError{path: joinPath(path, key), rule: "is required"}
	}
	if isJSONNull(raw) {
		return "", &protocolError{path: joinPath(path, key), rule: "must be a string"}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", &protocolError{path: joinPath(path, key), rule: "must be a string"}
	}
	return value, nil
}

func rejectUnknownFields(object map[string]json.RawMessage, path string, allowed map[string]struct{}) *protocolError {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := allowed[key]; !ok {
			return &protocolError{path: joinPath(path, key), rule: "is not an accepted field"}
		}
	}
	return nil
}

func joinPath(parent, child string) string {
	if parent == "$" || parent == "" {
		return child
	}
	return parent + "." + child
}
