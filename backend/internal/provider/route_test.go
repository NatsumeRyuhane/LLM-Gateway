package provider

import (
	"net/http"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

func TestValidateRouteSnapshotsConfigurationAndPlacesOnlyCredential(t *testing.T) {
	credential, err := NewBearerCredential("provider-secret")
	if err != nil {
		t.Fatalf("NewBearerCredential() error = %v", err)
	}
	capabilities := supportedCapabilities(protocol.CapabilityEndpointBuffered, protocol.CapabilityRoleUser, protocol.CapabilityContentText)
	route, err := ValidateRoute(Route{
		ID: "route-1", Endpoint: "https://provider.example/v1/chat/completions", UpstreamModel: "provider-model",
		Credential: credential, Capabilities: capabilities, Limits: DefaultAdapterLimits(),
	})
	if err != nil {
		t.Fatalf("ValidateRoute() error = %v", err)
	}
	capabilities.Claims[protocol.CapabilityEndpointBuffered] = protocol.CapabilityClaim{State: protocol.CapabilityUnsupported}

	header := make(http.Header)
	header.Set("Cookie", "application-cookie")
	if err := route.ApplyCredential(header); err != nil {
		t.Fatalf("ApplyCredential() error = %v", err)
	}
	if got := header.Get("Authorization"); got != "Bearer provider-secret" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := header.Get("Cookie"); got != "application-cookie" {
		t.Fatalf("credential placement mutated unrelated header: %q", got)
	}

	request := validatedTextRequest(t, false)
	if failure := route.CheckRequest(request); failure != nil {
		t.Fatalf("CheckRequest() failure = %#v", failure)
	}
}

func TestValidateRouteRejectsUnsafeOrIncompleteConfiguration(t *testing.T) {
	credential, err := NewBearerCredential("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	valid := Route{
		ID: "route-1", Endpoint: "https://provider.example/v1/chat/completions", UpstreamModel: "provider-model",
		Credential: credential, Capabilities: supportedCapabilities(protocol.CapabilityEndpointBuffered), Limits: DefaultAdapterLimits(),
	}
	tests := []struct {
		name string
		edit func(*Route)
	}{
		{"userinfo", func(route *Route) { route.Endpoint = "https://secret@provider.example/v1/chat/completions" }},
		{"query", func(route *Route) { route.Endpoint += "?key=secret" }},
		{"plain HTTP", func(route *Route) { route.Endpoint = "http://provider.example/v1/chat/completions" }},
		{"non-loopback development HTTP", func(route *Route) {
			route.Endpoint = "http://192.0.2.1/v1/chat/completions"
			route.AllowInsecureLoopback = true
		}},
		{"missing credential", func(route *Route) { route.Credential = Credential{} }},
		{"zero limits", func(route *Route) { route.Limits = AdapterLimits{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.edit(&candidate)
			if _, err := ValidateRoute(candidate); err == nil {
				t.Fatal("ValidateRoute() error = nil")
			}
		})
	}

	valid.Endpoint = "http://127.0.0.1:8080/v1/chat/completions"
	valid.AllowInsecureLoopback = true
	if _, err := ValidateRoute(valid); err != nil {
		t.Fatalf("development loopback route error = %v", err)
	}
}

func TestValidatedRouteRejectsMissingCapabilitiesBeforeDispatch(t *testing.T) {
	credential, _ := NewBearerCredential("provider-secret")
	route, err := ValidateRoute(Route{
		ID: "route-1", Endpoint: "https://provider.example/v1/chat/completions", UpstreamModel: "provider-model",
		Credential: credential, Capabilities: supportedCapabilities(protocol.CapabilityEndpointBuffered), Limits: DefaultAdapterLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	failure := route.CheckRequest(validatedTextRequest(t, false))
	if failure == nil || failure.Code != protocol.FailureCapabilityUnsupported {
		t.Fatalf("CheckRequest() = %#v", failure)
	}
}

func supportedCapabilities(capabilities ...protocol.Capability) protocol.RouteCapabilities {
	claims := make(map[protocol.Capability]protocol.CapabilityClaim, len(capabilities))
	for _, capability := range capabilities {
		claims[capability] = protocol.CapabilityClaim{State: protocol.CapabilitySupported, FixtureVersion: protocol.ContractVersion}
	}
	return protocol.RouteCapabilities{Claims: claims}
}

func validatedTextRequest(t *testing.T, stream bool) protocol.ValidatedChatRequest {
	t.Helper()
	request, failure := protocol.ValidateChatRequest(protocol.CanonicalChatRequest{
		ContractVersion: protocol.ContractVersion,
		RequestID:       "request-1",
		Target:          "gateway-model",
		Messages: []protocol.CanonicalMessage{{
			Role: protocol.RoleUser, Content: []protocol.CanonicalContentPart{{Type: protocol.ContentText, Text: "hello"}},
		}},
		ToolChoice:     protocol.ToolChoice{Kind: protocol.ToolChoiceNone},
		ResponseFormat: protocol.ResponseFormat{Kind: protocol.ResponseFormatText},
		Stream:         stream,
		Deadline:       time.Now().Add(time.Minute),
	}, protocol.DefaultLimits())
	if failure != nil {
		t.Fatalf("ValidateChatRequest() = %#v", failure)
	}
	return request
}
