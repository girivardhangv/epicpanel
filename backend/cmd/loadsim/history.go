package main

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// historyStats reports what the async historical writer actually persisted
// during the run window. The writer drains the latest live sample per server
// every 10s (scheduler DrainLiveSnapshot), so at interval < 10s the raw table
// receives a decimated feed by design — measured here, not guessed.

// measureHistory counts server_metrics rows written since runStart and the
// maximum insert lag (now - max(collected_at)).
func measureHistory(dbURL string, runStart time.Time) *historyStats {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		log.Printf("[loadsim] db accounting skipped: %v", err)
		return nil
	}
	cfg.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		log.Printf("[loadsim] db accounting skipped: %v", err)
		return nil
	}
	defer pool.Close()

	var rows int64
	var maxLag time.Duration
	if err := pool.QueryRow(ctx, `
		SELECT count(*),
		       COALESCE(max(now() - collected_at), interval '0')
		FROM server_metrics WHERE collected_at >= $1
	`, runStart).Scan(&rows, &maxLag); err != nil {
		log.Printf("[loadsim] db accounting failed: %v", err)
		return nil
	}
	// Drain window: the scheduler pulls live snapshots every 10s.
	const drainEvery = 10 * time.Second
	elapsed := time.Since(runStart)
	st := &historyStats{
		Rows:       rows,
		RowsPerSec: float64(rows) / elapsed.Seconds(),
		MaxLag:     durStr{maxLag},
	}
	st.Expected = int64(elapsed / drainEvery)
	st.Notes = append(st.Notes,
		"history writer drains the LATEST live sample per node every 10s; sub-10s cadences are decimated by design",
	)
	return st
}
