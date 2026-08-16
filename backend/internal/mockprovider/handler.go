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
		_ = writeProviderError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	decoded, err := decodeRequest(request)
	if err != nil {
		_ = writeProviderError(writer, http.StatusBadRequest, "invalid_request")
		return
	}
	mode := ModeBuffered
	if decoded.Stream {
		mode = ModeStreaming
	}
	profile := h.scenario.profile
	if profile.Mode != ModeEither && profile.Mode != mode {
		_ = writeProviderError(writer, http.StatusBadRequest, "mode_mismatch")
		return
	}
	state := h.scenario.begin(mode)
	if err := state.reach(request.Context(), EventResponseHeadersReady); err != nil {
		return
	}
	h.serveProfile(writer, request, decoded, &state)
}

func (h *Handler) serveProfile(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	switch h.scenario.profile.Behavior.Kind {
	case "success_buffered":
		h.serveSuccessBuffered(writer, request, decoded, state)
	case "success_stream":
		h.serveSuccessStream(writer, request, decoded, state)
	case "gated_stream":
		h.serveSuccessStream(writer, request, decoded, state)
	case "stall_after_headers", "await_cancellation":
		h.serveUntilCancellation(writer, request, decoded, state)
	case "fail_then_succeed":
		h.serveFailureSequence(writer, request, decoded, state)
	case "http_status":
		h.serveHTTPStatus(writer, request, state)
	case "oversized_buffered":
		h.serveOversizedBuffered(writer, request, state)
	case "oversized_sse":
		h.serveOversizedSSE(writer, request, state)
	case "malformed_json":
		h.serveMalformedJSON(writer, request, state)
	case "malformed_sse":
		if err := h.serveRawStream(writer, request, state, "event: message\ndata: {}\n\n", false); err != nil {
			state.cancel()
		}
	case "malformed_sse_json":
		if err := h.serveRawStream(writer, request, state, "data: {\n\n", false); err != nil {
			state.cancel()
		}
	case "early_eof_pre_output":
		if err := h.serveRawStream(writer, request, state, "", false); err != nil {
			state.cancel()
		}
	case "early_eof_post_output":
		h.serveEarlyEOFPostOutput(writer, request, decoded, state)
	case "empty_output":
		h.serveEmptyOutput(writer, request, decoded, state)
	case "invalid_tool_arguments":
		h.serveInvalidToolArguments(writer, request, decoded, state)
	case "partial_tool_arguments":
		h.servePartialToolArguments(writer, request, decoded, state)
	case "structured_schema_violation":
		h.serveStructuredViolation(writer, request, decoded, state)
	case "invalid_event_order":
		h.serveInvalidEventOrder(writer, request, decoded, state)
	case "usage_inconsistent":
		h.serveUsageInconsistent(writer, request, decoded, state)
	case "silent_parameter_ignored":
		h.serveSyntheticSuccess(writer, request, decoded, state, "deterministic parameter baseline")
	case "silent_context_truncation":
		h.serveSyntheticSuccess(writer, request, decoded, state, "synthetic context prefix only")
	case "silent_degenerate_output":
		h.serveSyntheticSuccess(writer, request, decoded, state, "repeat repeat repeat repeat")
	case "silent_unstable_recovery":
		content := "stable synthetic recovery"
		if state.ordinal <= uint64(h.scenario.profile.Behavior.FailuresBeforeSuccess) {
			content = "repeat repeat repeat repeat"
		}
		h.serveSyntheticSuccess(writer, request, decoded, state, content)
	default:
		if err := writeProviderError(writer, http.StatusNotImplemented, "profile_not_implemented"); err != nil {
			state.cancel()
		}
	}
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
	h.serveBufferedText(writer, request, decoded, state, "deterministic buffered response")
}

func (h *Handler) serveBufferedText(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState, content string) {
	if err := state.reach(request.Context(), EventResponseTerminalReady); err != nil {
		return
	}
	message := map[string]any{"role": "assistant", "content": content}
	if err := h.writeBuffered(writer, decoded.Model, state.ordinal, message, "stop", validUsage()); err != nil {
		state.cancel()
	}
}

func (h *Handler) serveSuccessStream(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	h.serveStreamText(writer, request, decoded, state, h.scenario.profile.Behavior.Steps)
}

func (h *Handler) serveStreamText(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState, steps []string) {
	_, ok := writer.(http.Flusher)
	if !ok {
		if err := writeProviderError(writer, http.StatusInternalServerError, "streaming_unsupported"); err != nil {
			state.cancel()
		}
		return
	}
	controller := http.NewResponseController(writer)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	identifier := h.responseID(state.ordinal)
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
		if err := controller.Flush(); err != nil {
			state.cancel()
			return
		}
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
	if _, err := io.WriteString(writer, "data: [DONE]\n\n"); err != nil {
		state.cancel()
		return
	}
	if err := controller.Flush(); err != nil {
		state.cancel()
	}
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

func writeProviderError(writer http.ResponseWriter, status int, code string) error {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	return writeJSON(writer, map[string]any{"error": map[string]any{"type": code, "code": code}})
}

func writeJSON(writer io.Writer, value any) error { return json.NewEncoder(writer).Encode(value) }
