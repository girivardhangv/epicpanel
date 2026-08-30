// Telemetry ingestion: authenticate (via the standard agent bearer token
// middleware, enforced by the router), validate, bound and persist samples.
package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IngestService struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewIngestService(pool *pgxpool.Pool, log *slog.Logger) *IngestService {
	return &IngestService{pool: pool, log: log}
}

// IngestResult reports per-batch outcomes for the agent's retry logic.
type IngestResult struct {
	Accepted int `json:"accepted"`
	Duplicates int `json:"duplicates"`
	Rejected int `json:"rejected"`
}

// validateSample enforces per-sample bounds; failures are counted, not fatal
// for the whole batch (one malformed sample must not drop the others).
func validateSample(s *Sample) error {
	if s.Sequence < 0 {
		return errors.New("sequence must be >= 0")
	}
	if len(s.Disks) > MaxDisksPerSample {
		s.Disks = s.Disks[:MaxDisksPerSample]
	}
	if len(s.Network) > MaxInterfacesSample {
		s.Network = s.Network[:MaxInterfacesSample]
	}
	if len(s.Processes) > MaxProcessesPerSample {
		s.Processes = s.Processes[:MaxProcessesPerSample]
	}
	if len(s.Services) > MaxServicesPerSample {
		s.Services = s.Services[:MaxServicesPerSample]
	}
	for _, d := range s.Disks {
		if d.Mount == "" || len(d.Mount) > 128 {
			return errors.New("disk metric has an invalid mount")
		}
	}
	for _, n := range s.Network {
		if n.Interface == "" || len(n.Interface) > 64 {
			return errors.New("network metric has an invalid interface name")
		}
	}
	for _, p := range s.Processes {
		if len(p.Name) > 256 {
			return errors.New("process name too long")
		}
	}
	for _, svc := range s.Services {
		if svc.Name == "" || len(svc.Name) > 128 {
			return errors.New("service metric has an invalid name")
		}
	}
	return nil
}

// parseAgentTimestamp handles the agent clock: untrusted by definition.
// Returns nil when absent/invalid. Obviously-future timestamps are clamped
// to the receive time so charts never render the future.
func parseAgentTimestamp(raw string, received time.Time) *time.Time {
	if raw == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil || t.IsZero() {
		return nil
	}
	if t.After(received.Add(MaxAgentClockSkew)) {
		clamped := received
		return &clamped
	}
	utc := t.UTC()
	return &utc
}

// Ingest stores a bounded batch for an already-authenticated server.
// Duplicate sequence numbers are no-ops (safe retries); ordering relies on
// server_received_at, so slightly wrong agent clocks cannot corrupt history.
func (s *IngestService) Ingest(ctx context.Context, serverID string, batch TelemetryBatch) (*IngestResult, error) {
	if len(batch.Samples) == 0 {
		return nil, apierror.BadRequest("telemetry batch is empty")
	}
	if len(batch.Samples) > MaxSamplesPerBatch {
		return nil, apierror.BadRequest(fmt.Sprintf(
			"telemetry batch exceeds %d samples", MaxSamplesPerBatch))
	}

	res := &IngestResult{}
	received := time.Now().UTC()

	b := &pgx.Batch{}
	for i := range batch.Samples {
		smp := batch.Samples[i]
		if err := validateSample(&smp); err != nil {
			res.Rejected++
			s.log.Warn("telemetry sample rejected",
				"server", serverID, "sequence", smp.Sequence, "err", err)
			continue
		}
		agentTS := parseAgentTimestamp(smp.Timestamp, received)

		b.Queue(`INSERT INTO server_metric_samples (
			server_id, sequence, agent_timestamp, server_received_at, agent_version,
			cpu_usage_percent, cpu_user_percent, cpu_system_percent, cpu_idle_percent,
			load_1m, load_5m, load_15m,
			memory_total_bytes, memory_used_bytes, memory_available_bytes, memory_free_bytes,
			memory_usage_percent, swap_total_bytes, swap_used_bytes, swap_usage_percent,
			uptime_seconds, disks, network, processes, services, monitoring_errors)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26)
		ON CONFLICT (server_id, sequence) DO NOTHING`,
			serverID, smp.Sequence, tsOrNull(agentTS), received, smp.AgentVer,
			smp.CPUUsage, smp.CPUUser, smp.CPUSystem, smp.CPUIdle,
			smp.Load1, smp.Load5, smp.Load15,
			smp.MemoryTotalBytes, smp.MemoryUsedBytes, smp.MemoryAvailableBytes, smp.MemoryFreeBytes,
			smp.MemoryUsagePercent, smp.SwapTotalBytes, smp.SwapUsedBytes, smp.SwapUsagePercent,
			smp.UptimeSeconds, mustJSON(smp.Disks), mustJSON(smp.Network),
			mustJSON(smp.Processes), mustJSON(smp.Services), mustJSON(smp.Errors),
		)
	}

	if b.Len() > 0 {
		br := s.pool.SendBatch(ctx, b)
		defer br.Close()
		for range b.Len() {
			tag, err := br.Exec()
			if err != nil {
				// A single insert failure must not abort the batch.
				res.Rejected++
				s.log.Warn("telemetry insert failed", "server", serverID, "err", err)
				continue
			}
			if tag.RowsAffected() == 0 {
				res.Duplicates++
			} else {
				res.Accepted++
			}
		}
	}
	return res, nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func tsOrNull(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}

func mustJSON(v any) string {
	if v == nil {
		return "{}"
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
