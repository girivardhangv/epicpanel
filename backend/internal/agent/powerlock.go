package agent

import (
	"fmt"
	"sync"
)

type powerLocker struct {
	mu   sync.Mutex
	held map[string]bool
}

var powerLocks = &powerLocker{held: map[string]bool{}}

func acquirePowerLock(workloadID string) error {
	if workloadID == "" {
		return nil
	}
	powerLocks.mu.Lock()
	defer powerLocks.mu.Unlock()
	if powerLocks.held[workloadID] {
		return errPowerLocked
	}
	powerLocks.held[workloadID] = true
	return nil
}

func releasePowerLock(workloadID string) {
	if workloadID == "" {
		return
	}
	powerLocks.mu.Lock()
	delete(powerLocks.held, workloadID)
	powerLocks.mu.Unlock()
}

var errPowerLocked = fmt.Errorf("another power action is currently being processed for this server, please try again later")
