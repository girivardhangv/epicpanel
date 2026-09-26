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
	// Corrupt file → .bak fallback (the previous generation, same values).
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if got := loadBwState(path); got.Sites["w1"] != 12345 {
		t.Fatalf("corrupt state must fall back to .bak, got %+v", got)
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
	if strings.Count(joined, "inet") != 1 {
		// epicTable already carries the family token; a second "inet" makes
		// nft reject the rule with "syntax error, unexpected inet".
		t.Fatalf("rule args must contain exactly one family token: %s", joined)
	}
}

// TestBwAccountantStateRoundTrip covers the accountant's slice of
// bw_state.json: Sites (HTTP month-to-date) + Log checkpoint survive
// save/load, and a corrupt main file falls back to the .bak generation
// (a single corrupt state must not destroy accounting).
func TestBwAccountantStateRoundTrip(t *testing.T) {
	path := t.TempDir() + "/bw_state.json"
	st := &bwState{Chains: map[string]string{"acct_ep": "2026-09"}, Sites: map[string]int64{"w1": 12345}}
	if err := st.save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	// Second writer generation (e.g. the accountant's checkpoint): save
	// again and confirm the .bak generation materialized.
	st2 := loadBwState(path)
	st2.Log = &bwLogCheckpoint{Inode: 4242, Offset: 987}
	if err := st2.save(path); err != nil {
		t.Fatalf("second save: %v", err)
	}
	// Save again so the .bak generation also carries the checkpoint.
	if err := st2.save(path); err != nil {
		t.Fatalf("third save: %v", err)
	}
	month, sites, cp := LoadBwAccountantState(path)
	if month != bwMonth(time.Now()) || sites["w1"] != 12345 {
		t.Fatalf("state roundtrip mismatch: month=%q sites=%v", month, sites)
	}
	if cp == nil || cp.Inode != 4242 || cp.Offset != 987 {
		t.Fatalf("log checkpoint roundtrip mismatch: %+v", cp)
	}
	// Corrupt main → .bak fallback (previous generation).
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	got := loadBwState(path)
	if got.Sites["w1"] != 12345 || got.Log == nil || got.Log.Inode != 4242 {
		t.Fatalf("corrupt main must fall back to .bak, got %+v", got)
	}
	// Both gone → fresh state.
	if got := loadBwState(path + ".absent"); len(got.Sites) != 0 || got.Log != nil {
		t.Fatalf("missing state must load empty, got %+v", got)
	}
}
