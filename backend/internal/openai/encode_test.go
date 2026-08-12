package openai

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

func TestEncodeBufferedChatCompletionGolden(t *testing.T) {
	validatedRequest := mustDecodeRequest(t, `{
      "model":"agent",
      "messages":[{"role":"user","content":"hello"}],
      "tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}}}]
    }`)
	canonicalResponse := protocol.CanonicalChatResponse{
		ResponseID: "resp_1", RequestID: testMetadata.RequestID, AttemptID: "attempt_1", RouteID: "route_1",
		Model: "agent", CreatedAt: time.Unix(1_700_000_000, 0), FinishReason: protocol.FinishToolCalls,
		Message: protocol.CanonicalMessage{
			Role:      protocol.RoleAssistant,
			Content:   []protocol.CanonicalContentPart{{Type: protocol.ContentText, Text: "hel"}, {Type: protocol.ContentText, Text: "lo"}},
			Refusal:   protocol.Some("cannot comply"),
			ToolCalls: []protocol.CanonicalToolCall{{ID: "call_1", Name: "lookup", Arguments: `{"query":"x"}`}},
		},
		Usage: protocol.Some(protocol.CanonicalUsage{
			InputTokens: 12, OutputTokens: 4, TotalTokens: 16,
			CachedTokens: protocol.Some[int64](3), ReasoningTokens: protocol.Some[int64](2),
			Provenance: protocol.UsageProviderReported,
		}),
	}
	validatedResponse, validationErr := protocol.ValidateChatResponse(canonicalResponse, validatedRequest)
	if validationErr != nil {
		t.Fatalf("ValidateChatResponse() error = %v", validationErr)
	}
	encoded, encodeErr := NewCodec(protocol.DefaultLimits()).EncodeBufferedChatCompletion(validatedResponse, CorrelationVisibility{AttemptID: true, RouteID: true})
	if encodeErr != nil {
		t.Fatalf("EncodeBufferedChatCompletion() error = %v", encodeErr)
	}
	if encoded.Header.Get(HeaderRequestID) != "req_test" || encoded.Header.Get(HeaderAttemptID) != "attempt_1" || encoded.Header.Get(HeaderRouteID) != "route_1" {
		t.Fatalf("correlation headers = %#v", encoded.Header)
	}

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "tests", "conformance", "gateway.adapter.v0", "http", "buffered-response.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ContractVersion string          `json:"contract_version"`
		ExpectedBody    json.RawMessage `json:"expected_body"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, fixture.ExpectedBody); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded.Body, compact.Bytes()) {
		t.Fatalf("body = %s\nwant = %s", encoded.Body, compact.Bytes())
	}
}

func TestEncodeBufferedCorrelationVisibility(t *testing.T) {
	validatedRequest := mustDecodeRequest(t, `{"model":"agent","messages":[{"role":"user","content":"hello"}]}`)
	validatedResponse, err := protocol.ValidateChatResponse(protocol.CanonicalChatResponse{
		ResponseID: "resp", RequestID: testMetadata.RequestID, AttemptID: "attempt", RouteID: "route", Model: "agent",
		CreatedAt: time.Unix(1, 0), Message: protocol.CanonicalMessage{Role: protocol.RoleAssistant, Content: []protocol.CanonicalContentPart{{Type: protocol.ContentText, Text: "ok"}}},
		FinishReason: protocol.FinishStop,
	}, validatedRequest)
	if err != nil {
		t.Fatal(err)
	}
	encoded, encodeErr := NewCodec(protocol.DefaultLimits()).EncodeBufferedChatCompletion(validatedResponse, CorrelationVisibility{})
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if encoded.Header.Get(HeaderRequestID) == "" || encoded.Header.Get(HeaderAttemptID) != "" || encoded.Header.Get(HeaderRouteID) != "" {
		t.Fatalf("headers = %#v", encoded.Header)
	}
}

func TestEncodeErrorUsesSafeOpenAIEnvelope(t *testing.T) {
	failure := &protocol.CanonicalError{
		Code: protocol.FailureUpstreamServerError, Domain: protocol.DomainUpstream,
		RetryDisposition: protocol.RetryPreOutputAlternate, SafeMessage: "unsafe\nprovider secret",
		HTTPStatus: 502, RequestID: "req_1", AttemptID: "attempt_1", RouteID: "route_1", ProviderStatus: 503,
		Validation: &protocol.ValidationIssue{Path: "messages[0].content", Rule: "secret rejected value"},
	}
	encoded := NewCodec(protocol.DefaultLimits()).EncodeError(failure, CorrelationVisibility{AttemptID: true})
	if encoded.Status != 502 || encoded.Header.Get(HeaderRequestID) != "req_1" || encoded.Header.Get(HeaderAttemptID) != "attempt_1" || encoded.Header.Get(HeaderRouteID) != "" {
		t.Fatalf("encoded metadata = status %d headers %#v", encoded.Status, encoded.Header)
	}
	var body publicErrorEnvelope
	if err := json.Unmarshal(encoded.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Message != "The gateway could not complete the request." || body.Error.Type != "upstream_error" || body.Error.Code != string(protocol.FailureUpstreamServerError) {
		t.Fatalf("public error = %#v", body.Error)
	}
	if body.Error.Param == nil || *body.Error.Param != "messages[0].content" || bytes.Contains(encoded.Body, []byte("secret rejected value")) || bytes.Contains(encoded.Body, []byte("503")) {
		t.Fatalf("public body leaks or loses safe param: %s", encoded.Body)
	}
}

func TestEncodeErrorRejectsInventedCodesAndUnsafeCorrelation(t *testing.T) {
	failure := &protocol.CanonicalError{
		Code: "invented.secret_code", Domain: protocol.DomainUpstream, RetryDisposition: protocol.RetryNever,
		SafeMessage: "bounded", HTTPStatus: 418, RequestID: "req\nunsafe", AttemptID: "attempt\nunsafe", RouteID: "route\nunsafe",
	}
	encoded := NewCodec(protocol.DefaultLimits()).EncodeError(failure, CorrelationVisibility{AttemptID: true, RouteID: true})
	if encoded.Status != 500 || encoded.Header.Get(HeaderRequestID) != "" || encoded.Header.Get(HeaderAttemptID) != "" || encoded.Header.Get(HeaderRouteID) != "" {
		t.Fatalf("encoded status/headers = %d %#v", encoded.Status, encoded.Header)
	}
	var body publicErrorEnvelope
	if err := json.Unmarshal(encoded.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != string(protocol.FailureGatewayInternal) || body.Error.Type != "server_error" {
		t.Fatalf("error = %#v", body.Error)
	}
}

func TestEncodeErrorUsesConfiguredIdentifierLimitAndRejectsC1Controls(t *testing.T) {
	limits := protocol.DefaultLimits()
	limits.MaxIdentifierBytes = 8
	codec := NewCodec(limits)
	failure := &protocol.CanonicalError{
		Code: protocol.FailureClientInvalidRequest, Domain: protocol.DomainClient,
		RetryDisposition: protocol.RetryNever, SafeMessage: "invalid", HTTPStatus: 400,
		RequestID: "too-long-id", AttemptID: "attempt-1", RouteID: "route-1",
		Validation: &protocol.ValidationIssue{Path: "bad\u0085param", Rule: "invalid"},
	}
	encoded := codec.EncodeError(failure, CorrelationVisibility{AttemptID: true, RouteID: true})
	if encoded.Header.Get(HeaderRequestID) != "" || encoded.Header.Get(HeaderAttemptID) != "" || encoded.Header.Get(HeaderRouteID) != "route-1" {
		t.Fatalf("correlation headers = %#v", encoded.Header)
	}
	var body publicErrorEnvelope
	if err := json.Unmarshal(encoded.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Param != nil {
		t.Fatalf("param = %q, want null", *body.Error.Param)
	}
	if body.RequestID != "" {
		t.Fatalf("request_id = %q, want empty", body.RequestID)
	}
}

func TestEncodeModelsResponse(t *testing.T) {
	encoded, err := NewCodec(protocol.DefaultLimits()).EncodeModelsResponse("req_models", []Model{{ID: "general", CreatedAt: time.Unix(10, 0), OwnedBy: "gateway"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(encoded.Body); got != `{"object":"list","data":[{"id":"general","object":"model","created":10,"owned_by":"gateway"}]}` {
		t.Fatalf("body = %s", got)
	}
}

func mustDecodeRequest(t *testing.T, body string) protocol.ValidatedChatRequest {
	t.Helper()
	validated, err := NewCodec(protocol.DefaultLimits()).DecodeChatCompletions(newChatRequest(body), testMetadata)
	if err != nil {
		t.Fatalf("DecodeChatCompletions() error = %v", err)
	}
	return validated
}
