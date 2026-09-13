package agent

import (
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// Explicit server state machine + per-workload runtime registry (spec §6, §7,
// §49–§50). No global booleans: every workload has a desired state and an
// observed actual state; transitions are reported to the panel as events.

// Workload states (runtime).
const (
	StateOffline       = "offline"
	StateInstalling    = "installing"
	StateStarting      = "starting"
	StateRunning       = "running"
	StateStopping      = "stopping"
	StateStoppingForce = "stopping_force"
	StateCrashed       = "crashed"
	StateError         = "error"
	StateInstallFailed = "install_failed"
)

// WorkloadRuntime is the agent's truth for one workload.
type WorkloadRuntime struct {
	WorkloadID string
	Unit       string
	Desired    string
	Actual     string
	ExitCode   int
	Signal     string
	StartedAt  time.Time
	UpdatedAt  time.Time
}

var (
	runtimesMu sync.Mutex
	runtimes   = map[string]*WorkloadRuntime{}
)

// SetDesired records the customer's intent for a workload.
func SetDesired(workloadID, desired string) {
	if workloadID == "" {
		return
	}
	runtimesMu.Lock()
	rt := runtimeFor(workloadID)
	rt.Desired = desired
	rt.UpdatedAt = time.Now().UTC()
	runtimesMu.Unlock()
}

// Transition moves a workload to a new actual state and returns the event to
// report (nil when the state is unchanged).
func Transition(workloadID, unit, state, reason string) *agentproto.ServerStateEvent {
	if workloadID == "" {
		return nil
	}
	runtimesMu.Lock()
	rt := runtimeFor(workloadID)
	if unit != "" {
		rt.Unit = unit
	}
	if rt.Actual == state {
		runtimesMu.Unlock()
		return nil
	}
	rt.Actual = state
	rt.UpdatedAt = time.Now().UTC()
	if state == StateRunning {
		rt.StartedAt = time.Now().UTC()
	}
	ev := &agentproto.ServerStateEvent{
		ServerID: workloadID,
		State:    state,
		Desired:  rt.Desired,
		Reason:   reason,
		At:       rt.UpdatedAt.Format(time.RFC3339),
	}
	runtimesMu.Unlock()
	return ev
}

// MarkExit records an unexpected process exit and transitions to crashed,
// returning the crashed event (nil when already crashed).
func MarkExit(workloadID, unit string, exitCode int, signal, reason string) *agentproto.ServerStateEvent {
	if workloadID == "" {
		return nil
	}
	runtimesMu.Lock()
	rt := runtimeFor(workloadID)
	if unit != "" {
		rt.Unit = unit
	}
	if rt.Actual == StateCrashed && rt.ExitCode == exitCode {
		runtimesMu.Unlock()
		return nil
	}
	rt.Actual = StateCrashed
	rt.ExitCode = exitCode
	rt.Signal = signal
	rt.UpdatedAt = time.Now().UTC()
	ev := &agentproto.ServerStateEvent{
		ServerID: workloadID,
		State:    StateCrashed,
		Desired:  rt.Desired,
		Reason:   reason,
		ExitCode: exitCode,
		Signal:   signal,
		At:       rt.UpdatedAt.Format(time.RFC3339),
	}
	runtimesMu.Unlock()
	return ev
}

// RuntimeSnapshot copies one workload's runtime (nil when unknown).
func RuntimeSnapshot(workloadID string) *WorkloadRuntime {
	runtimesMu.Lock()
	defer runtimesMu.Unlock()
	rt := runtimes[workloadID]
	if rt == nil {
		return nil
	}
	cp := *rt
	return &cp
}

// RuntimeSnapshots lists every known workload (for reconnect sync).
func RuntimeSnapshots() []WorkloadRuntime {
	runtimesMu.Lock()
	defer runtimesMu.Unlock()
	out := make([]WorkloadRuntime, 0, len(runtimes))
	for _, rt := range runtimes {
		out = append(out, *rt)
	}
	return out
}

// DropRuntime removes a deleted workload's runtime.
func DropRuntime(workloadID string) {
	runtimesMu.Lock()
	delete(runtimes, workloadID)
	runtimesMu.Unlock()
}

func runtimeFor(workloadID string) *WorkloadRuntime {
	rt := runtimes[workloadID]
	if rt == nil {
		rt = &WorkloadRuntime{WorkloadID: workloadID, Actual: StateOffline, UpdatedAt: time.Now().UTC()}
		runtimes[workloadID] = rt
	}
	return rt
}
