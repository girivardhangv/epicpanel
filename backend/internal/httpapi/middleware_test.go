package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStatusRecorderImplementsHijacker — WebSocket upgrades (gorilla) require
// the ResponseWriter to implement http.Hijacker; wrapper middleware must
// preserve that interface (ADR-035).
func TestStatusRecorderImplementsHijacker(t *testing.T) {
	inner := httptest.NewRecorder()
	var outer http.ResponseWriter = &statusRecorder{ResponseWriter: inner, status: 200}
	if _, ok := outer.(http.Hijacker); !ok {
		t.Fatal("statusRecorder does not implement http.Hijacker — WebSocket upgrades will fail with 500")
	}
	if _, ok := outer.(http.Flusher); !ok {
		t.Fatal("statusRecorder does not implement http.Flusher")
	}
}
