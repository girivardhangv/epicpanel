package agent

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ============================================================================
// Bandwidth recalculation / reconciliation — the operational recovery arm.
//
// The live accounting pipeline (bwtail.go) is incremental and crash-safe;
// recalc is the explicit, admin-invoked "what does the RAW platform log
// say?" path: re-scan every bandwidth.log generation (current, rotated
// plain, rotated compressed) for one site + day range and produce per-day
// request/response totals. The control plane uses the result as a
// reconciliation report (comparison against the DB) or, with apply=true, as
// a repair input that can only RAISE the monthly high-water — never lower
// usage (monotonic billing). Customers cannot invoke it (admin-gated route,
// and the job runs agent-side on platform-owned files).
// ============================================================================

// RecalcBandwidthPayload matches the control plane's recalc enqueue body.
// FromDay/ToDay are inclusive UTC days (YYYY-MM-DD).
type RecalcBandwidthPayload struct {
	WebsiteID string `json:"website_id"`
	FromDay   string `json:"from_day"`
	ToDay     string `json:"to_day"`
}

// RecalcDay is one UTC day's raw-log aggregate for the site.
type RecalcDay struct {
	Date          string `json:"date"`
	RequestBytes  int64  `json:"request_bytes"`
	ResponseBytes int64  `json:"response_bytes"`
	TotalBytes    int64  `json:"total_bytes"`
}

// RecalcBandwidthOutcome is the job result: the raw accounting-log truth
// for the range (per-day + total), enough for a byte-level reconciliation.
type RecalcBandwidthOutcome struct {
	WebsiteID      string      `json:"website_id"`
	FromDay        string      `json:"from_day"`
	ToDay          string      `json:"to_day"`
	Days           []RecalcDay `json:"days"`
	TotalBytes     int64       `json:"total_bytes"`
	FilesScanned   int         `json:"files_scanned"`
	RecordsMatched int64       `json:"records_matched"`
}

// recalcRangeCapDays bounds one recalc job (mirrors the history API cap).
const recalcRangeCapDays = 400

// RecalcBandwidth re-scans the platform accounting logs for one site and
// day range. Bounded memory: every file is streamed line-by-line (gzip
// decompressed on the fly); nothing is loaded whole.
func (e *Executor) RecalcBandwidth(ctx context.Context, p RecalcBandwidthPayload) (*RecalcBandwidthOutcome, error) {
	if _, err := uuid.Parse(p.WebsiteID); err != nil {
		return nil, fmt.Errorf("invalid website id")
	}
	from, err := time.Parse("2006-01-02", p.FromDay)
	if err != nil {
		return nil, fmt.Errorf("invalid from_day (want YYYY-MM-DD)")
	}
	to, err := time.Parse("2006-01-02", p.ToDay)
	if err != nil {
		return nil, fmt.Errorf("invalid to_day (want YYYY-MM-DD)")
	}
	if to.Before(from) {
		return nil, fmt.Errorf("to_day before from_day")
	}
	if to.Sub(from) > recalcRangeCapDays*24*time.Hour {
		return nil, fmt.Errorf("range exceeds %d days", recalcRangeCapDays)
	}
	logDir := e.bwLogDir
	if logDir == "" {
		logDir = bwLogDir
	}
	out, err := recalcScan(logDir, p.WebsiteID, from, to)
	if err != nil {
		return nil, err
	}
	slog.Info("bandwidth recalc complete", "website", p.WebsiteID,
		"from", p.FromDay, "to", p.ToDay, "total_bytes", out.TotalBytes,
		"files", out.FilesScanned, "records", out.RecordsMatched)
	return out, nil
}

// recalcScan is the pure-ish scan core (log dir injectable for tests).
func recalcScan(logDir, siteID string, from, to time.Time) (*RecalcBandwidthOutcome, error) {
	out := &RecalcBandwidthOutcome{WebsiteID: siteID, FromDay: from.Format("2006-01-02"), ToDay: to.Format("2006-01-02")}
	perDay := map[string]*RecalcDay{}
	matches, err := filepath.Glob(filepath.Join(logDir, "bandwidth.log*"))
	if err != nil {
		return nil, err
	}
	for _, path := range matches {
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() {
			continue
		}
		out.FilesScanned++
		f, err := os.Open(path) // #nosec G304 — glob over the platform log dir
		if err != nil {
			continue
		}
		var r io.Reader = f
		if strings.HasSuffix(path, ".gz") {
			gz, gzErr := gzip.NewReader(f)
			if gzErr != nil {
				f.Close()
				continue // unreadable generation: skip, don't fail the scan
			}
			defer gz.Close()
			r = gz
		}
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		for scanner.Scan() {
			site, ts, reqLen, bytesSent, ok := parseBWRecord(scanner.Text())
			if !ok || site != siteID {
				continue
			}
			day := ts.UTC().Format("2006-01-02")
			if day < out.FromDay || day > out.ToDay {
				continue
			}
			d := perDay[day]
			if d == nil {
				d = &RecalcDay{Date: day}
				perDay[day] = d
			}
			d.RequestBytes += reqLen
			d.ResponseBytes += bytesSent
			d.TotalBytes += reqLen + bytesSent
			out.RecordsMatched++
		}
		f.Close()
	}
	for _, d := range perDay {
		out.Days = append(out.Days, *d)
		out.TotalBytes += d.TotalBytes
	}
	// Chronological order for a stable, diffable report.
	for i := 1; i < len(out.Days); i++ {
		for j := i; j > 0 && out.Days[j].Date < out.Days[j-1].Date; j-- {
			out.Days[j], out.Days[j-1] = out.Days[j-1], out.Days[j]
		}
	}
	return out, nil
}
