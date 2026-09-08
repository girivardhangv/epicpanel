package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
)

// HistoryRow is one node sample queued for asynchronous persistence.
type HistoryRow struct {
	ServerID uuid.UUID
	Sample   *agentproto.Sample
	At       time.Time
}

// Writer persists historical samples ASYNCHRONOUSLY — never inside an API
// request or the live ingest path (master doc: do not perform expensive
// aggregation synchronously). Ingest appends to a bounded channel; this
// loop batches rows and flushes to Postgres.
type Writer struct {
	Pool    *pgxpool.Pool
	Live    *LiveStore
	queue   chan HistoryRow
	batch   int
	every   time.Duration
	dropped int64
}

const (
	// 1s × 8s flush ≈ up to 8 rows/server/flush; queue absorbs 60s of a
	// 100-node fleet before dropping (dropped counter surfaces it).
	queueCap     = 2000
	flushEvery   = 8 * time.Second
	flushBatch   = 500
	retentionRaw = 24 * time.Hour
)

func NewWriter(pool *pgxpool.Pool, live *LiveStore) *Writer {
	return &Writer{Pool: pool, Live: live, queue: make(chan HistoryRow, queueCap), batch: flushBatch, every: flushEvery}
}

// Enqueue queues one sample for persistence. Non-blocking: when the queue
// is full the sample is DROPPED and counted — the historical graph is
// allowed to lose points under overload, the live path is not.
func (w *Writer) Enqueue(row HistoryRow) {
	select {
	case w.queue <- row:
	default:
		w.dropped++
		if w.dropped%500 == 1 {
			slog.Warn("metrics history queue full; dropping samples", "dropped_total", w.dropped)
		}
	}
}

// Run drains the queue until ctx is done.
func (w *Writer) Run(ctx context.Context) {
	ticker := time.NewTicker(w.every)
	defer ticker.Stop()
	buf := make([]HistoryRow, 0, w.batch)
	for {
		select {
		case <-ctx.Done():
			w.flush(ctx, buf)
			return
		case row := <-w.queue:
			buf = append(buf, row)
			if len(buf) >= w.batch {
				buf = w.flush(ctx, buf)
			}
		case <-ticker.C:
			buf = w.flush(ctx, buf)
			w.prune(ctx)
		}
	}
}

// DrainLiveSnapshot periodically enqueues current live samples (used when
// the stream is down but legacy heartbeats still update the live store).
func (w *Writer) DrainLiveSnapshot(ctx context.Context) {
	for _, row := range w.Live.HistoryBatches() {
		w.Enqueue(row)
	}
}

// Flush persists everything currently queued (used by tests and shutdown).
func (w *Writer) Flush(ctx context.Context) {
	drain := make([]HistoryRow, 0, w.batch)
loop:
	for {
		select {
		case row := <-w.queue:
			drain = append(drain, row)
		default:
			break loop
		}
	}
	w.flush(ctx, drain)
}

func (w *Writer) flush(ctx context.Context, buf []HistoryRow) []HistoryRow {
	if len(buf) == 0 {
		return buf[:0]
	}
	batch := &pgxBatch{}
	for _, row := range buf {
		batch.queue(row)
	}
	if err := w.Pool.SendBatch(ctx, &batch.batch).Close(); err != nil {
		slog.Warn("metrics history flush failed", "rows", len(buf), "err", err)
		return buf // keep rows; retried next cycle
	}
	return buf[:0]
}

// prune bounds server_metrics growth: raw 10s-resolution rows older than
// 24h are deleted; the 5-minute rollup table keeps 30 days (audit §★5).
func (w *Writer) prune(ctx context.Context) {
	_, err := w.Pool.Exec(ctx, `DELETE FROM server_metrics WHERE collected_at < now() - $1::interval`, retentionRaw)
	if err != nil {
		slog.Warn("server_metrics prune failed", "err", err)
	}
	_, err = w.Pool.Exec(ctx, `DELETE FROM server_metrics_rollup_5m WHERE bucket < now() - interval '30 days'`)
	if err != nil {
		slog.Warn("metrics rollup prune failed", "err", err)
	}
}
