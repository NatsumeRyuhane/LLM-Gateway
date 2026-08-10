package health

import (
	"net/http"
	"sync/atomic"
)

const (
	// LivenessPath reports whether the process can serve HTTP.
	LivenessPath = "/livez"
	// ReadinessPath reports whether the process should receive new work.
	ReadinessPath = "/readyz"
)

// State stores process readiness independently from liveness. Its atomic flag
// is safe for HTTP handlers and lifecycle transitions to access concurrently.
type State struct {
	ready atomic.Bool
}

// NewState returns a live but initially unready health state.
func NewState() *State {
	return &State{}
}

// SetReady changes whether the process should receive new work.
func (s *State) SetReady(ready bool) {
	s.ready.Store(ready)
}

// IsReady reports the current readiness state.
func (s *State) IsReady() bool {
	return s.ready.Load()
}

// Register attaches process health handlers to mux. Liveness remains successful
// while the process runs; readiness is withdrawn during startup and shutdown.
func (s *State) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET "+LivenessPath, func(writer http.ResponseWriter, request *http.Request) {
		writeStatus(writer, request, http.StatusOK, `{"status":"live"}`)
	})
	mux.HandleFunc("GET "+ReadinessPath, func(writer http.ResponseWriter, request *http.Request) {
		if !s.IsReady() {
			writeStatus(writer, request, http.StatusServiceUnavailable, `{"status":"not_ready"}`)
			return
		}
		writeStatus(writer, request, http.StatusOK, `{"status":"ready"}`)
	})
}

func writeStatus(writer http.ResponseWriter, request *http.Request, status int, body string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write([]byte(body + "\n"))
}
