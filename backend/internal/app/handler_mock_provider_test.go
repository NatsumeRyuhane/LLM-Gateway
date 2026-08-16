package app

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/mockprovider"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/openai"
	"github.com/NatsumeRyuhane/LLM-Gateway/backend/internal/telemetry"
)

func TestMockProviderProfilesDriveAuthenticatedVerticalSliceEvidence(t *testing.T) {
	tests := []struct {
		profileID string
		body      string
	}{
		{profileID: "http.503", body: `{"model":"gateway-model","messages":[{"role":"user","content":"private prompt"}]}`},
		{profileID: "protocol.malformed_json", body: `{"model":"gateway-model","messages":[{"role":"user","content":"private prompt"}]}`},
		{profileID: "protocol.early_eof_pre_output", body: `{"model":"gateway-model","messages":[{"role":"user","content":"private prompt"}],"stream":true}`},
		{profileID: "protocol.early_eof_post_output", body: `{"model":"gateway-model","messages":[{"role":"user","content":"private prompt"}],"stream":true}`},
	}

	for _, test := range tests {
		t.Run(test.profileID, func(t *testing.T) {
			observations := make(chan mockprovider.Observation, 16)
			upstream, profile := newMockProviderVerticalSliceServer(t, test.profileID, mockprovider.ScenarioOptions{
				Observer: mockprovider.ObserverFunc(func(observation mockprovider.Observation) { observations <- observation }),
			})
			defer upstream.Close()

			evidence := &memoryEvidence{}
			handler := newVerticalSliceHandler(t, upstream.URL+mockprovider.ChatCompletionsPath, evidence)
			request := authenticatedChatRequest(t, test.body)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			expected := profile.Expected
			attempt := onlyAttempt(t, evidence)
			assertAttemptMatchesProfile(t, attempt, expected)
			requests, decisions, attempts := evidence.snapshot()
			if len(requests) != 1 || len(decisions) != 1 {
				t.Fatalf("evidence counts = requests:%d decisions:%d attempts:%d", len(requests), len(decisions), len(attempts))
			}
			requestEvidence, decision := requests[0], decisions[0]
			wantOutcome := telemetry.OutcomeFailed
			if expected.OutputVisible || expected.ToolActionable {
				wantOutcome = telemetry.OutcomePartial
			}
			if requestEvidence.Outcome != wantOutcome || decision.Outcome != wantOutcome || string(requestEvidence.FailureDomain) != expected.Domain || string(requestEvidence.FailureCode) != expected.FailureCode || string(decision.FailureCode) != expected.FailureCode {
				t.Fatalf("request = %#v, decision = %#v, expected = %#v", requestEvidence, decision, expected)
			}
			if requestEvidence.RequestID != decision.RequestID || decision.AttemptID != attempt.AttemptID || decision.SelectedRouteID != attempt.RouteID || attempt.DecisionID != decision.DecisionID {
				t.Fatalf("evidence correlation = request:%#v decision:%#v attempt:%#v", requestEvidence, decision, attempt)
			}
			if expected.OutputVisible {
				if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content":"visible"`) || strings.Contains(response.Body.String(), `"error":`) || strings.Contains(response.Body.String(), "[DONE]") {
					t.Fatalf("partial response status=%d body=%s", response.Code, response.Body.String())
				}
			} else if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"code":"`+expected.FailureCode+`"`) {
				t.Fatalf("failure response status=%d body=%s", response.Code, response.Body.String())
			}
			observation := waitForMockProviderObservation(t, observations, mockprovider.EventRequestReceived)
			if observation.SchemaVersion != mockprovider.ObservationSchemaVersion || observation.ProfileID != profile.ID || observation.GroundTruth != profile.GroundTruth || observation.Event != mockprovider.EventRequestReceived {
				t.Fatalf("first observation = %#v", observation)
			}
			assertEvidenceExcludesContent(t, requests, decisions, attempts, "private prompt", "visible", "provider-secret", upstream.URL)
		})
	}
}

func TestMockProviderSilentProfileRemainsValidAcrossPublicBoundary(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "buffered"
		body := `{"model":"gateway-model","messages":[{"role":"user","content":"private prompt"}]}`
		if stream {
			name = "streaming"
			body = `{"model":"gateway-model","messages":[{"role":"user","content":"private prompt"}],"stream":true}`
		}
		t.Run(name, func(t *testing.T) {
			upstream, profile := newMockProviderVerticalSliceServer(t, "silent.degenerate_output", mockprovider.ScenarioOptions{})
			defer upstream.Close()
			evidence := &memoryEvidence{}
			handler := newVerticalSliceHandler(t, upstream.URL+mockprovider.ChatCompletionsPath, evidence)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, authenticatedChatRequest(t, body))
			if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "repeat repeat") {
				t.Fatalf("silent response status=%d body=%s", response.Code, response.Body.String())
			}
			requests, decisions, attempts := evidence.snapshot()
			if len(requests) != 1 || len(decisions) != 1 || len(attempts) != 1 || requests[0].Outcome != telemetry.OutcomeSuccess || decisions[0].Outcome != telemetry.OutcomeSuccess {
				t.Fatalf("silent evidence = requests:%#v decisions:%#v attempts:%#v", requests, decisions, attempts)
			}
			attempt := attempts[0]
			if attempt.Terminal != telemetry.AttemptCompleted || attempt.FailureCode != "" || !attempt.CanonicalOutputAccepted || !attempt.DownstreamCommitted || profile.DetectionOwner != "m3_health" {
				t.Fatalf("silent attempt = %#v, profile = %#v", attempt, profile)
			}
			assertEvidenceExcludesContent(t, requests, decisions, attempts, "private prompt", "repeat repeat", "provider-secret", upstream.URL)
		})
	}
}

func TestMockProviderDownstreamDisconnectHarnessMatchesExternalMatrix(t *testing.T) {
	catalog, err := mockprovider.LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	disconnectProfile, ok := catalog.Profile("downstream.disconnect")
	if !ok || disconnectProfile.InjectionLayer != mockprovider.LayerDownstream {
		t.Fatalf("downstream profile = %#v, present = %t", disconnectProfile, ok)
	}
	observations := make(chan mockprovider.Observation, 16)
	var chunks atomic.Uint64
	upstream, _ := newMockProviderVerticalSliceServer(t, "timing.slow_inter_token", mockprovider.ScenarioOptions{
		Observer: mockprovider.ObserverFunc(func(observation mockprovider.Observation) { observations <- observation }),
		Scheduler: mockprovider.SchedulerFunc(func(ctx context.Context, event mockprovider.Event) error {
			if event == mockprovider.EventResponseChunkReady && chunks.Add(1) > 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		}),
	})
	defer upstream.Close()

	evidence := &memoryEvidence{}
	handler := newVerticalSliceHandler(t, upstream.URL+mockprovider.ChatCompletionsPath, evidence)
	writer := &failingResponseWriter{header: make(http.Header), failWrite: true}
	handler.ServeHTTP(writer, authenticatedChatRequest(t, `{"model":"gateway-model","messages":[{"role":"user","content":"private prompt"}],"stream":true}`))
	attempt := onlyAttempt(t, evidence)
	assertAttemptMatchesProfile(t, attempt, disconnectProfile.Expected)
	if writer.status != http.StatusOK || bytes.Contains(writer.body.Bytes(), []byte(`"error":`)) || bytes.Contains(writer.body.Bytes(), []byte("[DONE]")) {
		t.Fatalf("disconnect response status=%d body=%s", writer.status, writer.body.String())
	}
	waitForMockProviderObservation(t, observations, mockprovider.EventRequestCancelled)
	requests, decisions, attempts := evidence.snapshot()
	assertEvidenceExcludesContent(t, requests, decisions, attempts, "private prompt", "provider-secret", upstream.URL)
}

func newMockProviderVerticalSliceServer(t *testing.T, profileID string, options mockprovider.ScenarioOptions) (*httptest.Server, mockprovider.Profile) {
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

func authenticatedChatRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, openai.ChatCompletionsPath, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+testApplicationCredential)
	request.Header.Set("Content-Type", openai.MediaTypeJSON)
	return request
}

func assertAttemptMatchesProfile(t *testing.T, attempt telemetry.AttemptEvidence, expected mockprovider.Expected) {
	t.Helper()
	if string(attempt.FailureCode) != expected.FailureCode || string(attempt.RetryDisposition) != expected.RetryDisposition || attempt.ProviderStatus != expected.ProviderStatus || attempt.CanonicalOutputAccepted != expected.OutputVisible || attempt.ToolActionable != expected.ToolActionable || string(attempt.Terminal) != expected.Terminal {
		t.Fatalf("attempt = %#v, expected = %#v", attempt, expected)
	}
	if attempt.DownstreamCommitted != expected.OutputVisible {
		t.Fatalf("downstream commit = %t, expected output visibility = %t", attempt.DownstreamCommitted, expected.OutputVisible)
	}
}

func waitForMockProviderObservation(t *testing.T, observations <-chan mockprovider.Observation, want mockprovider.Event) mockprovider.Observation {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case observation := <-observations:
			if observation.Event == want {
				return observation
			}
		case <-timer.C:
			t.Fatalf("mock provider did not observe %s", want)
		}
	}
}
