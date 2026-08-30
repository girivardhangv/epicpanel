package monitoring

import "testing"

func TestParseProcStatCPU(t *testing.T) {
	procStat := []byte(`cpu  100 0 50 750 50 0 10 40 0 0 0
cpu0 50 0 25 375 25 0 5 20 0 0 0
intr 12345
`)
	got, ok := parseProcStatCPU(procStat)
	if !ok {
		t.Fatal("aggregate cpu line not parsed")
	}
	// total = 100+0+50+750+50+0+10+40 = 1000
	if got.total != 1000 {
		t.Errorf("total = %d, want 1000", got.total)
	}
	if got.user != 100 || got.system != 50 || got.idle != 750 || got.iowait != 50 {
		t.Errorf("fields wrong: %+v", got)
	}
	if got.steal != 40 {
		t.Errorf("steal = %d, want 40", got.steal)
	}

	if _, ok := parseProcStatCPU([]byte("no cpu line here\n")); ok {
		t.Error("missing cpu line must report not-ok")
	}
}

// CPU delta math: usage = 100 − (idle+iowait delta)/total delta.
func TestCPUDeltaMath(t *testing.T) {
	prev, _ := parseProcStatCPU([]byte("cpu  100 0 50 750 50 0 10 40 0 0 0\n"))
	// advance: user+100, system+50, idle+800, iowait+50 → total delta 1000,
	// idle+iowait delta 850 → 15% busy.
	cur, _ := parseProcStatCPU([]byte("cpu  200 0 100 1550 100 0 10 40 0 0 0\n"))
	if cur.total-prev.total != 1000 {
		t.Fatalf("total delta = %d, want 1000", cur.total-prev.total)
	}
	dTotal := float64(cur.total - prev.total)
	dIdle := float64((cur.idle + cur.iowait) - (prev.idle + prev.iowait))
	usage := 100 - 100*dIdle/dTotal
	if usage < 14.999 || usage > 15.001 {
		t.Errorf("usage = %v, want 15", usage)
	}
}

func TestParseLoadavg(t *testing.T) {
	l1, l5, l15, ok := parseLoadavg([]byte("1.20 0.95 0.77 3/812 22811\n"))
	if !ok || l1 == nil || *l1 != 1.20 || *l5 != 0.95 || *l15 != 0.77 {
		t.Errorf("loadavg wrong: %v %v %v %v", l1, l5, l15, ok)
	}
	if _, _, _, ok := parseLoadavg([]byte("")); ok {
		t.Error("empty loadavg must not parse")
	}
}

func TestParseMemInfo(t *testing.T) {
	meminfo := []byte(`MemTotal:       16384 kB
MemFree:         2048 kB
MemAvailable:    8192 kB
Buffers:          512 kB
Cached:          2048 kB
SwapTotal:       4096 kB
SwapFree:        3072 kB
HugePages_Total:       0
`)
	mi := parseMemInfo(meminfo)
	if mi["MemTotal"] != 16384*1024 {
		t.Errorf("MemTotal = %d, want bytes", mi["MemTotal"])
	}
	// Contract (§3): used = total − available, so page cache is NOT "used".
	used := mi["MemTotal"] - mi["MemAvailable"]
	if used != (16384-8192)*1024 {
		t.Errorf("used = %d", used)
	}
	if mi["HugePages_Total"] != 0 {
		t.Errorf("unit-less entries should parse as raw numbers; got %d", mi["HugePages_Total"])
	}
	swapUsed := mi["SwapTotal"] - mi["SwapFree"]
	if swapUsed != 1024*1024 {
		t.Errorf("swapUsed = %d", swapUsed)
	}
}

func TestParseNetDev(t *testing.T) {
	// Data rows have no pipe separators — that decoration is header-only.
	netdev := []byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 9999999 9999 0 0 0 0 0 0 9999999 9999 0 0 0 0 0 0
  eth0: 1000000 800 2 3 0 0 0 0 2000000 900 4 5 0 0 0 0
`)
	got := parseNetDev(netdev)
	if len(got) != 1 {
		t.Fatalf("interfaces = %d, want 1 (loopback excluded)", len(got))
	}
	if got[0].Interface != "eth0" || got[0].RxBytes != 1000000 || got[0].TxBytes != 2000000 {
		t.Errorf("eth0 metrics wrong: %+v", got[0])
	}
	if got[0].Errors != 2 || got[0].Drops != 3 {
		t.Errorf("rx errors/drops wrong: %+v", got[0])
	}
}

func TestParseMountsFiltersPseudo(t *testing.T) {
	mounts := []byte(`/dev/nvme0n1p2 / ext4 rw,relatime 0 0
proc /proc proc rw,nosuid 0 0
sysfs /sys sysfs rw 0 0
tmpfs /run tmpfs rw 0 0
/dev/sda1 /mnt/data xfs rw 0 0
/dev/loop0 /snap/core squashfs ro 0 0
/dev/sdb1 /mnt/usb vfat rw 0 0
`)
	got := parseMounts(mounts)
	want := []string{"/", "/mnt/data", "/mnt/usb"}
	if len(got) != len(want) {
		t.Fatalf("mounts = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i][1] != w {
			t.Errorf("mount[%d] = %q, want %q", i, got[i][1], w)
		}
	}
}

func TestMapState(t *testing.T) {
	cases := map[string]string{
		"R": "running", "S": "sleeping", "D": "disk-wait",
		"Z": "zombie", "T": "stopped", "I": "unknown",
	}
	for in, want := range cases {
		if got := mapState(in); got != want {
			t.Errorf("mapState(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseUptime(t *testing.T) {
	v, ok := parseUptime([]byte("987654.32 1234567.89\n"))
	if !ok || v < 987654.31 || v > 987654.33 {
		t.Errorf("uptime = %v ok=%v", v, ok)
	}
}
