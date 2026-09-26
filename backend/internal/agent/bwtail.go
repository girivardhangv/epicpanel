package agent

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// Bandwidth accounting — the GLOBAL nginx accounting stream.
//
// Billing-grade HTTP accounting must not depend on anything a customer can
// touch, so the authoritative source is a platform-controlled access log in
// the http{} context (/var/log/epicpanel/bandwidth.log, conf.d/
// epicpanel-bandwidth.conf): every EpicPanel vhost stamps
// $epicpanel_site_id from EpicPanel-generated configuration, and the format
// records site id, request time, request bytes and response bytes.
//
// ONE accountant worker per node consumes that stream (no per-site
// watchers): delta-read → aggregate per site → persist the accumulator AND
// the inode+offset checkpoint in a single atomic bw_state.json write → the
// hourly enforce_limits job reports the cumulative month-to-date to the
// control plane (GREATEST high-water upsert). The checkpoint never
// advances before the corresponding bytes are durable, so a crash between
// read and persist replays the range with no double-counting: the
// accumulator that would have been inflated never survived the crash.
//
// This is deliberately NOT real-time (several minutes of delay are fine);
// the live per-window sampler (traffic.go) keeps feeding graphs, history
// buckets and attack detection exactly as before — billing is a different
// pipeline with different guarantees.
// ============================================================================

// Default accounting locations (platform-controlled; not under any site
// tree a customer can reach).
const (
	bwLogDir  = "/var/log/epicpanel"
	bwLogPath = bwLogDir + "/bandwidth.log"
)

// BWAccountant tails the global accounting stream and maintains the
// per-site HTTP month-to-date totals that the enforce path composes with
// the per-uid nft direct-egress counters.
type BWAccountant struct {
	mu sync.Mutex

	logPath   string
	statePath string
	interval  time.Duration
	nowFn     func() time.Time

	// http is the current month's HTTP bytes per site id (request +
	// response), attributed by RECORD timestamp — a record written just
	// before a UTC month boundary is never charged to the next month.
	http      map[string]int64
	httpMonth string

	// cp is the read checkpoint; nil until first boot (seek to EOF —
	// unknown history is never replayed as billable).
	cp *bwLogCheckpoint
	// partial holds a trailing line without its newline; it is re-read from
	// the same offset next pass. Dropped when the file generation changes.
	partial []byte
}

// NewBWAccountant wires the accountant with production defaults. The tally
// interval defaults to 5 minutes (EPICPANEL_AGENT_BW_TALLY_INTERVAL
// overrides, clamped to >= 1s for tests).
func NewBWAccountant() *BWAccountant {
	interval := 5 * time.Minute
	if v := os.Getenv("EPICPANEL_AGENT_BW_TALLY_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			interval = d
		}
	}
	if interval < time.Second {
		interval = time.Second
	}
	return &BWAccountant{
		logPath:   bwLogPath,
		statePath: bwStatePath,
		interval:  interval,
		nowFn:     time.Now,
		http:      map[string]int64{},
	}
}

// Restore seeds the month-to-date accumulator and the log checkpoint from
// the persisted bw_state (cmd/agent wiring at boot). A snapshot from a
// different month is ignored (fresh period); a nil checkpoint (first boot
// of the accountant, e.g. the upgrade from the access-log era) seeks to EOF
// while the restored accumulator carries the billed continuity.
func (a *BWAccountant) Restore(month string, sites map[string]int64, cp *bwLogCheckpoint) {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur := bwMonth(a.nowFn())
	if month == cur {
		for id, bytes := range sites {
			if bytes > 0 {
				a.http[id] = bytes
			}
		}
		// Mark the accumulator's period so the first tally folds new records
		// on top of the seeded value instead of rolling (wiping) it.
		a.httpMonth = cur
	}
	a.cp = cp
}

// StatePath exposes the accountant's state file path (cmd/agent boot
// wiring reads the same file it will persist to).
func (a *BWAccountant) StatePath() string { return a.statePath }

// MonthEgressBytes returns the site's HTTP month-to-date bytes and the
// month they belong to ("" when the site has no recorded traffic) — the
// enforce composition only folds it when the month matches the current
// period.
func (a *BWAccountant) MonthEgressBytes(siteID string) (int64, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	b, ok := a.http[siteID]
	if !ok {
		return 0, ""
	}
	return b, a.httpMonth
}

// Run drives the periodic tally until the context is cancelled, with an
// immediate pass at boot so a reboot/restart drains the backlog (reboot
// gap = at most one tally interval, not until the next tick).
func (a *BWAccountant) Run(ctx context.Context) {
	a.Tally()
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.Tally() // best-effort final persist (checkpoint + accumulator)
			return
		case <-ticker.C:
			a.Tally()
		}
	}
}

// Tally consumes newly available accounting records, folds them into the
// month-to-date accumulator and persists accumulator + checkpoint in one
// atomic write. Idempotent under replay: the checkpoint only advances in
// the same write that makes the folded bytes durable.
func (a *BWAccountant) Tally() {
	a.mu.Lock()
	defer a.mu.Unlock()
	cur := bwMonth(a.nowFn())
	if a.httpMonth != cur {
		a.httpMonth = cur
		a.http = map[string]int64{}
	}
	fi, err := os.Stat(a.logPath)
	if err != nil {
		// No accounting log yet (nginx conf not deployed yet / fresh node):
		// nothing to read, checkpoint unchanged. Not an error — the pipeline
		// is deploy-order tolerant (docs/bandwidth-accounting.md).
		return
	}
	inode := inodeOf(fi)
	if a.cp == nil {
		// First boot of the accountant: start at EOF (never bill unknown
		// history); the restored accumulator carries billed continuity.
		a.cp = &bwLogCheckpoint{Inode: inode, Offset: fi.Size()}
		a.persistLocked(cur)
		return
	}
	if inode == a.cp.Inode {
		if fi.Size() < a.cp.Offset {
			// Truncated in place (copytruncate-style): the bytes between the
			// old offset and the truncation point are gone — log it honestly
			// and re-read from zero (everything now in the file is new data).
			slog.Warn("bandwidth accounting log truncated; unprocessed bytes lost", "lost", a.cp.Offset)
			a.cp.Offset = 0
			a.partial = nil
		}
		a.cp.Offset = a.readRange(a.logPath, a.cp.Offset)
		a.persistLocked(cur)
		return
	}
	// Inode changed: the log was rotated (rename + create + reopen). Drain
	// the previous generation from the stored offset BEFORE switching, so
	// unprocessed records are never abandoned (logrotate delaycompress
	// keeps .1 plain text; .2 covers a double rotation within one interval).
	drained := false
	for _, cand := range []string{a.logPath + ".1", a.logPath + ".2"} {
		cfi, err := os.Stat(cand)
		if err != nil || inodeOf(cfi) != a.cp.Inode {
			continue
		}
		a.cp.Offset = a.readRange(cand, a.cp.Offset)
		drained = true
		break
	}
	if !drained && a.cp.Offset > 0 {
		slog.Warn("bandwidth accounting log rotated without a recognizable predecessor; unprocessed bytes lost",
			"inode", a.cp.Inode, "offset", a.cp.Offset)
	}
	// A partial line belongs to the old generation; the new file starts with
	// a complete record.
	a.partial = nil
	a.cp = &bwLogCheckpoint{Inode: inode, Offset: 0}
	a.cp.Offset = a.readRange(a.logPath, 0)
	a.persistLocked(cur)
}

// readRange consumes complete log lines from `from` to the current EOF and
// returns the new offset (first unprocessed byte). Only bytes consumed as
// complete lines advance the offset — a trailing partial line is re-read
// next pass.
func (a *BWAccountant) readRange(path string, from int64) int64 {
	f, err := os.Open(path) // #nosec G304 — platform-controlled constant path
	if err != nil {
		return from
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < from {
		return from
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return from
	}
	r := bufio.NewReaderSize(f, 64*1024)
	consumed := int64(0)
	for {
		chunk, err := r.ReadBytes('\n')
		consumed += int64(len(chunk))
		if len(chunk) > 0 {
			var line []byte
			if len(a.partial) > 0 {
				line = append(a.partial, chunk...)
				a.partial = nil
			} else {
				line = chunk
			}
			if err == nil {
				a.parseLine(string(line))
			} else {
				a.partial = line
			}
		}
		if err != nil {
			break
		}
	}
	return from + consumed - int64(len(a.partial))
}

// parseBWRecord parses one accounting record:
//
//	<site-uuid> <time_iso8601> <request_length> <bytes_sent> "<user agent>"
//
// Returns ok=false for malformed records and unknown site ids (the default
// vhost logs "-"). Shared by the live accountant and the recalc scanner.
func parseBWRecord(line string) (siteID string, ts time.Time, reqLen, bytesSent int64, ok bool) {
	line = strings.TrimRight(line, "\r\n")
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return "", time.Time{}, 0, 0, false
	}
	siteID = fields[0]
	if _, err := uuid.Parse(siteID); err != nil {
		return "", time.Time{}, 0, 0, false
	}
	ts, err := time.Parse(time.RFC3339, fields[1])
	if err != nil {
		return "", time.Time{}, 0, 0, false
	}
	reqLen, err = strconv.ParseInt(fields[2], 10, 64)
	if err != nil || reqLen < 0 {
		return "", time.Time{}, 0, 0, false
	}
	bytesSent, err = strconv.ParseInt(fields[3], 10, 64)
	if err != nil || bytesSent < 0 {
		return "", time.Time{}, 0, 0, false
	}
	if q1 := strings.IndexByte(line, '"'); q1 >= 0 {
		if q2 := strings.LastIndexByte(line, '"'); q2 > q1 {
			if strings.HasPrefix(line[q1+1:q2], "EpicPanel-") {
				return "", time.Time{}, 0, 0, false
			}
		}
	}
	return siteID, ts, reqLen, bytesSent, true
}

// parseLine folds one accounting record into the month-to-date accumulator.
//
//	<site-uuid> <time_iso8601> <request_length> <bytes_sent> "<user agent>"
//
// The accountant skips everything the shared record parser rejects
// (malformed lines, "-" from the default vhost, platform self-traffic).
// Bytes = request + response (the documented billing definition; both
// fields are logged, so the definition is auditable from raw logs).
func (a *BWAccountant) parseLine(line string) {
	siteID, ts, reqLen, bytesSent, ok := parseBWRecord(line)
	if !ok {
		return
	}
	// Record-time attribution: a record stamped in a month other than the
	// accumulator's never enters it (no September traffic in October, no
	// late-processed boundary traffic lost into the wrong month).
	if bwMonth(ts) != a.httpMonth {
		return
	}
	a.http[siteID] += reqLen + bytesSent
}

// persistLocked folds the accountant's state into the shared bw_state.json
// under the global mutex. The accumulator and the checkpoint land in ONE
// atomic write: a crash between read and persist replays the range, a crash
// after it never does — that is the whole idempotency contract.
func (a *BWAccountant) persistLocked(cur string) {
	bwStateMu.Lock()
	defer bwStateMu.Unlock()
	st := loadBwState(a.statePath)
	st.Month = cur
	if a.httpMonth == cur {
		sites := make(map[string]int64, len(a.http))
		for id, b := range a.http {
			sites[id] = b
		}
		st.Sites = sites
	}
	cp := *a.cp
	st.Log = &cp
	// Persistence failure must not fail the tally: worst case the next pass
	// re-reads the same range (the in-memory accumulator was already folded,
	// so a replay would double-count in memory — hence re-read only happens
	// after a restart, which reloads the LAST DURABLE accumulator; see the
	// crash-ordering note at the top of this file).
	if err := st.save(a.statePath); err != nil {
		slog.Warn("bandwidth state persist failed", "err", err)
	}
}

func inodeOf(fi os.FileInfo) uint64 {
	return uint64(fi.Sys().(*syscallStatT).Ino)
}
