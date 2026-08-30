//go:build windows

package platform

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Current identifies the compiled-in platform implementation.
func Current() Kind { return KindWindows }

var (
	modntdll   = windows.NewLazySystemDLL("ntdll.dll")
	rtlGetVer  = modntdll.NewProc("RtlGetVersion")
	memStatusP = kernel32().NewProc("GlobalMemoryStatusEx")

	kernel32Once = newKernel32()
)

func kernel32() *windows.LazyDLL { return kernel32Once }

func newKernel32() *windows.LazyDLL { return windows.NewLazySystemDLL("kernel32.dll") }

type osVersionInfoW struct {
	DwOSVersionInfoSize uint32
	DwMajorVersion      uint32
	DwMinorVersion      uint32
	DwBuildNumber       uint32
	DwPlatformID        uint32
	CSDVersion          [128]uint16
}

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func osVersion() string {
	var v osVersionInfoW
	v.DwOSVersionInfoSize = uint32(unsafe.Sizeof(v))
	if r, _, _ := rtlGetVer.Call(uintptr(unsafe.Pointer(&v))); r != 0 {
		return fmt.Sprintf("Windows %d.%d build %d", v.DwMajorVersion, v.DwMinorVersion, v.DwBuildNumber)
	}
	return ""
}

func cpuInfo() CPUInfo {
	return CPUInfo{
		CoresLogical: numCPU(),
		Model:        "",
	}
}

func numCPU() int { return runtime.NumCPU() }

func memoryInfo() MemoryInfo {
	var m MemoryInfo
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	if r1, _, _ := memStatusP.Call(uintptr(unsafe.Pointer(&ms))); r1 != 0 {
		m.TotalMB = int64(ms.TotalPhys) / (1024 * 1024)
		m.AvailableMB = int64(ms.AvailPhys) / (1024 * 1024)
	}
	return m
}

func diskInfo() []DiskInfo {
	out := []DiskInfo{}
	for _, volume := range []string{"C:\\"} {
		if _, err := os.Stat(volume); err != nil {
			continue // drive letter may not exist on some hosts
		}
		var freeBytesToCaller, totalBytes, totalFreeBytes uint64
		ptr, err := windows.UTF16PtrFromString(volume)
		if err != nil {
			continue
		}
		if err := windows.GetDiskFreeSpaceEx(ptr, &freeBytesToCaller, &totalBytes, &totalFreeBytes); err != nil {
			continue
		}
		out = append(out, DiskInfo{
			Mountpoint: strings.TrimSuffix(volume, `\`),
			Filesystem: "NTFS-or-other",
			TotalMB:    int64(totalBytes / (1024 * 1024)),
			FreeMB:     int64(totalFreeBytes / (1024 * 1024)),
		})
	}
	return out
}

func hostname() string {
	h, err := os.Hostname()
	if err == nil && h != "" {
		return strings.TrimSpace(h)
	}
	return strings.TrimSpace(os.Getenv("COMPUTERNAME"))
}
