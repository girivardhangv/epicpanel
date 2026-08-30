package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type ctxKey int

const (
	ctxRequestID ctxKey = iota
)

// RequestID assigns a unique id per request and echoes it back as a header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		} else if len(id) > 64 {
			id = ""
		}
		if id == "" {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

// RequestIDFrom retrieves the request id stored by RequestID middleware.
func RequestIDFrom(r *http.Request) string {
	v, _ := r.Context().Value(ctxRequestID).(string)
	return v
}

// SecurityHeaders sets baseline hardening headers on every response.
func SecurityHeaders(isProd bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			if strings.HasPrefix(r.URL.Path, "/api/") {
				h.Set("Cache-Control", "no-store")
				h.Set("Content-Type", "application/json; charset=utf-8")
			}
			next.ServeHTTP(w, r)
		})
	}
}

// BodyLimit wraps MaxBytesReader with a fixed cap.
func BodyLimit(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && limit > 0 {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP resolves the caller IP honouring at most one trusted proxy hop.
func ClientIP(r *http.Request, trustedProxyCIDR string) string {
	if trustedProxyCIDR != "" {
		if _, network, err := net.ParseCIDR(trustedProxyCIDR); err == nil {
			remote := remoteAddrIP(r)
			if remote != nil && network.Contains(remote) {
				if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
					parts := strings.Split(fwd, ",")
					if ip := net.ParseIP(strings.TrimSpace(parts[0])); ip != nil {
						return ip.String()
					}
				}
			}
		}
	}
	if ip := remoteAddrIP(r); ip != nil {
		return ip.String()
	}
	return ""
}

func remoteAddrIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return net.ParseIP(r.RemoteAddr)
	}
	return net.ParseIP(host)
}
