package agent

import (
	"context"
	"fmt"
	"os/exec"
	osuser "os/user"
	"strconv"
	"strings"
)

// ============================================================================
// Node hardening — closes the two residual gaps of the sandbox/limits stack
// (see internal/isolation and enforce.go):
//
//   1. Network guard (nftables): site unix users share the host network
//      namespace (composer/npm/git need outbound; NetworkPolicy
//      SharedHostNetwork default), which otherwise lets a site reach
//      host-local services — loopback-bound admin endpoints and the cloud
//      metadata service (169.254.169.254, AWS IPv6 fd00:ec2::254). A
//      dedicated table holds a per-uid egress guard: traffic FROM any site
//      uid to loopback is dropped except the provisioned service ports (DNS,
//      MySQL, PostgreSQL); link-local metadata ranges are always dropped.
//      Site uids accumulate in a set, so per-site work is one idempotent
//      element add. External wipes/reloads of the table are detected on the
//      next convergence run and repaired by recreating the table; other
//      sites re-add their uid on their own next run (≤ one hour), the same
//      convergence model every other limit uses.
//
//   2. Session caps (systemd): web-terminal sessions are capped inside the
//      site's epicpanel slice (isolation.Command systemd-run scope), but SSH
//      login shells land in user-<uid>.slice, outside every enforced limit.
//      The plan's memory/cpu/pids caps are mirrored onto that slice via
//      systemctl set-property so every process path of a site user is
//      bounded. Before the first login the slice does not exist and the
//      outcome says so honestly (validated-only) — converges on later runs.
//
// Both mechanisms are detect-then-apply and degrade WITH a reason, matching
// the enforce.go contract. Stale rules/properties of deleted sites are
// harmless: the guard is uniform per-uid and caps are overwritten by the
// next enforce run if a uid is ever reused.
// ============================================================================

const (
	netGuardTable   = "inet epicpanel_guard"
	netGuardChain   = "output"
	netGuardSet     = "site_uids"
	netGuardComment = "epicpanel_guard"
)

// Loopback ports a site user may still reach: DNS (systemd-resolved listens
// on 127.0.0.53) and the panel-provisioned database engines. Everything else
// on loopback is dropped for site uids (admin APIs, panel endpoints, ...).
// Unix-socket DB connections are unaffected — filesystem permissions govern
// those, and the sandbox never binds host sockets anyway.
var (
	netGuardLoopbackTCPPorts = []int{53, 3306, 5432}
	netGuardLoopbackUDPPorts = []int{53}
)

// netGuardRules renders the guard ruleset. Every rule carries
// netGuardComment so presence can be verified by counting tagged lines in
// `nft list chain` output.
func netGuardRules() []string {
	return []string{
		fmt.Sprintf(`add rule %s %s meta skuid @%s ip daddr 127.0.0.0/8 tcp dport { %s } accept comment "%s"`,
			netGuardTable, netGuardChain, netGuardSet, portList(netGuardLoopbackTCPPorts), netGuardComment),
		fmt.Sprintf(`add rule %s %s meta skuid @%s ip daddr 127.0.0.0/8 udp dport { %s } accept comment "%s"`,
			netGuardTable, netGuardChain, netGuardSet, portList(netGuardLoopbackUDPPorts), netGuardComment),
		fmt.Sprintf(`add rule %s %s meta skuid @%s ip daddr 127.0.0.0/8 drop comment "%s"`,
			netGuardTable, netGuardChain, netGuardSet, netGuardComment),
		fmt.Sprintf(`add rule %s %s meta skuid @%s ip daddr 169.254.0.0/16 drop comment "%s"`,
			netGuardTable, netGuardChain, netGuardSet, netGuardComment),
		fmt.Sprintf(`add rule %s %s meta skuid @%s ip6 daddr ::1 drop comment "%s"`,
			netGuardTable, netGuardChain, netGuardSet, netGuardComment),
		fmt.Sprintf(`add rule %s %s meta skuid @%s ip6 daddr fe80::/10 drop comment "%s"`,
			netGuardTable, netGuardChain, netGuardSet, netGuardComment),
		fmt.Sprintf(`add rule %s %s meta skuid @%s ip6 daddr fd00:ec2::254 drop comment "%s"`,
			netGuardTable, netGuardChain, netGuardSet, netGuardComment),
	}
}

func portList(ports []int) string {
	parts := make([]string, len(ports))
	for i, p := range ports {
		parts[i] = strconv.Itoa(p)
	}
	return strings.Join(parts, ", ")
}

// netGuardCreateBatch provisions table + set + chain + rules + element in one
// atomic netlink transaction (fresh node).
func netGuardCreateBatch(uid int) string {
	var b strings.Builder
	b.WriteString("add table " + netGuardTable + "\n")
	fmt.Fprintf(&b, "add set %s %s { type uint32; }\n", netGuardTable, netGuardSet)
	fmt.Fprintf(&b, "add chain %s %s { type filter hook output priority 10; policy accept; }\n", netGuardTable, netGuardChain)
	writeNetGuardRules(&b)
	fmt.Fprintf(&b, "add element %s %s { %d }\n", netGuardTable, netGuardSet, uid)
	return b.String()
}

// netGuardRecreateBatch repairs drift (external flush/reload of our table):
// delete + full re-provision atomically. Other sites' uids are re-added by
// their own next convergence run.
func netGuardRecreateBatch(uid int) string {
	return "delete table " + netGuardTable + "\n" + netGuardCreateBatch(uid)
}

// netGuardElementBatch adds one uid to the guarded set (steady state).
func netGuardElementBatch(uid int) string {
	return fmt.Sprintf("add element %s %s { %d }\n", netGuardTable, netGuardSet, uid)
}

func writeNetGuardRules(b *strings.Builder) {
	for _, r := range netGuardRules() {
		b.WriteString(r + "\n")
	}
}

// countNetGuardRules counts our tagged rules in `nft list chain` output.
// Matches the full quoted comment attribute — the bare table name also
// appears in the `table inet epicpanel_guard {` header and must not count.
func countNetGuardRules(out []byte) int {
	tag := `comment "` + netGuardComment + `"`
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, tag) {
			n++
		}
	}
	return n
}

// runNftBatch applies one nft batch atomically via stdin: a single netlink
// transaction, so there is no window where the guard is half-applied.
func runNftBatch(ctx context.Context, batch string) error {
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(batch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("nft batch: %w: %s", err, tail(out, 300))
	}
	return nil
}

// enforceNetworkGuard ensures the per-uid egress guard covers this site's
// unix user. Outcome modes follow the enforce.go contract.
func (e *Executor) enforceNetworkGuard(ctx context.Context, user string) MechanismOutcome {
	const resource = "network"
	if !nftAvailable() {
		return MechanismOutcome{Resource: resource, Mode: "skipped", Mechanism: "nftables",
			Detail: "nftables unavailable; loopback/metadata egress of site users unfiltered"}
	}
	uid, ok := unixUID(user)
	if !ok {
		return MechanismOutcome{Resource: resource, Mode: "skipped", Mechanism: "nftables",
			Detail: "unix user unresolved; no per-uid guard"}
	}

	want := len(netGuardRules())
	_, tableErr := exec.CommandContext(ctx, "nft", "list", "table", netGuardTable).CombinedOutput()
	batch := ""
	if tableErr != nil {
		batch = netGuardCreateBatch(uid)
	} else {
		chainOut, chainErr := exec.CommandContext(ctx, "nft", "list", "chain", netGuardTable, netGuardChain).CombinedOutput()
		_, setErr := exec.CommandContext(ctx, "nft", "list", "set", netGuardTable, netGuardSet).CombinedOutput()
		switch {
		case chainErr != nil || setErr != nil || countNetGuardRules(chainOut) != want:
			batch = netGuardRecreateBatch(uid)
		default:
			if _, elemErr := exec.CommandContext(ctx, "nft", "get", "element", netGuardTable, netGuardSet, fmt.Sprintf("{ %d }", uid)).CombinedOutput(); elemErr == nil {
				return MechanismOutcome{Resource: resource, Mode: "enforced", Mechanism: "nftables",
					Detail: fmt.Sprintf("skuid guard active for uid %d (loopback allowlist %v, link-local deny)", uid, netGuardLoopbackTCPPorts)}
			}
			batch = netGuardElementBatch(uid)
		}
	}
	if err := runNftBatch(ctx, batch); err != nil {
		return MechanismOutcome{Resource: resource, Mode: "validated-only", Mechanism: "nftables",
			Detail: fmt.Sprintf("guard apply failed: %v", err)}
	}
	return MechanismOutcome{Resource: resource, Mode: "enforced", Mechanism: "nftables",
		Detail: fmt.Sprintf("skuid guard active for uid %d (loopback allowlist %v, link-local deny)", uid, netGuardLoopbackTCPPorts)}
}

// enforceSessionCaps mirrors the plan's memory/cpu/pids caps onto the login
// shell slice (user-<uid>.slice), which systemd places all SSH sessions in.
// Without this, shell-spawned processes escape every enforced limit while
// terminal sessions (epicpanel slice) and FPM pools are capped.
func (e *Executor) enforceSessionCaps(ctx context.Context, user string, p EnforceJobPayload) MechanismOutcome {
	const resource = "session_caps"
	props := buildSessionCapsProps(p.MemoryMB, p.CPUPercent, p.PidsMax)
	if len(props) == 0 {
		return MechanismOutcome{Resource: resource, Mode: "skipped", Mechanism: "systemd-slice",
			Detail: "plan sets no memory/cpu/pids caps"}
	}
	uid, ok := unixUID(user)
	if !ok {
		return MechanismOutcome{Resource: resource, Mode: "skipped", Mechanism: "systemd-slice",
			Detail: "unix user unresolved; no session slice caps"}
	}
	slice := fmt.Sprintf("user-%d.slice", uid)
	// systemd creates the slice with the first login session; before that
	// there is nothing to cap — report honestly, converge later.
	out, err := exec.CommandContext(ctx, "systemctl", "show", "-p", "LoadState", "--value", slice).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) == "not-found" {
		return MechanismOutcome{Resource: resource, Mode: "validated-only", Mechanism: "systemd-slice",
			Detail: fmt.Sprintf("%s not loaded yet (no login session); converges after first login", slice)}
	}
	if err := e.run(ctx, "systemctl", append([]string{"set-property", slice}, props...)...); err != nil {
		return MechanismOutcome{Resource: resource, Mode: "validated-only", Mechanism: "systemd-slice",
			Detail: fmt.Sprintf("set-property failed: %v", err)}
	}
	return MechanismOutcome{Resource: resource, Mode: "enforced", Mechanism: "systemd-slice",
		Detail: fmt.Sprintf("%s: %s (login sessions, same plan matrix as the epicpanel slice)", slice, strings.Join(props, " "))}
}

// buildSessionCapsProps renders the systemctl set-property arguments. Zero
// values are skipped (0 = unlimited in the plan payload).
func buildSessionCapsProps(memMB int64, cpuPercent float64, pidsMax int64) []string {
	var props []string
	if memMB > 0 {
		props = append(props, fmt.Sprintf("MemoryMax=%dM", memMB))
	}
	if cpuPercent > 0 {
		props = append(props, "CPUQuota="+strconv.FormatFloat(cpuPercent, 'f', -1, 64)+"%")
	}
	if pidsMax > 0 {
		props = append(props, fmt.Sprintf("PidsMax=%d", pidsMax))
	}
	return props
}

// unixUID resolves a site unix username to its uid. Refuses root/unknown:
// the guard and caps must never bind to uid 0.
func unixUID(user string) (int, bool) {
	if user == "" {
		return 0, false
	}
	u, err := osuser.Lookup(user)
	if err != nil || u.Uid == "0" {
		return 0, false
	}
	id, err := strconv.Atoi(u.Uid)
	if err != nil {
		return 0, false
	}
	return id, true
}
