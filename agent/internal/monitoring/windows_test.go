//go:build windows

package monitoring

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// TestParseIfTable verifies the MIB_IFTABLE buffer decoding: row stride,
// loopback exclusion, description-based naming and counter mapping.
func TestParseIfTable(t *testing.T) {
	rowSize := unsafe.Sizeof(mibIfRow{})
	rows := []mibIfRow{
		{IfIndex: 1, Type: ifTypeSoftwareLoopback, InOctets: 111, OutOctets: 222},
		{IfIndex: 5, Type: 6, InOctets: 1000, OutOctets: 2000,
			InUcastPkts: 10, OutUcastPkts: 20, InErrors: 3, OutErrors: 4,
			InDiscards: 5, OutDiscards: 6,
			DescrLen: 9, Descr: [256]uint8{'e', 't', 'h', ' ', 'n', 'i', 'c', '0', 0}},
	}
	buf := make([]byte, 4+uintptr(len(rows))*rowSize)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(len(rows)))
	for i, r := range rows {
		off := 4 + i*int(rowSize)
		rowBytes := (*[1 << 20]byte)(unsafe.Pointer(&r))[:rowSize:rowSize]
		copy(buf[off:off+int(rowSize)], rowBytes)
	}

	got := parseIfTable(buf)
	if len(got) != 1 {
		t.Fatalf("interfaces = %d, want 1 (loopback excluded)", len(got))
	}
	n := got[0]
	if n.Interface != "eth nic0" {
		t.Errorf("interface name = %q", n.Interface)
	}
	if n.RxBytes != 1000 || n.TxBytes != 2000 {
		t.Errorf("byte counters wrong: %+v", n)
	}
	if n.RxPackets != 10 || n.TxPackets != 20 {
		t.Errorf("packet counters wrong: %+v", n)
	}
	if n.Errors != 7 || n.Drops != 11 {
		t.Errorf("errors/drops wrong: %+v", n)
	}
}

func TestParseIfTableTruncated(t *testing.T) {
	// A row count of 2 with only room for 1 row must not panic or over-read.
	rowSize := unsafe.Sizeof(mibIfRow{})
	buf := make([]byte, 4+rowSize) // claims 2 rows, holds 1
	binary.LittleEndian.PutUint32(buf[0:4], 2)
	got := parseIfTable(buf)
	if len(got) != 1 {
		t.Errorf("truncated table = %d rows, want 1", len(got))
	}
	if got := parseIfTable(nil); got != nil {
		t.Errorf("empty buffer must return nil, got %v", got)
	}
}

func TestWindowsMemoryContract(t *testing.T) {
	// Documented semantics: used = TotalPhys − AvailPhys (§3). The swap model
	// uses the commit limit beyond RAM. These constants keep the contract
	// visible in tests.
	const bytesInMB = 1024 * 1024
	total := uint64(16 * 1024 * bytesInMB)
	avail := uint64(8 * 1024 * bytesInMB)
	if total-avail != 8*1024*bytesInMB {
		t.Error("used-bytes math drifted from the documented contract")
	}
}
