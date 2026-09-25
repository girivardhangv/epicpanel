package agent

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestBwMonthAndRotate covers the period key + rotation decision: a missing
// stamp rotates (bootstrap), the same month does not, the next month does.
func TestBwMonthAndRotate(t *testing.T) {
	jan := time.Date(2026, 1, 31, 23, 59, 59, 0, time.UTC)
	feb := time.Date(2026, 2, 1, 0, 0, 5, 0, time.UTC)
	if bwMonth(jan) != "2026-01" || bwMonth(feb) != "2026-02" {
		t.Fatalf("month keys: %s %s", bwMonth(jan), bwMonth(feb))
	}
	if !bwNeedsRotate("", "2026-01") {
		t.Fatal("bootstrap (no stamp) must rotate")
	}
	if bwNeedsRotate("2026-01", "2026-01") {
		t.Fatal("same month must not rotate")
	}
	if !bwNeedsRotate("2026-01", "2026-02") {
		t.Fatal("month change must rotate")
	}
}

// TestBwStateRoundTrip covers atomic save/load and the corrupt-file path
// (an unreadable state must yield a fresh state, never a half-parsed one).
func TestBwStateRoundTrip(t *testing.T) {
	path := t.TempDir() + "/bw_state.json"
	st := &bwState{Chains: map[string]string{"acct_ep": "2026-09"}, Sites: map[string]int64{"w1": 12345}}
	if err := st.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := loadBwState(path)
	if got.Chains["acct_ep"] != "2026-09" || got.Sites["w1"] != 12345 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
	if got.Month != bwMonth(time.Now()) {
		t.Fatalf("save must stamp the current month, got %q", got.Month)
	}
	if err := st.save(path); err != nil {
		t.Fatalf("second save (overwrite via rename): %v", err)
	}
	// Corrupt file → fresh state.
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if got := loadBwState(path); len(got.Chains) != 0 || len(got.Sites) != 0 {
		t.Fatalf("corrupt state must load empty, got %+v", got)
	}
	// Missing file → fresh state.
	if got := loadBwState(path + ".absent"); len(got.Chains) != 0 {
		t.Fatalf("missing state must load empty")
	}
}

// TestBwChainDecisions covers the convergence predicates on synthetic
// `nft list chain` output: legacy FORWARD-hooked chains and rule-less
// chains must both be recreated; a converged chain must be left alone.
func TestBwChainDecisions(t *testing.T) {
	legacy := []byte(`table inet epicpanel_limits {
	chain acct_ep {
		type filter hook forward priority -300; policy accept;
		counter packets 5 bytes 640
	}
}`)
	converged := []byte(`table inet epicpanel_limits {
	chain acct_ep {
		type filter hook output priority -300; policy accept;
		meta skuid 1007 oifname != "lo" counter packets 5 bytes 640
	}
}`)
	ruleLess := []byte(`table inet epicpanel_limits {
	chain acct_ep {
		type filter hook output priority -300; policy accept;
	}
}`)
	if !bwChainNeedsRecreate(legacy) {
		t.Fatal("forward-hooked chain must be recreated")
	}
	if bwChainNeedsRecreate(converged) || bwChainNeedsRecreate(ruleLess) {
		t.Fatal("output-hooked chains must not be recreated for the hook")
	}
	if !bwChainNeedsRule(converged, 1008) {
		t.Fatal("different uid rule must be reported missing")
	}
	if !bwChainNeedsRule(ruleLess, 1007) {
		t.Fatal("rule-less chain must be reported missing")
	}
	if bwChainNeedsRule(converged, 1007) {
		t.Fatal("converged chain must be left alone")
	}
	args := bwChainRuleArgs("acct_ep", 1007)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "meta skuid 1007") || !strings.Contains(joined, `"lo"`) {
		t.Fatalf("rule args must pin the uid and exclude loopback: %s", joined)
	}
}

// TestTrafficMonthAccumulator covers the month-to-date egress accumulator:
// additive within the month, rolled over at the boundary, restorable from a
// persisted snapshot of the same month only.
func TestTrafficMonthAccumulator(t *testing.T) {
	s := NewTrafficSampler()
	jan := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	feb := time.Date(2026, 2, 1, 0, 0, 10, 0, time.UTC)
	s.nowFn = func() time.Time { return jan }

	s.addMonthEgress("w1", 1000)
	s.addMonthEgress("w1", 500)
	s.addMonthEgress("w2", 7)
	if b, m := s.MonthEgressBytes("w1"); b != 1500 || m != "2026-01" {
		t.Fatalf("w1: %d %s", b, m)
	}
	if b, _ := s.MonthEgressBytes("missing"); b != 0 {
		t.Fatalf("unknown site must read zero")
	}
	// Month boundary rolls the entry.
	s.nowFn = func() time.Time { return feb }
	s.addMonthEgress("w1", 10)
	if b, m := s.MonthEgressBytes("w1"); b != 10 || m != "2026-02" {
		t.Fatalf("rollover: %d %s", b, m)
	}
	// Restore seeds only same-month snapshots with positive values.
	s2 := NewTrafficSampler()
	s2.nowFn = func() time.Time { return feb }
	s2.RestoreMonthEgress("2026-01", map[string]int64{"w1": 9999})
	if b, _ := s2.MonthEgressBytes("w1"); b != 0 {
		t.Fatalf("stale-month restore must be dropped, got %d", b)
	}
	s2.RestoreMonthEgress("2026-02", map[string]int64{"w1": 9999, "w2": -5, "w3": 42})
	if b, _ := s2.MonthEgressBytes("w1"); b != 9999 {
		t.Fatalf("same-month restore: %d", b)
	}
	if b, _ := s2.MonthEgressBytes("w2"); b != 0 {
		t.Fatalf("negative snapshot values must be dropped")
	}
	snap := s2.MonthEgressSnapshot()
	if snap["w1"] != 9999 || len(snap) != 2 {
		t.Fatalf("snapshot: %+v", snap)
	}
}
