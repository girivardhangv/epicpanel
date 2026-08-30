// Package httpx provides an in-memory fixed-window rate limiter used for
// authentication brute-force protection and general abuse throttling.
// It is deliberately dependency-free; horizontal deployments can later swap
// this implementation for a shared store without touching handlers.
package httpx

import (
	"net/http"
	"sync"
	"time"
)

type bucket struct {
	count     int
	windowEnd time.Time
}

type RateLimiter struct {
	mu      sync.Mutex
	limit   int
	per     time.Duration
	hits    map[string]*bucket
	sweptAt time.Time
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		per:     window,
		hits:    map[string]*bucket{},
		sweptAt: time.Now(),
	}
}

// Allow reports whether key may proceed within the current window.
func (rl *RateLimiter) Allow(key string) bool {
	if rl.limit <= 0 {
		return true // disabled
	}
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if now.Sub(rl.sweptAt) > time.Minute {
		for k, b := range rl.hits {
			if now.After(b.windowEnd) {
				delete(rl.hits, k)
			}
		}
		rl.sweptAt = now
	}

	b, ok := rl.hits[key]
	if !ok || now.After(b.windowEnd) {
		rl.hits[key] = &bucket{count: 1, windowEnd: now.Add(rl.per)}
		return true
	}
	b.count++
	return b.count <= rl.limit
}

// Middleware applies the limiter keyed by client IP.
func (rl *RateLimiter) Middleware(keyFn func(*http.Request) string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := "?"
			if keyFn != nil {
				key = keyFn(r)
			}
			if !rl.Allow(key) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, `{"error":{"code":"RATE_LIMITED","message":"Too many requests"}}`, http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
