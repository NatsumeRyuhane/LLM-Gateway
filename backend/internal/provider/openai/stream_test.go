package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

func TestStreamEmitsOrderedTextRefusalUsageAndCompletion(t *testing.T) {
	var received chatRequest
	server := streamServer(t, func(writer http.ResponseWriter, request *http.Request) {
		assertOutboundHeaderAllowlist(t, request, "text/event-stream")
		if err := json.NewDecoder(request.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeSSE(writer,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1760000000,"model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"},"finish_reason":null}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1760000000,"model":"provider-model","choices":[{"index":0,"delta":{"content":"lo","refusal":"cannot"},"finish_reason":"stop"}]}`,
			`{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1760000000,"model":"provider-model","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
			`[DONE]`,
		)
	})
	defer server.Close()

	request := withAttribution(t, validatedTextRequest(t, true, true))
	var events []protocol.CanonicalEvent
	result, failure := New().Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL, request), func(event protocol.CanonicalEvent) *protocol.CanonicalError {
		events = append(events, event)
		return nil
	})
	if failure != nil {
		t.Fatalf("Stream() failure = %#v", failure)
	}
	wantTypes := []protocol.EventType{protocol.EventResponseStarted, protocol.EventOutputTextDelta, protocol.EventOutputTextDelta, protocol.EventRefusalDelta, protocol.EventUsageUpdated, protocol.EventResponseCompleted}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %#v", events)
	}
	for index, want := range wantTypes {
		if events[index].Type != want || events[index].Sequence != uint64(index+1) {
			t.Fatalf("event[%d] = %#v, want %s", index, events[index], want)
		}
	}
	refusal, hasRefusal := result.Message.Refusal.Get()
	if result.Message.Content[0].Text != "Hello" || !hasRefusal || refusal != "cannot" || result.FinishReason != protocol.FinishStop {
		t.Fatalf("result = %#v", result)
	}
	usage, ok := result.Usage.Get()
	if !ok || usage.TotalTokens != 5 || usage.Partial {
		t.Fatalf("usage = %#v, present = %v", usage, ok)
	}
	if !received.Stream || received.StreamOptions == nil || !received.StreamOptions.IncludeUsage {
		t.Fatalf("translated stream request = %#v", received)
	}
}

func TestStreamPreservesFragmentedToolCalls(t *testing.T) {
	server := streamServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writeSSE(writer,
			`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1760000000,"model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-tool","object":"chat.completion.chunk","created":1760000000,"model":"provider-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Shanghai\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		)
	})
	defer server.Close()

	request := validatedToolRequest(t, true)
	var types []protocol.EventType
	result, failure := New().Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL, request), func(event protocol.CanonicalEvent) *protocol.CanonicalError {
		types = append(types, event.Type)
		return nil
	})
	if failure != nil {
		t.Fatalf("Stream() failure = %#v", failure)
	}
	want := []protocol.EventType{protocol.EventResponseStarted, protocol.EventToolCallStarted, protocol.EventToolCallArgumentsDelta, protocol.EventToolCallArgumentsDelta, protocol.EventToolCallCompleted, protocol.EventResponseCompleted}
	if len(types) != len(want) {
		t.Fatalf("event types = %v", types)
	}
	for index := range want {
		if types[index] != want[index] {
			t.Fatalf("event types = %v", types)
		}
	}
	if len(result.Message.ToolCalls) != 1 || result.Message.ToolCalls[0].Arguments != `{"city":"Shanghai"}` || result.FinishReason != protocol.FinishToolCalls {
		t.Fatalf("result = %#v", result)
	}
}

func TestStreamRequiresDoneAndPreservesVisibility(t *testing.T) {
	server := streamServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writeSSE(writer, `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1760000000,"model":"provider-model","choices":[{"index":0,"delta":{"content":"visible"},"finish_reason":"stop"}]}`)
	})
	defer server.Close()

	request := validatedTextRequest(t, true, false)
	_, failure := New().Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL, request), func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
	if failure == nil || failure.Code != protocol.FailureProtocolEarlyEOF || !failure.OutputVisible || failure.RetryDisposition != protocol.RetryClientDecides {
		t.Fatalf("Stream() failure = %#v", failure)
	}
}

func TestStreamRejectsMalformedAndOversizedEvents(t *testing.T) {
	request := validatedTextRequest(t, true, false)
	tests := []struct {
		name  string
		body  string
		limit int
		code  protocol.FailureCode
	}{
		{"invalid field", "event: message\ndata: {}\n\n", 4096, protocol.FailureProtocolInvalidSSE},
		{"malformed JSON", "data: {\n\n", 4096, protocol.FailureProtocolInvalidJSON},
		{"done before finish", "data: [DONE]\n\n", 4096, protocol.FailureProtocolInvalidSSE},
		{"oversized line", "data: " + strings.Repeat("x", 512) + "\n\n", 128, protocol.FailureUpstreamResponseTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := streamServer(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = writer.Write([]byte(test.body)) })
			defer server.Close()
			limits := provider.DefaultAdapterLimits()
			limits.MaxSSELineBytes = test.limit
			if limits.MaxSSEEventBytes < test.limit {
				limits.MaxSSEEventBytes = test.limit
			}
			_, failure := New().Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRouteWithLimits(t, server.URL, request, limits), func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
			if failure == nil || failure.Code != test.code {
				t.Fatalf("Stream() failure = %#v, want %s", failure, test.code)
			}
		})
	}
}

func TestStreamStopsWhenConsumerRejectsEvent(t *testing.T) {
	server := streamServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writeSSE(writer, `{"id":"chatcmpl-1","object":"chat.completion.chunk","created":1760000000,"model":"provider-model","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`, `[DONE]`)
	})
	defer server.Close()

	request := validatedTextRequest(t, true, false)
	consumerFailure := &protocol.CanonicalError{Code: protocol.FailureClientCancelled, Domain: protocol.DomainClient, RetryDisposition: protocol.RetryNever, SafeMessage: "The downstream consumer stopped.", HTTPStatus: 499}
	_, failure := New().Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL, request), func(event protocol.CanonicalEvent) *protocol.CanonicalError {
		if event.Type == protocol.EventOutputTextDelta {
			return consumerFailure
		}
		return nil
	})
	if failure == consumerFailure || failure.Code != protocol.FailureClientCancelled {
		t.Fatalf("Stream() failure = %#v", failure)
	}
	if consumerFailure.RequestID != "" || consumerFailure.AttemptID != "" || consumerFailure.RouteID != "" {
		t.Fatalf("Stream() mutated consumer-owned failure = %#v", consumerFailure)
	}
	if failure.RequestID != "request-1" || failure.AttemptID != "attempt-1" || failure.RouteID != "route-1" {
		t.Fatalf("Stream() did not attach attempt metadata to its copy = %#v", failure)
	}
}

func TestStreamClassifiesScannerReadErrorsAsTransportFailures(t *testing.T) {
	request := validatedTextRequest(t, true, false)
	body := &trackedBody{Reader: io.MultiReader(
		strings.NewReader("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1760000000,\"model\":\"provider-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"visible\"},\"finish_reason\":null}]}\n\n"),
		readerFunc(func([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }),
	)}
	adapter := New()
	adapter.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
	})

	_, failure := adapter.Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, "https://provider.example/v1/chat/completions", request), func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
	if failure == nil || failure.Code != protocol.FailureUpstreamConnectFailed || !failure.OutputVisible || failure.RetryDisposition != protocol.RetryClientDecides {
		t.Fatalf("Stream() failure = %#v", failure)
	}
	if !body.closed {
		t.Fatal("Stream() did not close the body after a scanner read failure")
	}
}

type readerFunc func([]byte) (int, error)

func (function readerFunc) Read(buffer []byte) (int, error) { return function(buffer) }

func TestStreamPropagatesCancellationAfterHeaders(t *testing.T) {
	dispatched := make(chan struct{})
	cancelObserved := make(chan struct{})
	server := streamServer(t, func(writer http.ResponseWriter, request *http.Request) {
		close(dispatched)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(cancelObserved)
	})
	defer server.Close()

	request := validatedTextRequest(t, true, false)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *protocol.CanonicalError, 1)
	go func() {
		_, failure := New().Stream(ctx, provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL, request), func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
		done <- failure
	}()
	select {
	case <-dispatched:
	case <-time.After(2 * time.Second):
		t.Fatal("stream was not dispatched")
	}
	cancel()
	select {
	case failure := <-done:
		if failure == nil || failure.Code != protocol.FailureClientCancelled {
			t.Fatalf("Stream() failure = %#v", failure)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stream() did not return after cancellation")
	}
	select {
	case <-cancelObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream stream did not observe cancellation")
	}
}

func streamServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		handler(writer, request)
	}))
}

func writeSSE(writer http.ResponseWriter, values ...string) {
	for _, value := range values {
		_, _ = writer.Write([]byte("data: " + value + "\n\n"))
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}
