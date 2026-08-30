// Query side of monitoring: current samples, smoothed health states,
// bounded historical series and the fleet overview. Historical queries are
// always bounded — the largest allowed range is 30 days and every response
// is downsampled to a sane number of points.
package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type QueryService struct {
	pool     *pgxpool.Pool
	settings *settings.Service
}

func NewQueryService(pool *pgxpool.Pool, settings *settings.Service) *QueryService {
	return &QueryService{pool: pool, settings: settings}
}

// RawSample is a stored sample as returned to the API.
type RawSample struct {
	Sequence   int64     `json:"sequence"`
	AgentTS    *time.Time `json:"agent_timestamp"`
	ReceivedAt time.Time  `json:"server_received_at"`
	Sample
}

const sampleCols = `
	sequence, agent_timestamp, server_received_at, agent_version,
	cpu_usage_percent, cpu_user_percent, cpu_system_percent, cpu_idle_percent,
	load_1m, load_5m, load_15m,
	memory_total_bytes, memory_used_bytes, memory_available_bytes, memory_free_bytes,
	memory_usage_percent, swap_total_bytes, swap_used_bytes, swap_usage_percent,
	uptime_seconds, disks::text, network::text, processes::text, services::text, monitoring_errors::text`

func scanSample(row pgx.Row) (*RawSample, error) {
	var (
		rs    RawSample
		disks, network, procs, services, errs string
	)
	err := row.Scan(&rs.Sequence, &rs.AgentTS, &rs.ReceivedAt, &rs.AgentVer,
		&rs.CPUUsage, &rs.CPUUser, &rs.CPUSystem, &rs.CPUIdle,
		&rs.Load1, &rs.Load5, &rs.Load15,
		&rs.MemoryTotalBytes, &rs.MemoryUsedBytes, &rs.MemoryAvailableBytes, &rs.MemoryFreeBytes,
		&rs.MemoryUsagePercent, &rs.SwapTotalBytes, &rs.SwapUsedBytes, &rs.SwapUsagePercent,
		&rs.UptimeSeconds, &disks, &network, &procs, &services, &errs)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(disks), &rs.Disks)
	_ = json.Unmarshal([]byte(network), &rs.Network)
	_ = json.Unmarshal([]byte(procs), &rs.Processes)
	_ = json.Unmarshal([]byte(services), &rs.Services)
	_ = json.Unmarshal([]byte(errs), &rs.Errors)
	return &rs, nil
}

// Current returns the newest sample plus the smoothed health state. Health
// averages the last `basis` samples (default 4 ≈ 1 minute) so a single noisy
// sample never flips a server's state.
func (q *QueryService) Current(ctx context.Context, serverID string) (*CurrentView, error) {
	latest, err := q.latestSample(ctx, serverID)
	if err != nil {
		return nil, err
	}
	recent, err := q.recentSamples(ctx, serverID, healthBasis)
	if err != nil {
		return nil, err
	}
	view := &CurrentView{
		ServerID:   serverID,
		Latest:     latest,
		HasData:    latest != nil,
		Health:     q.healthFromSamples(recent, q.thresholds(ctx)),
		Thresholds: q.thresholds(ctx),
	}
	if latest != nil {
		view.Monitoring = capabilityMatrix(latest)
	}
	return view, nil
}

const healthBasis = 4

type CurrentView struct {
	ServerID    string            `json:"server_id"`
	HasData     bool              `json:"has_data"`
	Latest      *RawSample        `json:"latest,omitempty"`
	Health      HealthState       `json:"health"`
	Thresholds  Thresholds        `json:"thresholds"`
	Monitoring  map[string]any    `json:"monitoring_capabilities,omitempty"`
}

// thresholds reads operator-tunable thresholds from settings.
func (q *QueryService) thresholds(ctx context.Context) Thresholds {
	t := DefaultThresholds()
	t.CPUWarn = float64(q.settings.Int(ctx, "monitoring.threshold_cpu_warn", int(t.CPUWarn), 1, 100))
	t.CPUCrit = float64(q.settings.Int(ctx, "monitoring.threshold_cpu_crit", int(t.CPUCrit), 1, 100))
	t.MemWarn = float64(q.settings.Int(ctx, "monitoring.threshold_memory_warn", int(t.MemWarn), 1, 100))
	t.MemCrit = float64(q.settings.Int(ctx, "monitoring.threshold_memory_crit", int(t.MemCrit), 1, 100))
	t.DiskWarn = float64(q.settings.Int(ctx, "monitoring.threshold_disk_warn", int(t.DiskWarn), 1, 100))
	t.DiskCrit = float64(q.settings.Int(ctx, "monitoring.threshold_disk_crit", int(t.DiskCrit), 1, 100))
	t.OfflineAfterSeconds = q.settings.Int(ctx, "monitoring.offline_after_seconds", t.OfflineAfterSeconds, 30, 86400)
	return t
}

func (q *QueryService) latestSample(ctx context.Context, serverID string) (*RawSample, error) {
	rs, err := scanSample(q.pool.QueryRow(ctx,
		`SELECT `+sampleCols+` FROM server_metric_samples
		 WHERE server_id = $1 ORDER BY server_received_at DESC, sequence DESC LIMIT 1`, serverID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, apierror.From(err)
	}
	return rs, nil
}

func (q *QueryService) recentSamples(ctx context.Context, serverID string, n int) ([]*RawSample, error) {
	rows, err := q.pool.Query(ctx,
		`SELECT `+sampleCols+` FROM server_metric_samples
		 WHERE server_id = $1 ORDER BY server_received_at DESC, sequence DESC LIMIT $2`, serverID, n)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []*RawSample{}
	for rows.Next() {
		rs, err := scanSample(rows)
		if err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, rs)
	}
	return out, rows.Err()
}

// healthFromSamples evaluates component health over the recent samples.
// Thresholds are passed in so evaluation never touches ambient context.
func (q *QueryService) healthFromSamples(samples []*RawSample, t Thresholds) HealthState {
	if len(samples) == 0 {
		return HealthState{State: HealthUnknown, Basis: 0, Points: []HealthPoint{
			{Component: "telemetry", State: HealthUnknown},
		}}
	}

	cpu := avgFloat(collect(samples, func(s *RawSample) *float64 { return s.CPUUsage }))
	mem := avgFloat(collect(samples, func(s *RawSample) *float64 { return s.MemoryUsagePercent }))
	var worstDisk *float64
	for _, s := range samples {
		if u, _, ok := maxDiskUsage(s.Disks); ok {
			v := u
			if worstDisk == nil || v > *worstDisk {
				worstDisk = &v
			}
		}
	}

	points := []HealthPoint{}
	if cpu != nil {
		points = append(points, HealthPoint{Component: "cpu", Value: cpu, State: evaluateLevel(*cpu, t.CPUWarn, t.CPUCrit)})
	}
	if mem != nil {
		points = append(points, HealthPoint{Component: "memory", Value: mem, State: evaluateLevel(*mem, t.MemWarn, t.MemCrit)})
	}
	if worstDisk != nil {
		points = append(points, HealthPoint{Component: "disk", Value: worstDisk, State: evaluateLevel(*worstDisk, t.DiskWarn, t.DiskCrit)})
	}
	if len(points) == 0 {
		points = append(points, HealthPoint{Component: "telemetry", State: HealthUnknown})
	}

	states := make([]string, 0, len(points)+1)
	for _, p := range points {
		states = append(states, p.State)
	}
	// A dead telemetry channel is itself a health signal: if we have samples
	// but they are all old, the state degrades below.
	return HealthState{State: worstState(states...), Points: points, Basis: len(samples)}
}

func collect(samples []*RawSample, get func(*RawSample) *float64) []*float64 {
	out := make([]*float64, 0, len(samples))
	for _, s := range samples {
		out = append(out, get(s))
	}
	return out
}

// capabilityMatrix reports which monitoring features actually produced data
// in the latest sample — ✓ only when verified (§41).
func capabilityMatrix(s *RawSample) map[string]any {
	ok := func(v any) string { return "ok" }
	fail := func(reason string) string { return "error:" + reason }
	unsupported := "unsupported"

	m := map[string]any{
		"cpu":       unsupported, "memory": unsupported, "disk": unsupported,
		"network":   unsupported, "processes": unsupported, "services": unsupported,
	}
	if err, okv := s.Errors["cpu"]; okv && err != "" {
		m["cpu"] = fail(err)
	} else if s.CPUUsage != nil {
		m["cpu"] = ok(nil)
	}
	if err, okv := s.Errors["memory"]; okv && err != "" {
		m["memory"] = fail(err)
	} else if s.MemoryUsagePercent != nil {
		m["memory"] = ok(nil)
	}
	if err, okv := s.Errors["disk"]; okv && err != "" {
		m["disk"] = fail(err)
	} else if len(s.Disks) > 0 {
		m["disk"] = ok(nil)
	}
	if err, okv := s.Errors["network"]; okv && err != "" {
		m["network"] = fail(err)
	} else if len(s.Network) > 0 {
		m["network"] = ok(nil)
	}
	if err, okv := s.Errors["processes"]; okv && err != "" {
		m["processes"] = fail(err)
	} else if len(s.Processes) > 0 {
		m["processes"] = ok(nil)
	}
	if err, okv := s.Errors["services"]; okv && err != "" {
		m["services"] = fail(err)
	} else if len(s.Services) > 0 {
		m["services"] = ok(nil)
	}
	return m
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

var allowedRanges = map[string]time.Duration{
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
	"30d": 30 * 24 * time.Hour,
}

// ParseRange validates a range parameter ("1h".."30d") or falls back to the
// default 24h. Custom from/to windows are bounded to the maximum range.
func ParseRange(raw string) (time.Duration, error) {
	if raw == "" {
		return 24 * time.Hour, nil
	}
	if d, ok := allowedRanges[strings.ToLower(raw)]; ok {
		return d, nil
	}
	return 0, apierror.BadRequest("range must be one of 1h, 6h, 24h, 7d, 30d")
}

// HistoryPoint is one downsampled chart point.
type HistoryPoint struct {
	T               string   `json:"t"`
	CPU             *float64 `json:"cpu_usage"`
	Memory          *float64 `json:"memory_usage"`
	Load1           *float64 `json:"load_1m"`
	Load5           *float64 `json:"load_5m"`
	Load15          *float64 `json:"load_15m"`
	Swap            *float64 `json:"swap_usage"`
	MaxDiskUsage    *float64 `json:"max_disk_usage"`
}

type HistoryView struct {
	Range    string         `json:"range"`
	Interval int            `json:"interval_seconds"` // bucket size of points
	Source   string         `json:"source"`           // raw | hourly | daily
	Points   []HistoryPoint `json:"points"`
}

// History returns a bounded, downsampled series. Ranges up to 6h read raw
// samples bucketed in SQL; 24h/7d read hourly aggregates; 30d reads daily.
func (q *QueryService) History(ctx context.Context, serverID, rawRange string) (*HistoryView, error) {
	span, err := ParseRange(rawRange)
	if err != nil {
		return nil, err
	}
	from := time.Now().UTC().Add(-span)

	switch {
	case span <= 6*time.Hour:
		return q.rawHistory(ctx, serverID, from, span)
	case span <= 7*24*time.Hour:
		return q.aggHistory(ctx, serverID, from, "hourly", rawRange)
	default:
		return q.aggHistory(ctx, serverID, from, "daily", rawRange)
	}
}

func (q *QueryService) rawHistory(ctx context.Context, serverID string, from time.Time, span time.Duration) (*HistoryView, error) {
	// Target ≤ 240 points regardless of collection frequency.
	bucketSec := int(span.Seconds() / 240)
	if bucketSec < 15 {
		bucketSec = 15
	}
	rows, err := q.pool.Query(ctx, `
		SELECT
			to_timestamp(floor(extract(epoch from server_received_at) / $3) * $3) AS bucket,
			avg(cpu_usage_percent), avg(memory_usage_percent),
			avg(load_1m), avg(load_5m), avg(load_15m), avg(swap_usage_percent),
			max(disk_max)
		FROM (
			SELECT server_received_at, cpu_usage_percent, memory_usage_percent,
			       load_1m, load_5m, load_15m, swap_usage_percent,
			       (SELECT max((d->>'usage_percent')::float) FROM jsonb_array_elements(disks) d) AS disk_max
			FROM server_metric_samples
			WHERE server_id = $1 AND server_received_at >= $2
		) s
		GROUP BY bucket ORDER BY bucket`,
		serverID, from, bucketSec)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	pts := []HistoryPoint{}
	for rows.Next() {
		var p HistoryPoint
		var t time.Time
		var diskMax *float64
		if err := rows.Scan(&t, &p.CPU, &p.Memory, &p.Load1, &p.Load5, &p.Load15, &p.Swap, &diskMax); err != nil {
			return nil, apierror.From(err)
		}
		p.MaxDiskUsage = diskMax
		p.T = t.UTC().Format(time.RFC3339)
		pts = append(pts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, apierror.From(err)
	}
	return &HistoryView{Range: fmt.Sprintf("%ds", int(span.Seconds())), Interval: bucketSec, Source: "raw", Points: pts}, nil
}

func (q *QueryService) aggHistory(ctx context.Context, serverID string, from time.Time, table, rangeLabel string) (*HistoryView, error) {
	var bucketExpr string
	interval := 3600
	if table == "hourly" {
		bucketExpr = "bucket"
	} else {
		bucketExpr = "bucket::timestamptz"
		interval = 86400
	}
	rows, err := q.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s, cpu_avg, mem_avg, load_max, NULL::real AS swap, disk_max::text
		FROM server_metric_%s WHERE server_id = $1 AND bucket >= $2
		ORDER BY bucket`, bucketExpr, table), serverID, from)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	pts := []HistoryPoint{}
	for rows.Next() {
		var p HistoryPoint
		var t time.Time
		var diskMaxJSON string
		if err := rows.Scan(&t, &p.CPU, &p.Memory, &p.Load1, &p.Swap, &diskMaxJSON); err != nil {
			return nil, apierror.From(err)
		}
		var diskMap map[string]float64
		_ = json.Unmarshal([]byte(diskMaxJSON), &diskMap)
		for _, v := range diskMap {
			vv := v
			if p.MaxDiskUsage == nil || vv > *p.MaxDiskUsage {
				p.MaxDiskUsage = &vv
			}
		}
		p.T = t.UTC().Format(time.RFC3339)
		pts = append(pts, p)
	}
	if err := rows.Err(); err != nil {
		return nil, apierror.From(err)
	}
	return &HistoryView{Range: rangeLabel, Interval: interval, Source: table, Points: pts}, nil
}

// ---------------------------------------------------------------------------
// Network / Disk series + Services / Processes
// ---------------------------------------------------------------------------

type NetworkPoint struct {
	T          string  `json:"t"`
	Interface  string  `json:"interface"`
	RxBytes    float64 `json:"rx_bytes"`
	TxBytes    float64 `json:"tx_bytes"`
	RxMbps     *float64 `json:"rx_mbps"`
	TxMbps     *float64 `json:"tx_mbps"`
}

// NetworkSeries returns the latest counters per interface plus a bounded
// rate series computed centrally from cumulative counters. Range is capped
// at 24h because rate series are raw-sample based.
func (q *QueryService) NetworkSeries(ctx context.Context, serverID, rawRange string) (map[string]any, error) {
	span, err := ParseRange(rawRange)
	if err != nil {
		return nil, err
	}
	if span > 24*time.Hour {
		span = 24 * time.Hour
	}
	from := time.Now().UTC().Add(-span)

	rows, err := q.pool.Query(ctx, `
		SELECT server_received_at, network::text FROM server_metric_samples
		WHERE server_id = $1 AND server_received_at >= $2
		ORDER BY server_received_at ASC`, serverID, from)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()

	type ifaceState struct {
		last      map[string]float64
		points    []NetworkPoint
	}
	states := map[string]*ifaceState{}
	var prevTime time.Time
	var prev map[string]NetworkMetric

	for rows.Next() {
		var t time.Time
		var raw string
		if err := rows.Scan(&t, &raw); err != nil {
			return nil, apierror.From(err)
		}
		var nets []NetworkMetric
		_ = json.Unmarshal([]byte(raw), &nets)
		cur := map[string]NetworkMetric{}
		for _, n := range nets {
			cur[n.Interface] = n
		}
		if prev != nil && !prevTime.IsZero() {
			dt := t.Sub(prevTime).Seconds()
			if dt >= 1 {
				for name, n := range cur {
					p, ok := prev[name]
					if !ok {
						continue
					}
					drx := n.RxBytes - p.RxBytes
					dtx := n.TxBytes - p.TxBytes
					if drx < 0 || dtx < 0 {
						continue // counter wrap/restart: skip this interval
					}
					rx := drx * 8 / dt / 1e6
					tx := dtx * 8 / dt / 1e6
					st, ok2 := states[name]
					if !ok2 {
						st = &ifaceState{points: []NetworkPoint{}}
						states[name] = st
					}
					rxv, txv := rx, tx
					st.points = append(st.points, NetworkPoint{
						T: t.UTC().Format(time.RFC3339), Interface: name,
						RxBytes: n.RxBytes, TxBytes: n.TxBytes,
						RxMbps: &rxv, TxMbps: &txv,
					})
				}
			}
		}
		prev = cur
		prevTime = t
	}
	if err := rows.Err(); err != nil {
		return nil, apierror.From(err)
	}

	// Downsample any interface series beyond 240 points.
	series := map[string]any{}
	for name, st := range states {
		pts := st.points
		if len(pts) > 240 {
			step := len(pts) / 240
			down := make([]NetworkPoint, 0, 240)
			for i := 0; i < len(pts); i += step {
				down = append(down, pts[i])
			}
			pts = down
		}
		if len(pts) == 0 {
			pts = []NetworkPoint{}
		}
		series[name] = map[string]any{"points": pts}
	}
	return map[string]any{"range_seconds": int(span.Seconds()), "interfaces": series}, nil
}

// DiskSeries returns the most recent disk inventory plus the bounded max-usage history.
func (q *QueryService) DiskSeries(ctx context.Context, serverID, rawRange string) (map[string]any, error) {
	latest, err := q.latestSample(ctx, serverID)
	if err != nil {
		return nil, err
	}
	hist, err := q.History(ctx, serverID, rawRange)
	if err != nil {
		return nil, err
	}
	var disks []DiskMetric
	if latest != nil {
		disks = latest.Disks
	}
	if disks == nil {
		disks = []DiskMetric{}
	}
	return map[string]any{
		"current": disks,
		"history": map[string]any{
			"range": hist.Range, "interval_seconds": hist.Interval,
			"source": hist.Source, "points": hist.Points,
		},
	}, nil
}

// LatestServices / LatestProcesses return the newest sample's slices with a
// clear has-data flag instead of fabricating state.
func (q *QueryService) LatestServices(ctx context.Context, serverID string) (map[string]any, error) {
	latest, err := q.latestSample(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return map[string]any{"has_data": false, "services": []ServiceHealth{}}, nil
	}
	return map[string]any{
		"has_data":      true,
		"observed_at":   latest.ReceivedAt,
		"services":      orEmptyServices(latest.Services),
	}, nil
}

func (q *QueryService) LatestProcesses(ctx context.Context, serverID string) (map[string]any, error) {
	latest, err := q.latestSample(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if latest == nil {
		return map[string]any{"has_data": false, "processes": []ProcessMetric{}}, nil
	}
	return map[string]any{
		"has_data":    true,
		"observed_at": latest.ReceivedAt,
		"processes":   orEmptyProcesses(latest.Processes),
	}, nil
}

func orEmptyServices(in []ServiceHealth) []ServiceHealth {
	if in == nil {
		return []ServiceHealth{}
	}
	return in
}

func orEmptyProcesses(in []ProcessMetric) []ProcessMetric {
	if in == nil {
		return []ProcessMetric{}
	}
	return in
}

// WebsiteHealth derives lightweight website health (§30) from persisted
// state: the website's provisioning status, the server's newest service
// snapshot and server liveness. No external HTTP probes are performed.
func (q *QueryService) WebsiteHealth(ctx context.Context, serverID, websiteStatus, phpVersion string) (map[string]any, error) {
	latest, err := q.latestSample(ctx, serverID)
	if err != nil {
		return nil, err
	}

	simple := func(status string) map[string]any {
		return map[string]any{"status": status, "running": status == ServiceRunning}
	}
	nginx := map[string]any{"status": ServiceUnknown, "running": false}
	php := map[string]any{"status": ServiceUnknown, "running": false}
	hasData := false
	if latest != nil {
		hasData = true
		for _, svc := range latest.Services {
			if svc.Name == "nginx" {
				nginx = simple(svc.Status)
			}
			if phpVersion != "" && (svc.Name == "php"+phpVersion+"-fpm" || svc.Name == "php-cgi") {
				php = simple(svc.Status)
			}
		}
	}

	// Configuration health mirrors the provisioning state — honest, derived.
	configStatus := ServiceUnknown
	switch websiteStatus {
	case "active":
		configStatus = "Valid"
	case "error":
		configStatus = "Failed"
	case "provisioning":
		configStatus = "Pending"
	case "disabled":
		configStatus = "Stopped"
	}

	serverOnline := false
	if latest != nil && time.Since(latest.ReceivedAt) < 10*time.Minute {
		serverOnline = true
	}

	return map[string]any{
		"has_data":       hasData,
		"website_status": websiteStatus,
		"nginx":          nginx,
		"php":            php,
		"php_version":    phpVersion,
		"configuration":  map[string]any{"status": configStatus},
		"server":         map[string]any{"status": map[bool]string{true: "online", false: "offline"}[serverOnline]},
	}, nil
}

// ---------------------------------------------------------------------------
// Fleet overview (global dashboard)
// ---------------------------------------------------------------------------

type FleetServer struct {
	ServerID    string     `json:"server_id"`
	Name        string     `json:"name"`
	Hostname    string     `json:"hostname"`
	Status      string     `json:"status"` // online/offline
	Online      bool       `json:"online"`
	LastSeenAt  *time.Time `json:"last_seen_at"`
	CPU         *float64   `json:"cpu_usage"`
	Memory      *float64   `json:"memory_usage"`
	MaxDisk     *float64   `json:"max_disk_usage"`
	UptimeHours *float64   `json:"uptime_hours"`
	Health      string     `json:"health"`
}

// Fleet joins live inventory with the newest smoothed metrics per server.
func (q *QueryService) Fleet(ctx context.Context, offlineAfterMin int) ([]FleetServer, error) {
	rows, err := q.pool.Query(ctx, `
		SELECT s.id, COALESCE(s.label,''), s.hostname, s.status, s.last_seen_at,
		       m.cpu_usage, m.mem_usage, m.disk_usage, m.uptime_seconds
		FROM servers s
		LEFT JOIN LATERAL (
			SELECT avg(cpu_usage_percent) AS cpu_usage,
			       avg(memory_usage_percent) AS mem_usage,
			       (SELECT max((d->>'usage_percent')::float) FROM jsonb_array_elements(disks) d) AS disk_usage,
			       max(uptime_seconds) AS uptime_seconds
			FROM (
				SELECT * FROM server_metric_samples
				WHERE server_id = s.id
				ORDER BY server_received_at DESC LIMIT 4
			) recent
		) m ON true
		WHERE s.status <> 'revoked'
		ORDER BY s.registered_at DESC`)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()

	cutoff := time.Now().Add(-time.Duration(offlineAfterMin) * time.Minute)
	t := q.thresholds(ctx)
	out := []FleetServer{}
	for rows.Next() {
		var f FleetServer
		var uptime *int64
		var status string
		if err := rows.Scan(&f.ServerID, &f.Name, &f.Hostname, &status, &f.LastSeenAt,
			&f.CPU, &f.Memory, &f.MaxDisk, &uptime); err != nil {
			return nil, apierror.From(err)
		}
		f.Status = status
		f.Online = status == "online" && f.LastSeenAt != nil && f.LastSeenAt.After(cutoff)
		if uptime != nil {
			h := float64(*uptime) / 3600
			f.UptimeHours = &h
		}
		if !f.Online {
			f.Health = HealthOffline
		} else {
			points := []string{}
			if f.CPU != nil {
				points = append(points, evaluateLevel(*f.CPU, t.CPUWarn, t.CPUCrit))
			}
			if f.Memory != nil {
				points = append(points, evaluateLevel(*f.Memory, t.MemWarn, t.MemCrit))
			}
			if f.MaxDisk != nil {
				points = append(points, evaluateLevel(*f.MaxDisk, t.DiskWarn, t.DiskCrit))
			}
			if len(points) == 0 {
				f.Health = HealthUnknown
			} else {
				f.Health = worstState(points...)
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
