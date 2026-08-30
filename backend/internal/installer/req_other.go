//go:build !linux && !windows

package installer

// Fallbacks for unsupported-but-buildable targets (e.g. darwin development).
func totalMemoryMB() int64    { return 0 }
func dataDirFreeDiskMB() int64 { return 0 }
