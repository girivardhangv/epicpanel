package httpapi

import (
	"sync"
	"time"
)

// TokenBucket rate limiter (per key). In-memory; suitable for a single
// control-plane instance. Multi-instance deployments should front this with
// a shared limiter (e.g. Redis) — noted in EPICPANEL.md.
type TokenBucket struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	rate   float64 // tokens per second
	burst  float64
}

func NewTokenBucket() *TokenBucket {
	rl := &TokenBucket{buckets: make(map[string]*bucket)}
	// Evict idle buckets so spoofed-XFF key rotation cannot grow memory
	// without bound (audit S11).
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for range t.C {
			rl.evictIdle(30 * time.Minute)
		}
	}()
	return rl
}

func (rl *TokenBucket) evictIdle(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle)
	rl.mu.Lock()
	defer rl.mu.Unlock()
	for k, b := range rl.buckets {
		if b.last.Before(cutoff) {
			delete(rl.buckets, k)
		}
	}
}

// Allow reports whether one request for key is permitted under rate/burst.
func (rl *TokenBucket) Allow(key string, rate, burst float64) bool {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &bucket{tokens: burst - 1, last: now, rate: rate, burst: burst}
		return true
	}
	elapsed := now.Sub(b.last).Seconds()
	b.tokens += elapsed * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true
	}
	return false
}

// BucketCount is exposed for tests.
func (rl *TokenBucket) BucketCount() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return len(rl.buckets)
}
