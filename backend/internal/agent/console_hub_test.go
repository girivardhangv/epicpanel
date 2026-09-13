package agent

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// TestConsoleHubBatchingAndSeq verifies the core console contract: monotonic
// per-workload sequence, batched output, and gap-free backfill by cursor.
func TestConsoleHubBatchingAndSeq(t *testing.T) {
	h := NewConsoleHub()
	var mu sync.Mutex
	var got []agentproto.ConsoleOutput
	h.SetEmitter(func(out agentproto.ConsoleOutput) {
		mu.Lock()
		got = append(got, out)
		mu.Unlock()
	})
	h.Start()

	h.Append("w1", agentproto.StreamStdout, []string{"line one", "line two"})
	h.Append("w1", agentproto.StreamStdout, []string{"line three"})

	deadline := time.Now().Add(2 * time.Second)
	for {
		h.flushAll()
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n > 0 && h.Seq("w1") >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for frames; seq=%d frames=%d", h.Seq("w1"), n)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if h.Seq("w1") != 3 {
		t.Fatalf("seq = %d, want 3", h.Seq("w1"))
	}

	mu.Lock()
	joined := ""
	for _, f := range got {
		joined += f.Data
	}
	mu.Unlock()
	for _, want := range []string{"line one", "line two", "line three"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("emitted output missing %q: %q", want, joined)
		}
	}

	lines, from, to := h.History("w1", 0, 100)
	if len(lines) != 3 || from != 1 || to != 3 {
		t.Fatalf("history = %v from=%d to=%d, want 3 lines 1..3", lines, from, to)
	}
	lines, from, to = h.History("w1", 1, 100)
	if len(lines) != 2 || from != 2 || to != 3 {
		t.Fatalf("cursor history = %v from=%d to=%d, want 2 lines 2..3", lines, from, to)
	}
}

// TestConsoleHubResetKeepsSeqMonotonic proves a restart does not rewind the
// sequence (the panel's cursor must keep matching after Reset).
func TestConsoleHubResetKeepsSeqMonotonic(t *testing.T) {
	h := NewConsoleHub()
	h.Append("w2", agentproto.StreamStdout, []string{"a", "b", "c"})
	if h.Seq("w2") != 3 {
		t.Fatalf("seq = %d, want 3", h.Seq("w2"))
	}
	h.Reset("w2")
	if h.Seq("w2") != 3 {
		t.Fatalf("seq after reset = %d, want 3 (monotonic)", h.Seq("w2"))
	}
	if lines, _, _ := h.History("w2", 0, 100); len(lines) != 0 {
		t.Fatalf("history after reset = %v, want empty", lines)
	}
	h.Append("w2", agentproto.StreamStdout, []string{"d"})
	if h.Seq("w2") != 4 {
		t.Fatalf("seq after post-reset append = %d, want 4", h.Seq("w2"))
	}
}

// TestConsoleHubStaleCursor verifies a cursor past the ring end reads fresh
// instead of returning nothing (agent restart / ring reset).
func TestConsoleHubStaleCursor(t *testing.T) {
	h := NewConsoleHub()
	h.Append("w3", agentproto.StreamStdout, []string{"x", "y"})
	lines, from, to := h.History("w3", 999, 100)
	if len(lines) != 2 || from != 1 || to != 2 {
		t.Fatalf("stale cursor = %v from=%d to=%d, want 2 lines 1..2", lines, from, to)
	}
}

// TestRuntimeStateMachine verifies transitions emit once and desired/actual
// are tracked independently.
func TestRuntimeStateMachine(t *testing.T) {
	SetDesired("w4", StateRunning)
	ev := Transition("w4", "unit-w4", StateStarting, "start requested")
	if ev == nil || ev.State != StateStarting || ev.Desired != StateRunning {
		t.Fatalf("first transition = %+v", ev)
	}
	if again := Transition("w4", "unit-w4", StateStarting, "dup"); again != nil {
		t.Fatalf("duplicate transition emitted: %+v", again)
	}
	ev = Transition("w4", "unit-w4", StateRunning, "process started")
	if ev == nil || ev.State != StateRunning {
		t.Fatalf("running transition = %+v", ev)
	}
	crash := MarkExit("w4", "unit-w4", 1, "", "process exited unexpectedly")
	if crash == nil || crash.State != StateCrashed || crash.ExitCode != 1 {
		t.Fatalf("crash event = %+v", crash)
	}
	rt := RuntimeSnapshot("w4")
	if rt == nil || rt.Actual != StateCrashed || rt.Desired != StateRunning {
		t.Fatalf("runtime = %+v", rt)
	}
	DropRuntime("w4")
}
