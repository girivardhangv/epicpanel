package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// bwStatePath is the durable bandwidth-period state of the agent: which
// month the per-account nft counters were last rotated in, and the per-site
// access-log egress month-to-date (crash recovery for the in-memory
// accumulator). Lives under /var/lib (survives reboots; nft counters do
// not — recreating a chain on boot is the normal fresh-period path).
const bwStatePath = "/var/lib/epicpanel/agent/bw_state.json"

// bwState is the on-disk shape. Chains maps an nft chain name to the month
// ("2006-01") its counters were last rotated in; Sites maps a website id to
// its access-log egress bytes for the state month.
type bwState struct {
	Month  string            `json:"month"`
	Chains map[string]string `json:"chains,omitempty"`
	Sites  map[string]int64  `json:"sites,omitempty"`
}

// bwMonth is the period key for bandwidth accounting (UTC months; the
// control plane's workload_resource_usage rows use date_trunc('month') in
// the database session timezone, so both sides are pinned to UTC monthly
// boundaries by convention).
func bwMonth(t time.Time) string {
	return t.UTC().Format("2006-01")
}

// bwNeedsRotate reports whether a stored rotation stamp means the counter
// must be zeroed: a different month, or no stamp at all (bootstrap — the
// counter's age is unknown, so a deterministic fresh period wins over
// carrying an unbounded cumulative counter into a billed month).
func bwNeedsRotate(stored, current string) bool {
	return stored != current
}

// loadBwState reads the state file; a missing or unreadable file yields an
// empty state (every chain rotates, traffic accumulators start at zero —
// the control-plane GREATEST high-water keeps accounting monotonic).
func loadBwState(path string) *bwState {
	st := &bwState{Chains: map[string]string{}, Sites: map[string]int64{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	var parsed bwState
	if json.Unmarshal(b, &parsed) != nil {
		return st
	}
	if parsed.Chains != nil {
		st.Chains = parsed.Chains
	}
	if parsed.Sites != nil {
		st.Sites = parsed.Sites
	}
	st.Month = parsed.Month
	return st
}

// LoadBwMonthEgress returns the persisted access-log egress snapshot and
// its month, for boot-time seeding of the traffic sampler accumulator
// (cmd/agent wiring). A missing file yields an empty snapshot.
func LoadBwMonthEgress() (month string, sites map[string]int64) {
	st := loadBwState(bwStatePath)
	return st.Month, st.Sites
}

// save writes the state atomically (tmp + rename) so a crash mid-write
// never leaves a truncated JSON behind.
func (s *bwState) save(path string) error {
	s.Month = bwMonth(time.Now())
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
