package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/auth"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/openai"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/protocol"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider"
	provideropenai "github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/provider/openai"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/telemetry"
)

const testApplicationCredential = "application-secret"

func TestAuthenticatedModelsAndBufferedVerticalSlice(t *testing.T) {
	var dispatches atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		dispatches.Add(1)
		if request.Header.Get("Authorization") != "Bearer provider-secret" || request.Header.Get("Cookie") != "" {
			t.Errorf("provider headers = %#v", request.Header)
		}
		writer.Header().Set("Content-Type", openai.MediaTypeJSON)
		_, _ = io.WriteString(writer, `{"id":"upstream-1","object":"chat.completion","created":1786700000,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	defer upstream.Close()

	evidence := &memoryEvidence{}
	handler := newVerticalSliceHandler(t, upstream.URL, evidence)
	gateway := httptest.NewServer(handler)
	defer gateway.Close()

	unauthorizedRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, gateway.URL+openai.ModelsPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized, err := http.DefaultClient.Do(unauthorizedRequest)
	if err != nil {
		t.Fatal(err)
	}
	unauthorizedBody := readResponseBody(t, unauthorized)
	if unauthorized.StatusCode != http.StatusUnauthorized || bytes.Contains(unauthorizedBody, []byte("gateway-model")) || dispatches.Load() != 0 {
		t.Fatalf("unauthorized models status=%d body=%s dispatches=%d", unauthorized.StatusCode, unauthorizedBody, dispatches.Load())
	}

	modelsRequest, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, gateway.URL+openai.ModelsPath, nil)
	modelsRequest.Header.Set("Authorization", "Bearer "+testApplicationCredential)
	modelsResponse, err := http.DefaultClient.Do(modelsRequest)
	if err != nil {
		t.Fatal(err)
	}
	modelsBody := readResponseBody(t, modelsResponse)
	if modelsResponse.StatusCode != http.StatusOK || !bytes.Contains(modelsBody, []byte(`"id":"gateway-model"`)) || dispatches.Load() != 0 {
		t.Fatalf("models status=%d body=%s dispatches=%d", modelsResponse.StatusCode, modelsBody, dispatches.Load())
	}

	chatResponse := doChat(t, gateway.URL, `{"model":"gateway-model","messages":[{"role":"user","content":"private prompt"}]}`)
	chatBody := readResponseBody(t, chatResponse)
	if chatResponse.StatusCode != http.StatusOK || !bytes.Contains(chatBody, []byte(`"content":"hello"`)) || dispatches.Load() != 1 {
		t.Fatalf("chat status=%d body=%s dispatches=%d", chatResponse.StatusCode, chatBody, dispatches.Load())
	}
	if chatResponse.Header.Get(openai.HeaderRequestID) == "" || chatResponse.Header.Get(openai.HeaderAttemptID) == "" || chatResponse.Header.Get(openai.HeaderRouteID) != "route-1" {
		t.Fatalf("correlation headers = %#v", chatResponse.Header)
	}

	requests, decisions, attempts := evidence.snapshot()
	if len(requests) != 3 || len(decisions) != 1 || len(attempts) != 1 {
		t.Fatalf("evidence counts = requests:%d decisions:%d attempts:%d", len(requests), len(decisions), len(attempts))
	}
	attempt := attempts[0]
	if attempt.Ordinal != 1 || attempt.Terminal != telemetry.AttemptCompleted || !attempt.CanonicalOutputAccepted || !attempt.DownstreamCommitted || attempt.Usage == nil || attempt.Usage.TotalTokens != 4 {
		t.Fatalf("attempt evidence = %#v", attempt)
	}
	assertEvidenceExcludesContent(t, requests, decisions, attempts, "private prompt", "hello", "provider-secret", upstream.URL)
}

func TestStreamingVerticalSlicePreservesContentRefusalToolsUsageAndDone(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", openai.MediaTypeEventStream)
		writer.WriteHeader(http.StatusOK)
		writeSSEFrames(writer,
			`{"id":"upstream-stream","object":"chat.completion.chunk","created":1786700000,"model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hello","refusal":"cannot","tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"city\":"}}]},"finish_reason":null}]}`,
			`{"id":"upstream-stream","object":"chat.completion.chunk","created":1786700000,"model":"provider-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Shanghai\"}"}}]},"finish_reason":"tool_calls"}]}`,
			`{"id":"upstream-stream","object":"chat.completion.chunk","created":1786700000,"model":"provider-model","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13}}`,
			`[DONE]`,
		)
	}))
	defer upstream.Close()

	evidence := &memoryEvidence{}
	gateway := httptest.NewServer(newVerticalSliceHandler(t, upstream.URL, evidence))
	defer gateway.Close()
	body := `{"model":"gateway-model","messages":[{"role":"user","content":"use a tool"}],"stream":true,"stream_options":{"include_usage":true},"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}}}]}`
	response := doChat(t, gateway.URL, body)
	stream := string(readResponseBody(t, response))
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != openai.MediaTypeEventStream {
		t.Fatalf("stream status=%d headers=%#v body=%s", response.StatusCode, response.Header, stream)
	}
	ordered := []string{`"content":"hello"`, `"refusal":"cannot"`, `"id":"call-1"`, `"arguments":"{\"city\":"`, `"arguments":"\"Shanghai\"}"`, `"finish_reason":"tool_calls"`, `"total_tokens":13`, "data: [DONE]"}
	last := -1
	for _, fragment := range ordered {
		index := strings.Index(stream, fragment)
		if index <= last {
			t.Fatalf("fragment %q out of order in %s", fragment, stream)
		}
		last = index
	}
	attempt := onlyAttempt(t, evidence)
	if attempt.Terminal != telemetry.AttemptCompleted || !attempt.CanonicalOutputAccepted || !attempt.DownstreamCommitted || !attempt.ToolActionable || attempt.Usage == nil || attempt.Usage.TotalTokens != 13 {
		t.Fatalf("stream attempt evidence = %#v", attempt)
	}
}

func TestStreamingFailureBoundariesDoNotCommitFalseSuccess(t *testing.T) {
	tests := []struct {
		name          string
		frames        []string
		wantStatus    int
		wantBody      []string
		forbiddenBody []string
		wantTerminal  telemetry.AttemptTerminal
		wantAccepted  bool
		wantCommitted bool
	}{
		{
			name:       "early EOF before output",
			frames:     []string{`{"id":"early","object":"chat.completion.chunk","created":1786700000,"model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`},
			wantStatus: http.StatusBadGateway, wantBody: []string{`"code":"protocol.early_eof"`}, forbiddenBody: []string{"data: [DONE]"},
			wantTerminal: telemetry.AttemptEarlyEOF,
		},
		{
			name:       "failure after partial output",
			frames:     []string{`{"id":"partial","object":"chat.completion.chunk","created":1786700000,"model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"visible"},"finish_reason":null}]}`},
			wantStatus: http.StatusOK, wantBody: []string{`"content":"visible"`}, forbiddenBody: []string{"data: [DONE]", `"error":`},
			wantTerminal: telemetry.AttemptFailedPartial, wantAccepted: true, wantCommitted: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", openai.MediaTypeEventStream)
				writeSSEFrames(writer, test.frames...)
			}))
			defer upstream.Close()
			evidence := &memoryEvidence{}
			gateway := httptest.NewServer(newVerticalSliceHandler(t, upstream.URL, evidence))
			defer gateway.Close()
			response := doChat(t, gateway.URL, `{"model":"gateway-model","messages":[{"role":"user","content":"hello"}],"stream":true}`)
			body := string(readResponseBody(t, response))
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			for _, fragment := range test.wantBody {
				if !strings.Contains(body, fragment) {
					t.Fatalf("body %q missing %q", body, fragment)
				}
			}
			for _, fragment := range test.forbiddenBody {
				if strings.Contains(body, fragment) {
					t.Fatalf("body %q contains forbidden %q", body, fragment)
				}
			}
			attempt := onlyAttempt(t, evidence)
			if attempt.Terminal != test.wantTerminal || attempt.CanonicalOutputAccepted != test.wantAccepted || attempt.DownstreamCommitted != test.wantCommitted {
				t.Fatalf("attempt evidence = %#v", attempt)
			}
		})
	}
}

func TestKnownPathsReturnMethodNotAllowedAfterAuthentication(t *testing.T) {
	handler := newVerticalSliceHandler(t, "https://provider.example/v1/chat/completions", nil)
	for _, test := range []struct {
		name       string
		path       string
		allow      string
		authorized bool
		wantStatus int
	}{
		{name: "models", path: openai.ModelsPath, allow: http.MethodGet, authorized: true, wantStatus: http.StatusMethodNotAllowed},
		{name: "chat", path: openai.ChatCompletionsPath, allow: http.MethodPost, authorized: true, wantStatus: http.StatusMethodNotAllowed},
		{name: "authentication precedes allow metadata", path: openai.ModelsPath, wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, test.path, nil)
			if test.authorized {
				request.Header.Set("Authorization", "Bearer "+testApplicationCredential)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.wantStatus || response.Header().Get("Allow") != test.allow {
				t.Fatalf("status=%d Allow=%q body=%s", response.Code, response.Header().Get("Allow"), response.Body.String())
			}
		})
	}
}

func TestIncompleteSuccessfulAdapterResultIsGatewayFailure(t *testing.T) {
	evidence := &memoryEvidence{}
	handler := newVerticalSliceHandlerWithAdapter(t, "https://provider.example/v1/chat/completions", evidence, incompleteStreamAdapter{})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(`{"model":"gateway-model","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	request.Header.Set("Authorization", "Bearer "+testApplicationCredential)
	request.Header.Set("Content-Type", openai.MediaTypeJSON)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), `"code":"gateway.internal"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	attempt := onlyAttempt(t, evidence)
	if attempt.Terminal != telemetry.AttemptFailedPreOutput || attempt.FailureCode != protocol.FailureGatewayInternal || attempt.DownstreamCommitted {
		t.Fatalf("invariant attempt = %#v", attempt)
	}
}

func TestClientCancellationPropagatesBeforeAndAfterOutput(t *testing.T) {
	tests := []struct {
		name       string
		writeFrame bool
		wantCommit bool
	}{
		{name: "before output"},
		{name: "after output", writeFrame: true, wantCommit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatched := make(chan struct{})
			cancelled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", openai.MediaTypeEventStream)
				writer.WriteHeader(http.StatusOK)
				if test.writeFrame {
					writeSSEFrames(writer, `{"id":"cancel","object":"chat.completion.chunk","created":1786700000,"model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"visible"},"finish_reason":null}]}`)
				} else if flusher, ok := writer.(http.Flusher); ok {
					flusher.Flush()
				}
				close(dispatched)
				<-request.Context().Done()
				close(cancelled)
			}))
			defer upstream.Close()
			evidence := &memoryEvidence{}
			gateway := httptest.NewServer(newVerticalSliceHandler(t, upstream.URL, evidence))
			defer gateway.Close()

			ctx, cancel := context.WithCancel(t.Context())
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+openai.ChatCompletionsPath, strings.NewReader(`{"model":"gateway-model","messages":[{"role":"user","content":"cancel"}],"stream":true}`))
			request.Header.Set("Authorization", "Bearer "+testApplicationCredential)
			request.Header.Set("Content-Type", openai.MediaTypeJSON)
			done := make(chan struct{})
			outputRead := make(chan struct{})
			go func() {
				response, _ := http.DefaultClient.Do(request)
				if response != nil {
					if test.writeFrame {
						reader := bufio.NewReader(response.Body)
						_, _ = reader.ReadString('\n')
						close(outputRead)
						<-ctx.Done()
					}
					_ = response.Body.Close()
				}
				close(done)
			}()
			awaitSignal(t, dispatched, "upstream dispatch")
			if test.writeFrame {
				awaitSignal(t, outputRead, "downstream output")
			}
			cancel()
			awaitSignal(t, cancelled, "upstream cancellation")
			awaitSignal(t, done, "client completion")
			attempt := awaitAttempt(t, evidence)
			if attempt.Terminal != telemetry.AttemptCancelledClient || attempt.DownstreamCommitted != test.wantCommit {
				t.Fatalf("cancellation attempt = %#v", attempt)
			}
		})
	}
}

func TestDownstreamWriteAndFlushFailuresCancelUpstreamAndRecordPartial(t *testing.T) {
	for _, test := range []struct {
		name      string
		failWrite bool
	}{
		{name: "write", failWrite: true},
		{name: "flush"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatched := make(chan struct{})
			cancelled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", openai.MediaTypeEventStream)
				writer.WriteHeader(http.StatusOK)
				writeSSEFrames(writer, `{"id":"downstream-fail","object":"chat.completion.chunk","created":1786700000,"model":"provider-model","choices":[{"index":0,"delta":{"role":"assistant","content":"visible"},"finish_reason":null}]}`)
				close(dispatched)
				<-request.Context().Done()
				close(cancelled)
			}))
			defer upstream.Close()

			evidence := &memoryEvidence{}
			handler := newVerticalSliceHandler(t, upstream.URL, evidence)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(`{"model":"gateway-model","messages":[{"role":"user","content":"hello"}],"stream":true}`))
			request.Header.Set("Authorization", "Bearer "+testApplicationCredential)
			request.Header.Set("Content-Type", openai.MediaTypeJSON)
			writer := &failingResponseWriter{header: make(http.Header), failWrite: test.failWrite, failFlush: !test.failWrite}
			handler.ServeHTTP(writer, request)
			awaitSignal(t, dispatched, "upstream dispatch")
			awaitSignal(t, cancelled, "upstream cancellation")
			attempt := onlyAttempt(t, evidence)
			if attempt.Terminal != telemetry.AttemptFailedPartial || attempt.FailureCode != protocol.FailureGatewayDownstreamWriteFailed || !attempt.CanonicalOutputAccepted || !attempt.DownstreamCommitted {
				t.Fatalf("downstream failure attempt = %#v", attempt)
			}
			if writer.status != http.StatusOK || bytes.Contains(writer.body.Bytes(), []byte(`"error":`)) || bytes.Contains(writer.body.Bytes(), []byte("[DONE]")) {
				t.Fatalf("writer status=%d body=%s", writer.status, writer.body.String())
			}
		})
	}

	t.Run("buffered write", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", openai.MediaTypeJSON)
			_, _ = io.WriteString(writer, `{"id":"buffered-write-fail","object":"chat.completion","created":1786700000,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"visible"},"finish_reason":"stop"}]}`)
		}))
		defer upstream.Close()
		evidence := &memoryEvidence{}
		handler := newVerticalSliceHandler(t, upstream.URL, evidence)
		request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(`{"model":"gateway-model","messages":[{"role":"user","content":"hello"}]}`))
		request.Header.Set("Authorization", "Bearer "+testApplicationCredential)
		request.Header.Set("Content-Type", openai.MediaTypeJSON)
		writer := &failingResponseWriter{header: make(http.Header), failWrite: true}
		handler.ServeHTTP(writer, request)
		attempt := onlyAttempt(t, evidence)
		if attempt.Terminal != telemetry.AttemptFailedPartial || attempt.FailureCode != protocol.FailureGatewayDownstreamWriteFailed || !attempt.CanonicalOutputAccepted || !attempt.DownstreamCommitted {
			t.Fatalf("buffered downstream failure attempt = %#v", attempt)
		}
		if writer.status != http.StatusOK || bytes.Contains(writer.body.Bytes(), []byte(`"error":`)) {
			t.Fatalf("writer status=%d body=%s", writer.status, writer.body.String())
		}
	})
}

func TestConcurrentVerticalSliceUsesIndependentRequestAndAttemptState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", openai.MediaTypeJSON)
		_, _ = io.WriteString(writer, `{"id":"concurrent","object":"chat.completion","created":1786700000,"model":"provider-model","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	evidence := &memoryEvidence{}
	gateway := httptest.NewServer(newVerticalSliceHandler(t, upstream.URL, evidence))
	defer gateway.Close()

	const count = 24
	var group sync.WaitGroup
	errorsFound := make(chan error, count)
	requestIDs := sync.Map{}
	attemptIDs := sync.Map{}
	for index := 0; index < count; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			response, err := chatRequest(t.Context(), gateway.URL, `{"model":"gateway-model","messages":[{"role":"user","content":"concurrent"}]}`)
			if err != nil {
				errorsFound <- err
				return
			}
			_, copyErr := io.Copy(io.Discard, response.Body)
			closeErr := response.Body.Close()
			if copyErr != nil {
				errorsFound <- copyErr
				return
			}
			if closeErr != nil {
				errorsFound <- closeErr
				return
			}
			if response.StatusCode != http.StatusOK {
				errorsFound <- fmt.Errorf("status %d", response.StatusCode)
				return
			}
			if _, duplicate := requestIDs.LoadOrStore(response.Header.Get(openai.HeaderRequestID), struct{}{}); duplicate {
				errorsFound <- errors.New("duplicate request ID")
			}
			if _, duplicate := attemptIDs.LoadOrStore(response.Header.Get(openai.HeaderAttemptID), struct{}{}); duplicate {
				errorsFound <- errors.New("duplicate attempt ID")
			}
		}()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	requests, decisions, attempts := evidence.snapshot()
	if len(requests) != count || len(decisions) != count || len(attempts) != count {
		t.Fatalf("concurrent evidence counts = %d, %d, %d", len(requests), len(decisions), len(attempts))
	}
	for _, attempt := range attempts {
		if attempt.Ordinal != 1 || attempt.Terminal != telemetry.AttemptCompleted {
			t.Fatalf("attempt = %#v", attempt)
		}
	}
}

type fixtureAuthenticator struct{}

func (fixtureAuthenticator) Authenticate(_ context.Context, values []string) (auth.PrincipalContext, *protocol.CanonicalError) {
	if len(values) != 1 || values[0] != "Bearer "+testApplicationCredential {
		return auth.PrincipalContext{}, &protocol.CanonicalError{
			Code: protocol.FailureAuthInvalidCredential, Domain: protocol.DomainAuth,
			RetryDisposition: protocol.RetryNever, SafeMessage: "Authentication failed.", HTTPStatus: http.StatusUnauthorized,
		}
	}
	return auth.PrincipalContext{
		ApplicationID: "application-a", CredentialID: "credential-a",
		SubjectKind: auth.SubjectKindApplication, SubjectID: "application-a",
		Scopes:             []auth.Scope{auth.ScopeModelsRead, auth.ScopeChatCompletionsCreate},
		AuthenticationTime: time.Now().UTC(), AuthenticationMethod: auth.AuthenticationMethodApplicationKey,
	}, nil
}

func newVerticalSliceHandler(t *testing.T, endpoint string, evidence EvidenceSink) http.Handler {
	t.Helper()
	return newVerticalSliceHandlerWithAdapter(t, endpoint, evidence, provideropenai.New())
}

func newVerticalSliceHandlerWithAdapter(t *testing.T, endpoint string, evidence EvidenceSink, adapter provider.Adapter) http.Handler {
	t.Helper()
	credential, err := provider.NewBearerCredential("provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	claims := make(map[protocol.Capability]protocol.CapabilityClaim)
	for _, capability := range []protocol.Capability{
		protocol.CapabilityEndpointBuffered, protocol.CapabilityEndpointStreaming,
		protocol.CapabilityRoleUser, protocol.CapabilityContentText, protocol.CapabilityRefusalOutput,
		protocol.CapabilityToolsFunction, protocol.CapabilityToolsChoiceAuto, protocol.CapabilityFinishStop,
		protocol.CapabilityFinishToolCalls, protocol.CapabilityUsageBuffered, protocol.CapabilityUsageStreaming,
	} {
		claims[capability] = protocol.CapabilityClaim{State: protocol.CapabilitySupported, FixtureVersion: protocol.ContractVersion}
	}
	validatedRoute, err := provider.ValidateRoute(provider.Route{
		ID: "route-1", Endpoint: endpoint, UpstreamModel: "provider-model", Credential: credential,
		Capabilities: protocol.RouteCapabilities{Claims: claims}, Limits: provider.DefaultAdapterLimits(), AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	route, err := NewSingleAuthorizedRoute("application-a", openai.Model{
		ID: "gateway-model", CreatedAt: time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC), OwnedBy: "gateway",
	}, validatedRoute)
	if err != nil {
		t.Fatal(err)
	}
	codec := openai.NewCodec(protocol.DefaultLimits())
	boundary, err := NewDataPlaneBoundary(fixtureAuthenticator{}, codec)
	if err != nil {
		t.Fatal(err)
	}
	var sequence atomic.Uint64
	handler, err := NewDataPlaneHandler(boundary, route, adapter, evidence, DataPlaneHandlerOptions{
		Now: time.Now, RequestTimeout: 10 * time.Second,
		NewIdentifier: func(prefix string) (string, error) {
			return fmt.Sprintf("%s-%d", prefix, sequence.Add(1)), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func doChat(t *testing.T, gatewayURL, body string) *http.Response {
	t.Helper()
	response, err := chatRequest(t.Context(), gatewayURL, body)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func chatRequest(ctx context.Context, gatewayURL, body string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, gatewayURL+openai.ChatCompletionsPath, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+testApplicationCredential)
	request.Header.Set("Content-Type", openai.MediaTypeJSON)
	return http.DefaultClient.Do(request)
}

func readResponseBody(t *testing.T, response *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return body
}

type incompleteStreamAdapter struct{}

func (incompleteStreamAdapter) Buffered(context.Context, provider.Attempt, protocol.ValidatedChatRequest, provider.ValidatedRoute) (protocol.ValidatedChatResponse, *protocol.CanonicalError) {
	return protocol.ValidatedChatResponse{}, &protocol.CanonicalError{
		Code: protocol.FailureGatewayInternal, Domain: protocol.DomainGateway,
		RetryDisposition: protocol.RetryNever, SafeMessage: "Unexpected buffered call.", HTTPStatus: http.StatusInternalServerError,
	}
}

func (incompleteStreamAdapter) Stream(context.Context, provider.Attempt, protocol.ValidatedChatRequest, provider.ValidatedRoute, provider.EventSink) (protocol.StreamResult, *protocol.CanonicalError) {
	return protocol.StreamResult{}, nil
}

func writeSSEFrames(writer http.ResponseWriter, frames ...string) {
	for _, frame := range frames {
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", frame)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

type memoryEvidence struct {
	mu        sync.Mutex
	requests  []telemetry.RequestEvidence
	decisions []telemetry.DecisionEvidence
	attempts  []telemetry.AttemptEvidence
}

func (e *memoryEvidence) RecordRequest(record telemetry.RequestEvidence) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, record)
}

func (e *memoryEvidence) RecordDecision(record telemetry.DecisionEvidence) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.decisions = append(e.decisions, record)
}

func (e *memoryEvidence) RecordAttempt(record telemetry.AttemptEvidence) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attempts = append(e.attempts, record)
}

func (e *memoryEvidence) snapshot() ([]telemetry.RequestEvidence, []telemetry.DecisionEvidence, []telemetry.AttemptEvidence) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]telemetry.RequestEvidence(nil), e.requests...), append([]telemetry.DecisionEvidence(nil), e.decisions...), append([]telemetry.AttemptEvidence(nil), e.attempts...)
}

func onlyAttempt(t *testing.T, evidence *memoryEvidence) telemetry.AttemptEvidence {
	t.Helper()
	_, _, attempts := evidence.snapshot()
	if len(attempts) != 1 {
		t.Fatalf("attempt count = %d", len(attempts))
	}
	return attempts[0]
}

func awaitAttempt(t *testing.T, evidence *memoryEvidence) telemetry.AttemptEvidence {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, _, attempts := evidence.snapshot()
		if len(attempts) == 1 {
			return attempts[0]
		}
		time.Sleep(time.Millisecond)
	}
	return onlyAttempt(t, evidence)
}

func awaitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

type failingResponseWriter struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	failWrite bool
	failFlush bool
}

func (w *failingResponseWriter) Header() http.Header    { return w.header }
func (w *failingResponseWriter) WriteHeader(status int) { w.status = status }
func (w *failingResponseWriter) Write(body []byte) (int, error) {
	if w.failWrite && len(body) > 0 {
		_ = w.body.WriteByte(body[0])
		return 1, io.ErrClosedPipe
	}
	return w.body.Write(body)
}
func (w *failingResponseWriter) FlushError() error {
	if w.failFlush {
		return io.ErrClosedPipe
	}
	return nil
}

func assertEvidenceExcludesContent(
	t *testing.T,
	requests []telemetry.RequestEvidence,
	decisions []telemetry.DecisionEvidence,
	attempts []telemetry.AttemptEvidence,
	forbidden ...string,
) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		Requests  []telemetry.RequestEvidence
		Decisions []telemetry.DecisionEvidence
		Attempts  []telemetry.AttemptEvidence
	}{requests, decisions, attempts})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if bytes.Contains(encoded, []byte(value)) {
			t.Fatalf("evidence contains forbidden value %q: %s", value, encoded)
		}
	}
}
