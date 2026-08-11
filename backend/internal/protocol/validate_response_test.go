package protocol

import (
	"testing"
	"time"
)

func TestValidateChatResponseKeepsTextAndRefusalDistinct(t *testing.T) {
	t.Parallel()

	request := mustValidateRequest(t, validRequest())
	response := validResponse(request)
	response.Message.Content = []CanonicalContentPart{{Type: ContentText, Text: "ordinary output"}}
	response.Message.Refusal = Some("policy refusal")
	response.Usage = Some(CanonicalUsage{
		InputTokens: 5, OutputTokens: 3, TotalTokens: 8,
		CachedTokens: Some(int64(2)), Provenance: UsageProviderReported,
	})

	validated, err := ValidateChatResponse(response, request)
	if err != nil {
		t.Fatalf("ValidateChatResponse() error = %v", err)
	}
	canonical := validated.Canonical()
	if got := canonical.Message.Content[0].Text; got != "ordinary output" {
		t.Fatalf("content = %q", got)
	}
	if got, present := canonical.Message.Refusal.Get(); !present || got != "policy refusal" {
		t.Fatalf("refusal = (%q, %v)", got, present)
	}
}

func TestValidateChatResponseValidatesStructuredOutput(t *testing.T) {
	t.Parallel()

	requestValue := validRequest()
	requestValue.ResponseFormat = ResponseFormat{
		Kind:   ResponseFormatJSONSchema,
		Schema: NewJSONSchema([]byte(`{"type":"object","properties":{"answer":{"type":"integer","minimum":1}},"required":["answer"],"additionalProperties":false}`)),
		Strict: Some(true),
	}
	request := mustValidateRequest(t, requestValue)

	response := validResponse(request)
	response.Message.Content[0].Text = `{"answer":2}`
	if _, err := ValidateChatResponse(response, request); err != nil {
		t.Fatalf("valid structured response error = %v", err)
	}

	response.Message.Content[0].Text = `{"answer":0}`
	_, err := ValidateChatResponse(response, request)
	if err == nil || err.Code != FailureProtocolInvalidStructured || err.RetryDisposition != RetryPreOutputAlternate {
		t.Fatalf("invalid structured response error = %#v", err)
	}
}

func TestValidateChatResponseValidatesToolsAndUsage(t *testing.T) {
	t.Parallel()

	requestValue := validRequest()
	requestValue.Tools = []CanonicalFunctionTool{{
		Name:       "weather",
		Parameters: NewJSONSchema([]byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)),
	}}
	requestValue.ToolChoice = ToolChoice{Kind: ToolChoiceAuto}
	request := mustValidateRequest(t, requestValue)

	response := validResponse(request)
	response.Message.Content = nil
	response.Message.ToolCalls = []CanonicalToolCall{{ID: "call-1", Name: "weather", Arguments: `{"city":"Shanghai"}`}}
	response.FinishReason = FinishToolCalls
	if _, err := ValidateChatResponse(response, request); err != nil {
		t.Fatalf("valid tool response error = %v", err)
	}

	tests := []struct {
		name string
		edit func(*CanonicalChatResponse)
		code FailureCode
	}{
		{"schema mismatch", func(r *CanonicalChatResponse) { r.Message.ToolCalls[0].Arguments = `{"unknown":true}` }, FailureProtocolInvalidToolCall},
		{"unknown function", func(r *CanonicalChatResponse) { r.Message.ToolCalls[0].Name = "missing" }, FailureProtocolInvalidToolCall},
		{"wrong finish", func(r *CanonicalChatResponse) { r.FinishReason = FinishStop }, FailureProtocolInvalidEventOrder},
		{"duplicate tool call id", func(r *CanonicalChatResponse) {
			r.Message.ToolCalls = append(r.Message.ToolCalls, r.Message.ToolCalls[0])
		}, FailureProtocolInvalidToolCall},
		{"empty output", func(r *CanonicalChatResponse) {
			r.Message.ToolCalls = nil
			r.FinishReason = FinishStop
		}, FailureProtocolEmptyOutput},
		{"usage sum", func(r *CanonicalChatResponse) {
			r.Usage = Some(CanonicalUsage{InputTokens: 2, OutputTokens: 2, TotalTokens: 3, Provenance: UsageProviderReported})
		}, FailureProtocolUsageInconsistent},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := cloneResponse(response)
			test.edit(&candidate)
			_, err := ValidateChatResponse(candidate, request)
			if err == nil || err.Code != test.code {
				t.Fatalf("ValidateChatResponse() error = %#v, want %s", err, test.code)
			}
		})
	}
}

func TestValidateChatResponseRejectsParallelCallsAndOversizedText(t *testing.T) {
	t.Parallel()

	requestValue := validRequest()
	requestValue.Tools = []CanonicalFunctionTool{{Name: "tool", Parameters: NewJSONSchema([]byte(`{"type":"object"}`))}}
	requestValue.ToolChoice = ToolChoice{Kind: ToolChoiceAuto}
	requestValue.ParallelToolCalls = Some(false)
	request := mustValidateRequest(t, requestValue)
	response := validResponse(request)
	response.Message.Content = nil
	response.Message.ToolCalls = []CanonicalToolCall{
		{ID: "call-1", Name: "tool", Arguments: `{}`},
		{ID: "call-2", Name: "tool", Arguments: `{}`},
	}
	response.FinishReason = FinishToolCalls
	_, err := ValidateChatResponse(response, request)
	if err == nil || err.Code != FailureProtocolInvalidToolCall {
		t.Fatalf("parallel false error = %#v", err)
	}

	limits := DefaultLimits()
	limits.MaxResponseTextBytes = 4
	textRequest, requestErr := ValidateChatRequest(validRequest(), limits)
	if requestErr != nil {
		t.Fatalf("ValidateChatRequest() error = %v", requestErr)
	}
	textResponse := validResponse(textRequest)
	_, err = ValidateChatResponse(textResponse, textRequest)
	if err == nil || err.Code != FailureUpstreamResponseTooLarge || err.Domain != DomainUpstream || err.HTTPStatus != 502 {
		t.Fatalf("oversized response error = %#v", err)
	}
}

func mustValidateRequest(t *testing.T, request CanonicalChatRequest) ValidatedChatRequest {
	t.Helper()
	validated, err := ValidateChatRequest(request, DefaultLimits())
	if err != nil {
		t.Fatalf("ValidateChatRequest() error = %v", err)
	}
	return validated
}

func validResponse(request ValidatedChatRequest) CanonicalChatResponse {
	return CanonicalChatResponse{
		ResponseID: "response-1",
		RequestID:  request.request.RequestID,
		AttemptID:  "attempt-1",
		RouteID:    "route-1",
		Model:      request.request.Target,
		CreatedAt:  time.Unix(1_800_000_001, 0),
		Message: CanonicalMessage{
			Role:    RoleAssistant,
			Content: []CanonicalContentPart{{Type: ContentText, Text: "hello"}},
		},
		FinishReason: FinishStop,
	}
}
