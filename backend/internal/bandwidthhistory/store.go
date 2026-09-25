// Package bandwidthhistory persists per-site bandwidth usage into hourly
// and daily buckets. Source data = completed per-site access-log windows
// (60s, agent TrafficSampler) riding the metrics stream — response
// (egress-dominated) bytes attributed per virtual host, which the nftables
// per-uid chains cannot see (reverse-proxied responses leave via the
// web-server user). The authoritative MONTHLY quota number remains the
// nft + access-log composition reported by enforce_limits into
// workload_resource_usage; this store adds the time dimension for graphs.
// Hourly rows are pruned after 90 days; daily rows are kept indefinitely.
package bandwidthhistory

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// hourlyRetention bounds the fine-grained bucket table.
const hourlyRetention = 90 * 24 * time.Hour

// pruneInterval throttles the retention sweep ( piggybacks on Ingest; no
// dedicated loop needed for a write-light table ).
const pruneInterval = 6 * time.Hour

type Store struct {
	Pool *pgxpool.Pool
	now  func() time.Time

	pruneMu   sync.Mutex
	lastPrune time.Time
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool, now: time.Now}
}

// sampleRow is one site-hour fold of completed windows.
type sampleRow struct {
	WebsiteID string
	Hour      time.Time
	TxBytes   int64
	Requests  int64
}

// groupWindows folds completed windows into per-hour bucket rows keyed by
// site. SiteTraffic carries no timestamp, so windows are attributed to the
// hour of their receive time — the agent emits a completed window
// immediately, so the worst-case error is one window crossing an hour
// boundary.
func groupWindows(frames []agentproto.SiteTraffic, recv time.Time) []sampleRow {
	hour := recv.Truncate(time.Hour)
	idx := map[string]int{}
	rows := make([]sampleRow, 0, len(frames))
	for _, f := range frames {
		if f.WebsiteID == "" {
			continue
		}
		i, ok := idx[f.WebsiteID]
		if !ok {
			i = len(rows)
			idx[f.WebsiteID] = i
			rows = append(rows, sampleRow{WebsiteID: f.WebsiteID, Hour: hour})
		}
		rows[i].TxBytes += f.Bytes
		rows[i].Requests += f.Requests
	}
	return rows
}

// Ingest folds windows into the hourly buckets (additive: several 60s
// windows land in the same hour) and rolls the same deltas into the daily
// buckets. Best-effort per site: a site deleted mid-window (FK violation)
// skips only its own rows.
func (s *Store) Ingest(frames []agentproto.SiteTraffic) {
	if s.Pool == nil || len(frames) == 0 {
		return
	}
	rows := groupWindows(frames, s.now())
	if len(rows) == 0 {
		return
	}
	ctx := context.Background()
	b := &pgx.Batch{}
	for _, r := range rows {
		b.Queue(`INSERT INTO website_bandwidth_samples (website_id, hour_bucket, tx_bytes, requests)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (website_id, hour_bucket) DO UPDATE SET
				tx_bytes = website_bandwidth_samples.tx_bytes + EXCLUDED.tx_bytes,
				requests = website_bandwidth_samples.requests + EXCLUDED.requests`,
			r.WebsiteID, r.Hour, r.TxBytes, r.Requests)
		b.Queue(`INSERT INTO website_bandwidth_daily (website_id, day, tx_bytes, requests)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (website_id, day) DO UPDATE SET
				tx_bytes = website_bandwidth_daily.tx_bytes + EXCLUDED.tx_bytes,
				requests = website_bandwidth_daily.requests + EXCLUDED.requests`,
			r.WebsiteID, r.Hour.UTC(), r.TxBytes, r.Requests)
	}
	br := s.Pool.SendBatch(ctx, b)
	for i := 0; i < len(rows)*2; i++ {
		if _, err := br.Exec(); err != nil {
			slog.Debug("bandwidth history upsert skipped", "row", i, "err", err)
		}
	}
	if err := br.Close(); err != nil {
		slog.Debug("bandwidth history batch closed with error", "err", err)
	}
	s.maybePrune()
}

// maybePrune deletes hourly rows past retention, at most once per
// pruneInterval. Daily rows are kept indefinitely.
func (s *Store) maybePrune() {
	s.pruneMu.Lock()
	defer s.pruneMu.Unlock()
	if s.now().Sub(s.lastPrune) < pruneInterval {
		return
	}
	s.lastPrune = s.now()
	ctx := context.Background()
	tag, err := s.Pool.Exec(ctx,
		`DELETE FROM website_bandwidth_samples WHERE hour_bucket < $1`,
		s.now().Add(-hourlyRetention))
	if err != nil {
		slog.Debug("bandwidth history prune failed", "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("bandwidth history pruned", "rows", tag.RowsAffected())
	}
}
