package openai

import (
	"bytes"
	"encoding/json"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

var messageFields = map[string]struct{}{
	"role": {}, "name": {}, "content": {}, "tool_calls": {}, "tool_call_id": {}, "refusal": {},
}

func (c Codec) decodeMessages(raw []byte) ([]protocol.CanonicalMessage, *protocol.CanonicalError) {
	items, violation := decodeArray(raw, "messages")
	if violation != nil {
		return nil, invalidRequest(violation.path, violation.rule)
	}
	messages := make([]protocol.CanonicalMessage, len(items))
	for index, item := range items {
		path := formatArrayPath("messages", index)
		object, violation := decodeObject(item, path)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if violation := rejectUnknownFields(object, path, messageFields); violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		roleValue, violation := decodeRequiredString(object, "role", path)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if roleValue == "function" {
			return nil, unsupported(path+".role", "the deprecated function role is deferred")
		}
		message := protocol.CanonicalMessage{Role: protocol.MessageRole(roleValue)}
		if _, present := object["refusal"]; present {
			return nil, unsupported(path+".refusal", "assistant refusal history is deferred")
		}
		if nameRaw, present := object["name"]; present {
			var name string
			if err := json.Unmarshal(nameRaw, &name); err != nil {
				return nil, invalidRequest(path+".name", "must be a string")
			}
			message.Name = protocol.Some(name)
		}
		contentRaw, contentPresent := object["content"]
		if contentPresent && !bytes.Equal(bytes.TrimSpace(contentRaw), []byte("null")) {
			content, err := decodeMessageContent(contentRaw, path+".content")
			if err != nil {
				return nil, err
			}
			message.Content = content
		}
		if callsRaw, present := object["tool_calls"]; present {
			calls, err := c.decodeHistoricalToolCalls(callsRaw, path+".tool_calls")
			if err != nil {
				return nil, err
			}
			message.ToolCalls = calls
		}
		if callIDRaw, present := object["tool_call_id"]; present {
			var callID string
			if err := json.Unmarshal(callIDRaw, &callID); err != nil {
				return nil, invalidRequest(path+".tool_call_id", "must be a string")
			}
			message.ToolCallID = protocol.Some(callID)
		}
		messages[index] = message
	}
	return messages, nil
}

func decodeMessageContent(raw []byte, path string) ([]protocol.CanonicalContentPart, *protocol.CanonicalError) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return []protocol.CanonicalContentPart{{Type: protocol.ContentText, Text: text}}, nil
	}
	items, violation := decodeArray(raw, path)
	if violation != nil {
		return nil, invalidRequest(path, "must be a string or text-part array")
	}
	parts := make([]protocol.CanonicalContentPart, len(items))
	for index, item := range items {
		partPath := formatArrayPath(path, index)
		object, violation := decodeObject(item, partPath)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		kind, violation := decodeRequiredString(object, "type", partPath)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if kind != "text" {
			return nil, unsupported(partPath+".type", "only text input content is supported")
		}
		if violation := rejectUnknownFields(object, partPath, map[string]struct{}{"type": {}, "text": {}}); violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		text, violation := decodeRequiredString(object, "text", partPath)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		parts[index] = protocol.CanonicalContentPart{Type: protocol.ContentText, Text: text}
	}
	return parts, nil
}

func (c Codec) decodeHistoricalToolCalls(raw []byte, path string) ([]protocol.CanonicalToolCall, *protocol.CanonicalError) {
	items, violation := decodeArray(raw, path)
	if violation != nil {
		return nil, invalidRequest(violation.path, violation.rule)
	}
	calls := make([]protocol.CanonicalToolCall, len(items))
	for index, item := range items {
		callPath := formatArrayPath(path, index)
		object, violation := decodeObject(item, callPath)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if violation := rejectUnknownFields(object, callPath, map[string]struct{}{"id": {}, "type": {}, "function": {}}); violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		id, violation := decodeRequiredString(object, "id", callPath)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		kind, violation := decodeRequiredString(object, "type", callPath)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if kind != "function" {
			return nil, unsupported(callPath+".type", "only function tool calls are supported")
		}
		functionRaw, present := object["function"]
		if !present {
			return nil, invalidRequest(callPath+".function", "is required")
		}
		function, violation := decodeObject(functionRaw, callPath+".function")
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if violation := rejectUnknownFields(function, callPath+".function", map[string]struct{}{"name": {}, "arguments": {}}); violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		name, violation := decodeRequiredString(function, "name", callPath+".function")
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		arguments, violation := decodeRequiredString(function, "arguments", callPath+".function")
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		calls[index] = protocol.CanonicalToolCall{ID: id, Name: name, Arguments: arguments}
	}
	return calls, nil
}
