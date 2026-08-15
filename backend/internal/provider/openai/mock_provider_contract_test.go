package openai

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/mockprovider"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

func TestMockProviderSuccessProfilesPassAdapterContract(t *testing.T) {
	t.Run("buffered", func(t *testing.T) {
		request := validatedTextRequest(t, false, false)
		server, profile := newMockProviderServer(t, "success.buffered")
		defer server.Close()
		response, failure := New().Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL+mockprovider.ChatCompletionsPath, request))
		if failure != nil {
			t.Fatalf("Buffered() failure = %#v", failure)
		}
		if response.Canonical().Message.Content[0].Text == "" || profile.Expected.FailureCode != "" {
			t.Fatalf("response = %#v profile = %#v", response.Canonical(), profile)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		request := validatedTextRequest(t, true, true)
		server, profile := newMockProviderServer(t, "success.streaming")
		defer server.Close()
		result, failure := New().Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, server.URL+mockprovider.ChatCompletionsPath, request), func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
		if failure != nil {
			t.Fatalf("Stream() failure = %#v", failure)
		}
		if result.Message.Content[0].Text == "" || profile.Expected.FailureCode != "" {
			t.Fatalf("result = %#v profile = %#v", result, profile)
		}
	})
}

func TestMockProviderStatefulAndSilentProfilesPreserveContractBoundary(t *testing.T) {
	t.Run("recovery sequence", func(t *testing.T) {
		request := validatedTextRequest(t, false, false)
		server, _ := newMockProviderServer(t, "sequence.recover_after_2")
		defer server.Close()
		route := testRoute(t, server.URL+mockprovider.ChatCompletionsPath, request)
		for ordinal := 1; ordinal <= 3; ordinal++ {
			response, failure := New().Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route)
			if ordinal <= 2 {
				if failure == nil || failure.Code != protocol.FailureUpstreamServerError {
					t.Fatalf("request %d failure = %#v", ordinal, failure)
				}
				continue
			}
			if failure != nil || response.Canonical().Message.Content[0].Text == "" {
				t.Fatalf("recovery response = %#v failure = %#v", response.Canonical(), failure)
			}
		}
	})

	for _, profileID := range []string{"silent.parameter_ignored", "silent.context_truncation", "silent.degenerate_output", "silent.unstable_recovery"} {
		profileID := profileID
		t.Run(profileID, func(t *testing.T) {
			server, profile := newMockProviderServer(t, profileID)
			defer server.Close()
			if profile.DetectionOwner != "m3_health" || profile.Expected.FailureCode != "" {
				t.Fatalf("silent profile contract = %#v", profile)
			}
			buffered := validatedTextRequest(t, false, false)
			if _, failure := New().Buffered(context.Background(), provider.Attempt{ID: "attempt-buffered"}, buffered, testRoute(t, server.URL+mockprovider.ChatCompletionsPath, buffered)); failure != nil {
				t.Fatalf("Buffered() failure = %#v", failure)
			}
			streaming := validatedTextRequest(t, true, false)
			if _, failure := New().Stream(context.Background(), provider.Attempt{ID: "attempt-stream"}, streaming, testRoute(t, server.URL+mockprovider.ChatCompletionsPath, streaming), func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil }); failure != nil {
				t.Fatalf("Stream() failure = %#v", failure)
			}
		})
	}
}

func TestMockProviderCancellationAndStallPropagateThroughAdapter(t *testing.T) {
	t.Run("client cancellation", func(t *testing.T) {
		observed := make(chan mockprovider.Observation, 8)
		server, _ := newMockProviderServerWithOptions(t, "lifecycle.await_cancellation", mockprovider.ScenarioOptions{Observer: mockprovider.ObserverFunc(func(value mockprovider.Observation) { observed <- value })})
		defer server.Close()
		request := validatedTextRequest(t, true, false)
		route := testRoute(t, server.URL+mockprovider.ChatCompletionsPath, request)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan *protocol.CanonicalError, 1)
		go func() {
			_, failure := New().Stream(ctx, provider.Attempt{ID: "attempt-1"}, request, route, func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
			done <- failure
		}()
		waitForMockEvent(t, observed, mockprovider.EventResponseHeadersReady)
		cancel()
		select {
		case failure := <-done:
			if failure == nil || failure.Code != protocol.FailureClientCancelled {
				t.Fatalf("failure = %#v", failure)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("adapter did not return after cancellation")
		}
		waitForMockEvent(t, observed, mockprovider.EventRequestCancelled)
	})

	t.Run("deadline stall", func(t *testing.T) {
		observed := make(chan mockprovider.Observation, 8)
		server, _ := newMockProviderServerWithOptions(t, "timing.stream_stall", mockprovider.ScenarioOptions{Observer: mockprovider.ObserverFunc(func(value mockprovider.Observation) { observed <- value })})
		defer server.Close()
		request := withRequestDeadline(t, validatedTextRequest(t, true, false), time.Now().Add(100*time.Millisecond))
		route := testRoute(t, server.URL+mockprovider.ChatCompletionsPath, request)
		done := make(chan *protocol.CanonicalError, 1)
		go func() {
			_, failure := New().Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route, func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
			done <- failure
		}()
		waitForMockEvent(t, observed, mockprovider.EventResponseHeadersReady)
		select {
		case failure := <-done:
			if failure == nil || failure.Code != protocol.FailureUpstreamTimeout {
				t.Fatalf("failure = %#v", failure)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("adapter did not enforce the request deadline")
		}
		waitForMockEvent(t, observed, mockprovider.EventRequestCancelled)
	})
}

func TestMockProviderImmediateFaultProfilesMatchAdapterTaxonomy(t *testing.T) {
	tests := []struct {
		profileID string
		request   func(*testing.T) protocol.ValidatedChatRequest
	}{
		{"http.429", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
		{"http.429_retry_after", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
		{"http.500", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
		{"http.502", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
		{"http.503", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
		{"bounds.buffered_body", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
		{"bounds.sse_event", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, true, false) }},
		{"protocol.malformed_json", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
		{"protocol.malformed_sse", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, true, false) }},
		{"protocol.malformed_sse_json", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, true, false) }},
		{"protocol.early_eof_pre_output", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, true, false) }},
		{"protocol.early_eof_post_output", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, true, false) }},
		{"protocol.empty_output", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
		{"protocol.invalid_tool_arguments", func(t *testing.T) protocol.ValidatedChatRequest { return validatedToolRequest(t, false) }},
		{"protocol.partial_tool_arguments", func(t *testing.T) protocol.ValidatedChatRequest { return validatedToolRequest(t, true) }},
		{"protocol.structured_schema_violation", func(t *testing.T) protocol.ValidatedChatRequest { return validatedStructuredRequest(t) }},
		{"protocol.invalid_event_order", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, true, false) }},
		{"protocol.usage_inconsistent", func(t *testing.T) protocol.ValidatedChatRequest { return validatedTextRequest(t, false, false) }},
	}

	for _, test := range tests {
		t.Run(test.profileID, func(t *testing.T) {
			request := test.request(t)
			server, profile := newMockProviderServer(t, test.profileID)
			defer server.Close()
			route := testRoute(t, server.URL+mockprovider.ChatCompletionsPath, request)
			var failure *protocol.CanonicalError
			if request.Canonical().Stream {
				_, failure = New().Stream(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route, func(protocol.CanonicalEvent) *protocol.CanonicalError { return nil })
			} else {
				_, failure = New().Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, route)
			}
			if failure == nil {
				t.Fatalf("profile %s returned no failure", test.profileID)
			}
			expected := profile.Expected
			if string(failure.Code) != expected.FailureCode || string(failure.Domain) != expected.Domain || string(failure.RetryDisposition) != expected.RetryDisposition || failure.ProviderStatus != expected.ProviderStatus || failure.OutputVisible != expected.OutputVisible || failure.ToolActionable != expected.ToolActionable {
				t.Fatalf("failure = %#v, expected = %#v", failure, expected)
			}
		})
	}
}

func newMockProviderServer(t *testing.T, profileID string) (*httptest.Server, mockprovider.Profile) {
	t.Helper()
	return newMockProviderServerWithOptions(t, profileID, mockprovider.ScenarioOptions{})
}

func newMockProviderServerWithOptions(t *testing.T, profileID string, options mockprovider.ScenarioOptions) (*httptest.Server, mockprovider.Profile) {
	t.Helper()
	catalog, err := mockprovider.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := mockprovider.NewScenario(catalog, profileID, options)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := mockprovider.NewHandler(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(handler), scenario.Profile()
}

func withRequestDeadline(t *testing.T, request protocol.ValidatedChatRequest, deadline time.Time) protocol.ValidatedChatRequest {
	t.Helper()
	canonical := request.Canonical()
	canonical.Deadline = deadline
	validated, failure := protocol.ValidateChatRequest(canonical, request.Limits())
	if failure != nil {
		t.Fatalf("ValidateChatRequest(deadline) = %#v", failure)
	}
	return validated
}

func waitForMockEvent(t *testing.T, observed <-chan mockprovider.Observation, want mockprovider.Event) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case observation := <-observed:
			if observation.Event == want {
				return
			}
		case <-deadline:
			t.Fatalf("did not observe %s", want)
		}
	}
}
