package health

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLivenessAndReadinessHaveDistinctSemantics(t *testing.T) {
	t.Parallel()

	state := NewState()
	mux := http.NewServeMux()
	state.Register(mux)

	assertStatus(t, mux, http.MethodGet, LivenessPath, http.StatusOK, "{\"status\":\"live\"}\n")
	assertStatus(t, mux, http.MethodGet, ReadinessPath, http.StatusServiceUnavailable, "{\"status\":\"not_ready\"}\n")

	state.SetReady(true)
	assertStatus(t, mux, http.MethodGet, ReadinessPath, http.StatusOK, "{\"status\":\"ready\"}\n")

	state.SetReady(false)
	assertStatus(t, mux, http.MethodGet, LivenessPath, http.StatusOK, "{\"status\":\"live\"}\n")
	assertStatus(t, mux, http.MethodGet, ReadinessPath, http.StatusServiceUnavailable, "{\"status\":\"not_ready\"}\n")
}

func TestHealthEndpointsAllowHeadButRejectMutationMethods(t *testing.T) {
	t.Parallel()

	state := NewState()
	state.SetReady(true)
	mux := http.NewServeMux()
	state.Register(mux)

	assertStatus(t, mux, http.MethodHead, LivenessPath, http.StatusOK, "")
	assertStatus(t, mux, http.MethodPost, ReadinessPath, http.StatusMethodNotAllowed, "Method Not Allowed\n")
}

func assertStatus(t *testing.T, handler http.Handler, method, path string, wantStatus int, wantBody string) {
	t.Helper()

	request := httptest.NewRequest(method, path, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != wantStatus {
		t.Errorf("%s %s status = %d, want %d", method, path, recorder.Code, wantStatus)
	}
	if recorder.Body.String() != wantBody {
		t.Errorf("%s %s body = %q, want %q", method, path, recorder.Body.String(), wantBody)
	}
}
