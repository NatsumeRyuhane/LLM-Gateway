package mockprovider

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSuccessProfilesAreDeterministicAndIsolated(t *testing.T) {
	t.Parallel()
	for _, profileID := range []string{"success.buffered", "success.streaming"} {
		profileID := profileID
		t.Run(profileID, func(t *testing.T) {
			t.Parallel()
			first, firstObservations := runProfile(t, profileID, 42)
			second, secondObservations := runProfile(t, profileID, 42)
			if first != second {
				t.Fatalf("same profile and seed produced different responses\nfirst: %s\nsecond: %s", first, second)
			}
			if len(firstObservations) == 0 || len(firstObservations) != len(secondObservations) {
				t.Fatalf("observations = %d and %d", len(firstObservations), len(secondObservations))
			}
			for _, observation := range firstObservations {
				if observation.ProfileID != profileID || observation.Seed != 42 || observation.RequestOrdinal != 1 || observation.GroundTruth != "success" {
					t.Fatalf("observation = %#v", observation)
				}
			}
		})
	}
}

func TestBufferedSuccessIsOpenAICompatible(t *testing.T) {
	body, _ := runProfile(t, "success.buffered", 1)
	var response struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.ID == "" || response.Object != "chat.completion" || len(response.Choices) != 1 || response.Choices[0].Message.Role != "assistant" || response.Choices[0].FinishReason != "stop" {
		t.Fatalf("response = %#v", response)
	}
}

func TestStreamingSuccessHasTerminalSentinel(t *testing.T) {
	body, observations := runProfile(t, "success.streaming", 1)
	if strings.Count(body, "data: ") != 5 || !strings.HasSuffix(body, "data: [DONE]\n\n") {
		t.Fatalf("stream body = %q", body)
	}
	var events []Event
	for _, observation := range observations {
		events = append(events, observation.Event)
	}
	want := []Event{EventRequestReceived, EventResponseHeadersReady, EventResponseChunkReady, EventResponseChunkReady, EventResponseTerminalReady}
	if len(events) != len(want) {
		t.Fatalf("events = %v", events)
	}
	for index := range want {
		if events[index] != want[index] {
			t.Fatalf("events = %v", events)
		}
	}
}

func TestHandlerRejectsUnknownSurfaceAndModeMismatch(t *testing.T) {
	handler := newTestHandler(t, "success.buffered", 1, nil)
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/unknown", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", unknown.Code)
	}
	mismatch := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"model":"m","stream":true}`))
	handler.ServeHTTP(mismatch, request)
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("mode mismatch status = %d", mismatch.Code)
	}
}

func TestRateLimitProfileEmitsConfiguredRetryAfter(t *testing.T) {
	handler := newTestHandler(t, "http.429_retry_after", 1, nil)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"model":"m","stream":false}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") != "3" {
		t.Fatalf("status = %d Retry-After = %q", response.Code, response.Header().Get("Retry-After"))
	}
}

func TestGatedStreamUsesEventsInsteadOfSleeps(t *testing.T) {
	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	scheduler := SchedulerFunc(func(ctx context.Context, event Event) error {
		if event != EventResponseChunkReady {
			return nil
		}
		once.Do(func() { close(reached) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	handler := newTestHandlerWithOptions(t, "timing.slow_first_token", ScenarioOptions{Scheduler: scheduler})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"model":"m","stream":true}`))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not reach the first-chunk gate")
	}
	select {
	case <-done:
		t.Fatal("stream completed before the gate was released")
	default:
	}
	close(release)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not complete after gate release")
	}
	if response.Code != http.StatusOK || !strings.HasSuffix(response.Body.String(), "data: [DONE]\n\n") {
		t.Fatalf("status = %d body = %q", response.Code, response.Body.String())
	}
}

func TestAwaitCancellationObservesAndStopsRequest(t *testing.T) {
	observed := make(chan Observation, 8)
	handler := newTestHandlerWithOptions(t, "lifecycle.await_cancellation", ScenarioOptions{Observer: ObserverFunc(func(value Observation) { observed <- value })})
	ctx, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequestWithContext(ctx, http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"model":"m","stream":true}`))
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(response, request)
		close(done)
	}()
	waitForObservation(t, observed, EventResponseHeadersReady)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after cancellation")
	}
	waitForObservation(t, observed, EventRequestCancelled)
}

func TestFailureRecoveryStateIsScenarioLocal(t *testing.T) {
	for scenarioIndex := 0; scenarioIndex < 2; scenarioIndex++ {
		handler := newTestHandler(t, "sequence.recover_after_2", 11, nil)
		for ordinal, wantStatus := range []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK} {
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"model":"m","stream":false}`))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != wantStatus {
				t.Fatalf("scenario %d request %d status = %d, want %d", scenarioIndex, ordinal+1, response.Code, wantStatus)
			}
		}
	}
}

func TestSilentProfilesRemainSuccessfulAndLabeled(t *testing.T) {
	for _, profileID := range []string{"silent.parameter_ignored", "silent.context_truncation", "silent.degenerate_output", "silent.unstable_recovery"} {
		t.Run(profileID, func(t *testing.T) {
			observed := make(chan Observation, 8)
			handler := newTestHandlerWithOptions(t, profileID, ScenarioOptions{Observer: ObserverFunc(func(value Observation) { observed <- value })})
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"model":"m","stream":false,"temperature":0.25}`))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
			}
			observation := waitForObservation(t, observed, EventRequestReceived)
			if observation.GroundTruth == "" || observation.ProfileID != profileID {
				t.Fatalf("observation = %#v", observation)
			}
		})
	}
}

func TestResponseWriteFailuresRecordCancellation(t *testing.T) {
	tests := []struct {
		profileID string
		stream    bool
		failAt    int
	}{
		{profileID: "http.503", failAt: 1},
		{profileID: "bounds.buffered_body", failAt: 1},
		{profileID: "protocol.malformed_json", failAt: 1},
		{profileID: "success.buffered", failAt: 1},
		{profileID: "protocol.malformed_sse", stream: true, failAt: 1},
		{profileID: "success.streaming", stream: true, failAt: 4},
	}
	for _, test := range tests {
		t.Run(test.profileID, func(t *testing.T) {
			observed := make(chan Observation, 8)
			handler := newTestHandlerWithOptions(t, test.profileID, ScenarioOptions{
				Observer: ObserverFunc(func(value Observation) { observed <- value }),
			})
			body := `{"model":"m","stream":false}`
			if test.stream {
				body = `{"model":"m","stream":true}`
			}
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, ChatCompletionsPath, strings.NewReader(body))
			writer := &writeFailureRecorder{header: make(http.Header), failAt: test.failAt}
			handler.ServeHTTP(writer, request)
			waitForObservation(t, observed, EventRequestCancelled)
		})
	}
}

func runProfile(t *testing.T, profileID string, seed int64) (string, []Observation) {
	t.Helper()
	var mutex sync.Mutex
	var observations []Observation
	handler := newTestHandler(t, profileID, seed, ObserverFunc(func(observation Observation) {
		mutex.Lock()
		defer mutex.Unlock()
		observations = append(observations, observation)
	}))
	stream := strings.Contains(profileID, "streaming")
	body := `{"model":"mock-model","stream":false}`
	if stream {
		body = `{"model":"mock-model","stream":true,"stream_options":{"include_usage":true}}`
	}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, ChatCompletionsPath, strings.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d body = %s", response.Code, responseBody)
	}
	mutex.Lock()
	defer mutex.Unlock()
	return response.Body.String(), append([]Observation(nil), observations...)
}

func newTestHandler(t *testing.T, profileID string, seed int64, observer Observer) *Handler {
	t.Helper()
	return newTestHandlerWithOptions(t, profileID, ScenarioOptions{Seed: seed, Observer: observer})
}

func newTestHandlerWithOptions(t *testing.T, profileID string, options ScenarioOptions) *Handler {
	t.Helper()
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := NewScenario(catalog, profileID, options)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func waitForObservation(t *testing.T, observed <-chan Observation, want Event) Observation {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case observation := <-observed:
			if observation.Event == want {
				return observation
			}
		case <-deadline:
			t.Fatalf("did not observe %s", want)
		}
	}
}

type writeFailureRecorder struct {
	header http.Header
	status int
	writes int
	failAt int
}

func (w *writeFailureRecorder) Header() http.Header { return w.header }

func (w *writeFailureRecorder) WriteHeader(status int) { w.status = status }

func (w *writeFailureRecorder) Write(body []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		return 0, io.ErrClosedPipe
	}
	return len(body), nil
}

func (w *writeFailureRecorder) Flush() {}

func (w *writeFailureRecorder) FlushError() error { return nil }
