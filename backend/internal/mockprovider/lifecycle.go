package mockprovider

import (
	"net/http"
)

func (h *Handler) serveUntilCancellation(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	if decoded.Stream {
		writer.Header().Set("Content-Type", "text/event-stream")
	} else {
		writer.Header().Set("Content-Type", "application/json")
	}
	writer.WriteHeader(http.StatusOK)
	if _, ok := writer.(http.Flusher); ok {
		if err := http.NewResponseController(writer).Flush(); err != nil {
			state.cancel()
			return
		}
	}
	<-request.Context().Done()
	state.cancel()
}

func (h *Handler) serveFailureSequence(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState) {
	behavior := h.scenario.profile.Behavior
	if state.ordinal <= uint64(behavior.FailuresBeforeSuccess) {
		if !h.readyTerminal(request, state) {
			return
		}
		if err := writeProviderError(writer, behavior.Status, "injected_sequence_failure"); err != nil {
			state.cancel()
		}
		return
	}
	h.serveSyntheticSuccess(writer, request, decoded, state, "deterministic recovered response")
}

func (h *Handler) serveSyntheticSuccess(writer http.ResponseWriter, request *http.Request, decoded chatRequest, state *requestState, content string) {
	if decoded.Stream {
		h.serveStreamText(writer, request, decoded, state, []string{content})
		return
	}
	h.serveBufferedText(writer, request, decoded, state, content)
}
