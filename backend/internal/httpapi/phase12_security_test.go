package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAgentReplayGuard covers the advisory replay-protection middleware:
// absent headers pass through; present headers must be strictly
// forward-moving per agent key; malformed and future-skewed frames are
// rejected; enrollment is exempt.
func TestAgentReplayGuard(t *testing.T) {
	store := NewInMemoryAgentReplay()
	keyFn := func(r *http.Request) (string, bool) {
		tok := r.Header.Get("Authorization")
		if tok == "" {
			return "", false
		}
		return tok, true
	}
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ })

	h := AgentReplayGuard(store, keyFn, next)

	do := func(headers map[string]string) int {
		req := httptest.NewRequest("POST", "/v1/agent/jobs/claim", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Advisory: no headers -> pass.
	if code := do(nil); code != 200 || calls != 1 {
		t.Fatalf("headerless request: %d (calls=%d) want pass", code, calls)
	}

	// First frame accepted.
	if code := do(map[string]string{"Authorization": "agt_1", "X-EpicPanel-Seq": "10", "X-EpicPanel-Time": "1700000000000"}); code != 200 {
		t.Fatalf("first frame: %d want 200", code)
	}
	// Same seq -> replay (409).
	if code := do(map[string]string{"Authorization": "agt_1", "X-EpicPanel-Seq": "10", "X-EpicPanel-Time": "1700000000001"}); code != 409 {
		t.Fatalf("duplicate frame: %d want 409", code)
	}
	// Lower seq -> replay.
	if code := do(map[string]string{"Authorization": "agt_1", "X-EpicPanel-Seq": "9", "X-EpicPanel-Time": "1700000000002"}); code != 409 {
		t.Fatalf("stale frame: %d want 409", code)
	}
	// Higher seq -> accepted.
	if code := do(map[string]string{"Authorization": "agt_1", "X-EpicPanel-Seq": "11", "X-EpicPanel-Time": "1700000000003"}); code != 200 {
		t.Fatalf("forward frame: %d want 200", code)
	}
	// Same seq under a DIFFERENT key -> accepted (per-agent high-water).
	if code := do(map[string]string{"Authorization": "agt_2", "X-EpicPanel-Seq": "10", "X-EpicPanel-Time": "1700000000004"}); code != 200 {
		t.Fatalf("per-key isolation: %d want 200", code)
	}
	// Malformed seq -> 422 (validation error).
	if code := do(map[string]string{"Authorization": "agt_1", "X-EpicPanel-Seq": "not-a-number"}); code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed seq: %d want 422", code)
	}
	// Timestamp too far in the future -> 409 (clock-sanity reject).
	if code := do(map[string]string{"Authorization": "agt_1", "X-EpicPanel-Seq": "12", "X-EpicPanel-Time": "9999999999999"}); code != 409 {
		t.Fatalf("future-skewed frame: %d want 409", code)
	}
	// Headers present but no agent identity -> 401 (fail closed).
	if code := do(map[string]string{"X-EpicPanel-Seq": "13"}); code != 401 {
		t.Fatalf("headers without key: %d want 401", code)
	}
	// Enrollment is exempt from the guard.
	if code := do(map[string]string{"Authorization": "agt_3", "X-EpicPanel-Seq": "1", "X-EpicPanel-Time": "1700000000000", "__path": "/v1/agent/enroll"}); code != 200 {
		_ = code // path override not supported by helper; covered below
	}
	enroll := httptest.NewRequest("POST", "/v1/agent/enroll", nil)
	enroll.Header.Set("Authorization", "agt_3")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, enroll)
	if rec.Code != 200 {
		t.Fatalf("enroll exempt: %d want 200", rec.Code)
	}
}

// TestWSConnLimiter enforces per-principal and per-IP caps and full release
// on handler return.
func TestWSConnLimiter(t *testing.T) {
	l := NewWSConnLimiter(2, 3)
	l.KeyFunc = func(r *http.Request) string { return r.Header.Get("X-Key") }

	block := make(chan struct{})
	served := make(chan struct{}, 16)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served <- struct{}{}
		<-block
	})

	h := l.Middleware(next)

	// Two concurrent connections for key A: OK (cap 2).
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/v1/ws", nil)
		req.Header.Set("X-Key", "A")
		go h.ServeHTTP(httptest.NewRecorder(), req)
		<-served
	}
	// Third for the same principal: 429.
	req := httptest.NewRequest("GET", "/v1/ws", nil)
	req.Header.Set("X-Key", "A")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("principal cap: %d want 429", rec.Code)
	}

	// A third connection from a different principal still fits under the
	// per-IP cap of 3.
	req = httptest.NewRequest("GET", "/v1/ws", nil)
	req.Header.Set("X-Key", "B")
	go h.ServeHTTP(httptest.NewRecorder(), req)
	<-served

	// Fourth concurrent from the same IP: 429 (per-IP cap).
	req = httptest.NewRequest("GET", "/v1/ws", nil)
	req.Header.Set("X-Key", "C")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("IP cap: %d want 429", rec.Code)
	}

	// Release everything; counters must drain to zero.
	close(block)
	waitFor(t, func() bool {
		pp, ip := l.Counts()
		return len(pp) == 0 && len(ip) == 0
	})
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached within deadline")
}
