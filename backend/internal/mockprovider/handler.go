package mockprovider

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	ChatCompletionsPath = "/v1/chat/completions"
	maxRequestBytes     = 4 << 20
)

type chatRequest struct {
	Model         string `json:"model"`
	Stream        bool   `json:"stream"`
	StreamOptions *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

// Handler serves one isolated scenario.
type Handler struct {
	scenario *Scenario
}

// NewHandler creates an OpenAI-compatible handler for one scenario.
func NewHandler(scenario *Scenario) (*Handler, error) {
	if scenario == nil {
		return nil, errors.New("mock-provider scenario is required")
	}
	return &Handler{scenario: scenario}, nil
}

// ServeHTTP serves the profile-selected Chat Completions behavior.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request == nil || request.URL == nil || request.URL.Path != ChatCompletionsPath {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeProviderError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	decoded, err := decodeRequest(request)
	if err != nil {
		writeProviderError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	mode := ModeBuffered
	if decoded.Stream {
		mode = ModeStreaming
	}
	profile := h.scenario.profile
	if profile.Mode != ModeEither && profile.Mode != mode {
		writeProviderError(writer, http.StatusBadRequest, "mode_mismatch")
		return
	}
	state := h.scenario.begin(mode)
	if err := state.reach(request.Context(), EventResponseHeadersReady); err != nil {
		return
	}
	if profile.Behavior.Kind != "success_buffered" && profile.Behavior.Kind != "success_stream" {
		writeProviderError(writer, http.StatusNotImplemented, "profile_not_implemented")
		return
	}
	if mode == ModeStreaming {
		h.serveSuccessStream(writer, request, decoded, &state)
		return
	}
	h.serveSuccessBuffered(writer, request, decoded, &state)
}

func decodeRequest(request *http.Request) (chatRequest, error) {
	if request.Body == nil {
		return chatRequest{}, errors.New("request body is required")
	}
	defer func() { _ = request.Body.Close() }()
	limited := http.MaxBytesReader(nil, request.Body, maxRequestBytes)
	decoder := json.NewDecoder(limited)
	var decoded chatRequest
	if err := decoder.Decode(&decoded); err != nil {
		return chatRequest{}, err
	}
	if err := requireEOF(decoder); err != nil {
		return chatRequest{}, err
	}
	if decoded.Model == "" {
		return chatRequest{}, errors.New("model is required")
	}
	return decoded, nil
}

func (h *Handler) serveSuccessBuffered(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if err := state.reach(request.Context(), EventResponseTerminalReady); err != nil {
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"id": h.responseID(state.ordinal), "object": "chat.completion", "created": h.createdAt(), "model": decoded.Model,
		"choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "deterministic buffered response"}, "finish_reason": "stop"}},
		"usage":   map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7},
	})
}

func (h *Handler) serveSuccessStream(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeProviderError(writer, http.StatusInternalServerError, "streaming_unsupported")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	identifier := h.responseID(state.ordinal)
	steps := h.scenario.profile.Behavior.Steps
	if len(steps) == 0 {
		steps = []string{"deterministic ", "streaming response"}
	}
	for index, step := range steps {
		if err := state.reach(request.Context(), EventResponseChunkReady); err != nil {
			return
		}
		chunk := map[string]any{
			"id": identifier, "object": "chat.completion.chunk", "created": h.createdAt(), "model": decoded.Model,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": step}, "finish_reason": nil}},
		}
		if index == 0 {
			chunk["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["role"] = "assistant"
		}
		if err := writeSSE(writer, chunk); err != nil {
			state.cancel()
			return
		}
		flusher.Flush()
	}
	if err := state.reach(request.Context(), EventResponseTerminalReady); err != nil {
		return
	}
	finish := map[string]any{
		"id": identifier, "object": "chat.completion.chunk", "created": h.createdAt(), "model": decoded.Model,
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	}
	if err := writeSSE(writer, finish); err != nil {
		state.cancel()
		return
	}
	if decoded.StreamOptions != nil && decoded.StreamOptions.IncludeUsage {
		usage := map[string]any{
			"id": identifier, "object": "chat.completion.chunk", "created": h.createdAt(), "model": decoded.Model,
			"choices": []any{}, "usage": map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7},
		}
		if err := writeSSE(writer, usage); err != nil {
			state.cancel()
			return
		}
	}
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	flusher.Flush()
}

func (h *Handler) responseID(ordinal uint64) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, MatrixSchemaVersion)
	_, _ = io.WriteString(hash, "\x00"+h.scenario.profile.ID+"\x00")
	var encoded [16]byte
	binary.BigEndian.PutUint64(encoded[:8], uint64(h.scenario.seed))
	binary.BigEndian.PutUint64(encoded[8:], ordinal)
	_, _ = hash.Write(encoded[:])
	return "chatcmpl-mock-" + hex.EncodeToString(hash.Sum(nil)[:8])
}

func (h *Handler) createdAt() int64 {
	value := h.scenario.seed % 1_000_000
	if value < 0 {
		value = -value
	}
	return 1_760_000_000 + value
}

func writeSSE(writer io.Writer, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(writer, "data: %s\n\n", encoded)
	return err
}

func writeProviderError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"error": map[string]any{"type": code, "code": code}})
}
