package mockprovider

import (
	"io"
	"net/http"
	"strings"
)

func (h *Handler) serveHTTPStatus(writer http.ResponseWriter, request *http.Request, state *requestState) {
	if !h.readyTerminal(request, state) {
		return
	}
	behavior := h.scenario.profile.Behavior
	if behavior.RetryAfter != "" {
		writer.Header().Set("Retry-After", behavior.RetryAfter)
	}
	writeProviderError(writer, behavior.Status, "injected_status")
}

func (h *Handler) serveOversizedBuffered(writer http.ResponseWriter, request *http.Request, state *requestState) {
	if !h.readyTerminal(request, state) {
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, strings.Repeat("x", h.scenario.profile.Behavior.Bytes))
}

func (h *Handler) serveOversizedSSE(writer http.ResponseWriter, request *http.Request, state *requestState) {
	if err := state.reach(request.Context(), EventResponseChunkReady); err != nil {
		return
	}
	h.serveRawStream(writer, request, state, "data: "+strings.Repeat("x", h.scenario.profile.Behavior.Bytes)+"\n\n", false)
}

func (h *Handler) serveMalformedJSON(writer http.ResponseWriter, request *http.Request, state *requestState) {
	if !h.readyTerminal(request, state) {
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, `{"id":`)
}

func (h *Handler) serveEarlyEOFPostOutput(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if err := state.reach(request.Context(), EventResponseChunkReady); err != nil {
		return
	}
	chunk := streamChunk(h.responseID(state.ordinal), h.createdAt(), decoded.Model, map[string]any{"role": "assistant", "content": "visible"}, nil)
	h.serveRawStream(writer, request, state, mustSSE(chunk), false)
}

func (h *Handler) serveEmptyOutput(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if !h.readyTerminal(request, state) {
		return
	}
	h.writeBuffered(writer, decoded.Model, state.ordinal, map[string]any{"role": "assistant", "content": nil}, "stop", validUsage())
}

func (h *Handler) serveInvalidToolArguments(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if !h.readyTerminal(request, state) {
		return
	}
	message := map[string]any{
		"role": "assistant", "content": nil,
		"tool_calls": []any{map[string]any{"id": "call-mock", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"city":`}}},
	}
	h.writeBuffered(writer, decoded.Model, state.ordinal, message, "tool_calls", validUsage())
}

func (h *Handler) servePartialToolArguments(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if err := state.reach(request.Context(), EventResponseChunkReady); err != nil {
		return
	}
	id := h.responseID(state.ordinal)
	started := streamChunk(id, h.createdAt(), decoded.Model, map[string]any{
		"role":       "assistant",
		"tool_calls": []any{map[string]any{"index": 0, "id": "call-mock", "type": "function", "function": map[string]any{"name": "lookup", "arguments": `{"city":`}}},
	}, nil)
	finished := streamChunk(id, h.createdAt(), decoded.Model, map[string]any{}, "tool_calls")
	h.serveRawStream(writer, request, state, mustSSE(started)+mustSSE(finished)+"data: [DONE]\n\n", false)
}

func (h *Handler) serveStructuredViolation(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if !h.readyTerminal(request, state) {
		return
	}
	h.writeBuffered(writer, decoded.Model, state.ordinal, map[string]any{"role": "assistant", "content": `{"answer":17}`}, "stop", validUsage())
}

func (h *Handler) serveInvalidEventOrder(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if err := state.reach(request.Context(), EventResponseChunkReady); err != nil {
		return
	}
	id := h.responseID(state.ordinal)
	finished := streamChunk(id, h.createdAt(), decoded.Model, map[string]any{"role": "assistant"}, "tool_calls")
	h.serveRawStream(writer, request, state, mustSSE(finished)+"data: [DONE]\n\n", false)
}

func (h *Handler) serveUsageInconsistent(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if !h.readyTerminal(request, state) {
		return
	}
	h.writeBuffered(writer, decoded.Model, state.ordinal, map[string]any{"role": "assistant", "content": "usage"}, "stop", map[string]any{"prompt_tokens": 2, "completion_tokens": 3, "total_tokens": 99})
}

func (h *Handler) serveRawStream(writer http.ResponseWriter, request *http.Request, state *requestState, body string, terminal bool) {
	if terminal && !h.readyTerminal(request, state) {
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, body)
	if flusher, ok := writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (h *Handler) readyTerminal(request *http.Request, state *requestState) bool {
	return state.reach(request.Context(), EventResponseTerminalReady) == nil
}

func (h *Handler) writeBuffered(writer http.ResponseWriter, model string, ordinal uint64, message map[string]any, finishReason string, usage map[string]any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_ = writeJSON(writer, map[string]any{
		"id": h.responseID(ordinal), "object": "chat.completion", "created": h.createdAt(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}}, "usage": usage,
	})
}

func streamChunk(id string, created int64, model string, delta map[string]any, finishReason any) map[string]any {
	return map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}},
	}
}

func validUsage() map[string]any {
	return map[string]any{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7}
}

func mustSSE(value any) string {
	var builder strings.Builder
	if err := writeSSE(&builder, value); err != nil {
		panic(err)
	}
	return builder.String()
}
