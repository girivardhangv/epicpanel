package websites

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/ports"
)

// BackendPortAllocator assigns unique private backend ports for proxy-mode
// web servers (nginx,apache / nginx,openlitespeed). Allocation order:
//  1. keep an existing valid allocation (stable across re-provisions/restarts)
//  2. pick the lowest free port in the type's range that is
//     a) not allocated to another website on the same server (DB state)
//     b) not bindable by us right now on 127.0.0.1 (not in use by any process)
//  3. persist the choice so panel restarts never renumber backends
//
// Ranges are configurable (EPICPANEL_APACHE_PORT_RANGE / EPICPANEL_OLS_PORT_RANGE)
// and shared with the agent via the ports package. Privileged and public ports
// (80/443) can never be produced — the ranges enforce that.
type BackendPortAllocator struct {
	Websites *Store
}

// AllocateFor returns the port to use for websiteID with the given mode.
// mode is "apache" or "openlitespeed". existingPort is the currently persisted
// port (0 = none).
func (a *BackendPortAllocator) AllocateFor(ctx context.Context, serverID, websiteID, mode string, existingPort int) (int, error) {
	var r ports.Range
	switch mode {
	case "apache":
		r = ports.Apache()
	case "openlitespeed":
		r = ports.OLS()
	default:
		return 0, fmt.Errorf("unknown backend type %q", mode)
	}

	// Keep the existing allocation when it is still sane: in range and not
	// claimed by a different website.
	if existingPort > 0 && r.Contains(existingPort) {
		used, err := a.Websites.UsedBackendPorts(ctx, uuid.MustParse(serverID))
		if err == nil && !used[existingPort] {
			return existingPort, nil
		}
		if err == nil && used[existingPort] {
			// Owned by someone else (stale row from an old mode) — fall through
			// and allocate fresh; the setter below will release it implicitly.
			return a.allocate(ctx, serverID, websiteID, r)
		}
		// DB error — keep the stable port anyway (best effort).
		return existingPort, nil
	}
	return a.allocate(ctx, serverID, websiteID, r)
}

func (a *BackendPortAllocator) allocate(ctx context.Context, serverID, websiteID string, r ports.Range) (int, error) {
	used, err := a.Websites.UsedBackendPorts(ctx, uuid.MustParse(serverID))
	if err != nil {
		return 0, fmt.Errorf("load allocated ports: %w", err)
	}
	for p := r.Min; p <= r.Max; p++ {
		if used[p] {
			continue
		}
		if !portFreeOnLoopback(p) {
			continue // some process (possibly stale) still holds it
		}
		if err := a.Websites.SetBackendPort(ctx, uuid.MustParse(websiteID), p); err != nil {
			return 0, fmt.Errorf("persist backend port: %w", err)
		}
		return p, nil
	}
	return 0, fmt.Errorf("no free %s backend port in range %s", modeName(r), r)
}

// portFreeOnLoopback verifies no process currently listens on 127.0.0.1:p.
func portFreeOnLoopback(p int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p), 300*time.Millisecond)
	if err != nil {
		return true // nothing accepted → free
	}
	_ = conn.Close()
	return false
}

func modeName(r ports.Range) string {
	if r == ports.OLS() {
		return "openlitespeed"
	}
	return "apache"
}
