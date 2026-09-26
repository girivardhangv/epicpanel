package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// bwStatePath is the durable bandwidth-period state of the agent: which
// month the per-account nft counters were last rotated in, the per-site
// HTTP month-to-date (global accounting log), and the checkpoint of that
// log (inode + offset). Lives under /var/lib (survives reboots; nft
// counters do not — recreating a chain on boot is the normal fresh-period
// path).
const bwStatePath = "/var/lib/epicpanel/agent/bw_state.json"

// bwStateMu guards the shared bw_state.json across its two writers: the
// enforce path (Chains rotation stamps, hourly) and the bandwidth
// accountant (Sites + Log checkpoint, every tally). Both load, mutate and
// save the whole file, so concurrent writers would lose each other's
// fields without it.
var bwStateMu sync.Mutex

// bwState is the on-disk shape. Chains maps an nft chain name to the month
// ("2006-01") its counters were last rotated in; Sites maps a website id to
// its HTTP month-to-date bytes for the state month (fed from the global
// nginx accounting log; historically the per-site access-log accumulator —
// the upgrade seeds the new source from the persisted value, so the
// accumulator carries over unchanged). Log is the accounting-log read
// checkpoint: it never advances before the corresponding bytes are in
// Sites, and both land in ONE atomic file write.
type bwState struct {
	Month  string            `json:"month"`
	Chains map[string]string `json:"chains,omitempty"`
	Sites  map[string]int64  `json:"sites,omitempty"`
	Log    *bwLogCheckpoint  `json:"log,omitempty"`
}

// bwLogCheckpoint is the durable read position in the global accounting
// log. The inode identifies the file generation (rename rotation keeps the
// inode; logrotate's create/reopen changes it), the offset is the first
// UNPROCESSED byte.
type bwLogCheckpoint struct {
	Inode  uint64 `json:"inode"`
	Offset int64  `json:"offset"`
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

// loadBwState reads the state file; a missing or unreadable file falls back
// to the .bak generation (save keeps one), and only when both are unusable
// yields an empty state. Every writer rebuilds the file from its own
// in-memory truth on the next save, so a single corrupt file cannot destroy
// accounting (spec: no single state entry may wipe billing).
func loadBwState(path string) *bwState {
	st := &bwState{Chains: map[string]string{}, Sites: map[string]int64{}}
	b, err := os.ReadFile(path)
	if err != nil {
		if b2, err2 := os.ReadFile(path + ".bak"); err2 == nil {
			if parsed, ok := parseBwState(b2); ok {
				return parsed
			}
		}
		return st
	}
	if parsed, ok := parseBwState(b); ok {
		return parsed
	}
	if b2, err2 := os.ReadFile(path + ".bak"); err2 == nil {
		if parsed, ok := parseBwState(b2); ok {
			return parsed
		}
	}
	return st
}

func parseBwState(b []byte) (*bwState, bool) {
	var parsed bwState
	if json.Unmarshal(b, &parsed) != nil {
		return nil, false
	}
	st := &bwState{Chains: map[string]string{}, Sites: map[string]int64{}, Month: parsed.Month, Log: parsed.Log}
	if parsed.Chains != nil {
		st.Chains = parsed.Chains
	}
	if parsed.Sites != nil {
		st.Sites = parsed.Sites
	}
	return st, true
}

// LoadBwAccountantState returns the persisted period state for the
// bandwidth accountant's boot: the state month, the per-site HTTP
// month-to-date map, and the accounting-log checkpoint (nil = first boot,
// the reader seeks to EOF rather than replaying unknown history).
func LoadBwAccountantState(path string) (month string, sites map[string]int64, cp *bwLogCheckpoint) {
	st := loadBwState(path)
	return st.Month, st.Sites, st.Log
}

// save writes the state atomically (tmp + rename) and keeps the previous
// generation as .bak so a crash mid-save or a corrupt write costs one
// generation of state, never the file. The Month stamp is refreshed to the
// current UTC month on every save.
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
	// Rotate generations: previous -> .bak, tmp -> current. A failure here
	// is survivable (worst case the next writer rebuilds from memory), so
	// the best-effort renames never fail the save outright.
	_ = os.Rename(path, path+".bak")
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}
