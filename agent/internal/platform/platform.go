// Package platform defines the OS abstraction boundary for the EpicPanel
// agent. Concrete implementations live in build-tagged files so the same
// source compiles cleanly for Linux (amd64/arm64) and Windows (amd64/arm64).
//
// Rules enforced by design here:
//   - No OS-specific calls outside *_linux.go / *_windows.go files.
//   - No shell execution from the panel; the agent exposes typed operations.
package platform

import (
	"runtime"
)

// Kind identifies the operating-system family.
type Kind string

const (
	KindLinux   Kind = "linux"
	KindWindows Kind = "windows"
)

// MemoryInfo reports host RAM figures in MiB (0 == unknown, never fabricated).
type MemoryInfo struct {
	TotalMB     int64 `json:"total_mb"`
	AvailableMB int64 `json:"available_mb"`
}

// DiskInfo describes one volume/filesystem.
type DiskInfo struct {
	Mountpoint string `json:"mountpoint"`
	Filesystem string `json:"filesystem,omitempty"`
	TotalMB    int64  `json:"total_mb"`
	FreeMB     int64  `json:"free_mb"`
}

// CPUInfo describes the processor.
type CPUInfo struct {
	CoresLogical int    `json:"cores_logical"`
	Model        string `json:"model,omitempty"`
}

// Info aggregates everything collected during a heartbeat snapshot.
type Info struct {
	OS       Kind       `json:"os"`
	OSName   string     `json:"os_name"`
	Arch     string     `json:"arch"` // amd64 | arm64
	Version  string     `json:"os_version"`
	CPU      CPUInfo    `json:"cpu"`
	Memory   MemoryInfo `json:"memory"`
	Disks    []DiskInfo `json:"disks"`
	Hostname string     `json:"hostname"`
}

// Collect gathers all supported host information through the active platform.
func Collect(kind Kind) (*Info, error) {
	info := &Info{
		OS:       kind,
		OSName:   osName(kind),
		Arch:     normalizeArch(runtime.GOARCH),
		Version:  osVersion(),
		CPU:      cpuInfo(),
		Memory:   memoryInfo(),
		Disks:    diskInfo(),
		Hostname: hostname(),
	}
	return info, nil
}

func normalizeArch(a string) string {
	switch a {
	case "amd64":
		return "amd64"
	case "arm64", "aarch64":
		return "arm64"
	default:
		return a // future: report unknown rather than fail registration
	}
}

func osName(kind Kind) string {
	switch kind {
	case KindWindows:
		return "windows"
	default:
		return "linux"
	}
}
