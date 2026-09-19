package agent

import (
	"strings"
	"testing"
)

func TestNetGuardCreateBatch(t *testing.T) {
	b := netGuardCreateBatch(4242)
	for _, want := range []string{
		"add table " + netGuardTable,
		"add set " + netGuardTable + " " + netGuardSet + " { type uint32; }",
		"add chain " + netGuardTable + " " + netGuardChain + " { type filter hook output priority 10; policy accept; }",
		"add element " + netGuardTable + " " + netGuardSet + " { 4242 }",
		"ip daddr 127.0.0.0/8 tcp dport { 53, 3306, 5432 } accept",
		"ip daddr 127.0.0.0/8 udp dport { 53 } accept",
		"ip daddr 127.0.0.0/8 drop",
		"ip daddr 169.254.0.0/16 drop",
		"ip6 daddr ::1 drop",
		"ip6 daddr fe80::/10 drop",
		"ip6 daddr fd00:ec2::254 drop",
	} {
		if !strings.Contains(b, want) {
			t.Errorf("create batch missing %q", want)
		}
	}
	tag := `comment "` + netGuardComment + `"`
	if got := strings.Count(b, tag); got != len(netGuardRules()) {
		t.Errorf("create batch tags %d rules with comment, want %d", got, len(netGuardRules()))
	}
	if !strings.HasSuffix(b, "\n") {
		t.Error("batch must end with newline (nft -f - input)")
	}
}

func TestNetGuardRecreateBatchStartsWithDelete(t *testing.T) {
	b := netGuardRecreateBatch(7)
	if !strings.HasPrefix(b, "delete table "+netGuardTable+"\n") {
		t.Errorf("recreate batch must delete the table first, got prefix %q", b[:40])
	}
	// repair must provision everything a fresh create does (minus the delete line)
	create := netGuardCreateBatch(7)
	for _, line := range strings.Split(create, "\n") {
		if line != "" && !strings.Contains(b, line) {
			t.Errorf("recreate batch missing provision line %q", line)
		}
	}
}

func TestCountNetGuardRules(t *testing.T) {
	sample := `
table inet epicpanel_guard {
	chain output {
		type filter hook output priority 10; policy accept;
		meta skuid @site_uids ip daddr 127.0.0.0/8 tcp dport { 53, 3306, 5432 } accept comment "epicpanel_guard"
		meta skuid @site_uids ip daddr 127.0.0.0/8 drop comment "epicpanel_guard"
		ip daddr 10.0.0.0/8 drop
	}
}
`
	// only tagged lines count; foreign rules and the header must not
	if got := countNetGuardRules([]byte(sample)); got != 2 {
		t.Errorf("countNetGuardRules = %d, want 2", got)
	}
	if got := countNetGuardRules([]byte(strings.Join(netGuardRules(), "\n"))); got != len(netGuardRules()) {
		t.Errorf("countNetGuardRules(own rules) = %d, want %d", got, len(netGuardRules()))
	}
}

func TestBuildSessionCapsProps(t *testing.T) {
	got := buildSessionCapsProps(2048, 25, 256)
	want := []string{"MemoryMax=2048M", "CPUQuota=25%", "PidsMax=256"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("props = %v, want %v", got, want)
	}
	// zero = unlimited → skipped, no "max" property ever written
	if got := buildSessionCapsProps(0, 0, 0); len(got) != 0 {
		t.Errorf("all-zero plan must produce no props, got %v", got)
	}
	if got := buildSessionCapsProps(1024, 0, 0); strings.Join(got, "|") != "MemoryMax=1024M" {
		t.Errorf("partial props = %v", got)
	}
	// fractional CPU quotas survive round-trip
	if got := buildSessionCapsProps(0, 12.5, 0); strings.Join(got, "|") != "CPUQuota=12.5%" {
		t.Errorf("fractional quota = %v", got)
	}
}

func TestUnixUIDRefusesRootAndUnknown(t *testing.T) {
	if _, ok := unixUID(""); ok {
		t.Error("empty user must be refused")
	}
	if _, ok := unixUID("root"); ok {
		t.Error("uid 0 must never be guarded/capped by the hardening layer")
	}
	if _, ok := unixUID("epicpanel-no-such-user-xyz"); ok {
		t.Error("unknown user must be refused")
	}
	if _, ok := unixUID("daemon"); !ok {
		t.Error("known non-root system user should resolve")
	}
}
