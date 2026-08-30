package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Bounded buffer (§31: never consume unlimited memory)
// ---------------------------------------------------------------------------

func TestBufferDropsOldest(t *testing.T) {
	b := NewBuffer(3)
	for i := 0; i < 5; i++ {
		b.Push(&Sample{Sequence: int64(i)})
	}
	if b.Len() != 3 {
		t.Fatalf("buffer len = %d, want 3 (bounded)", b.Len())
	}
	batch := b.TakeBatch(10)
	if len(batch) != 3 {
		t.Fatalf("batch len = %d, want 3", len(batch))
	}
	if batch[0].Sequence != 2 || batch[2].Sequence != 4 {
		t.Errorf("oldest samples were not dropped: got seq %d..%d", batch[0].Sequence, batch[2].Sequence)
	}
	if b.Len() != 0 {
		t.Errorf("buffer should be empty after full take, len=%d", b.Len())
	}
}

func TestBufferTakeBatchPartial(t *testing.T) {
	b := NewBuffer(10)
	for i := 0; i < 5; i++ {
		b.Push(&Sample{Sequence: int64(i)})
	}
	got := b.TakeBatch(2)
	if len(got) != 2 || got[0].Sequence != 0 || got[1].Sequence != 1 {
		t.Errorf("partial batch wrong: %+v", got)
	}
	if b.Len() != 3 {
		t.Errorf("remaining = %d, want 3", b.Len())
	}
}

// ---------------------------------------------------------------------------
// Sequence counter (persisted across restarts)
// ---------------------------------------------------------------------------

func TestSequencePersists(t *testing.T) {
	dir := t.TempDir()
	s1 := NewSequence(dir)
	if got := s1.Next(); got != 1 {
		t.Errorf("first sequence = %d, want 1", got)
	}
	if got := s1.Next(); got != 2 {
		t.Errorf("second sequence = %d, want 2", got)
	}
	// Simulate agent restart: a fresh instance must continue, not restart.
	s2 := NewSequence(dir)
	if got := s2.Next(); got != 3 {
		t.Errorf("sequence after restart = %d, want 3", got)
	}
}

// ---------------------------------------------------------------------------
// Runner: interval clamping, batching, backoff with restore (§11/§31)
// ---------------------------------------------------------------------------

type fakeCollector struct {
	mu      int
	samples []*Sample
	calls   int
}

func (f *fakeCollector) Collect(ctx context.Context) (*Sample, error) {
	f.calls++
	if len(f.samples) == 0 {
		return &Sample{}, nil
	}
	s := f.samples[0]
	f.samples = f.samples[1:]
	return s, nil
}

func TestRunnerClampsInterval(t *testing.T) {
	c := &fakeCollector{}
	r := NewRunner(c, NewSequence(t.TempDir()), func(ctx context.Context, s []*Sample) error { return nil }, time.Second, "test")
	if r.interval != 10*time.Second {
		t.Errorf("too-aggressive interval not clamped: %v", r.interval)
	}
	r2 := NewRunner(c, NewSequence(t.TempDir()), func(ctx context.Context, s []*Sample) error { return nil }, 10*time.Minute, "test")
	if r2.interval != 300*time.Second {
		t.Errorf("too-slow interval not clamped: %v", r2.interval)
	}
}

func TestRunnerDeliversWithSequence(t *testing.T) {
	c := &fakeCollector{}
	var sent [][]*Sample
	sender := func(ctx context.Context, s []*Sample) error {
		sent = append(sent, s)
		return nil
	}
	r := NewRunner(c, NewSequence(t.TempDir()), sender, 15*time.Second, "0.3.0-test")

	// Each cycle flushes what the buffer holds: two cycles deliver two
	// single-sample batches (aggregation happens only under backoff).
	c.samples = []*Sample{{}, {}}
	r.Cycle(context.Background())
	r.Cycle(context.Background())
	if len(sent) != 2 || len(sent[0]) != 1 || len(sent[1]) != 1 {
		t.Fatalf("expected two single-sample batches, got %d batches (%v)", len(sent), sent)
	}
	if sent[0][0].Sequence != 1 || sent[1][0].Sequence != 2 {
		t.Errorf("sequences not monotonic: %d, %d", sent[0][0].Sequence, sent[1][0].Sequence)
	}
	if sent[0][0].AgentVer != "0.3.0-test" {
		t.Errorf("agent version not stamped")
	}
	if sent[0][0].Timestamp == "" {
		t.Errorf("timestamp not stamped")
	}
}

func TestRunnerBackoffAndRestore(t *testing.T) {
	c := &fakeCollector{}
	fail := true
	var attempts int
	sender := func(ctx context.Context, s []*Sample) error {
		attempts++
		if fail {
			return errors.New("panel unreachable")
		}
		return nil
	}
	r := NewRunner(c, NewSequence(t.TempDir()), sender, 15*time.Second, "test")

	c.samples = []*Sample{{}}
	r.Cycle(context.Background())
	if attempts != 1 {
		t.Fatalf("first cycle must attempt delivery, attempts=%d", attempts)
	}
	if r.buffer.Len() != 1 {
		t.Errorf("failed batch must be restored to buffer, len=%d", r.buffer.Len())
	}

	// Backoff: the cycle immediately after a failure must NOT re-attempt.
	c.samples = []*Sample{{}}
	r.Cycle(context.Background())
	if attempts != 1 {
		t.Errorf("backoff violated: attempts=%d on the tick right after a failure", attempts)
	}

	// Persistent failures space attempts exponentially (2, then 4 ticks…).
	for i := 0; i < 4; i++ {
		r.Cycle(context.Background())
	}
	if attempts != 2 {
		t.Errorf("expected exactly one retry within 5 further cycles, attempts=%d", attempts)
	}

	// Recovery: once the sender succeeds, the buffer drains and state resets.
	fail = false
	for i := 0; i < 10 && r.buffer.Len() > 0; i++ {
		r.Cycle(context.Background())
	}
	if r.buffer.Len() != 0 {
		t.Errorf("buffer never drained after recovery, len=%d", r.buffer.Len())
	}
	if r.failures != 0 {
		t.Errorf("failure state not reset after success: %d", r.failures)
	}
}

// ---------------------------------------------------------------------------
// Telemetry serialization: nil (unavailable) fields survive the wire format
// ---------------------------------------------------------------------------

func TestSampleSerializationHonest(t *testing.T) {
	v := 42.5
	s := Sample{Sequence: 9, CPUUsage: &v} // memory fields intentionally nil
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	if got, ok := probe["cpu_usage_percent"].(float64); !ok || got != 42.5 {
		t.Errorf("cpu_usage_percent wrong: %v", probe["cpu_usage_percent"])
	}
	if val, exists := probe["memory_usage_percent"]; !exists || val != nil {
		t.Errorf("unavailable memory must serialize as explicit null, got %v (exists=%v)", val, exists)
	}
	if !strings.Contains(string(raw), `"sequence":9`) {
		t.Errorf("sequence missing from payload: %s", raw)
	}
}

func TestRunnerBounds(t *testing.T) {
	if BufferCapacity != 240 {
		t.Errorf("buffer capacity drifted: %d", BufferCapacity)
	}
	if MaxBatchSamples != 60 {
		t.Errorf("batch bound drifted: %d (backend accepts at most 60)", MaxBatchSamples)
	}
	if TopProcesses != 20 {
		t.Errorf("process bound drifted: %d", TopProcesses)
	}
}
