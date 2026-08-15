package mockprovider

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
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
	handler.ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/unknown", nil))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d", unknown.Code)
	}
	mismatch := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, ChatCompletionsPath, strings.NewReader(`{"model":"m","stream":true}`))
	handler.ServeHTTP(mismatch, request)
	if mismatch.Code != http.StatusBadRequest {
		t.Fatalf("mode mismatch status = %d", mismatch.Code)
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
	request := httptest.NewRequest(http.MethodPost, ChatCompletionsPath, strings.NewReader(body))
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
	catalog, err := LoadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	scenario, err := NewScenario(catalog, profileID, ScenarioOptions{Seed: seed, Observer: observer})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(scenario)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
