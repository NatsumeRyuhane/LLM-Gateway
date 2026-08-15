package openai

import (
	"context"
	"net/http/httptest"
	"testing"

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
	catalog, err := mockprovider.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := mockprovider.NewScenario(catalog, profileID, mockprovider.ScenarioOptions{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := mockprovider.NewHandler(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(handler), scenario.Profile()
}
