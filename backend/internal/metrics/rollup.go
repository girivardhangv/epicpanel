package metrics

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const insertSQL = `
	INSERT INTO server_metrics
		(server_id, cpu_percent, memory_total_bytes, memory_used_bytes, disk_total_bytes, disk_used_bytes,
		 load1, load5, load15, swap_total_bytes, swap_used_bytes, network_rx_bps, network_tx_bps,
		 read_bps, write_bps, tcp_established, tcp_total, processes, inodes_total, inodes_used, degraded, collected_at)
	VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`

// pgxBatch accumulates one flush's INSERTs into a single round trip.
type pgxBatch struct {
	batch pgx.Batch
}

func (b *pgxBatch) queue(row HistoryRow) {
	n := row.Sample.Node
	var diskTotal, diskUsed, inodesTotal, inodesUsed int64
	if len(n.Disks) > 0 {
		for _, d := range n.Disks {
			if d.Mount == "/" {
				diskTotal, diskUsed = d.TotalBytes, d.UsedBytes
				inodesTotal, inodesUsed = d.InodesTotal, d.InodesUsed
				break
			}
		}
		if diskTotal == 0 {
			diskTotal, diskUsed = n.Disks[0].TotalBytes, n.Disks[0].UsedBytes
			inodesTotal, inodesUsed = n.Disks[0].InodesTotal, n.Disks[0].InodesUsed
		}
	}
	b.batch.Queue(insertSQL,
		row.ServerID, n.CPUPercent, n.MemoryTotal, n.MemoryUsed, diskTotal, diskUsed,
		n.Load1, n.Load5, n.Load15, n.SwapTotal, n.SwapUsed, n.Net.RxBPS, n.Net.TxBPS,
		n.IO.ReadBPS, n.IO.WriteBPS, n.TCPEstablished, n.TCPTotal, n.Processes,
		inodesTotal, inodesUsed, n.Degraded, row.At,
	)
}

// Rollup aggregates raw samples into 5-minute buckets (hourly job): graphs
// beyond the raw retention read this table instead of losing resolution.
func Rollup(ctx context.Context, pool *pgxpool.Pool) error {
	tag, err := pool.Exec(ctx, `
		INSERT INTO server_metrics_rollup_5m
			(server_id, bucket, cpu_avg, cpu_max, mem_used_avg, mem_total_max, load1_avg, rx_avg, tx_avg)
		SELECT server_id,
			date_trunc('hour', collected_at) + interval '5 min' * floor(date_part('minute', collected_at) / 5),
			avg(cpu_percent), max(cpu_percent), avg(memory_used_bytes), max(memory_total_bytes),
			avg(load1), avg(network_rx_bps), avg(network_tx_bps)
		FROM server_metrics
		WHERE collected_at >= now() - interval '2 hours'
		GROUP BY server_id, 2
		ON CONFLICT (server_id, bucket) DO UPDATE SET
			cpu_avg = EXCLUDED.cpu_avg, cpu_max = EXCLUDED.cpu_max,
			mem_used_avg = EXCLUDED.mem_used_avg, mem_total_max = EXCLUDED.mem_total_max,
			load1_avg = EXCLUDED.load1_avg, rx_avg = EXCLUDED.rx_avg, tx_avg = EXCLUDED.tx_avg
	`)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		slog.Debug("metrics rollup upserted", "buckets", tag.RowsAffected())
	}
	return nil
}

// StartRollupJob runs Rollup hourly until ctx is done.
func StartRollupJob(ctx context.Context, pool *pgxpool.Pool) {
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := Rollup(ctx, pool); err != nil {
					slog.Warn("metrics rollup failed", "err", err)
				}
			}
		}
	}()
}
