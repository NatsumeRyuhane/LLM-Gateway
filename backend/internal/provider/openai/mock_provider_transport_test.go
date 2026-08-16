package openai

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/mockprovider"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
)

func TestMockProviderTransportHarnessMatchesExternalMatrix(t *testing.T) {
	tests := []struct {
		profileID string
		transport error
	}{
		{profileID: "transport.connection_refused", transport: syscall.ECONNREFUSED},
		{profileID: "transport.dns_failure", transport: &net.DNSError{Err: "synthetic lookup failure", Name: "provider.invalid", IsNotFound: true}},
		{profileID: "transport.tls_failure", transport: tls.RecordHeaderError{Msg: "synthetic TLS failure"}},
	}

	catalog, err := mockprovider.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests {
		t.Run(test.profileID, func(t *testing.T) {
			profile, ok := catalog.Profile(test.profileID)
			if !ok || profile.InjectionLayer != mockprovider.LayerTransport {
				t.Fatalf("transport profile = %#v, present = %t", profile, ok)
			}
			request := validatedTextRequest(t, false, false)
			adapter := New()
			adapter.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, test.transport
			})
			_, failure := adapter.Buffered(context.Background(), provider.Attempt{ID: "attempt-1"}, request, testRoute(t, "https://provider.invalid/v1/chat/completions", request))
			if failure == nil {
				t.Fatal("Buffered() returned no failure")
			}
			expected := profile.Expected
			if string(failure.Code) != expected.FailureCode || string(failure.Domain) != expected.Domain || string(failure.RetryDisposition) != expected.RetryDisposition || failure.ProviderStatus != expected.ProviderStatus || failure.OutputVisible != expected.OutputVisible || failure.ToolActionable != expected.ToolActionable {
				t.Fatalf("failure = %#v, expected = %#v", failure, expected)
			}
			if errors.Is(failure, test.transport) {
				t.Fatal("transport implementation detail escaped canonical failure")
			}
		})
	}
}
