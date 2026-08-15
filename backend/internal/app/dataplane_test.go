package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/auth"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/openai"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
)

var dataPlaneMetadata = openai.RequestMetadata{
	RequestID: "request-1",
	Deadline:  time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC),
}

type fakeApplicationAuthenticator struct {
	principal auth.PrincipalContext
	failure   *protocol.CanonicalError
	values    []string
}

func (a *fakeApplicationAuthenticator) Authenticate(_ context.Context, values []string) (auth.PrincipalContext, *protocol.CanonicalError) {
	a.values = append([]string(nil), values...)
	return a.principal, a.failure
}

func TestDataPlaneBoundaryProducesTypedModelsInput(t *testing.T) {
	verifier := &fakeApplicationAuthenticator{principal: applicationPrincipal(auth.ScopeModelsRead)}
	boundary := newTestDataPlaneBoundary(t, verifier)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, openai.ModelsPath, nil)
	request.Header.Set("Authorization", "Bearer canary-application-secret")
	request.Header.Set(openai.HeaderConversationID, " conversation-1 ")
	request.Header.Set("X-Authenticated-User", "attacker-selected-user")
	request.Header.Set("Cookie", "session=canary-cookie")

	decoded, failure := boundary.AuthenticateModelsRequest(request, dataPlaneMetadata)
	if failure != nil {
		t.Fatalf("AuthenticateModelsRequest() failure = %v", failure)
	}
	if len(verifier.values) != 1 || verifier.values[0] != "Bearer canary-application-secret" {
		t.Fatalf("authenticator values = %#v", verifier.values)
	}
	if decoded.RequestID != dataPlaneMetadata.RequestID || decoded.Principal.ApplicationID != "application-a" ||
		decoded.Principal.SubjectKind != auth.SubjectKindApplication {
		t.Fatalf("authenticated models request = %#v", decoded)
	}
	conversation, present := decoded.Attribution.ConversationID.Get()
	if !present || conversation.ApplicationID != "application-a" || conversation.Value != "conversation-1" {
		t.Fatalf("scoped conversation = %#v, %v", conversation, present)
	}
	if decoded.Principal.SubjectID == "attacker-selected-user" {
		t.Fatal("untrusted identity header changed the typed principal")
	}
}

func TestDataPlaneBoundaryBindsAndRemovesRawChatAttribution(t *testing.T) {
	verifier := &fakeApplicationAuthenticator{principal: applicationPrincipal(auth.ScopeChatCompletionsCreate)}
	boundary := newTestDataPlaneBoundary(t, verifier)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(`{
		"model":"general", "messages":[{"role":"user","content":"hello"}]
	}`))
	request.Header.Set("Content-Type", openai.MediaTypeJSON)
	request.Header.Set("Authorization", "Bearer canary-application-secret")
	request.Header.Set(openai.HeaderConversationID, "conversation-1")
	request.Header.Set(openai.HeaderRunID, "run-1")

	decoded, failure := boundary.AuthenticateChatCompletionsRequest(request, dataPlaneMetadata)
	if failure != nil {
		t.Fatalf("AuthenticateChatCompletionsRequest() failure = %v", failure)
	}
	canonical := decoded.Request.Canonical()
	if canonical.Attribution.ConversationID.IsSet() || canonical.Attribution.RunID.IsSet() {
		t.Fatalf("canonical provider-bound attribution = %#v", canonical.Attribution)
	}
	conversation, present := decoded.Attribution.ConversationID.Get()
	if !present || conversation != (auth.ScopedIdentifier{ApplicationID: "application-a", Value: "conversation-1"}) {
		t.Fatalf("scoped conversation = %#v, %v", conversation, present)
	}
	run, present := decoded.Attribution.RunID.Get()
	if !present || run != (auth.ScopedIdentifier{ApplicationID: "application-a", Value: "run-1"}) {
		t.Fatalf("scoped run = %#v, %v", run, present)
	}
}

func TestDataPlaneBoundaryLeavesAttributionSyntaxToPublicCodec(t *testing.T) {
	verifier := &fakeApplicationAuthenticator{principal: applicationPrincipal(auth.ScopeModelsRead)}
	boundary := newTestDataPlaneBoundary(t, verifier)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, openai.ModelsPath, nil)
	request.Header.Set("Authorization", "Bearer canary-application-secret")
	request.Header.Add(openai.HeaderConversationID, "conversation-a")
	request.Header.Add(openai.HeaderConversationID, "conversation-b")

	_, failure := boundary.AuthenticateModelsRequest(request, dataPlaneMetadata)
	if failure == nil || failure.Code != protocol.FailureClientInvalidRequest || failure.Validation == nil ||
		failure.Validation.Path != "headers.x-gateway-conversation-id" {
		t.Fatalf("AuthenticateModelsRequest() failure = %#v", failure)
	}
}

func TestDataPlaneBoundaryAuthorizesBeforeReadingChatBody(t *testing.T) {
	verifier := &fakeApplicationAuthenticator{principal: applicationPrincipal(auth.ScopeModelsRead)}
	boundary := newTestDataPlaneBoundary(t, verifier)
	body := &trackingBody{Reader: strings.NewReader(`{"model":"general"}`)}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, openai.ChatCompletionsPath, nil)
	request.Body = body
	request.Header.Set("Content-Type", openai.MediaTypeJSON)
	request.Header.Set("Authorization", "Bearer canary-application-secret")

	_, failure := boundary.AuthenticateChatCompletionsRequest(request, dataPlaneMetadata)
	if failure == nil || failure.Code != protocol.FailureAuthForbidden || body.read {
		t.Fatalf("scope denial = %#v, body read = %v", failure, body.read)
	}
}

func TestDataPlaneBoundaryAuthenticationFailurePrecedesDecoding(t *testing.T) {
	verifier := &fakeApplicationAuthenticator{failure: &protocol.CanonicalError{
		Code: protocol.FailureAuthInvalidCredential, Domain: protocol.DomainAuth,
		RetryDisposition: protocol.RetryNever, SafeMessage: "Authentication failed.", HTTPStatus: 401,
	}}
	boundary := newTestDataPlaneBoundary(t, verifier)
	body := &trackingBody{Reader: strings.NewReader(`not-json`)}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, openai.ChatCompletionsPath, nil)
	request.Body = body

	_, failure := boundary.AuthenticateChatCompletionsRequest(request, dataPlaneMetadata)
	if failure == nil || failure.Code != protocol.FailureAuthInvalidCredential || body.read {
		t.Fatalf("authentication failure = %#v, body read = %v", failure, body.read)
	}
	if failure.RequestID != dataPlaneMetadata.RequestID {
		t.Fatalf("failure request ID = %q", failure.RequestID)
	}
}

type trackingBody struct {
	io.Reader
	read bool
}

func (b *trackingBody) Read(buffer []byte) (int, error) {
	b.read = true
	return b.Reader.Read(buffer)
}

func (*trackingBody) Close() error { return nil }

func newTestDataPlaneBoundary(t *testing.T, verifier ApplicationAuthenticator) *DataPlaneBoundary {
	t.Helper()
	boundary, err := NewDataPlaneBoundary(verifier, openai.NewCodec(protocol.DefaultLimits()))
	if err != nil {
		t.Fatal(err)
	}
	return boundary
}

func applicationPrincipal(scopes ...auth.Scope) auth.PrincipalContext {
	return auth.PrincipalContext{
		ApplicationID: "application-a", CredentialID: "credential-a",
		SubjectKind: auth.SubjectKindApplication, SubjectID: "application-a", Scopes: scopes,
		AuthenticationTime:   dataPlaneMetadata.Deadline.Add(-time.Minute),
		AuthenticationMethod: auth.AuthenticationMethodApplicationKey,
	}
}
