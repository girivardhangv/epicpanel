//go:build windows

package platform

import "errors"

// ApplySiteLimits is a no-op on Windows (Job Object integration is a later
// step). The backend persists limits and logs a warning that the agent could
// not apply them — site isolation on Windows relies on inherited ACLs.
func ApplySiteLimits(slug string, cpuPct, memMB int) error {
	return errors.New("resource limits are not supported on this platform")
}