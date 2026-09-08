package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
)

// WSConnLimiter caps concurrent WebSocket connections per authenticated
// principal (session user, API-token org, or agent token) and per client IP.
// Without it a single browser tab loop or script can pin file descriptors
// and fan-out goroutines until the process exhausts them (DoS).
//
// Wiring (coordinator): wrap the mux (or the individual WS routes) with
// limiter.Middleware — it counts an upgrade as a connection for as long as
// the wrapped handler is running. Hub-style WS handlers block until the
// connection closes, so the decrement on handler return is exact.
type WSConnLimiter struct {
	mu sync.Mutex

	perPrincipal map[string]int
	perIP        map[string]int

	MaxPerPrincipal int
	MaxPerIP        int

	// KeyFunc overrides principal-key extraction (tests). Nil = default.
	KeyFunc func(r *http.Request) string
}

func NewWSConnLimiter(maxPerPrincipal, maxPerIP int) *WSConnLimiter {
	if maxPerPrincipal <= 0 {
		maxPerPrincipal = 8
	}
	if maxPerIP <= 0 {
		maxPerIP = 32
	}
	return &WSConnLimiter{
		perPrincipal:    make(map[string]int),
		perIP:           make(map[string]int),
		MaxPerPrincipal: maxPerPrincipal,
		MaxPerIP:        maxPerIP,
	}
}

// Counts snapshots the current tracked connection counts (tests/observability).
func (l *WSConnLimiter) Counts() (perPrincipal map[string]int, perIP map[string]int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := make(map[string]int, len(l.perPrincipal))
	for k, v := range l.perPrincipal {
		a[k] = v
	}
	b := make(map[string]int, len(l.perIP))
	for k, v := range l.perIP {
		b[k] = v
	}
	return a, b
}

// principalKey derives a stable per-identity key without storing the secret:
// session/API-token bearers and cookies hash to a fixed tag; when no
// credential is present the IP remains the only key.
func (l *WSConnLimiter) principalKey(r *http.Request) string {
	if l.KeyFunc != nil {
		return l.KeyFunc(r)
	}
	if u, ok := UserFrom(r.Context()); ok {
		if IsAPIToken(r.Context()) {
			return "tok:" + TokenOrgID(r.Context())
		}
		return "usr:" + u.ID
	}
	if authz := r.Header.Get("Authorization"); strings.HasPrefix(authz, "Bearer ") {
		sum := sha256.Sum256([]byte(strings.TrimPrefix(authz, "Bearer ")))
		return "tok:" + hex.EncodeToString(sum[:8])
	}
	if c, err := r.Cookie("epicpanel_session"); err == nil && c.Value != "" {
		sum := sha256.Sum256([]byte(c.Value))
		return "usr:" + hex.EncodeToString(sum[:8])
	}
	return ""
}

func (l *WSConnLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		principal := l.principalKey(r)

		l.mu.Lock()
		if principal != "" && l.perPrincipal[principal] >= l.MaxPerPrincipal {
			l.mu.Unlock()
			RespondError(w, &APIError{Status: http.StatusTooManyRequests, Code: "ws_connection_limit", Message: "too many concurrent websocket connections"})
			return
		}
		if l.perIP[ip] >= l.MaxPerIP {
			l.mu.Unlock()
			RespondError(w, &APIError{Status: http.StatusTooManyRequests, Code: "ws_connection_limit", Message: "too many concurrent websocket connections from this address"})
			return
		}
		if principal != "" {
			l.perPrincipal[principal]++
		}
		l.perIP[ip]++
		l.mu.Unlock()

		defer func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			if principal != "" {
				if l.perPrincipal[principal] > 0 {
					l.perPrincipal[principal]--
				}
				if l.perPrincipal[principal] == 0 {
					delete(l.perPrincipal, principal)
				}
			}
			if l.perIP[ip] > 0 {
				l.perIP[ip]--
			}
			if l.perIP[ip] == 0 {
				delete(l.perIP, ip)
			}
		}()

		next.ServeHTTP(w, r)
	})
}
