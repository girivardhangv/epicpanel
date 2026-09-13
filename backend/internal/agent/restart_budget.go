package agent

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// Agent-side crash-loop protection (spec §25). The supervisor restart policy
// (systemd Restart= / docker restart) is not trusted to terminate a loop: the
// agent counts unexpected exits per workload in a sliding window and, when the
// budget is exceeded, neutralizes the auto-restart and reports CRASHED so the
// server cannot spin forever.

const (
	crashWindowDefault = 10 * time.Minute
	crashMaxDefault    = 5
)

var (
	crashBudgetMu sync.Mutex
	crashBudget   = map[string][]time.Time{}
)

func crashLimit() int {
	if v := os.Getenv("EPICPANEL_AGENT_MAX_CRASHES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return crashMaxDefault
}

func crashWindow() time.Duration {
	if v := os.Getenv("EPICPANEL_AGENT_CRASH_WINDOW"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return crashWindowDefault
}

// resetCrashBudget clears a workload's history (called on a healthy start).
func resetCrashBudget(workloadID string) {
	crashBudgetMu.Lock()
	delete(crashBudget, workloadID)
	crashBudgetMu.Unlock()
}

// dropCrashBudget removes a deleted workload's history.
func dropCrashBudget(workloadID string) {
	resetCrashBudget(workloadID)
}

// recordCrash appends one crash and reports whether the restart budget has
// been exceeded within the window.
func recordCrash(workloadID string) bool {
	now := time.Now()
	cutoff := now.Add(-crashWindow())
	crashBudgetMu.Lock()
	defer crashBudgetMu.Unlock()
	hist := crashBudget[workloadID]
	kept := hist[:0]
	for _, t := range hist {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	kept = append(kept, now)
	crashBudget[workloadID] = kept
	return len(kept) > crashLimit()
}
