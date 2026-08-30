// Background maintenance for monitoring data: builds hourly/daily
// aggregates from raw samples and enforces retention. Runs as a single
// in-process worker behind an interface, structured so it can later move to
// a queue/worker deployment without touching the monitoring domain.
package monitoring

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Aggregator interface keeps the worker swappable (§33/§42: no global
// in-memory state as the only source of truth; everything persists).
type Aggregator interface {
	RunOnce(ctx context.Context) error
}

type MaintenanceWorker struct {
	pool *pgxpool.Pool
	log  *slog.Logger

	retentionRawDays    int
	retentionHourlyDays int
	retentionDailyDays  int
}

func NewMaintenanceWorker(pool *pgxpool.Pool, log *slog.Logger,
	rawDays, hourlyDays, dailyDays int) *MaintenanceWorker {
	return &MaintenanceWorker{
		pool: pool, log: log,
		retentionRawDays:    rawDays,
		retentionHourlyDays: hourlyDays,
		retentionDailyDays:  dailyDays,
	}
}

// Start launches the hourly maintenance loop. One goroutine total; a mutex
// guarantees a slow run never overlaps the next tick (no pile-up on restart).
func (w *MaintenanceWorker) Start(ctx context.Context) {
	go func() {
		// Backfill buckets missed while the panel was down, then tick hourly.
		if err := w.RunOnce(context.Background()); err != nil {
			w.log.Warn("monitoring maintenance failed", "err", err)
		}
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		var running sync.Mutex
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if running.TryLock() {
					go func() {
						defer running.Unlock()
						if err := w.RunOnce(ctx); err != nil {
							w.log.Warn("monitoring maintenance failed", "err", err)
						}
					}()
				}
			}
		}
	}()
}

// RunOnce aggregates pending buckets and prunes expired data. Idempotent
// upserts make restarts and overlaps safe.
func (w *MaintenanceWorker) RunOnce(ctx context.Context) error {
	if err := w.aggregateHourly(ctx); err != nil {
		return err
	}
	if err := w.aggregateDaily(ctx); err != nil {
		return err
	}
	return w.prune(ctx)
}

type numTuple struct {
	Min, Avg, Max *float32
}

// aggregateHourly folds complete raw hours into server_metric_hourly.
func (w *MaintenanceWorker) aggregateHourly(ctx context.Context) error {
	rows, err := w.pool.Query(ctx, `
		SELECT server_id,
		       to_timestamp(floor(extract(epoch from server_received_at)/3600)*3600) AS bucket,
		       count(*) AS samples,
		       min(cpu_usage_percent) FILTER (WHERE cpu_usage_percent IS NOT NULL),
		       avg(cpu_usage_percent) FILTER (WHERE cpu_usage_percent IS NOT NULL),
		       max(cpu_usage_percent) FILTER (WHERE cpu_usage_percent IS NOT NULL),
		       min(memory_usage_percent) FILTER (WHERE memory_usage_percent IS NOT NULL),
		       avg(memory_usage_percent) FILTER (WHERE memory_usage_percent IS NOT NULL),
		       max(memory_usage_percent) FILTER (WHERE memory_usage_percent IS NOT NULL),
		       max(load_5m),
		       max(uptime_seconds),
		       max(disk_max::text) AS disk_max
		FROM (
			SELECT server_id, server_received_at, cpu_usage_percent, memory_usage_percent,
			       load_5m, uptime_seconds,
			       (SELECT jsonb_object_agg(d->>'mount', (d->>'usage_percent')::text::float)
			          FROM jsonb_array_elements(disks) d) AS disk_max
			FROM server_metric_samples
			WHERE server_received_at >= now() - interval '25 hours'
			  AND server_received_at < to_timestamp(floor(extract(epoch from now())/3600)*3600)
		) s
		GROUP BY server_id, bucket`)
	if err != nil {
		return err
	}
	type aggRow struct {
		ServerID  string
		Bucket    time.Time
		Samples   int64
		CPU       numTuple
		Mem       numTuple
		LoadMax   *float32
		UptimeMax *int64
		DiskMax   string
	}
	parsed := []aggRow{}
	for rows.Next() {
		var r aggRow
		if err := rows.Scan(&r.ServerID, &r.Bucket, &r.Samples,
			&r.CPU.Min, &r.CPU.Avg, &r.CPU.Max, &r.Mem.Min, &r.Mem.Avg, &r.Mem.Max,
			&r.LoadMax, &r.UptimeMax, &r.DiskMax); err != nil {
			rows.Close()
			return err
		}
		parsed = append(parsed, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, r := range parsed {
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO server_metric_hourly
				(server_id, bucket, samples, cpu_min, cpu_avg, cpu_max,
				 mem_min, mem_avg, mem_max, load_max, disk_max, uptime_max, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb,$12, now())
			ON CONFLICT (server_id, bucket) DO UPDATE SET
				samples = EXCLUDED.samples,
				cpu_min = EXCLUDED.cpu_min, cpu_avg = EXCLUDED.cpu_avg, cpu_max = EXCLUDED.cpu_max,
				mem_min = EXCLUDED.mem_min, mem_avg = EXCLUDED.mem_avg, mem_max = EXCLUDED.mem_max,
				load_max = EXCLUDED.load_max, disk_max = EXCLUDED.disk_max,
				uptime_max = EXCLUDED.uptime_max, updated_at = now()`,
			r.ServerID, r.Bucket, r.Samples,
			r.CPU.Min, r.CPU.Avg, r.CPU.Max, r.Mem.Min, r.Mem.Avg, r.Mem.Max,
			r.LoadMax, r.DiskMax, r.UptimeMax); err != nil {
			return err
		}
	}
	return nil
}

// aggregateDaily folds complete hourly aggregates into server_metric_daily.
// Disk maxima are merged per mount across the day's hours in a second
// grouped query and joined in Go (portable across PostgreSQL versions).
func (w *MaintenanceWorker) aggregateDaily(ctx context.Context) error {
	rows, err := w.pool.Query(ctx, `
		SELECT server_id,
		       (bucket AT TIME ZONE 'UTC')::date AS day,
		       sum(samples)::bigint,
		       min(cpu_min), (sum(cpu_avg*samples)/NULLIF(sum(samples),0))::real, max(cpu_max),
		       min(mem_min), (sum(mem_avg*samples)/NULLIF(sum(samples),0))::real, max(mem_max),
		       max(uptime_max)
		FROM server_metric_hourly
		WHERE bucket >= now() - interval '400 days'
		GROUP BY server_id, day`)
	if err != nil {
		return err
	}
	type dailyRow struct {
		ServerID  string
		Day       time.Time
		Samples   int64
		CPU       numTuple
		Mem       numTuple
		UptimeMax *int64
	}
	parsed := []dailyRow{}
	for rows.Next() {
		var r dailyRow
		if err := rows.Scan(&r.ServerID, &r.Day, &r.Samples,
			&r.CPU.Min, &r.CPU.Avg, &r.CPU.Max, &r.Mem.Min, &r.Mem.Avg, &r.Mem.Max,
			&r.UptimeMax); err != nil {
			rows.Close()
			return err
		}
		parsed = append(parsed, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	diskMaxByDay, err := w.dailyDiskMaxima(ctx)
	if err != nil {
		return err
	}

	for _, r := range parsed {
		diskMaxJSON := "{}"
		if dm, ok := diskMaxByDay[r.ServerID][r.Day.Format("2006-01-02")]; ok {
			raw, _ := json.Marshal(dm)
			diskMaxJSON = string(raw)
		}
		if _, err := w.pool.Exec(ctx, `
			INSERT INTO server_metric_daily
				(server_id, bucket, samples, cpu_min, cpu_avg, cpu_max,
				 mem_min, mem_avg, mem_max, disk_max, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb, now())
			ON CONFLICT (server_id, bucket) DO UPDATE SET
				samples = EXCLUDED.samples,
				cpu_min = EXCLUDED.cpu_min, cpu_avg = EXCLUDED.cpu_avg, cpu_max = EXCLUDED.cpu_max,
				mem_min = EXCLUDED.mem_min, mem_avg = EXCLUDED.mem_avg, mem_max = EXCLUDED.mem_max,
				disk_max = EXCLUDED.disk_max, updated_at = now()`,
			r.ServerID, r.Day, r.Samples,
			r.CPU.Min, r.CPU.Avg, r.CPU.Max, r.Mem.Min, r.Mem.Avg, r.Mem.Max,
			diskMaxJSON); err != nil {
			return err
		}
	}
	return nil
}

// dailyDiskMaxima returns per (server, day) the maxima of every mounted
// filesystem across that day's hourly aggregates.
func (w *MaintenanceWorker) dailyDiskMaxima(ctx context.Context) (map[string]map[string]map[string]float64, error) {
	rows, err := w.pool.Query(ctx, `
		SELECT server_id, day, jsonb_object_agg(mount, val) FROM (
			SELECT server_id, (bucket AT TIME ZONE 'UTC')::date AS day,
			       kv.key AS mount, max((kv.value)::text::float) AS val
			FROM server_metric_hourly, jsonb_each(disk_max) kv
			WHERE bucket >= now() - interval '400 days' AND disk_max <> '{}'::jsonb
			GROUP BY server_id, day, kv.key
		) g
		GROUP BY server_id, day`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]map[string]float64{}
	for rows.Next() {
		var serverID, dayStr, raw string
		if err := rows.Scan(&serverID, &dayStr, &raw); err != nil {
			return nil, err
		}
		var dm map[string]float64
		if err := json.Unmarshal([]byte(raw), &dm); err != nil {
			continue // never trust unexpected JSON shapes
		}
		if out[serverID] == nil {
			out[serverID] = map[string]map[string]float64{}
		}
		out[serverID][dayStr] = dm
	}
	return out, rows.Err()
}

// prune deletes expired raw samples and aggregates according to retention.
func (w *MaintenanceWorker) prune(ctx context.Context) error {
	for _, job := range []struct {
		table string
		days  int
		column string
	}{
		{"server_metric_samples", w.retentionRawDays, "server_received_at"},
		{"server_metric_hourly", w.retentionHourlyDays, "bucket"},
		{"server_metric_daily", w.retentionDailyDays, "bucket"},
	} {
		q := "DELETE FROM " + job.table + " WHERE " + job.column +
			" < now() - ($1 || ' days')::interval"
		if _, err := w.pool.Exec(ctx, q, strconv.Itoa(job.days)); err != nil {
			return err
		}
	}
	return nil
}
