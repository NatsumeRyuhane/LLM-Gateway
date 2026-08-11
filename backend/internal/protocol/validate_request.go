package protocol

import (
	"fmt"
	"math"
	"regexp"
	"unicode"
	"unicode/utf8"
)

var canonicalNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidatedChatRequest is an immutable snapshot for routing and adapter use.
// Canonical and RequiredCapabilities return copies.
type ValidatedChatRequest struct {
	request        CanonicalChatRequest
	required       []Capability
	toolSchemas    map[string]map[string]any
	responseSchema map[string]any
	limits         Limits
}

// Canonical returns a deep copy of the validated canonical request.
func (r ValidatedChatRequest) Canonical() CanonicalChatRequest {
	return cloneRequest(r.request)
}

// RequiredCapabilities returns the immutable, deterministically sorted set
// derived from explicit request semantics.
func (r ValidatedChatRequest) RequiredCapabilities() []Capability {
	return append([]Capability(nil), r.required...)
}

// Limits returns the validated bounds associated with this request.
func (r ValidatedChatRequest) Limits() Limits { return r.limits }

// ValidateChatRequest validates and snapshots a canonical request. Callers must
// not route or dispatch the unvalidated input value.
func ValidateChatRequest(request CanonicalChatRequest, limits Limits) (ValidatedChatRequest, *CanonicalError) {
	if err := limits.validate(); err != nil {
		return ValidatedChatRequest{}, err
	}
	if request.ContractVersion != ContractVersion {
		return ValidatedChatRequest{}, invalidRequest("contract_version", "must equal gateway.adapter.v0")
	}
	if err := validateOpaqueIdentifier(request.RequestID, limits.MaxIdentifierBytes, "request_id"); err != nil {
		return ValidatedChatRequest{}, err
	}
	if err := validateOpaqueIdentifier(request.Target, limits.MaxTargetBytes, "target"); err != nil {
		return ValidatedChatRequest{}, err
	}
	if request.Deadline.IsZero() {
		return ValidatedChatRequest{}, invalidRequest("deadline", "must be set")
	}
	if len(request.Messages) == 0 || len(request.Messages) > limits.MaxMessages {
		return ValidatedChatRequest{}, invalidRequest("messages", "must contain a bounded non-empty message list")
	}
	if len(request.Tools) > limits.MaxTools {
		return ValidatedChatRequest{}, invalidRequest("tools", "exceeds the tool-count limit")
	}

	required := make(capabilitySet)
	if request.Stream {
		required.add(CapabilityEndpointStreaming)
	} else {
		required.add(CapabilityEndpointBuffered)
	}
	toolSchemas, err := validateFunctionTools(request.Tools, limits, required)
	if err != nil {
		return ValidatedChatRequest{}, err
	}
	if err := validateToolChoice(request.ToolChoice, request.Tools, required); err != nil {
		return ValidatedChatRequest{}, err
	}
	if _, explicit := request.ParallelToolCalls.Get(); explicit {
		if len(request.Tools) == 0 {
			return ValidatedChatRequest{}, invalidRequest("parallel_tool_calls", "requires function tools")
		}
		required.add(CapabilityToolsParallel)
	}

	responseSchema, err := validateResponseFormat(request.ResponseFormat, request.Stream, limits, required)
	if err != nil {
		return ValidatedChatRequest{}, err
	}
	if err := validateSampling(request.Sampling, limits, required); err != nil {
		return ValidatedChatRequest{}, err
	}
	if value, explicit := request.MaxOutputTokens.Get(); explicit {
		if value <= 0 || value > limits.MaxOutputTokens {
			return ValidatedChatRequest{}, invalidRequest("max_output_tokens", "must be within the configured positive bound")
		}
		required.add(CapabilityParameterMaxOutputTokens)
	}
	if request.IncludeUsage {
		if request.Stream {
			required.add(CapabilityUsageStreaming)
		} else {
			required.add(CapabilityUsageBuffered)
		}
	}
	if err := validateOptionalAttribution(request.Attribution.ConversationID, "attribution.conversation_id", limits); err != nil {
		return ValidatedChatRequest{}, err
	}
	if err := validateOptionalAttribution(request.Attribution.RunID, "attribution.run_id", limits); err != nil {
		return ValidatedChatRequest{}, err
	}
	if err := validateRequestMessages(request.Messages, toolSchemas, limits, required); err != nil {
		return ValidatedChatRequest{}, err
	}

	return ValidatedChatRequest{
		request:        cloneRequest(request),
		required:       required.sorted(),
		toolSchemas:    toolSchemas,
		responseSchema: responseSchema,
		limits:         limits,
	}, nil
}

func validateFunctionTools(tools []CanonicalFunctionTool, limits Limits, required capabilitySet) (map[string]map[string]any, *CanonicalError) {
	schemas := make(map[string]map[string]any, len(tools))
	for index, tool := range tools {
		path := fmt.Sprintf("tools[%d]", index)
		if err := validateName(tool.Name, limits.MaxToolNameBytes, path+".name"); err != nil {
			return nil, err
		}
		if _, duplicate := schemas[tool.Name]; duplicate {
			return nil, invalidRequest("tools", "function names must be unique")
		}
		if description, explicit := tool.Description.Get(); explicit {
			if !utf8.ValidString(description) || len(description) > limits.MaxToolDescriptionBytes {
				return nil, invalidRequest(path+".description", "must be valid UTF-8 within the byte limit")
			}
		}
		schema, violation := parseSchema(tool.Parameters, limits, path+".parameters")
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		if !schemaAllowsObject(schema) {
			return nil, invalidRequest(path+".parameters", "must allow a JSON object")
		}
		schemas[tool.Name] = schema
		required.add(CapabilityToolsFunction)
		if strict, explicit := tool.Strict.Get(); explicit && strict {
			required.add(CapabilityToolsFunctionSchemaStrict)
		}
	}
	return schemas, nil
}

func validateToolChoice(choice ToolChoice, tools []CanonicalFunctionTool, required capabilitySet) *CanonicalError {
	if len(tools) == 0 && choice.Kind != ToolChoiceNone {
		return invalidRequest("tool_choice", "must be none when tools are absent")
	}
	switch choice.Kind {
	case ToolChoiceNone:
		if len(tools) > 0 {
			required.add(CapabilityToolsChoiceNone)
		}
		if choice.FunctionName != "" {
			return invalidRequest("tool_choice.function", "must be empty unless a specific function is selected")
		}
	case ToolChoiceAuto:
		required.add(CapabilityToolsChoiceAuto)
		if choice.FunctionName != "" {
			return invalidRequest("tool_choice.function", "must be empty unless a specific function is selected")
		}
	case ToolChoiceRequired:
		required.add(CapabilityToolsChoiceRequired)
		if choice.FunctionName != "" {
			return invalidRequest("tool_choice.function", "must be empty unless a specific function is selected")
		}
	case ToolChoiceSpecific:
		required.add(CapabilityToolsChoiceSpecific)
		matched := false
		for _, tool := range tools {
			matched = matched || tool.Name == choice.FunctionName
		}
		if !matched {
			return invalidRequest("tool_choice.function", "must name a declared function")
		}
	default:
		return invalidRequest("tool_choice", "contains an unsupported selection mode")
	}
	return nil
}

func validateResponseFormat(format ResponseFormat, stream bool, limits Limits, required capabilitySet) (map[string]any, *CanonicalError) {
	switch format.Kind {
	case ResponseFormatText:
		if len(format.Schema.raw) != 0 || format.Strict.IsSet() {
			return nil, invalidRequest("response_format", "text format cannot carry a schema or strict flag")
		}
	case ResponseFormatJSONObject:
		if len(format.Schema.raw) != 0 || format.Strict.IsSet() {
			return nil, invalidRequest("response_format", "json_object format cannot carry a schema or strict flag")
		}
		required.add(CapabilityStructuredJSONObject)
		if stream {
			required.add(CapabilityStructuredStreaming)
		}
	case ResponseFormatJSONSchema:
		schema, violation := parseSchema(format.Schema, limits, "response_format.schema")
		if violation != nil {
			return nil, invalidRequest(violation.path, violation.rule)
		}
		required.add(CapabilityStructuredJSONSchema)
		if strict, explicit := format.Strict.Get(); explicit && strict {
			required.add(CapabilityStructuredJSONStrict)
		}
		if stream {
			required.add(CapabilityStructuredStreaming)
		}
		return schema, nil
	default:
		return nil, invalidRequest("response_format", "contains an unsupported format")
	}
	return nil, nil
}

func validateSampling(sampling SamplingParameters, limits Limits, required capabilitySet) *CanonicalError {
	if value, explicit := sampling.Temperature.Get(); explicit {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 2 {
			return invalidRequest("sampling.temperature", "must be finite and between 0 and 2")
		}
		required.add(CapabilityParameterTemperature)
	}
	if value, explicit := sampling.TopP.Get(); explicit {
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 {
			return invalidRequest("sampling.top_p", "must be finite and between 0 and 1")
		}
		required.add(CapabilityParameterTopP)
	}
	if sampling.Seed.IsSet() {
		required.add(CapabilityParameterSeed)
	}
	if stop, explicit := sampling.Stop.Get(); explicit {
		if len(stop) == 0 || len(stop) > limits.MaxStopSequences {
			return invalidRequest("sampling.stop", "must contain a bounded non-empty sequence list")
		}
		for _, sequence := range stop {
			if sequence == "" || !utf8.ValidString(sequence) || len(sequence) > limits.MaxStopSequenceBytes {
				return invalidRequest("sampling.stop", "contains an invalid or oversized sequence")
			}
		}
		required.add(CapabilityParameterStop)
	}
	return nil
}

func validateRequestMessages(messages []CanonicalMessage, toolSchemas map[string]map[string]any, limits Limits, required capabilitySet) *CanonicalError {
	outstanding := make(map[string]struct{})
	totalBytes := 0
	for index, message := range messages {
		path := fmt.Sprintf("messages[%d]", index)
		roleCapability, ok := roleCapability(message.Role)
		if !ok {
			return invalidRequest(path+".role", "contains an unsupported role")
		}
		required.add(roleCapability)
		messageBytes := 0
		if name, explicit := message.Name.Get(); explicit {
			if message.Role == RoleTool {
				return invalidRequest(path+".name", "is not supported for tool messages")
			}
			if err := validateName(name, limits.MaxParticipantNameBytes, path+".name"); err != nil {
				return err
			}
			messageBytes += len(name)
			required.add(CapabilityParticipantName)
		}
		if message.Refusal.IsSet() {
			return invalidRequest(path+".refusal", "refusal history is deferred in v0")
		}
		if len(message.Content) > limits.MaxContentParts {
			return invalidRequest(path+".content", "exceeds the content-part limit")
		}
		for partIndex, part := range message.Content {
			if part.Type != ContentText {
				return invalidRequest(fmt.Sprintf("%s.content[%d].type", path, partIndex), "only text content is supported")
			}
			if part.Text == "" || !utf8.ValidString(part.Text) {
				return invalidRequest(fmt.Sprintf("%s.content[%d].text", path, partIndex), "must be non-empty valid UTF-8")
			}
			messageBytes += len(part.Text)
			required.add(CapabilityContentText)
		}
		if len(message.ToolCalls) > limits.MaxToolCallsPerMessage {
			return invalidRequest(path+".tool_calls", "exceeds the tool-call limit")
		}
		if len(message.ToolCalls) > 0 {
			if message.Role != RoleAssistant {
				return invalidRequest(path+".tool_calls", "is valid only for assistant messages")
			}
			required.add(CapabilityToolsFunction)
			for callIndex, call := range message.ToolCalls {
				callPath := fmt.Sprintf("%s.tool_calls[%d]", path, callIndex)
				messageBytes += len(call.ID) + len(call.Name) + len(call.Arguments)
				if messageBytes > limits.MaxMessageBytes {
					return invalidRequest(path, "exceeds the per-message byte limit")
				}
				if totalBytes+messageBytes > limits.MaxRequestContentBytes {
					return invalidRequest("messages", "exceeds the request-content byte limit")
				}
				if err := validateOpaqueIdentifier(call.ID, limits.MaxIdentifierBytes, callPath+".id"); err != nil {
					return err
				}
				if _, duplicate := outstanding[call.ID]; duplicate {
					return invalidRequest(callPath+".id", "must be unique among unresolved calls")
				}
				if err := validateName(call.Name, limits.MaxToolNameBytes, callPath+".name"); err != nil {
					return err
				}
				arguments, violation := decodeBoundedJSON([]byte(call.Arguments), limits.MaxToolArgumentsBytes, limits.MaxSchemaDepth, callPath+".arguments")
				if violation != nil {
					return invalidRequest(violation.path, violation.rule)
				}
				if _, ok := arguments.(map[string]any); !ok {
					return invalidRequest(callPath+".arguments", "must be a JSON object")
				}
				if schema, ok := toolSchemas[call.Name]; ok {
					if violation := validateJSONAgainstSchema(arguments, schema, callPath+".arguments"); violation != nil {
						return invalidRequest(violation.path, violation.rule)
					}
				}
				outstanding[call.ID] = struct{}{}
			}
		}
		toolCallID, hasToolCallID := message.ToolCallID.Get()
		if message.Role == RoleTool {
			if !hasToolCallID {
				return invalidRequest(path+".tool_call_id", "is required for tool messages")
			}
			if err := validateOpaqueIdentifier(toolCallID, limits.MaxIdentifierBytes, path+".tool_call_id"); err != nil {
				return err
			}
			if _, exists := outstanding[toolCallID]; !exists {
				return invalidRequest(path+".tool_call_id", "must reference an unresolved prior assistant tool call")
			}
			delete(outstanding, toolCallID)
			messageBytes += len(toolCallID)
			required.add(CapabilityToolsFunction)
		} else if hasToolCallID {
			return invalidRequest(path+".tool_call_id", "is valid only for tool messages")
		}
		if message.Role == RoleAssistant {
			if len(message.Content) == 0 && len(message.ToolCalls) == 0 {
				return invalidRequest(path, "assistant messages require content or tool calls")
			}
		} else if len(message.Content) == 0 {
			return invalidRequest(path+".content", "is required for this message role")
		}
		if messageBytes > limits.MaxMessageBytes {
			return invalidRequest(path, "exceeds the per-message byte limit")
		}
		totalBytes += messageBytes
		if totalBytes > limits.MaxRequestContentBytes {
			return invalidRequest("messages", "exceeds the request-content byte limit")
		}
	}
	if len(outstanding) != 0 {
		return invalidRequest("messages", "contains unresolved assistant tool calls")
	}
	return nil
}

func validateOpaqueIdentifier(value string, maximum int, path string) *CanonicalError {
	if value == "" || !utf8.ValidString(value) || len(value) > maximum {
		return invalidRequest(path, "must be non-empty valid UTF-8 within the byte limit")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return invalidRequest(path, "must not contain control characters")
		}
	}
	return nil
}

func validateOptionalAttribution(value Optional[string], path string, limits Limits) *CanonicalError {
	identifier, explicit := value.Get()
	if !explicit {
		return nil
	}
	return validateOpaqueIdentifier(identifier, limits.MaxIdentifierBytes, path)
}

func validateName(value string, maximum int, path string) *CanonicalError {
	if value == "" || !utf8.ValidString(value) || len(value) > maximum || !canonicalNamePattern.MatchString(value) {
		return invalidRequest(path, "must match the bounded canonical name syntax")
	}
	return nil
}

func roleCapability(role MessageRole) (Capability, bool) {
	switch role {
	case RoleDeveloper:
		return CapabilityRoleDeveloper, true
	case RoleSystem:
		return CapabilityRoleSystem, true
	case RoleUser:
		return CapabilityRoleUser, true
	case RoleAssistant:
		return CapabilityRoleAssistant, true
	case RoleTool:
		return CapabilityRoleTool, true
	default:
		return "", false
	}
}

func schemaAllowsObject(schema map[string]any) bool {
	typeValue, constrained := schema["type"]
	return !constrained || matchesSchemaType(map[string]any{}, typeValue)
}

func cloneRequest(request CanonicalChatRequest) CanonicalChatRequest {
	cloned := request
	cloned.Messages = make([]CanonicalMessage, len(request.Messages))
	for index, message := range request.Messages {
		cloned.Messages[index] = message
		cloned.Messages[index].Content = append([]CanonicalContentPart(nil), message.Content...)
		cloned.Messages[index].ToolCalls = append([]CanonicalToolCall(nil), message.ToolCalls...)
	}
	cloned.Tools = make([]CanonicalFunctionTool, len(request.Tools))
	for index, tool := range request.Tools {
		cloned.Tools[index] = tool
		cloned.Tools[index].Parameters = NewJSONSchema(tool.Parameters.raw)
	}
	cloned.ResponseFormat.Schema = NewJSONSchema(request.ResponseFormat.Schema.raw)
	if stop, explicit := request.Sampling.Stop.Get(); explicit {
		cloned.Sampling.Stop = Some(append([]string(nil), stop...))
	}
	return cloned
}
