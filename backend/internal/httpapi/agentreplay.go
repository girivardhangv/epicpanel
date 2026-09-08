package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Agent replay protection (advisory middleware — agentproto is read-only for
// this phase). Node-agent REST calls (heartbeat, claim, results) carry
//
//	X-EpicPanel-Seq:    monotonic per-agent counter
//	X-EpicPanel-Time:   unix milliseconds at send
//
// Both must move FORWARD per agent token. This closes: captured-request
// replay (old result/claim frames re-submitted) and stale-packet injection.
// Strictly monotonic (not just "not older than") means a duplicate frame is
// rejected even if it arrives after the original was already processed.
//
// Enrollment (POST /v1/agent/enroll) is exempt: pre-token bootstrap.

type agentSeqEntry struct {
	seq    int64
	tsMs   int64
	seen   time.Time
}

// AgentReplayStore tracks the high-water sequence per agent token hash.
// It is an interface so a Redis-backed store can slot in for multi-instance
// deployments (same shape as the in-memory rate limiter note).
type AgentReplayStore interface {
	// Advance returns nil when (seq, ts) is strictly newer than the recorded
	// high-water mark for key, recording it. It returns false when the frame
	// is a replay or out of order.
	Advance(key string, seq, tsMs int64) (ok bool, err error)
}

// InMemoryAgentReplay is the single-instance implementation. Entries idle
// for 48h are evicted (revoked/rotated tokens stop growing the map).
type InMemoryAgentReplay struct {
	mu      sync.Mutex
	highest map[string]agentSeqEntry
}

func NewInMemoryAgentReplay() *InMemoryAgentReplay {
	r := &InMemoryAgentReplay{highest: make(map[string]agentSeqEntry)}
	go func() {
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		for range t.C {
			r.evict()
		}
	}()
	return r
}

func (r *InMemoryAgentReplay) evict() {
	cutoff := time.Now().Add(-48 * time.Hour)
	r.mu.Lock()
	defer r.mu.Unlock()
	for k, e := range r.highest {
		if e.seen.Before(cutoff) {
			delete(r.highest, k)
		}
	}
}

func (r *InMemoryAgentReplay) Advance(key string, seq, tsMs int64) (bool, error) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, ok := r.highest[key]
	if ok && seq <= cur.seq {
		return false, nil
	}
	// Clock sanity: reject timestamps unreasonably far in the future
	// (5 minutes of slack absorbs modest skew).
	if tsMs > 0 && tsMs > now.UnixMilli()+5*60*1000 {
		return false, nil
	}
	r.highest[key] = agentSeqEntry{seq: seq, tsMs: tsMs, seen: now}
	return true, nil
}

// keyFn mirrors servers.agentTokenKey without an import cycle.
type agentKeyFn func(r *http.Request) (string, bool)

// AgentReplayGuard is the advisory middleware. keyFn extracts a stable
// per-agent key (the raw bearer token hash is ideal). When the headers are
// ABSENT the request passes (advisory rollout); when present they must be
// forward-moving. Fail-closed on store errors.
func AgentReplayGuard(store AgentReplayStore, keyFn agentKeyFn, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/v1/agent/enroll") {
			next.ServeHTTP(w, r)
			return
		}
		seqH := r.Header.Get("X-EpicPanel-Seq")
		tsH := r.Header.Get("X-EpicPanel-Time")
		if seqH != "" || tsH != "" {
			key, ok := keyFn(r)
			if !ok || key == "" {
				RespondError(w, ErrUnauthorized("agent authentication required"))
				return
			}
			seq, err := strconv.ParseInt(seqH, 10, 64)
			if err != nil || seq < 0 {
				RespondError(w, ErrValidation("invalid X-EpicPanel-Seq header"))
				return
			}
			var ts int64
			var perr error
			if tsH != "" {
				ts, perr = strconv.ParseInt(tsH, 10, 64)
				if perr != nil || ts < 0 {
					RespondError(w, ErrValidation("invalid X-EpicPanel-Time header"))
					return
				}
			}
			allowed, aerr := store.Advance(key, seq, ts)
			if aerr != nil {
				RespondError(w, ErrInternal(aerr))
				return
			}
			if !allowed {
				RespondError(w, &APIError{Status: http.StatusConflict, Code: "replayed_frame", Message: "agent frame sequence is not forward-moving"})
				return
			}		}
		next.ServeHTTP(w, r)
	})
}

// Context keys for the agent identity (mirrors servers.withAgentServer
// shape; the api wiring adapts).
type agentIdentityKey struct{}

// WithAgentIdentity stores the resolved agent key in the request context.
func WithAgentIdentity(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, agentIdentityKey{}, key)
}

// AgentIdentityFrom returns the agent key set by the api wiring.
func AgentIdentityFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(agentIdentityKey{}).(string)
	return v, ok
}
