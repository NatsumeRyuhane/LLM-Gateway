package protocol

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// ValidatedChatResponse is an immutable, fully checked buffered response.
type ValidatedChatResponse struct {
	response CanonicalChatResponse
}

// Canonical returns a deep copy of the validated response.
func (r ValidatedChatResponse) Canonical() CanonicalChatResponse {
	return cloneResponse(r.response)
}

// ValidateChatResponse validates a complete buffered response before public
// status or body commitment.
func ValidateChatResponse(response CanonicalChatResponse, request ValidatedChatRequest) (ValidatedChatResponse, *CanonicalError) {
	limits := request.limits
	if err := validateProtocolIdentifier(response.ResponseID, limits.MaxIdentifierBytes, "response_id"); err != nil {
		return ValidatedChatResponse{}, err
	}
	if response.RequestID != request.request.RequestID {
		return ValidatedChatResponse{}, responseFailure(FailureProtocolInvalidJSON, "request_id", "must match the canonical request")
	}
	if err := validateProtocolIdentifier(response.AttemptID, limits.MaxIdentifierBytes, "attempt_id"); err != nil {
		return ValidatedChatResponse{}, err
	}
	if err := validateProtocolIdentifier(response.RouteID, limits.MaxIdentifierBytes, "route_id"); err != nil {
		return ValidatedChatResponse{}, err
	}
	if response.Model != request.request.Target {
		return ValidatedChatResponse{}, responseFailure(FailureProtocolInvalidJSON, "model", "must preserve the requested target")
	}
	if response.CreatedAt.IsZero() {
		return ValidatedChatResponse{}, responseFailure(FailureProtocolInvalidJSON, "created_at", "must be set")
	}
	if err := validateFinishReason(response.FinishReason, "finish_reason", false, false); err != nil {
		return ValidatedChatResponse{}, err
	}
	if err := validateResponseMessage(response.Message, response.FinishReason, request); err != nil {
		return ValidatedChatResponse{}, err
	}
	if usage, present := response.Usage.Get(); present {
		if violation := validateUsageValue(usage, "usage"); violation != nil {
			return ValidatedChatResponse{}, responseFailure(FailureProtocolUsageInconsistent, violation.path, violation.rule)
		}
		if usage.Partial {
			return ValidatedChatResponse{}, responseFailure(FailureProtocolUsageInconsistent, "usage.partial", "must be false for a buffered response")
		}
	}
	return ValidatedChatResponse{response: cloneResponse(response)}, nil
}

func validateResponseMessage(message CanonicalMessage, finish FinishReason, request ValidatedChatRequest) *CanonicalError {
	limits := request.limits
	if message.Role != RoleAssistant {
		return responseFailure(FailureProtocolInvalidJSON, "message.role", "must be assistant")
	}
	if message.Name.IsSet() || message.ToolCallID.IsSet() {
		return responseFailure(FailureProtocolInvalidJSON, "message", "contains request-only message fields")
	}
	textBytes := 0
	for index, part := range message.Content {
		if part.Type != ContentText || part.Text == "" || !utf8.ValidString(part.Text) {
			return responseFailure(FailureProtocolInvalidJSON, fmt.Sprintf("message.content[%d]", index), "must be non-empty text")
		}
		textBytes += len(part.Text)
		if textBytes > limits.MaxResponseTextBytes {
			return responseFailure(FailureUpstreamResponseTooLarge, "message.content", "exceeds the response-text limit")
		}
	}
	refusal, hasRefusal := message.Refusal.Get()
	if hasRefusal {
		if refusal == "" || !utf8.ValidString(refusal) {
			return responseFailure(FailureProtocolInvalidJSON, "message.refusal", "must be non-empty valid UTF-8")
		}
		textBytes += len(refusal)
		if textBytes > limits.MaxResponseTextBytes {
			return responseFailure(FailureUpstreamResponseTooLarge, "message.refusal", "exceeds the response-text limit")
		}
	}
	if len(message.ToolCalls) > limits.MaxToolCallsPerMessage {
		return responseFailure(FailureUpstreamResponseTooLarge, "message.tool_calls", "exceeds the tool-call limit")
	}
	if parallel, explicit := request.request.ParallelToolCalls.Get(); explicit && !parallel && len(message.ToolCalls) > 1 {
		return responseFailure(FailureProtocolInvalidToolCall, "message.tool_calls", "multiple calls violate explicit parallel_tool_calls false")
	}
	seenIDs := make(map[string]struct{}, len(message.ToolCalls))
	for index, call := range message.ToolCalls {
		path := fmt.Sprintf("message.tool_calls[%d]", index)
		if err := validateProtocolIdentifier(call.ID, limits.MaxIdentifierBytes, path+".id"); err != nil {
			return err
		}
		if _, duplicate := seenIDs[call.ID]; duplicate {
			return responseFailure(FailureProtocolInvalidToolCall, path+".id", "must be unique")
		}
		seenIDs[call.ID] = struct{}{}
		if !validCanonicalName(call.Name, limits.MaxToolNameBytes) {
			return responseFailure(FailureProtocolInvalidToolCall, path+".name", "is invalid")
		}
		schema, declared := request.toolSchemas[call.Name]
		if !declared {
			return responseFailure(FailureProtocolInvalidToolCall, path+".name", "must name a declared function")
		}
		arguments, violation := decodeBoundedJSON([]byte(call.Arguments), limits.MaxToolArgumentsBytes, limits.MaxSchemaDepth, path+".arguments")
		if violation != nil {
			return responseFailure(FailureProtocolInvalidToolCall, violation.path, violation.rule)
		}
		if _, ok := arguments.(map[string]any); !ok {
			return responseFailure(FailureProtocolInvalidToolCall, path+".arguments", "must be a JSON object")
		}
		if violation := validateJSONAgainstSchema(arguments, schema, path+".arguments"); violation != nil {
			return responseFailure(FailureProtocolInvalidToolCall, violation.path, violation.rule)
		}
	}
	if len(message.ToolCalls) > 0 && finish != FinishToolCalls {
		return responseFailure(FailureProtocolInvalidEventOrder, "finish_reason", "must be tool_calls when tool calls are present")
	}
	if len(message.ToolCalls) == 0 && finish == FinishToolCalls {
		return responseFailure(FailureProtocolInvalidEventOrder, "finish_reason", "requires at least one tool call")
	}
	if len(message.Content) == 0 && !hasRefusal && len(message.ToolCalls) == 0 {
		return responseFailure(FailureProtocolEmptyOutput, "message", "must contain text, refusal, or tool calls")
	}
	if len(message.Content) > 0 && len(message.ToolCalls) == 0 {
		if err := validateStructuredText(joinContent(message.Content), request, false, false); err != nil {
			return err
		}
	}
	return nil
}

func validateStructuredText(text string, request ValidatedChatRequest, outputVisible, toolActionable bool) *CanonicalError {
	format := request.request.ResponseFormat.Kind
	if format == ResponseFormatText {
		return nil
	}
	value, violation := decodeBoundedJSON([]byte(text), request.limits.MaxResponseTextBytes, request.limits.MaxSchemaDepth, "message.content")
	if violation != nil {
		return protocolFailure(FailureProtocolInvalidStructured, "The upstream structured output is invalid.", violation.path, violation.rule, outputVisible, toolActionable)
	}
	if format == ResponseFormatJSONObject {
		if _, ok := value.(map[string]any); !ok {
			return protocolFailure(FailureProtocolInvalidStructured, "The upstream structured output is invalid.", "message.content", "must be a JSON object", outputVisible, toolActionable)
		}
		return nil
	}
	if violation := validateJSONAgainstSchema(value, request.responseSchema, "message.content"); violation != nil {
		return protocolFailure(FailureProtocolInvalidStructured, "The upstream structured output is invalid.", violation.path, violation.rule, outputVisible, toolActionable)
	}
	return nil
}

func validateUsageValue(usage CanonicalUsage, path string) *schemaViolation {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 {
		return &schemaViolation{path: path, rule: "token counts must be non-negative"}
	}
	if usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return &schemaViolation{path: path + ".total_tokens", rule: "must equal input plus output tokens"}
	}
	if cached, present := usage.CachedTokens.Get(); present && (cached < 0 || cached > usage.InputTokens) {
		return &schemaViolation{path: path + ".input_details.cached_tokens", rule: "must be within input tokens"}
	}
	if reasoning, present := usage.ReasoningTokens.Get(); present && (reasoning < 0 || reasoning > usage.OutputTokens) {
		return &schemaViolation{path: path + ".output_details.reasoning_tokens", rule: "must be within output tokens"}
	}
	switch usage.Provenance {
	case UsageProviderReported, UsageGatewayEstimated:
	case UsageUnavailable:
		if usage.InputTokens != 0 || usage.OutputTokens != 0 || usage.CachedTokens.IsSet() || usage.ReasoningTokens.IsSet() {
			return &schemaViolation{path: path, rule: "unavailable usage cannot contain token counts"}
		}
	default:
		return &schemaViolation{path: path + ".provenance", rule: "is not recognized"}
	}
	return nil
}

func validateUsageMonotonic(previous, next CanonicalUsage, path string) *schemaViolation {
	if violation := validateUsageValue(next, path); violation != nil {
		return violation
	}
	if next.InputTokens < previous.InputTokens || next.OutputTokens < previous.OutputTokens || next.TotalTokens < previous.TotalTokens {
		return &schemaViolation{path: path, rule: "must not reduce token counts"}
	}
	if violation := optionalCountMonotonic(previous.CachedTokens, next.CachedTokens, path+".input_details.cached_tokens"); violation != nil {
		return violation
	}
	if violation := optionalCountMonotonic(previous.ReasoningTokens, next.ReasoningTokens, path+".output_details.reasoning_tokens"); violation != nil {
		return violation
	}
	if previous.Provenance != UsageUnavailable && next.Provenance != previous.Provenance {
		return &schemaViolation{path: path + ".provenance", rule: "must not change once available"}
	}
	if !previous.Partial && next.Partial {
		return &schemaViolation{path: path + ".partial", rule: "must not return to partial after final usage"}
	}
	return nil
}

func optionalCountMonotonic(previous, next Optional[int64], path string) *schemaViolation {
	oldValue, oldPresent := previous.Get()
	newValue, newPresent := next.Get()
	if oldPresent && (!newPresent || newValue < oldValue) {
		return &schemaViolation{path: path, rule: "must not disappear or decrease"}
	}
	return nil
}

func validateFinishReason(reason FinishReason, path string, outputVisible, toolActionable bool) *CanonicalError {
	switch reason {
	case FinishStop, FinishLength, FinishToolCalls, FinishContentFilter:
		return nil
	default:
		return protocolFailure(FailureProtocolInvalidEventOrder, "The upstream response has invalid termination semantics.", path, "contains an unrecognized finish reason", outputVisible, toolActionable)
	}
}

func validateProtocolIdentifier(value string, maximum int, path string) *CanonicalError {
	if value == "" || !utf8.ValidString(value) || len(value) > maximum {
		return responseFailure(FailureProtocolInvalidJSON, path, "must be non-empty valid UTF-8 within the byte limit")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return responseFailure(FailureProtocolInvalidJSON, path, "must not contain control characters")
		}
	}
	return nil
}

func responseFailure(code FailureCode, path, rule string) *CanonicalError {
	message := "The upstream response is invalid."
	if code == FailureProtocolUsageInconsistent {
		message = "The upstream usage is inconsistent."
	}
	return protocolFailure(code, message, path, rule, false, false)
}

func validCanonicalName(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maximum && canonicalNamePattern.MatchString(value)
}

func joinContent(parts []CanonicalContentPart) string {
	var output strings.Builder
	for _, part := range parts {
		output.WriteString(part.Text)
	}
	return output.String()
}

func cloneResponse(response CanonicalChatResponse) CanonicalChatResponse {
	cloned := response
	cloned.Message.Content = append([]CanonicalContentPart(nil), response.Message.Content...)
	cloned.Message.ToolCalls = append([]CanonicalToolCall(nil), response.Message.ToolCalls...)
	return cloned
}
