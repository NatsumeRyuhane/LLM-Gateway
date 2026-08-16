package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/config"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/mockprovider"
)

func TestNewProviderHandlerUsesStartupSelectedProfile(t *testing.T) {
	handler, err := newProviderHandler(config.MockProvider{Profile: "http.503", Seed: 42}, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, mockprovider.ChatCompletionsPath, strings.NewReader(`{"model":"m","stream":false}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestNewProviderHandlerRejectsUnknownAndExternalProfiles(t *testing.T) {
	for _, profile := range []string{"unknown.profile", "transport.dns_failure"} {
		if _, err := newProviderHandler(config.MockProvider{Profile: profile, Seed: 1}, nil); err == nil {
			t.Fatalf("profile %q was accepted", profile)
		}
	}
}
