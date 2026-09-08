package metrics

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

func sampleNode(cpu float64) agentproto.Sample {
	return agentproto.Sample{Node: agentproto.NodeSample{
		CPUPercent: cpu, MemoryTotal: 16 << 30, MemoryUsed: 8 << 30,
		Net: agentproto.NetSample{RxBPS: 125000, TxBPS: 125000},
	}}
}

func metricsFrame(session string, seq int64, cpu float64) agentproto.Frame {
	b, _ := json.Marshal(sampleNode(cpu))
	return agentproto.Frame{Type: agentproto.TypeMetrics, SessionID: session, Seq: seq, Ts: time.Now().UTC(), Data: b}
}

// TestIngestSequenceContinuity verifies dedup + gap tracking: duplicate seq
// frames are rejected (no double count, no duplicate sample), gaps are
// counted visibly.
func TestIngestSequenceContinuity(t *testing.T) {
	s := NewLiveStore()
	id := uuid.New()
	const sess = "sess-a"

	if !s.Ingest(id, metricsFrame(sess, 1, 10)) {
		t.Fatal("first frame must ingest")
	}
	if !s.Ingest(id, metricsFrame(sess, 2, 20)) {
		t.Fatal("seq 2 must ingest")
	}
	if s.Ingest(id, metricsFrame(sess, 2, 20)) {
		t.Fatal("duplicate seq 2 must be rejected")
	}
	// Gap 3..7 missing:
	if !s.Ingest(id, metricsFrame(sess, 8, 80)) {
		t.Fatal("seq 8 must ingest")
	}
	lv := s.Snapshot(id)
	if lv == nil {
		t.Fatal("no live entry")
	}
	if lv.LastSeq != 8 {
		t.Fatalf("LastSeq = %d, want 8", lv.LastSeq)
	}
	if lv.Gaps != 5 {
		t.Fatalf("Gaps = %d, want 5 (seqs 3-7 missing)", lv.Gaps)
	}
	if lv.RecvCount != 3 {
		t.Fatalf("RecvCount = %d, want 3", lv.RecvCount)
	}
	if lv.Sample.Node.CPUPercent != 80 {
		t.Fatalf("latest CPU = %v, want 80", lv.Sample.Node.CPUPercent)
	}
}

// TestIngestSessionRestart verifies a new session resets sequence accounting
// (agent process restart) instead of counting the whole stream as one gap.
func TestIngestSessionRestart(t *testing.T) {
	s := NewLiveStore()
	id := uuid.New()
	if !s.Ingest(id, metricsFrame("s1", 42, 10)) {
		t.Fatal("ingest s1")
	}
	if !s.Ingest(id, metricsFrame("s2", 1, 30)) {
		t.Fatal("ingest s2 (new session)")
	}
	lv := s.Snapshot(id)
	if lv.Gaps != 0 {
		t.Fatalf("Gaps across session restart = %d, want 0", lv.Gaps)
	}
	if lv.SessionID != "s2" || lv.Sample.Node.CPUPercent != 30 {
		t.Fatalf("session switch not applied: %+v", lv)
	}
}

// TestStaleDetectionTiming verifies LIVE→STALE→OFFLINE classification as a
// sample ages without new frames (the "never present old metrics as current"
// contract).
func TestStaleDetectionTiming(t *testing.T) {
	s := NewLiveStore()
	id := uuid.New()
	if !s.Ingest(id, metricsFrame("sess", 1, 55)) {
		t.Fatal("ingest")
	}
	lv := s.Snapshot(id)

	// Fresh: LIVE.
	if st := agentproto.Freshness(time.Since(lv.Collected), true); st != "LIVE" {
		t.Fatalf("fresh sample state = %s, want LIVE", st)
	}
	// Simulate aging by rewinding the sample timestamp.
	lv.Collected = time.Now().UTC().Add(-18 * time.Second)
	if st := agentproto.Freshness(time.Since(lv.Collected), true); st != "STALE" {
		t.Fatalf("18s-old sample state = %s, want STALE", st)
	}
	lv.Collected = time.Now().UTC().Add(-125 * time.Second)
	if st := agentproto.Freshness(time.Since(lv.Collected), true); st != "OFFLINE" {
		t.Fatalf("125s-old sample state = %s, want OFFLINE", st)
	}

	frame := s.Frame(id)
	if frame == nil || frame.Freshness["state"] != "OFFLINE" {
		t.Fatalf("Frame freshness = %+v, want OFFLINE", frame.Freshness)
	}
}

// TestNodeStateRegistry verifies the connection registry: ONLINE while
// connected and fresh, STALE with connection down but frames recent,
// OFFLINE past the offline threshold.
func TestNodeStateRegistry(t *testing.T) {
	s := NewLiveStore()
	id := uuid.New()
	s.Ingest(id, metricsFrame("sess", 1, 5))
	lv := s.Snapshot(id)

	if got := lv.NodeState(time.Now()); got != NodeOnline {
		t.Fatalf("connected+fresh = %s, want ONLINE", got)
	}
	// Connection dropped but frame 10s old → STALE (transitional).
	s.Connected(id, "sess", false)
	lv.LastFrame = time.Now().UTC().Add(-10 * time.Second)
	if got := lv.NodeState(time.Now()); got != NodeStale {
		t.Fatalf("disconnected+10s = %s, want STALE", got)
	}
	// Long silence → OFFLINE.
	lv.LastFrame = time.Now().UTC().Add(-3 * time.Minute)
	if got := lv.NodeState(time.Now()); got != NodeOffline {
		t.Fatalf("disconnected+3m = %s, want OFFLINE", got)
	}
}

// TestIngestHighLoadFanOut hammers the store with concurrent ingest while
// readers snapshot: every frame must be applied without races (run under
// -race) and the fan-out callback must receive every non-duplicate frame.
func TestIngestHighLoadFanOut(t *testing.T) {
	s := NewLiveStore()
	id := uuid.New()
	var fanout int64
	s.OnSample = func(sid uuid.UUID, frame map[string]any) {
		if frame != nil {
			fanout++
		}
	}
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 1; i <= 200; i++ {
				s.Ingest(id, metricsFrame("load", int64(i), float64(i%100)))
				if i%37 == 0 {
					_ = s.Frame(id)
				}
			}
		}(g)
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	lv := s.Snapshot(id)
	if lv.LastSeq != 200 || lv.RecvCount != 200 {
		t.Fatalf("LastSeq=%d RecvCount=%d, want 200/200", lv.LastSeq, lv.RecvCount)
	}
	if fanout != 200 {
		t.Fatalf("fanout = %d, want 200", fanout)
	}
	if len(lv.Ring) == 0 || len(lv.Ring) > ringPerServer {
		t.Fatalf("ring size = %d, want <= %d", len(lv.Ring), ringPerServer)
	}
}

// TestRingBound verifies the instantaneous ring never grows unbounded.
func TestRingBound(t *testing.T) {
	s := NewLiveStore()
	id := uuid.New()
	for i := 1; i <= ringPerServer*3; i++ {
		s.Ingest(id, metricsFrame("r", int64(i), 1))
	}
	lv := s.Snapshot(id)
	if len(lv.Ring) != ringPerServer {
		t.Fatalf("ring = %d, want %d", len(lv.Ring), ringPerServer)
	}
}

// TestHistoryBatchWindow verifies the writer only drains fresh samples
// (stale live entries must not be double-persisted).
func TestHistoryBatchWindow(t *testing.T) {
	s := NewLiveStore()
	id := uuid.New()
	s.Ingest(id, metricsFrame("h", 1, 5))
	// Old sample: past the 2×LiveMaxAge window.
	lv := s.Snapshot(id)
	lv.Collected = time.Now().UTC().Add(-agentproto.LiveMaxAge * 3)
	if got := len(s.HistoryBatches()); got != 0 {
		t.Fatalf("stale sample drained = %d rows, want 0", got)
	}
	s.Ingest(id, metricsFrame("h", 2, 6))
	if got := len(s.HistoryBatches()); got != 1 {
		t.Fatalf("fresh sample drained = %d rows, want 1", got)
	}
}
