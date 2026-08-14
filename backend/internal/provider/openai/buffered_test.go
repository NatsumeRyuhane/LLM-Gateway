package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

func TestBufferedTranslatesCanonicalRequestAndValidatesResponse(t *testing.T) {
	var received chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer provider-secret" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Cookie") != "" || request.Header.Get("X-Forwarded-For") != "" || request.Header.Get("X-Gateway-Conversation-ID") != "" {
			t.Errorf("forbidden headers were forwarded: %v", request.Header)
		}
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"id":"chatcmpl-1","object":"chat.completion","created":1760000000,"model":"provider-model",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Shanghai\"}"}}]},"finish_reason":"tool_calls"}],
			"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}
		}`))
	}))
	defer server.Close()

	request := validatedToolRequest(t, false)
	route := testRoute(t, server.URL, request)
	response, failure := New().Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route)
	if failure != nil {
		t.Fatalf("Buffered() failure = %#v", failure)
	}
	canonical := response.Canonical()
	if canonical.Model != "gateway-model" || canonical.ResponseID != "chatcmpl-1" || canonical.FinishReason != protocol.FinishToolCalls {
		t.Fatalf("canonical response = %#v", canonical)
	}
	if len(canonical.Message.ToolCalls) != 1 || canonical.Message.ToolCalls[0].Arguments != `{"city":"Shanghai"}` {
		t.Fatalf("tool calls = %#v", canonical.Message.ToolCalls)
	}
	usage, ok := canonical.Usage.Get()
	if !ok || usage.TotalTokens != 14 || usage.Provenance != protocol.UsageProviderReported {
		t.Fatalf("usage = %#v, present = %v", usage, ok)
	}
	if received.Model != "provider-model" || received.Stream || len(received.Tools) != 1 || received.MaxTokens == nil || *received.MaxTokens != 256 {
		t.Fatalf("translated request = %#v", received)
	}
	if received.Messages[0].Content == nil || *received.Messages[0].Content != "Find the weather" {
		t.Fatalf("messages = %#v", received.Messages)
	}
	choice, ok := received.ToolChoice.(map[string]any)
	if !ok || choice["type"] != "function" {
		t.Fatalf("tool choice = %#v", received.ToolChoice)
	}
}

func TestBufferedStructuredOutputPreservesExplicitParameters(t *testing.T) {
	var received chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = json.NewDecoder(request.Body).Decode(&received)
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = writer.Write([]byte(`{"id":"chatcmpl-json","object":"chat.completion","created":1760000000,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"{\"answer\":\"ok\"}"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer server.Close()

	request := validatedStructuredRequest(t)
	response, failure := New().Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL, request))
	if failure != nil {
		t.Fatalf("Buffered() failure = %#v", failure)
	}
	if response.Canonical().Message.Content[0].Text != `{"answer":"ok"}` {
		t.Fatalf("content = %#v", response.Canonical().Message.Content)
	}
	if received.ResponseFormat == nil || received.ResponseFormat.Type != "json_schema" || received.ResponseFormat.JSONSchema == nil || received.ResponseFormat.JSONSchema.Strict == nil || !*received.ResponseFormat.JSONSchema.Strict {
		t.Fatalf("response format = %#v", received.ResponseFormat)
	}
	if received.Temperature == nil || *received.Temperature != 0.2 || received.TopP == nil || *received.TopP != 0.9 || received.Seed == nil || *received.Seed != 42 || len(received.Stop) != 1 {
		t.Fatalf("sampling = %#v", received)
	}
}

func TestBufferedRejectsUnsupportedSemanticsBeforeDispatch(t *testing.T) {
	var dispatched atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { dispatched.Store(true) }))
	defer server.Close()

	request := validatedToolRequest(t, false)
	route := testRouteWithCapabilities(t, server.URL, providerCapabilities(protocol.CapabilityEndpointBuffered, protocol.CapabilityRoleUser, protocol.CapabilityContentText))
	_, failure := New().Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route)
	if failure == nil || failure.Code != protocol.FailureCapabilityUnsupported {
		t.Fatalf("Buffered() failure = %#v", failure)
	}
	if dispatched.Load() {
		t.Fatal("unsupported request was dispatched")
	}
}

func TestBufferedClassifiesStatusBoundsAndProtocolFailures(t *testing.T) {
	request := validatedTextRequest(t, false, false)
	tests := []struct {
		name   string
		status int
		header string
		body   string
		limit  int64
		code   protocol.FailureCode
	}{
		{"rate limited", http.StatusTooManyRequests, "application/json", `{"error":{"message":"provider secret"}}`, 4096, protocol.FailureUpstreamRateLimited},
		{"redirect", http.StatusTemporaryRedirect, "text/plain", "redirect", 4096, protocol.FailureUpstreamRedirectRejected},
		{"context limit", http.StatusBadRequest, "application/json", `{"error":{"message":"secret prompt","type":"invalid_request_error","code":"context_length_exceeded"}}`, 4096, protocol.FailureUpstreamContextLimit},
		{"content policy", http.StatusBadRequest, "application/json", `{"error":{"message":"secret prompt","type":"content_policy_violation"}}`, 4096, protocol.FailureUpstreamContentPolicy},
		{"wrong content type", http.StatusOK, "text/html", "<html>secret</html>", 4096, protocol.FailureProtocolInvalidJSON},
		{"oversized", http.StatusOK, "application/json", strings.Repeat("x", 512), 128, protocol.FailureUpstreamResponseTooLarge},
		{"malformed", http.StatusOK, "application/json", `{"id":`, 4096, protocol.FailureProtocolInvalidJSON},
		{"no choices", http.StatusOK, "application/json", `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[]}`, 4096, protocol.FailureProtocolInvalidJSON},
		{"multiple choices", http.StatusOK, "application/json", `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":"stop"},{"index":1,"message":{"role":"assistant","content":"b"},"finish_reason":"stop"}]}`, 4096, protocol.FailureProtocolInvalidJSON},
		{"missing finish reason", http.StatusOK, "application/json", `{"id":"x","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"a"},"finish_reason":null}]}`, 4096, protocol.FailureProtocolInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", test.header)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			route := testRoute(t, server.URL, request)
			limits := route.Limits()
			limits.MaxResponseBodyBytes = test.limit
			if limits.MaxSSEEventBytes > int(test.limit) {
				limits.MaxSSEEventBytes, limits.MaxSSELineBytes = int(test.limit), int(test.limit)
			}
			route = testRouteWithLimits(t, server.URL, request, limits)
			_, failure := New().Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route)
			if failure == nil || failure.Code != test.code {
				t.Fatalf("Buffered() failure = %#v, want %s", failure, test.code)
			}
			if strings.Contains(failure.Error(), "secret") || (len(test.body) > 32 && strings.Contains(failure.Error(), test.body)) {
				t.Fatalf("failure leaked provider body: %v", failure)
			}
		})
	}
}

func TestBufferedPropagatesCancellation(t *testing.T) {
	dispatched := make(chan struct{})
	cancelObserved := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(dispatched)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(cancelObserved)
	}))
	defer server.Close()

	request := validatedTextRequest(t, false, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *protocol.CanonicalError, 1)
	go func() {
		_, failure := New().Buffered(ctx, provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL, request))
		done <- failure
	}()
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request was not dispatched")
	}
	cancel()
	select {
	case failure := <-done:
		if failure == nil || failure.Code != protocol.FailureClientCancelled {
			t.Fatalf("Buffered() failure = %#v", failure)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Buffered() did not return after cancellation")
	}
	select {
	case <-cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request did not observe cancellation")
	}
}

func validatedTextRequest(t *testing.T, stream, usage bool) protocol.ValidatedChatRequest {
	t.Helper()
	return validateRequest(t, protocol.CanonicalChatRequest{
		ContractVersion: protocol.ContractVersion, RequestID: "request-1", Target: "gateway-model",
		Messages:   []protocol.CanonicalMessage{{Role: protocol.RoleUser, Content: []protocol.CanonicalContentPart{{Type: protocol.ContentText, Text: "hello"}}}},
		ToolChoice: protocol.ToolChoice{Kind: protocol.ToolChoiceNone}, ResponseFormat: protocol.ResponseFormat{Kind: protocol.ResponseFormatText},
		Stream: stream, IncludeUsage: usage, Deadline: time.Now().Add(time.Minute),
	})
}

func validatedToolRequest(t *testing.T, stream bool) protocol.ValidatedChatRequest {
	t.Helper()
	strict := true
	return validateRequest(t, protocol.CanonicalChatRequest{
		ContractVersion: protocol.ContractVersion, RequestID: "request-1", Target: "gateway-model",
		Messages:   []protocol.CanonicalMessage{{Role: protocol.RoleUser, Content: []protocol.CanonicalContentPart{{Type: protocol.ContentText, Text: "Find the weather"}}}},
		Tools:      []protocol.CanonicalFunctionTool{{Name: "lookup", Description: protocol.Some("Look up weather"), Parameters: protocol.NewJSONSchema([]byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)), Strict: protocol.Some(strict)}},
		ToolChoice: protocol.ToolChoice{Kind: protocol.ToolChoiceSpecific, FunctionName: "lookup"}, ParallelToolCalls: protocol.Some(false),
		ResponseFormat: protocol.ResponseFormat{Kind: protocol.ResponseFormatText}, MaxOutputTokens: protocol.Some(256),
		Stream: stream, IncludeUsage: true, Deadline: time.Now().Add(time.Minute),
	})
}

func validatedStructuredRequest(t *testing.T) protocol.ValidatedChatRequest {
	t.Helper()
	return validateRequest(t, protocol.CanonicalChatRequest{
		ContractVersion: protocol.ContractVersion, RequestID: "request-1", Target: "gateway-model",
		Messages:       []protocol.CanonicalMessage{{Role: protocol.RoleUser, Content: []protocol.CanonicalContentPart{{Type: protocol.ContentText, Text: "answer"}}}},
		ToolChoice:     protocol.ToolChoice{Kind: protocol.ToolChoiceNone},
		ResponseFormat: protocol.ResponseFormat{Kind: protocol.ResponseFormatJSONSchema, Schema: protocol.NewJSONSchema([]byte(`{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}`)), Strict: protocol.Some(true)},
		Sampling:       protocol.SamplingParameters{Temperature: protocol.Some(0.2), TopP: protocol.Some(0.9), Seed: protocol.Some(int64(42)), Stop: protocol.Some([]string{"END"})},
		Deadline:       time.Now().Add(time.Minute),
	})
}

func validateRequest(t *testing.T, input protocol.CanonicalChatRequest) protocol.ValidatedChatRequest {
	t.Helper()
	request, failure := protocol.ValidateChatRequest(input, protocol.DefaultLimits())
	if failure != nil {
		t.Fatalf("ValidateChatRequest() failure = %#v", failure)
	}
	return request
}

func testRoute(t *testing.T, endpoint string, request protocol.ValidatedChatRequest) provider.ValidatedRoute {
	t.Helper()
	return testRouteWithCapabilities(t, endpoint, providerCapabilities(request.RequiredCapabilities()...))
}

func testRouteWithLimits(t *testing.T, endpoint string, request protocol.ValidatedChatRequest, limits provider.AdapterLimits) provider.ValidatedRoute {
	t.Helper()
	return validateRoute(t, endpoint, providerCapabilities(request.RequiredCapabilities()...), limits)
}

func testRouteWithCapabilities(t *testing.T, endpoint string, capabilities protocol.RouteCapabilities) provider.ValidatedRoute {
	t.Helper()
	return validateRoute(t, endpoint, capabilities, provider.DefaultAdapterLimits())
}

func validateRoute(t *testing.T, endpoint string, capabilities protocol.RouteCapabilities, limits provider.AdapterLimits) provider.ValidatedRoute {
	t.Helper()
	credential, err := provider.NewBearerCredential("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	route, err := provider.ValidateRoute(provider.Route{
		ID: "route-1", Endpoint: endpoint, UpstreamModel: "provider-model", Credential: credential,
		Capabilities: capabilities, Limits: limits, AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatalf("ValidateRoute() error = %v", err)
	}
	return route
}

func providerCapabilities(values ...protocol.Capability) protocol.RouteCapabilities {
	claims := make(map[protocol.Capability]protocol.CapabilityClaim, len(values))
	for _, value := range values {
		claims[value] = protocol.CapabilityClaim{State: protocol.CapabilitySupported, FixtureVersion: protocol.ContractVersion}
	}
	return protocol.RouteCapabilities{Claims: claims}
}
