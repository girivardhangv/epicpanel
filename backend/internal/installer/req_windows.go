//go:build windows

package installer

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGlobalMemoryStatusEx = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalMemoryStatusEx")
)

// memoryStatusEx mirrors MEMORYSTATUSEX from the Win32 API.
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

// totalMemoryMB uses GlobalMemoryStatusEx on Windows.
func totalMemoryMB() int64 {
	var ms memoryStatusEx
	ms.Length = uint32(unsafe.Sizeof(ms))
	r1, _, _ := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&ms)))
	if r1 == 0 {
		return 0
	}
	return int64(ms.TotalPhys) / (1024 * 1024)
}

// dataDirFreeDiskMB reports free space of the volume holding ".".
func dataDirFreeDiskMB() int64 {
	var freeBytes, totalBytes, totalFree uint64
	ptr, err := windows.UTF16PtrFromString(".")
	if err != nil {
		return 0
	}
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeBytes, &totalBytes, &totalFree); err != nil {
		return 0
	}
	return int64(freeBytes / (1024 * 1024))
}
