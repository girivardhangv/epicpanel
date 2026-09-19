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
// mode is "apache", "openlitespeed" (web server backends) or "app"
// (node/python/go app processes). existingPort is the currently persisted
// port (0 = none).
func (a *BackendPortAllocator) AllocateFor(ctx context.Context, serverID, websiteID, mode string, existingPort int) (int, error) {
	var r ports.Range
	usedFn := func() (map[int]bool, error) { return nil, nil }
	setFn := func(int) error { return nil }
	switch mode {
	case "apache":
		r = ports.Apache()
		usedFn = func() (map[int]bool, error) { return a.Websites.UsedBackendPorts(ctx, uuid.MustParse(serverID)) }
		setFn = func(p int) error { return a.Websites.SetBackendPort(ctx, uuid.MustParse(websiteID), p) }
	case "openlitespeed":
		r = ports.OLS()
		usedFn = func() (map[int]bool, error) { return a.Websites.UsedBackendPorts(ctx, uuid.MustParse(serverID)) }
		setFn = func(p int) error { return a.Websites.SetBackendPort(ctx, uuid.MustParse(websiteID), p) }
	case "app":
		r = ports.App()
		usedFn = func() (map[int]bool, error) { return a.Websites.UsedAppPorts(ctx, uuid.MustParse(serverID)) }
		setFn = func(p int) error { return a.Websites.SetAppPort(ctx, uuid.MustParse(websiteID), p) }
	default:
		return 0, fmt.Errorf("unknown backend type %q", mode)
	}
	used, usedErr := usedFn()

	// Keep the existing allocation when it is still sane: in range and not
	// claimed by a different website.
	if existingPort > 0 && r.Contains(existingPort) {
		if usedErr != nil {
			// DB error — keep the stable port anyway (best effort).
			return existingPort, nil
		}
		if !used[existingPort] {
			return existingPort, nil
		}
		// Owned by someone else (stale row from an old mode) — fall through
		// and allocate fresh; the setter below will release it implicitly.
	}
	if usedErr != nil {
		return 0, fmt.Errorf("load allocated ports: %w", usedErr)
	}
	return a.allocate(ctx, r, setFn, used)
}

func (a *BackendPortAllocator) allocate(ctx context.Context, r ports.Range, setPort func(int) error, used map[int]bool) (int, error) {
	for p := r.Min; p <= r.Max; p++ {
		if used[p] {
			continue
		}
		if !portFreeOnLoopback(p) {
			continue // some process (possibly stale) still holds it
		}
		if err := setPort(p); err != nil {
			return 0, fmt.Errorf("persist backend port: %w", err)
		}
		return p, nil
	}
	return 0, fmt.Errorf("no free port in range %s", r)
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
