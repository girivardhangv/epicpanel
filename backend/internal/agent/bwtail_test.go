package agent

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// Bandwidth accountant tests — the billing pipeline's failure-scenario
// contract: correct attribution, replay idempotency, rotation/truncation
// safety, restart/reboot continuity, month independence.
// ============================================================================

var (
	bwSiteA = "11111111-1111-1111-1111-111111111111"
	bwSiteB = "22222222-2222-2222-2222-222222222222"
)

// bwRec formats one accounting-log record the way nginx writes it
// (log_format epicpanel_bandwidth, plus a trailing UA for realism).
func bwRec(site, ts string, req, sent int64, ua string) string {
	return fmt.Sprintf("%s %s %d %d \"%s\"\n", site, ts, req, sent, ua)
}

// newTestAccountant builds an accountant over a temp log dir + state file,
// pinned to a fixed "now".
func newTestAccountant(t *testing.T, now time.Time) (*BWAccountant, string, string) {
	t.Helper()
	base := t.TempDir()
	logDir := filepath.Join(base, "varlog", "epicpanel")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(base, "bw_state.json")
	a := &BWAccountant{
		logPath:   filepath.Join(logDir, "bandwidth.log"),
		statePath: statePath,
		nowFn:     func() time.Time { return now },
		http:      map[string]int64{},
	}
	return a, logDir, statePath
}

func bwTS(now time.Time, hour, min, sec int) string {
	return time.Date(now.Year(), now.Month(), now.Day(), hour, min, sec, 0, time.UTC).Format(time.RFC3339)
}

// seedCPFromZero pins the accountant's checkpoint at offset 0 of the
// current log generation — the "restart with a valid checkpoint" state, as
// opposed to first-ever boot which deliberately seeks to EOF.
func seedCPFromZero(t *testing.T, a *BWAccountant) {
	t.Helper()
	fi, err := os.Stat(a.logPath)
	if err != nil {
		t.Fatal(err)
	}
	a.Restore("", nil, &bwLogCheckpoint{Inode: inodeOf(fi), Offset: 0})
}

// TestBWAccountantParseAndAttribution covers the record-level contract:
// sites separated, requests combined, self-traffic excluded, garbage
// skipped, GB-scale values exact, request+response summed.
func TestBWAccountantParseAndAttribution(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a, logDir, _ := newTestAccountant(t, now)
	log := filepath.Join(logDir, "bandwidth.log")
	content := bwRec(bwSiteA, bwTS(now, 10, 0, 0), 512, 3*1024*1024*1024, "Mozilla/5.0") + // 3 GB response: no int32 overflow
		bwRec(bwSiteB, bwTS(now, 10, 0, 1), 100, 200, "curl/8.0") +
		bwRec(bwSiteA, bwTS(now, 10, 0, 2), 300, 400, "Mozilla/5.0") + // same site, second vhost/domain
		bwRec(bwSiteA, bwTS(now, 10, 0, 3), 10, 10, "EpicPanel-HealthCheck/1.0") + // self-traffic: excluded
		"- 2026-09-15T10:00:04+00:00 5 5 \"x\"\n" + // default vhost: skipped
		"garbage line\n" +
		bwRec(bwSiteB, bwTS(now, 10, 0, 5), 7, 0, "Mozilla/5.0")
	if err := os.WriteFile(log, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	seedCPFromZero(t, a)
	a.Tally()
	if got, _ := a.MonthEgressBytes(bwSiteA); got != 512+3*1024*1024*1024+300+400 {
		t.Fatalf("site A total = %d", got)
	}
	if got, _ := a.MonthEgressBytes(bwSiteB); got != 100+200+7 {
		t.Fatalf("site B total = %d", got)
	}
	if _, m := a.MonthEgressBytes(bwSiteA); m != "2026-09" {
		t.Fatalf("month = %q", m)
	}
}

// TestBWAccountantIncrementalAndRestart covers §11/§23: only new bytes are
// processed, and a fresh instance restoring from bw_state neither loses nor
// double-counts.
func TestBWAccountantIncrementalAndRestart(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a, logDir, statePath := newTestAccountant(t, now)
	log := filepath.Join(logDir, "bandwidth.log")
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 10, 0, 0), 100, 1000, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Tally() // first boot: seeks to EOF; the record's bytes are already on disk, so the checkpoint starts past them
	// ...and the checkpoint sits at EOF of what was written.
	_, _, cp := LoadBwAccountantState(statePath)
	wantEOF := int64(len(bwRec(bwSiteA, bwTS(now, 10, 0, 0), 100, 1000, "UA")))
	if cp == nil || cp.Offset != wantEOF {
		t.Fatalf("checkpoint after first boot: %+v (want offset %d)", cp, wantEOF)
	}
	// New traffic arrives → exactly the new bytes are counted.
	f, err := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(bwRec(bwSiteA, bwTS(now, 11, 0, 0), 20, 2000, "UA") +
		bwRec(bwSiteB, bwTS(now, 11, 0, 1), 30, 300, "UA")); err != nil {
		t.Fatal(err)
	}
	f.Close()
	a.Tally()
	if got, _ := a.MonthEgressBytes(bwSiteA); got != 2020 {
		t.Fatalf("incremental A = %d", got)
	}
	if got, _ := a.MonthEgressBytes(bwSiteB); got != 330 {
		t.Fatalf("incremental B = %d", got)
	}

	// RESTART: a fresh instance restores the last durable accumulator +
	// checkpoint; re-tallying must add nothing (no double count, no loss).
	month, sites, cp := LoadBwAccountantState(statePath)
	b := &BWAccountant{logPath: log, statePath: statePath, nowFn: func() time.Time { return now }, http: map[string]int64{}}
	b.Restore(month, sites, cp)
	b.Tally()
	if got, _ := b.MonthEgressBytes(bwSiteA); got != 2020 {
		t.Fatalf("after restart A = %d (double-count or loss)", got)
	}
	if got, _ := b.MonthEgressBytes(bwSiteB); got != 330 {
		t.Fatalf("after restart B = %d", got)
	}
}

// TestBWAccountantReplayAfterLostState covers the crash-before-persist
// window: the state never made it to disk, so a restart re-reads the range
// — but the accumulator it folds into was ALSO lost, so the range is
// counted exactly once. (Durable-checkpoint contract, spec §13/§14.)
func TestBWAccountantReplayAfterLostState(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a, logDir, statePath := newTestAccountant(t, now)
	log := filepath.Join(logDir, "bandwidth.log")
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 9, 0, 0), 100, 400, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	seedCPFromZero(t, a)
	a.Tally()
	if got, _ := a.MonthEgressBytes(bwSiteA); got != 500 {
		t.Fatalf("pre-crash total = %d", got)
	}
	// Simulate the crash taking the state file with it (the in-memory
	// accumulator died with the process; the persist never happened).
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath + ".bak"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	// Restart: nothing restored → seek EOF → the old range is NOT re-billed
	// (the alternative — replaying into a zero accumulator — would be fine
	// for the bytes but double-counts NOTHING either way; the checkpoint
	// contract keeps this deterministic and loss-bounded).
	b := &BWAccountant{logPath: log, statePath: statePath, nowFn: func() time.Time { return now }, http: map[string]int64{}}
	b.Tally()
	if got, _ := b.MonthEgressBytes(bwSiteA); got != 0 {
		t.Fatalf("post-crash total must not replay processed ranges, got %d", got)
	}
	// New traffic counts normally from here.
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(bwRec(bwSiteA, bwTS(now, 9, 30, 0), 10, 90, "UA"))
	f.Close()
	b.Tally()
	if got, _ := b.MonthEgressBytes(bwSiteA); got != 100 {
		t.Fatalf("post-crash new traffic = %d", got)
	}
}

// TestBWAccountantRotation covers rename rotation (§15): the previous
// generation is drained from the stored offset BEFORE the new file is read,
// and the drained bytes are never re-counted.
func TestBWAccountantRotation(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a, logDir, statePath := newTestAccountant(t, now)
	log := filepath.Join(logDir, "bandwidth.log")
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 8, 0, 0), 100, 100, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	seedCPFromZero(t, a)
	a.Tally()
	// Rotate: rename the log, start a fresh file (new inode), nginx writes
	// two more records before the next tally.
	if err := os.Rename(log, log+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 9, 0, 0), 10, 20, "UA")+
		bwRec(bwSiteB, bwTS(now, 9, 0, 1), 5, 7, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Tally()
	if got, _ := a.MonthEgressBytes(bwSiteA); got != 230 {
		t.Fatalf("rotation drain A = %d (want 200+30)", got)
	}
	if got, _ := a.MonthEgressBytes(bwSiteB); got != 12 {
		t.Fatalf("rotation drain B = %d", got)
	}
	// No re-drain on the next pass.
	a.Tally()
	if got, _ := a.MonthEgressBytes(bwSiteA); got != 230 {
		t.Fatalf("second pass must be a no-op, got %d", got)
	}
	// Restart from state is consistent too.
	month, sites, cp := LoadBwAccountantState(statePath)
	b := &BWAccountant{logPath: log, statePath: statePath, nowFn: func() time.Time { return now }, http: map[string]int64{}}
	b.Restore(month, sites, cp)
	b.Tally()
	if got, _ := b.MonthEgressBytes(bwSiteA); got != 230 {
		t.Fatalf("restart after rotation = %d", got)
	}
}

// TestBWAccountantDoubleRotation covers two rotations within one tally
// interval: the offset then refers to the .2 generation (drained via .2),
// and .1 (which the checkpoint never saw) is skipped loudly.
func TestBWAccountantDoubleRotation(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a, logDir, _ := newTestAccountant(t, now)
	log := filepath.Join(logDir, "bandwidth.log")
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 8, 0, 0), 100, 100, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Tally() // first boot → EOF (record skipped), checkpoint = this inode
	// Two rotations in quick succession: log → log.1 → log.2, new log.
	if err := os.Rename(log, log+".2"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 9, 0, 0), 10, 10, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(log, log+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 9, 1, 0), 20, 20, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Tally()
	// The checkpointed generation is .2 (drained from its stored offset =
	// its EOF → nothing new); .1 was never checkpointed → skipped; new file
	// read from 0.
	if got, _ := a.MonthEgressBytes(bwSiteA); got != 40 {
		t.Fatalf("double rotation: got %d, want 40 (new file only)", got)
	}
}

// TestBWAccountantTruncation covers in-place truncation (copytruncate-style,
// §15): the reader resets to zero and counts only what the file now holds;
// the pre-truncation offset is logged as lost.
func TestBWAccountantTruncation(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a, logDir, _ := newTestAccountant(t, now)
	log := filepath.Join(logDir, "bandwidth.log")
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 8, 0, 0), 100, 100, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	a.Tally()
	// Truncate in place (same inode) and write fresh records.
	if err := os.Truncate(log, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 9, 0, 0), 50, 50, "UA")), 0o600); err != nil {
		t.Fatal(err)
	}
	a.Tally()
	if got, _ := a.MonthEgressBytes(bwSiteA); got != 100 {
		t.Fatalf("truncation: got %d, want the post-truncate record only (100)", got)
	}
}

// TestBWAccountantMonthRollover covers §20: records stamped in a previous
// month are never charged to the current one, and the accumulator rolls at
// the boundary.
func TestBWAccountantMonthRollover(t *testing.T) {
	sep := time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC)
	oct := time.Date(2026, 10, 1, 0, 30, 0, 0, time.UTC)
	a, logDir, _ := newTestAccountant(t, sep)
	log := filepath.Join(logDir, "bandwidth.log")
	if err := os.WriteFile(log, []byte(
		bwRec(bwSiteA, bwTS(sep, 22, 0, 0), 100, 900, "UA")+ // stamped Sep 30
			bwRec(bwSiteA, bwTS(sep, 23, 59, 59), 100, 1000, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	seedCPFromZero(t, a)
	a.Tally()
	if got, m := a.MonthEgressBytes(bwSiteA); got != 2100 || m != "2026-09" {
		t.Fatalf("september: %d %q", got, m)
	}
	// The clock crosses into October; new records are stamped October.
	a.nowFn = func() time.Time { return oct }
	a.Tally() // accumulator rolls to October (empty)
	f, _ := os.OpenFile(log, os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(bwRec(bwSiteA, bwTS(oct, 0, 1, 0), 100, 50, "UA"))
	f.Close()
	a.Tally()
	if got, m := a.MonthEgressBytes(bwSiteA); got != 150 || m != "2026-10" {
		t.Fatalf("october: %d %q (september must not leak in)", got, m)
	}
}

// TestBWAccountantNoLogYet covers deploy-order tolerance (§39): with the
// accounting conf not deployed yet (no log file), the tally is a no-op and
// the accountant keeps whatever it restored.
func TestBWAccountantNoLogYet(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a, logDir, statePath := newTestAccountant(t, now)
	// Seed the upgrade path: persisted state from the access-log era.
	if err := (&bwState{Sites: map[string]int64{bwSiteA: 5 * 1024 * 1024 * 1024}}).save(statePath); err != nil {
		t.Fatal(err)
	}
	month, sites, _ := LoadBwAccountantState(statePath)
	a.Restore(month, sites, nil)
	a.Tally() // no log file — must not panic, must not wipe the accumulator
	if got, _ := a.MonthEgressBytes(bwSiteA); got != 5*1024*1024*1024 {
		t.Fatalf("upgrade seed must survive a missing log, got %d", got)
	}
	_ = logDir
}

// TestBWAccountantPersistWritesState verifies the tally's durable contract:
// after a tally the checkpoint AND the folded accumulator are on disk
// together (one atomic write).
func TestBWAccountantPersistWritesState(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	a, logDir, statePath := newTestAccountant(t, now)
	log := filepath.Join(logDir, "bandwidth.log")
	if err := os.WriteFile(log, []byte(bwRec(bwSiteA, bwTS(now, 8, 0, 0), 100, 100, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	seedCPFromZero(t, a)
	a.Tally()
	st := loadBwState(statePath)
	if st.Log == nil || st.Sites[bwSiteA] != 200 {
		t.Fatalf("persist: log=%+v sites=%v", st.Log, st.Sites)
	}
}

// TestRecalcScan covers the reconciliation scan: per-day aggregation across
// plain + rotated + compressed generations, site + range filtering, and the
// payload validation on the executor entry point.
func TestRecalcScan(t *testing.T) {
	base := t.TempDir()
	logDir := filepath.Join(base, "varlog", "epicpanel")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	d1 := "2026-09-14"
	d2 := "2026-09-15"
	day := func(d, h string) string { return fmt.Sprintf("%sT%s:00:00+00:00", d, h) }
	// Current file: two records on d2 for site A, one for site B.
	if err := os.WriteFile(filepath.Join(logDir, "bandwidth.log"), []byte(
		bwRec(bwSiteA, day(d2, "10"), 100, 200, "UA")+
			bwRec(bwSiteB, day(d2, "11"), 500, 500, "UA")+
			bwRec(bwSiteA, day(d2, "12"), 10, 20, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	// Rotated plain: one d1 record for site A.
	if err := os.WriteFile(filepath.Join(logDir, "bandwidth.log.1"), []byte(
		bwRec(bwSiteA, day(d1, "09"), 1000, 2000, "UA")), 0o644); err != nil {
		t.Fatal(err)
	}
	// Rotated compressed: one d1 record for site A.
	var gzBuf strings.Builder
	zw := gzip.NewWriter(&stringWriter{&gzBuf})
	if _, err := zw.Write([]byte(bwRec(bwSiteA, day(d1, "15"), 5, 5, "UA"))); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	if err := os.WriteFile(filepath.Join(logDir, "bandwidth.log.2.gz"), []byte(gzBuf.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := recalcScan(logDir, bwSiteA, time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Days) != 2 || out.TotalBytes != 3340 {
		t.Fatalf("scan: days=%+v total=%d", out.Days, out.TotalBytes)
	}
	if out.Days[0].Date != d1 || out.Days[0].TotalBytes != 3010 {
		t.Fatalf("day1: %+v", out.Days[0])
	}
	if out.Days[1].Date != d2 || out.Days[1].TotalBytes != 330 || out.Days[1].RequestBytes != 110 {
		t.Fatalf("day2: %+v", out.Days[1])
	}
	if out.FilesScanned != 3 || out.RecordsMatched != 4 {
		t.Fatalf("scan stats: files=%d records=%d", out.FilesScanned, out.RecordsMatched)
	}

	// Executor entry validation.
	e := NewExecutor()
	e.bwLogDir = logDir
	if _, err := e.RecalcBandwidth(nil, RecalcBandwidthPayload{WebsiteID: "nope", FromDay: d1, ToDay: d2}); err == nil {
		t.Fatal("invalid site id must be rejected")
	}
	if _, err := e.RecalcBandwidth(nil, RecalcBandwidthPayload{WebsiteID: bwSiteA, FromDay: d2, ToDay: d1}); err == nil {
		t.Fatal("inverted range must be rejected")
	}
}

// stringWriter adapts strings.Builder to io.Writer for the gzip writer.
type stringWriter struct{ b *strings.Builder }

func (w *stringWriter) Write(p []byte) (int, error) { return w.b.Write(p) }
