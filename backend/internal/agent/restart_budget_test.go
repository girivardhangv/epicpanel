package agent

import (
	"testing"
	"time"
)

// TestCrashBudgetTriggers verifies the sliding-window crash-loop breaker:
// under the limit crashes do not trip; past it, the breaker reports true and
// neutralizes auto-restart.
func TestCrashBudgetTriggers(t *testing.T) {
	const id = "crash-budget-test"
	resetCrashBudget(id)
	t.Cleanup(func() { dropCrashBudget(id) })

	limit := crashLimit()
	for i := 0; i < limit; i++ {
		if recordCrash(id) {
			t.Fatalf("crash %d/%d tripped the breaker early", i+1, limit)
		}
	}
	if !recordCrash(id) {
		t.Fatalf("crash %d did not trip the breaker", limit+1)
	}
}

// TestCrashBudgetWindowEviction verifies old crashes age out of the window so
// a server that crashes rarely is never falsely suspended.
func TestCrashBudgetWindowEviction(t *testing.T) {
	const id = "crash-window-test"
	resetCrashBudget(id)
	t.Cleanup(func() { dropCrashBudget(id) })

	crashBudgetMu.Lock()
	crashBudget[id] = []time.Time{time.Now().Add(-2 * crashWindow())}
	crashBudgetMu.Unlock()

	if recordCrash(id) {
		t.Fatal("stale crash inside window eviction tripped the breaker")
	}
}

// TestWriteStdinWithoutTransport verifies WriteStdin reports false for a
// workload with no open pipe so the caller falls back to RCON cleanly. The
// check is environment-independent: it never opens a transport.
func TestWriteStdinWithoutTransport(t *testing.T) {
	const id = "stdin-not-opened-test"
	StopStdinTransport(id)
	if WriteStdin(id, "say hi") {
		t.Fatal("WriteStdin must be false when no transport is open")
	}
}
