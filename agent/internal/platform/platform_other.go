//go:build !linux && !windows

package platform

import (
	"os"
	"runtime"
)

// Development fallbacks so the agent still builds on unsupported developer
// hosts (e.g. darwin). Values are reported as unknown rather than fabricated;
// registering from such a host is explicitly allowed for local development.

func Current() Kind { return Kind("other") }

func osVersion() string { return "" }

func cpuInfo() CPUInfo { return CPUInfo{CoresLogical: runtime.NumCPU()} }

func memoryInfo() MemoryInfo { return MemoryInfo{} }

func diskInfo() []DiskInfo { return []DiskInfo{} }

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}

func numCPU() int { return runtime.NumCPU() }
