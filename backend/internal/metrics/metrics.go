// Package metrics is EpicPanel's internal observability foundation (Phase 3
// §42): a tiny, dependency-free counter/gauge registry plus HTTP middleware
// instrumentation. These metrics describe the panel itself — request volume,
// latency, job queue depth, telemetry ingestion — and are intentionally kept
// out of the customer dashboard for now. Future exposition (admin endpoint,
// Prometheus bridge) can read the same registry without redesign.
package metrics

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

type counter struct {
	mu     sync.Mutex
	values map[string]uint64 // label-set hash -> value
	labels map[string]string
}

// Registry holds all panel-internal metrics.
type Registry struct {
	mu           sync.Mutex
	counters     map[string]*counter
	gauges       map[string]float64
	requestCount uint64
	latencySumMS uint64
	latencyCount uint64
}

var (
	defaultRegistry     *Registry
	defaultRegistryOnce sync.Once
)

// Default returns the process-wide registry.
func Default() *Registry {
	defaultRegistryOnce.Do(func() { defaultRegistry = New() })
	return defaultRegistry
}

func New() *Registry {
	return &Registry{
		counters: map[string]*counter{},
		gauges:   map[string]float64{},
	}
}

// IncCounter increments a named counter, optionally with one label dimension
// (e.g. route, outcome). Labels keep cardinality bounded by design: callers
// must only pass fixed vocabularies.
func (r *Registry) IncCounter(name, labelKey, labelValue string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.counters[name]
	if !ok {
		c = &counter{values: map[string]uint64{}, labels: map[string]string{}}
		r.counters[name] = c
	}
	key := labelKey + "=" + labelValue
	c.values[key]++
	c.labels[key] = labelValue
}

// SetGauge stores the latest value of a gauge (queue depth, rates, health).
func (r *Registry) SetGauge(name string, value float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[name] = value
}

// ObserveRequest records one API request's duration.
func (r *Registry) ObserveRequest(method, path, status string, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requestCount++
	r.latencySumMS += uint64(d.Milliseconds())
	r.latencyCount++
}

// Snapshot returns a bounded, human-readable view for operators/future
// exposition endpoints.
func (r *Registry) Snapshot() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]any{
		"api_requests_total": r.requestCount,
	}
	if r.latencyCount > 0 {
		out["api_latency_avg_ms"] = r.latencySumMS / r.latencyCount
	}
	counters := map[string]map[string]uint64{}
	for name, c := range r.counters {
		byLabel := map[string]uint64{}
		for key, v := range c.values {
			byLabel[key] = v
		}
		counters[name] = byLabel
	}
	out["counters"] = counters
	gauges := map[string]float64{}
	for k, v := range r.gauges {
		gauges[k] = v
	}
	out["gauges"] = gauges
	return out
}

// ---------------------------------------------------------------------------
// HTTP middleware instrumentation
// ---------------------------------------------------------------------------

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Middleware instruments every request into the default registry. Route
// patterns are used (not raw paths) to keep label cardinality finite.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		pattern := "unmatched"
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			if p := rctx.RoutePattern(); p != "" {
				pattern = p
			}
		}
		label := fmt.Sprintf("%s %s", r.Method, pattern)
		Default().IncCounter("api_requests", "route", label)
		statusClass := fmt.Sprintf("%dxx", rec.status/100)
		Default().IncCounter("api_responses", "class", statusClass)
		Default().ObserveRequest(r.Method, pattern, statusClass, time.Since(start))
	})
}

// SortedKeys helps deterministic output in tests.
func SortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
