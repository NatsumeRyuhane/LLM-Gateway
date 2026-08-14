package provider

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestNewRequestConstructsOnlyAllowlistedHeaders(t *testing.T) {
	inbound := &http.Request{Header: http.Header{
		"Authorization":             []string{"Bearer inbound-application-secret"},
		"Cookie":                    []string{"session=inbound-cookie"},
		"Forwarded":                 []string{"for=attacker"},
		"Proxy-Authorization":       []string{"Basic inbound-proxy-secret"},
		"X-Authenticated-User":      []string{"attacker"},
		"X-Api-Key":                 []string{"inbound-provider-secret"},
		"X-Gateway-Conversation-Id": []string{"conversation-1"},
		"X-Gateway-Run-Id":          []string{"run-1"},
		"Openai-Organization":       []string{"organization-1"},
		"Openai-Project":            []string{"project-1"},
	}}
	credential, err := NewBearerCredential("route-owned-provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	request, err := NewRequest(
		context.Background(), http.MethodPost, "https://provider.example/v1/chat/completions", strings.NewReader("{}"),
		RequestHeaders{ContentType: "application/json", Accept: "text/event-stream", UserAgent: "llm-gateway/0"}, credential,
	)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	want := http.Header{
		"Authorization": []string{"Bearer route-owned-provider-secret"},
		"Content-Type":  []string{"application/json"},
		"Accept":        []string{"text/event-stream"},
		"User-Agent":    []string{"llm-gateway/0"},
	}
	if !reflect.DeepEqual(request.Header, want) {
		t.Fatalf("outbound headers = %#v, want %#v", request.Header, want)
	}
	for name, values := range inbound.Header {
		if name == "Authorization" {
			continue
		}
		if len(request.Header.Values(name)) != 0 {
			t.Fatalf("outbound request retained inbound %s = %#v", name, values)
		}
	}
}

func TestNewRequestAPIHasNoInboundHeaderOrRequestInput(t *testing.T) {
	requestType := reflect.TypeOf((*http.Request)(nil))
	headerType := reflect.TypeOf(http.Header{})
	builder := reflect.TypeOf(NewRequest)
	for index := range builder.NumIn() {
		input := builder.In(index)
		if input == requestType || input == headerType {
			t.Fatalf("NewRequest input %d accepts inbound transport type %s", index, input)
		}
	}
}

func TestProviderCredentialFormattingIsRedacted(t *testing.T) {
	credential, err := NewBearerCredential("canary-provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, formatted := range []string{fmt.Sprint(credential), fmt.Sprintf("%#v", credential)} {
		if strings.Contains(formatted, "canary-provider-secret") || !strings.Contains(formatted, "REDACTED") {
			t.Fatalf("formatted credential = %q", formatted)
		}
	}
}

func TestProviderRequestRejectsUnsafeHeaderValues(t *testing.T) {
	if _, err := NewBearerCredential("secret with spaces"); err == nil {
		t.Fatal("NewBearerCredential() accepted whitespace")
	}
	credential, err := NewBearerCredential("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRequest(
		context.Background(), http.MethodPost, "https://provider.example/v1/chat/completions", nil,
		RequestHeaders{ContentType: "application/json\r\nX-Leak: secret"}, credential,
	)
	if err == nil {
		t.Fatal("NewRequest() accepted a header injection")
	}
}
