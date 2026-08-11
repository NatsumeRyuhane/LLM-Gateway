package openai

import (
	"encoding/json"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

func (c Codec) decodeTools(raw []byte) ([]protocol.CanonicalFunctionTool, *protocol.CanonicalError) {
	items, violation := decodeArray(raw, "tools")
	if violation != nil {
		return nil, invalidRequest(violation.path, violation.rule)
	}
	tools := make([]protocol.CanonicalFunctionTool, len(items))
	for index, item := range items {
		path := formatArrayPath("tools", index)
		object, violation := decodeObject(item, path)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if violation := rejectUnknownFields(object, path, map[string]struct{}{"type": {}, "function": {}}); violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		kind, violation := decodeRequiredString(object, "type", path)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if kind != "function" {
			return nil, unsupported(path+".type", "only function tools are supported")
		}
		functionRaw, present := object["function"]
		if !present {
			return nil, invalidRequest(path+".function", "is required")
		}
		function, violation := decodeObject(functionRaw, path+".function")
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		functionPath := path + ".function"
		if violation := rejectUnknownFields(function, functionPath, map[string]struct{}{"name": {}, "description": {}, "parameters": {}, "strict": {}}); violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		name, violation := decodeRequiredString(function, "name", functionPath)
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		parameters, present := function["parameters"]
		if !present {
			return nil, invalidRequest(functionPath+".parameters", "is required")
		}
		tool := protocol.CanonicalFunctionTool{Name: name, Parameters: protocol.NewJSONSchema(parameters)}
		if descriptionRaw, present := function["description"]; present {
			var description string
			if err := json.Unmarshal(descriptionRaw, &description); err != nil {
				return nil, invalidRequest(functionPath+".description", "must be a string")
			}
			tool.Description = protocol.Some(description)
		}
		if strictRaw, present := function["strict"]; present {
			strict, err := decodeBool(strictRaw, functionPath+".strict")
			if err != nil {
				return nil, err
			}
			tool.Strict = protocol.Some(strict)
		}
		tools[index] = tool
	}
	return tools, nil
}

func decodeToolChoice(raw []byte) (protocol.ToolChoice, *protocol.CanonicalError) {
	var simple string
	if err := json.Unmarshal(raw, &simple); err == nil {
		switch simple {
		case "none":
			return protocol.ToolChoice{Kind: protocol.ToolChoiceNone}, nil
		case "auto":
			return protocol.ToolChoice{Kind: protocol.ToolChoiceAuto}, nil
		case "required":
			return protocol.ToolChoice{Kind: protocol.ToolChoiceRequired}, nil
		default:
			return protocol.ToolChoice{}, unsupported("tool_choice", "contains an unsupported selection mode")
		}
	}
	object, violation := decodeObject(raw, "tool_choice")
	if violation != nil {
		return protocol.ToolChoice{}, invalidRequest(violation.path, "must be none, auto, required, or a named function object")
	}
	if violation := rejectUnknownFields(object, "tool_choice", map[string]struct{}{"type": {}, "function": {}}); violation != nil {
		return protocol.ToolChoice{}, invalidRequest(violation.path, violation.rule)
	}
	kind, violation := decodeRequiredString(object, "type", "tool_choice")
	if violation != nil {
		return protocol.ToolChoice{}, invalidRequest(violation.path, violation.rule)
	}
	if kind != "function" {
		return protocol.ToolChoice{}, unsupported("tool_choice.type", "only named function selection is supported")
	}
	functionRaw, present := object["function"]
	if !present {
		return protocol.ToolChoice{}, invalidRequest("tool_choice.function", "is required")
	}
	function, violation := decodeObject(functionRaw, "tool_choice.function")
	if violation != nil {
		return protocol.ToolChoice{}, invalidRequest(violation.path, violation.rule)
	}
	if violation := rejectUnknownFields(function, "tool_choice.function", map[string]struct{}{"name": {}}); violation != nil {
		return protocol.ToolChoice{}, invalidRequest(violation.path, violation.rule)
	}
	name, violation := decodeRequiredString(function, "name", "tool_choice.function")
	if violation != nil {
		return protocol.ToolChoice{}, invalidRequest(violation.path, violation.rule)
	}
	return protocol.ToolChoice{Kind: protocol.ToolChoiceSpecific, FunctionName: name}, nil
}

func decodeResponseFormat(raw []byte) (protocol.ResponseFormat, *protocol.CanonicalError) {
	object, violation := decodeObject(raw, "response_format")
	if violation != nil {
		return protocol.ResponseFormat{}, invalidRequest(violation.path, violation.rule)
	}
	if violation := rejectUnknownFields(object, "response_format", map[string]struct{}{"type": {}, "json_schema": {}}); violation != nil {
		return protocol.ResponseFormat{}, invalidRequest(violation.path, violation.rule)
	}
	kind, violation := decodeRequiredString(object, "type", "response_format")
	if violation != nil {
		return protocol.ResponseFormat{}, invalidRequest(violation.path, violation.rule)
	}
	switch kind {
	case "text":
		if _, present := object["json_schema"]; present {
			return protocol.ResponseFormat{}, invalidRequest("response_format.json_schema", "is valid only for json_schema format")
		}
		return protocol.ResponseFormat{Kind: protocol.ResponseFormatText}, nil
	case "json_object":
		if _, present := object["json_schema"]; present {
			return protocol.ResponseFormat{}, invalidRequest("response_format.json_schema", "is valid only for json_schema format")
		}
		return protocol.ResponseFormat{Kind: protocol.ResponseFormatJSONObject}, nil
	case "json_schema":
		definitionRaw, present := object["json_schema"]
		if !present {
			return protocol.ResponseFormat{}, invalidRequest("response_format.json_schema", "is required")
		}
		definition, violation := decodeObject(definitionRaw, "response_format.json_schema")
		if violation != nil {
			return protocol.ResponseFormat{}, invalidRequest(violation.path, violation.rule)
		}
		if violation := rejectUnknownFields(definition, "response_format.json_schema", map[string]struct{}{"schema": {}, "strict": {}}); violation != nil {
			return protocol.ResponseFormat{}, invalidRequest(violation.path, violation.rule)
		}
		schema, present := definition["schema"]
		if !present {
			return protocol.ResponseFormat{}, invalidRequest("response_format.json_schema.schema", "is required")
		}
		format := protocol.ResponseFormat{Kind: protocol.ResponseFormatJSONSchema, Schema: protocol.NewJSONSchema(schema)}
		if strictRaw, present := definition["strict"]; present {
			strict, err := decodeBool(strictRaw, "response_format.json_schema.strict")
			if err != nil {
				return protocol.ResponseFormat{}, err
			}
			format.Strict = protocol.Some(strict)
		}
		return format, nil
	default:
		return protocol.ResponseFormat{}, unsupported("response_format.type", "contains an unsupported response format")
	}
}
