// Package monitoring is the agent's telemetry subsystem: periodic bounded
// metric collection (Linux: /proc-based; Windows: native APIs behind
// build-tagged collectors), a monotonic sequence counter, a bounded send
// buffer with drop-oldest semantics, and retry with backoff. Collection
// never blocks and never overlaps itself; hosting operations are unaffected
// by telemetry failures.
package monitoring

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Collector produces one normalized snapshot per cycle. Implementations are
// build-tagged (linux.go / windows.go); nothing OS-specific lives here.
type Collector interface {
	Collect(ctx context.Context) (*Sample, error)
}

// Sample mirrors the backend's normalized telemetry payload (internal/
// monitoring types.go). Keep the JSON tags in sync — see docs/api.md.
type Sample struct {
	Timestamp string `json:"timestamp"`
	Sequence  int64  `json:"sequence"`
	AgentVer  string `json:"agent_version,omitempty"`

	CPUUsage   *float64 `json:"cpu_usage_percent"`
	CPUUser    *float64 `json:"cpu_user_percent"`
	CPUSystem  *float64 `json:"cpu_system_percent"`
	CPUIdle    *float64 `json:"cpu_idle_percent"`
	Load1      *float64 `json:"load_1m"`
	Load5      *float64 `json:"load_5m"`
	Load15     *float64 `json:"load_15m"`

	MemoryTotalBytes     *int64   `json:"memory_total_bytes"`
	MemoryUsedBytes      *int64   `json:"memory_used_bytes"`
	MemoryAvailableBytes *int64   `json:"memory_available_bytes"`
	MemoryFreeBytes      *int64   `json:"memory_free_bytes"`
	MemoryUsagePercent   *float64 `json:"memory_usage_percent"`
	SwapTotalBytes       *int64   `json:"swap_total_bytes"`
	SwapUsedBytes        *int64   `json:"swap_used_bytes"`
	SwapUsagePercent     *float64 `json:"swap_usage_percent"`

	UptimeSeconds *int64 `json:"uptime_seconds"`

	Disks     []DiskMetric      `json:"disk"`
	Network   []NetworkMetric   `json:"network"`
	Processes []ProcessMetric   `json:"processes"`
	Services  []ServiceHealth   `json:"services"`
	Errors    map[string]string `json:"monitoring_errors,omitempty"`
}

// List-shaped metrics (payload bounds enforced on the backend as well).
type DiskMetric struct {
	Mount        string  `json:"mount"`
	Filesystem   string  `json:"fs,omitempty"`
	TotalBytes   int64   `json:"total_bytes"`
	UsedBytes    int64   `json:"used_bytes"`
	FreeBytes    int64   `json:"free_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

type NetworkMetric struct {
	Interface string  `json:"interface"`
	RxBytes   float64 `json:"rx_bytes"`
	TxBytes   float64 `json:"tx_bytes"`
	RxPackets float64 `json:"rx_packets"`
	TxPackets float64 `json:"tx_packets"`
	Errors    float64 `json:"errors"`
	Drops     float64 `json:"drops"`
}

type ProcessMetric struct {
	Name        string   `json:"name"`
	PID         int32    `json:"pid"`
	CPUPercent  *float64 `json:"cpu_percent"`
	MemoryBytes uint64   `json:"memory_bytes"`
	Status      string   `json:"status"`
}

type ServiceHealth struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
	Running     bool   `json:"running"`
	Enabled     *bool  `json:"enabled"`
	LastChecked string `json:"last_checked,omitempty"`
}

// Service status vocabulary (shared with the panel contract).
const (
	ServiceRunning      = "Running"
	ServiceStopped      = "Stopped"
	ServiceFailed       = "Failed"
	ServiceUnknown      = "Unknown"
	ServiceNotInstalled = "NotInstalled"
)

// Bounds.
const (
	BufferCapacity     = 240 // ~1h of 15s samples; oldest are dropped when full
	MaxBatchSamples    = 60  // must stay <= backend MaxSamplesPerBatch
	TopProcesses       = 20  // top-CPU + top-memory, deduplicated
	MaxDisks           = 24
	MaxInterfaces      = 32
	MaxSendBackoffTick = 20 // ticks between sends after repeated failures
)

// ---------------------------------------------------------------------------
// Sequence counter (persisted across agent restarts)
// ---------------------------------------------------------------------------

type Sequence struct {
	mu   sync.Mutex
	path string
	next int64
}

// NewSequence loads (or initializes) the persisted counter.
func NewSequence(stateDir string) *Sequence {
	s := &Sequence{path: filepath.Join(stateDir, "telemetry_state.json"), next: 1}
	if raw, err := os.ReadFile(s.path); err == nil {
		var st struct {
			Last int64 `json:"last_sequence"`
		}
		if json.Unmarshal(raw, &st) == nil && st.Last > 0 {
			s.next = st.Last + 1
		}
	}
	return s
}

// Next returns the next monotonically increasing number and persists it.
// A failed persistence keeps counting in memory (ordering still holds
// within the process; a restart may resume from the last durable value).
func (s *Sequence) Next() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.next
	s.next++
	raw, _ := json.Marshal(struct {
		Last int64 `json:"last_sequence"`
	}{v})
	_ = os.MkdirAll(filepath.Dir(s.path), 0o700)
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, raw, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
	return v
}

// ---------------------------------------------------------------------------
// Bounded buffer with drop-oldest semantics (§31: telemetry failure must
// never consume unlimited memory)
// ---------------------------------------------------------------------------

type Buffer struct {
	mu       sync.Mutex
	items    []*Sample
	capacity int
}

func NewBuffer(capacity int) *Buffer {
	return &Buffer{capacity: capacity}
}

// Push appends a sample; when full the oldest sample is dropped.
func (b *Buffer) Push(s *Sample) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.items) >= b.capacity {
		b.items = b.items[1:]
	}
	b.items = append(b.items, s)
}

// TakeBatch removes up to n oldest samples for delivery.
func (b *Buffer) TakeBatch(n int) []*Sample {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > len(b.items) {
		n = len(b.items)
	}
	out := b.items[:n]
	b.items = b.items[n:]
	return out
}

// Len reports the current depth (for tests/observability).
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.items)
}

// ---------------------------------------------------------------------------
// Runner: non-overlapping collection + backoff-driven delivery
// ---------------------------------------------------------------------------

type Sender func(ctx context.Context, samples []*Sample) error

type Runner struct {
	collector   Collector
	sequence    *Sequence
	buffer      *Buffer
	sender      Sender
	interval    time.Duration
	version     string

	collecting  sync.Mutex // prevents overlapping collection cycles (§11)
	inFlight    sync.Mutex // prevents overlapping sends
	ticks       int
	failures    int
	nextAttempt int // tick index when delivery may be attempted again
}

// NewRunner validates the interval: anything outside 10s..300s is clamped so
// a misconfiguration can never hammer the host or the panel (§11/§32).
func NewRunner(collector Collector, sequence *Sequence, sender Sender, interval time.Duration, version string) *Runner {
	if interval < 10*time.Second {
		interval = 10 * time.Second
	}
	if interval > 300*time.Second {
		interval = 300 * time.Second
	}
	return &Runner{
		collector: collector,
		sequence:  sequence,
		buffer:    NewBuffer(BufferCapacity),
		sender:    sender,
		interval:  interval,
		version:   version,
	}
}

// Run blocks until ctx is done, collecting and sending on the ticker.
func (r *Runner) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.cycle(ctx)
		}
	}
}

// Cycle runs one collect+flush pass; exported for tests.
func (r *Runner) Cycle(ctx context.Context) { r.cycle(ctx) }

func (r *Runner) cycle(ctx context.Context) {
	// Collect without ever overlapping (§11: if one collection takes longer
	// than the interval, skip the tick rather than piling up goroutines).
	sample := func() *Sample {
		if !r.collecting.TryLock() {
			return nil
		}
		defer r.collecting.Unlock()
		s, err := r.collector.Collect(ctx)
		if err != nil || s == nil {
			return nil
		}
		return s
	}()
	if sample != nil {
		sample.Sequence = r.sequence.Next()
		sample.Timestamp = time.Now().UTC().Format(time.RFC3339)
		sample.AgentVer = r.version
		r.buffer.Push(sample)
	}

	// Deliver with exponential backoff; failures never crash the agent (§31)
	// and never block collection.
	if !r.inFlight.TryLock() {
		return
	}
	defer r.inFlight.Unlock()
	r.ticks++
	if r.ticks < r.nextAttempt {
		return
	}
	batch := r.buffer.TakeBatch(MaxBatchSamples)
	if len(batch) == 0 {
		r.failures = 0
		return
	}
	if err := r.sender(ctx, batch); err != nil {
		// Put the batch back (bounded buffer will drop oldest if it fills),
		// then back off exponentially: 2, 4, 8, 16, 32 ticks.
		r.restore(batch)
		if r.failures < 5 {
			r.failures++
		}
		r.nextAttempt = r.ticks + (1 << r.failures)
		return
	}
	r.failures = 0
	r.nextAttempt = 0
}

func (r *Runner) restore(batch []*Sample) {
	for _, s := range batch {
		r.buffer.Push(s)
	}
}
