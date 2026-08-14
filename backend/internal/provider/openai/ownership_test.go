package openai

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

func TestAdapterClosesEveryOwnedResponseBody(t *testing.T) {
	tests := []struct {
		name        string
		stream      bool
		status      int
		contentType string
		body        string
	}{
		{
			name: "buffered success", status: http.StatusOK, contentType: "application/json",
			body: `{"id":"chatcmpl-1","object":"chat.completion","created":1760000000,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
		},
		{name: "upstream error", status: http.StatusTooManyRequests, contentType: "application/json", body: `{"error":{"type":"rate_limit_error"}}`},
		{
			name: "stream success", stream: true, status: http.StatusOK, contentType: "text/event-stream",
			body: "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1760000000,\"model\":\"provider-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &trackedBody{Reader: strings.NewReader(test.body)}
			adapter := New()
			adapter.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: test.status,
					Header:     http.Header{"Content-Type": []string{test.contentType}},
					Body:       body,
				}, nil
			})
			request := validatedTextRequest(t, test.stream, false)
			route := testRoute(t, "https://provider.example/v1/chat/completions", request)
			if test.stream {
				_, _ = adapter.Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route, func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
			} else {
				_, _ = adapter.Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route)
			}
			if !body.closed {
				t.Fatal("adapter did not close its response body")
			}
		})
	}
}

type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
