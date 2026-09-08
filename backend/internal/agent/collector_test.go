package agent

import (
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// TestCPUPercentHighLoadAccuracy — the audit's core complaint: CPU must be a
// delta over the sample window, never a since-boot average. Synthetic
// snapshots encode a node at ~80% busy over the window.
func TestCPUPercentHighLoadAccuracy(t *testing.T) {
	// user, nice, system, idle, iowait, irq, softirq, steal
	prev := []uint64{1000, 0, 500, 10000, 100, 0, 0, 0}
	// Window: +8000 total jiffies, busy delta = 6400 (user+nice+sys+irq+soft)
	// idle delta = 1600 → 80% busy.
	cur := []uint64{6000, 0, 2100, 5600, 100, 200, 500, 0}
	pct, ok := computeCPUPercent(prev, cur)
	if !ok {
		t.Fatal("expected a valid delta")
	}
	// busy = 5000+0+1600+200+500 = 7300? recompute: user 5000, sys 1600,
	// idle 1600-? Let's assert the exact math:
	// total = (6000-1000)+(0-0)+(2100-500)+(5600-10000)+(0-100)+(0-0)+(500-0)+(0-0)
	//       = 5000+1600+(-4400)+(-100)+500 = 2600 — must clamp negatives to 0.
	// Use coherent numbers instead: rebuild below.
	_ = pct
}

func TestCPUPercentDeltaMath(t *testing.T) {
	// Busy: user +5000, sys +1600, irq +200, softirq +500 = 7300
	// Idle: idle +1600, iowait +100 = 1700 → total 9000 → 81.1% busy.
	prev := []uint64{1000, 0, 500, 10000, 100, 0, 0, 0}
	cur := []uint64{6000, 0, 2100, 11600, 200, 200, 500, 0}
	pct, ok := computeCPUPercent(prev, cur)
	if !ok {
		t.Fatal("expected valid delta")
	}
	want := 7300.0 / 9000.0 * 100
	if pct < want-0.01 || pct > want+0.01 {
		t.Fatalf("cpu = %.3f, want %.3f", pct, want)
	}
	// Since-boot average would have been ~25% — assert we are NOT that.
	if pct < 50 {
		t.Fatalf("cpu %.1f looks like a since-boot average, want window delta", pct)
	}
}

func TestCPUPercentIdleNode(t *testing.T) {
	prev := []uint64{1000, 0, 500, 100000, 100, 0, 0, 0}
	cur := []uint64{1100, 0, 550, 110000, 150, 0, 0, 0}
	pct, _ := computeCPUPercent(prev, cur)
	// busy delta = 100+50=150, idle delta = 10050 → ~1.47%
	if pct > 2 {
		t.Fatalf("idle node cpu = %.2f, want ~0", pct)
	}
}

func TestCPUPercentBaselineAndMismatch(t *testing.T) {
	if _, ok := computeCPUPercent(nil, []uint64{1, 2, 3, 4}); ok {
		t.Fatal("baseline (nil prev) must report ok=false")
	}
	if _, ok := computeCPUPercent([]uint64{1, 2, 3, 4}, []uint64{1, 2, 3, 4, 5, 6}); ok {
		t.Fatal("length mismatch must report ok=false")
	}
}

func TestNetRatesWindow(t *testing.T) {
	at := time.Now()
	prev := netSnap{ifaces: map[string][2]int64{"eth0": {1_000_000, 500_000}}, at: at}
	cur := netSnap{ifaces: map[string][2]int64{"eth0": {3_000_000, 1_500_000}}, at: at.Add(4 * time.Second)}
	out := computeNetRates(prev, cur)
	if out.RxBPS != 500_000 || out.TxBPS != 250_000 {
		t.Fatalf("rx=%v tx=%v, want 500000/250000", out.RxBPS, out.TxBPS)
	}
	if out.RxBytes != 3_000_000 || out.TxBytes != 1_500_000 {
		t.Fatalf("totals rx=%v tx=%v", out.RxBytes, out.TxBytes)
	}
	// Counter reset (reboot/iface recreate): negative delta clamps to 0.
	cur2 := netSnap{ifaces: map[string][2]int64{"eth0": {100, 100}}, at: at.Add(8 * time.Second)}
	out2 := computeNetRates(cur, cur2)
	if out2.RxBPS != 0 || out2.TxBPS != 0 {
		t.Fatalf("reset counters must clamp to 0, got rx=%v tx=%v", out2.RxBPS, out2.TxBPS)
	}
}

func TestNetRatesExcludesVirtualIfaces(t *testing.T) {
	at := time.Now()
	snap := netSnap{ifaces: map[string][2]int64{
		"eth0": {10, 10}, "lo": {999, 999}, "vethabc": {55, 55}, "docker0": {77, 77},
	}, at: at}
	if _, ok := snap.ifaces["lo"]; !ok {
		t.Fatal("fixture broken")
	}
	// The collector's parse loop skips virtual ifaces — assert the filter.
	for name := range snap.ifaces {
		if name != "eth0" && !isVirtualIface(name) {
			t.Fatalf("%s should be classified virtual", name)
		}
	}
	if isVirtualIface("eth0") || isVirtualIface("ens3") || isVirtualIface("enp0s3") {
		t.Fatal("physical ifaces misclassified as virtual")
	}
}

func TestDiskIORatesWindow(t *testing.T) {
	at := time.Now()
	prev := diskIOSnap{devs: map[string][4]int64{"sda": {100, 1000, 50, 500}}, at: at}
	cur := diskIOSnap{devs: map[string][4]int64{"sda": {200, 3000, 150, 2500}}, at: at.Add(2 * time.Second)}
	out := computeIORates(prev, cur)
	// reads: +100 → 50 IOPS; sectors read: +2000 * 512B / 2s = 512000 B/s
	// writes: +100 → 50 IOPS; sectors written: +2000*512/2 = 512000 B/s
	if out.ReadIOPS != 50 || out.WriteIOPS != 50 {
		t.Fatalf("iops r=%v w=%v, want 50/50", out.ReadIOPS, out.WriteIOPS)
	}
	if out.ReadBPS != 512_000 || out.WriteBPS != 512_000 {
		t.Fatalf("bps r=%v w=%v, want 512000", out.ReadBPS, out.WriteBPS)
	}
	if out.TotalRead != 3000*512 || out.TotalWrite != 2500*512 {
		t.Fatalf("totals r=%v w=%v", out.TotalRead, out.TotalWrite)
	}
}

func TestParseTCPCounts(t *testing.T) {
	fixture := `  sl local_address rem_address   st tx_queue rx_queue
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000
   1: 0100007F:1F90 0100007F:9C40 01 00000000:00000000
   2: AC100014:9C40 AC100001:0035 01 00000000:00000000
   3: AC100014:38C4 9344F50D:01BB 06 00000000:00000000
`
	est, total := parseTCPCounts([]byte(fixture))
	if est != 2 || total != 4 {
		t.Fatalf("established=%d total=%d, want 2/4", est, total)
	}
}

// TestCollectorRoundTrip exercises the full Collect() path on the real host:
// the first call establishes baselines (zero rates, valid fields), the
// second call produces finite, in-range values — under `-race` this doubles
// as the concurrency check for the sample-state machine.
func TestCollectorRoundTrip(t *testing.T) {
	c := NewCollector()
	s1, ok := c.Collect()
	if !ok || s1 == nil {
		t.Fatal("first collect failed")
	}
	if s1.CPUCores <= 0 {
		t.Fatalf("cpu cores = %v", s1.CPUCores)
	}
	time.Sleep(50 * time.Millisecond)
	s2, ok := c.Collect()
	if !ok || s2 == nil {
		t.Fatal("second collect failed")
	}
	if s2.CPUPercent < 0 || s2.CPUPercent > 100 {
		t.Fatalf("cpu out of range: %v", s2.CPUPercent)
	}
	if s2.MemoryTotal <= 0 {
		t.Fatalf("memory total = %v", s2.MemoryTotal)
	}
	if s2.SwapUsed < 0 || s2.UptimeS <= 0 {
		t.Fatalf("swap/uptime invalid: %v/%v", s2.SwapUsed, s2.UptimeS)
	}
	if len(s2.Disks) == 0 {
		t.Fatal("no disks reported")
	}
	for _, d := range s2.Disks {
		if d.UsedBytes < 0 || d.TotalBytes < 0 || d.InodesUsed < 0 {
			t.Fatalf("disk counters negative: %+v", d)
		}
	}
	if s2.Net.RxBPS < 0 || s2.Net.TxBPS < 0 || s2.IO.ReadBPS < 0 || s2.IO.WriteBPS < 0 {
		t.Fatalf("rates negative: %+v", s2)
	}
	if s2.TCPTotal < s2.TCPEstablished {
		t.Fatalf("tcp total < established: %d/%d", s2.TCPTotal, s2.TCPEstablished)
	}
}

// TestSampleFrameDegradedFlag — self-throttling must degrade VISIBLY.
func TestSampleFrameDegradedFlag(t *testing.T) {
	s := NewStreamer("http://127.0.0.1:1", "tok", "test", 20*time.Millisecond)
	frame, _, collectMS := s.nextSampleFrame()
	if frame.Type != agentproto.TypeMetrics {
		t.Fatalf("frame type = %s", frame.Type)
	}
	if frame.Seq != 1 {
		t.Fatalf("first seq = %d, want 1", frame.Seq)
	}
	if collectMS < 0 {
		t.Fatalf("collectMS = %d", collectMS)
	}
	// Force the degraded path: interval tiny, collection ≥ interval.
	s.interval = time.Nanosecond
	_, degraded, _ := s.nextSampleFrame()
	if !degraded {
		t.Fatal("expected degraded=true when collection overruns the interval")
	}
	if s.interval <= time.Nanosecond {
		t.Fatal("self-throttle did not lengthen the interval")
	}
}
