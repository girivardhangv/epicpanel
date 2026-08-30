// Alert foundation: rules, duration-based evaluation with de-duplication
// (one active alert per rule+server — no storms) and automatic resolution.
// The evaluator is a single in-process worker whose entire state persists in
// PostgreSQL, so restarts and future horizontal deployments stay consistent.
package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AlertService struct {
	pool  *pgxpool.Pool
	audit *audit.Service
	log   *slog.Logger

	// Notifier enqueues delivery jobs when an alert changes state (Phase 5).
	Notifier func(ctx context.Context, event, ruleName, ruleType, severity, serverID, serverName, message string, metricValue, threshold *float64, triggeredAt string)
}

func NewAlertService(pool *pgxpool.Pool, auditSvc *audit.Service, log *slog.Logger) *AlertService {
	return &AlertService{pool: pool, audit: auditSvc, log: log}
}

// AlertRule is a persisted evaluation rule.
type AlertRule struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	RuleType        string   `json:"rule_type"`
	Threshold       *float64 `json:"threshold"`
	DurationSeconds int      `json:"duration_seconds"`
	Severity        string   `json:"severity"`
	Enabled         bool     `json:"enabled"`
}

// Alert is one alert state row as returned to the UI.
type Alert struct {
	ID          string     `json:"id"`
	RuleID      string     `json:"rule_id"`
	RuleName    string     `json:"rule_name"`
	RuleType    string     `json:"rule_type"`
	ServerID    *string    `json:"server_id"`
	ServerName  string     `json:"server_name"`
	Status      string     `json:"status"`
	Severity    string     `json:"severity"`
	MetricValue *float64   `json:"metric_value"`
	Threshold   *float64   `json:"threshold"`
	Message     string     `json:"message"`
	TriggeredAt time.Time  `json:"triggered_at"`
	AckedAt     *time.Time `json:"acknowledged_at,omitempty"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
}

// Alert statuses.
const (
	AlertTriggered    = "triggered"
	AlertAcknowledged = "acknowledged"
	AlertResolved     = "resolved"
)

// List returns alerts, newest first, optionally filtered by status/server.
func (s *AlertService) List(ctx context.Context, status, serverID string, limit int) ([]Alert, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := `SELECT a.id, a.rule_id, r.name, r.rule_type, a.server_id,
	             COALESCE(NULLIF(sv.label,''), sv.hostname, ''),
	             a.status, a.severity, a.metric_value, a.threshold, a.message,
	             a.triggered_at, a.acknowledged_at, a.resolved_at
	      FROM alerts a
	      JOIN alert_rules r ON r.id = a.rule_id
	      LEFT JOIN servers sv ON sv.id = a.server_id`
	args := []any{}
	conds := []string{}
	if status != "" {
		args = append(args, status)
		conds = append(conds, fmt.Sprintf("a.status = $%d", len(args)))
	}
	if serverID != "" {
		args = append(args, serverID)
		conds = append(conds, fmt.Sprintf("a.server_id = $%d", len(args)))
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, limit)
	q += fmt.Sprintf(" ORDER BY a.triggered_at DESC LIMIT $%d", len(args))

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.RuleID, &a.RuleName, &a.RuleType, &a.ServerID,
			&a.ServerName, &a.Status, &a.Severity, &a.MetricValue, &a.Threshold,
			&a.Message, &a.TriggeredAt, &a.AckedAt, &a.ResolvedAt); err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListRules returns the rule catalog.
func (s *AlertService) ListRules(ctx context.Context) ([]AlertRule, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, rule_type, threshold, duration_seconds, severity, enabled
		FROM alert_rules ORDER BY name`)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []AlertRule{}
	for rows.Next() {
		var r AlertRule
		if err := rows.Scan(&r.ID, &r.Name, &r.RuleType, &r.Threshold,
			&r.DurationSeconds, &r.Severity, &r.Enabled); err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateRule changes threshold/duration/enabled for one rule.
func (s *AlertService) UpdateRule(ctx context.Context, id string, threshold *float64, durationSeconds *int, enabled *bool) (*AlertRule, error) {
	if threshold != nil && (*threshold < 0 || *threshold > 100000) {
		return nil, apierror.BadRequest("threshold out of range")
	}
	if durationSeconds != nil && (*durationSeconds < 0 || *durationSeconds > 86400) {
		return nil, apierror.BadRequest("duration out of range")
	}
	var r AlertRule
	err := s.pool.QueryRow(ctx, `
		UPDATE alert_rules SET
			threshold = COALESCE($2, threshold),
			duration_seconds = COALESCE($3, duration_seconds),
			enabled = COALESCE($4, enabled),
			updated_at = now()
		WHERE id = $1
		RETURNING id, name, rule_type, threshold, duration_seconds, severity, enabled`,
		id, threshold, durationSeconds, enabled).
		Scan(&r.ID, &r.Name, &r.RuleType, &r.Threshold, &r.DurationSeconds, &r.Severity, &r.Enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierror.NotFound("rule")
	}
	if err != nil {
		return nil, apierror.From(err)
	}
	return &r, nil
}

// Acknowledge marks a triggered alert acknowledged.
func (s *AlertService) Acknowledge(ctx context.Context, id, actorID, actorLabel string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE alerts SET status = 'acknowledged', acknowledged_at = now(),
		       acknowledged_by = NULLIF($2,'')::uuid
		WHERE id = $1 AND status = 'triggered'`, id, actorID)
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.Conflict("alert not found or already acknowledged/resolved")
	}
	s.audit.Log(ctx, audit.Entry{
		ActorType: "user", Label: actorLabel,
		Action: "alerts.acknowledged", Resource: "alert", ResourceID: id,
	})
	return nil
}

// ---------------------------------------------------------------------------
// Evaluation worker
// ---------------------------------------------------------------------------

// Evaluate runs one pass over every enabled rule. Duration semantics: a
// breach requires ALL samples in the window to exceed the threshold AND the
// window to span ~the configured duration with ≥2 samples — a single noisy
// sample can never fire an alert (§27). Resolution clears the active alert.
func (s *AlertService) Evaluate(ctx context.Context) error {
	rules, err := s.ListRules(ctx)
	if err != nil {
		return err
	}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		switch rule.RuleType {
		case "cpu_high":
			s.evaluateMetricRule(ctx, rule, "cpu_usage_percent", "CPU")
		case "memory_high":
			s.evaluateMetricRule(ctx, rule, "memory_usage_percent", "Memory")
		case "disk_high":
			s.evaluateDiskRule(ctx, rule)
		case "server_offline":
			s.evaluateOfflineRule(ctx, rule)
		case "service_stopped":
			s.evaluateServiceRule(ctx, rule)
		}
	}
	return nil
}

// evaluateMetricRule handles cpu/memory rules driven by stored samples.
func (s *AlertService) evaluateMetricRule(ctx context.Context, rule AlertRule, column, label string) {
	if rule.Threshold == nil {
		return
	}
	window := max(rule.DurationSeconds, 60)
	rows, err := s.pool.Query(ctx, `
		SELECT server_id,
		       bool_and(`+column+` > $1) AS breaching,
		       max(`+column+`),
		       min(`+column+`),
		       count(*),
		       extract(epoch from (max(server_received_at) - min(server_received_at)))
		FROM (
			SELECT server_id, `+column+`, server_received_at
			FROM server_metric_samples
			WHERE server_received_at >= now() - ($2 || ' seconds')::interval
			  AND `+column+` IS NOT NULL
			ORDER BY server_received_at DESC
			LIMIT 500
		) recent
		GROUP BY server_id`,
		*rule.Threshold, strconv.Itoa(window))
	if err != nil {
		s.log.Warn("alert evaluation query failed", "rule", rule.Name, "err", err)
		return
	}
	type serverEval struct {
		ServerID  string
		Breaching bool
		Max, Min  *float64
		Samples   int64
		SpanSec   float64
	}
	var evals []serverEval
	for rows.Next() {
		var e serverEval
		if err := rows.Scan(&e.ServerID, &e.Breaching, &e.Max, &e.Min, &e.Samples, &e.SpanSec); err != nil {
			rows.Close()
			s.log.Warn("alert evaluation scan failed", "rule", rule.Name, "err", err)
			return
		}
		evals = append(evals, e)
	}
	rows.Close()

	for _, e := range evals {
		consistent := e.Breaching && e.Samples >= 2 && e.SpanSec >= float64(window)*0.8
		if consistent {
			s.triggerAlert(ctx, rule, e.ServerID, e.Max,
				fmt.Sprintf("%s usage above %s%% for %s", label,
					strconv.FormatFloat(*rule.Threshold, 'f', -1, 64), humanDuration(window)))
		} else {
			s.resolveAlert(ctx, rule.ID, e.ServerID)
		}
	}
}

// evaluateDiskRule uses the max per-mount usage from recent samples.
func (s *AlertService) evaluateDiskRule(ctx context.Context, rule AlertRule) {
	if rule.Threshold == nil {
		return
	}
	window := max(rule.DurationSeconds, 60)
	rows, err := s.pool.Query(ctx, `
		SELECT server_id,
		       bool_and(breaching) AS breaching,
		       max(usage),
		       min(usage),
		       count(*),
		       extract(epoch from (max(received) - min(received)))
		FROM (
			SELECT server_id, server_received_at AS received,
			       (SELECT max((d->>'usage_percent')::text::float)
			          FROM jsonb_array_elements(disks) d) AS usage,
			       (SELECT max((d->>'usage_percent')::text::float)
			          FROM jsonb_array_elements(disks) d) > $1 AS breaching
			FROM server_metric_samples
			WHERE server_received_at >= now() - ($2 || ' seconds')::interval
			ORDER BY server_received_at DESC
			LIMIT 500
		) recent
		GROUP BY server_id`,
		*rule.Threshold, strconv.Itoa(window))
	if err != nil {
		s.log.Warn("disk alert evaluation failed", "rule", rule.Name, "err", err)
		return
	}
	type diskEval struct {
		ServerID  string
		Breaching bool
		Max, Min  *float64
		Samples   int64
		SpanSec   float64
	}
	var evals []diskEval
	for rows.Next() {
		var e diskEval
		if err := rows.Scan(&e.ServerID, &e.Breaching, &e.Max, &e.Min, &e.Samples, &e.SpanSec); err != nil {
			rows.Close()
			return
		}
		evals = append(evals, e)
	}
	rows.Close()

	for _, e := range evals {
		consistent := e.Breaching && e.Samples >= 2 && e.SpanSec >= float64(window)*0.8
		if consistent {
			s.triggerAlert(ctx, rule, e.ServerID, e.Max,
				fmt.Sprintf("Disk usage above %s%% for %s",
					strconv.FormatFloat(*rule.Threshold, 'f', -1, 64), humanDuration(window)))
		} else {
			s.resolveAlert(ctx, rule.ID, e.ServerID)
		}
	}
}

// evaluateOfflineRule triggers when a server's last_seen is older than the
// offline threshold (the threshold itself is the duration).
func (s *AlertService) evaluateOfflineRule(ctx context.Context, rule AlertRule) {
	offlineAfter := max(int(rule.ThresholdOr(300)), 60)
	rows, err := s.pool.Query(ctx, `
		SELECT s.id,
		       s.status = 'online'
		         AND (s.last_seen_at IS NULL OR s.last_seen_at < now() - ($1 || ' seconds')::interval)
		FROM servers s
		WHERE s.status <> 'revoked'`,
		strconv.Itoa(offlineAfter))
	if err != nil {
		s.log.Warn("offline alert evaluation failed", "err", err)
		return
	}
	type offlineRow struct {
		ServerID string
		Offline  bool
	}
	var rowsData []offlineRow
	for rows.Next() {
		var r offlineRow
		if err := rows.Scan(&r.ServerID, &r.Offline); err != nil {
			rows.Close()
			return
		}
		rowsData = append(rowsData, r)
	}
	rows.Close()

	for _, r := range rowsData {
		if r.Offline {
			s.triggerAlert(ctx, rule, r.ServerID, nil,
				fmt.Sprintf("Server has not reported telemetry for %s", humanDuration(offlineAfter)))
		} else {
			s.resolveAlert(ctx, rule.ID, r.ServerID)
		}
	}
}

// evaluateServiceRule watches the newest service snapshot per server. Only
// services that were installed-and-enabled are considered incidents.
func (s *AlertService) evaluateServiceRule(ctx context.Context, rule AlertRule) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (server_id)
		       server_id, services::text
		FROM server_metric_samples
		WHERE server_received_at >= now() - interval '1 hour'
		ORDER BY server_id, server_received_at DESC`)
	if err != nil {
		s.log.Warn("service alert evaluation failed", "err", err)
		return
	}
	type svcRow struct {
		ServerID string
		Services string
	}
	var rowsData []svcRow
	for rows.Next() {
		var r svcRow
		if err := rows.Scan(&r.ServerID, &r.Services); err != nil {
			rows.Close()
			return
		}
		rowsData = append(rowsData, r)
	}
	rows.Close()

	for _, r := range rowsData {
		var services []ServiceHealth
		if err := json.Unmarshal([]byte(r.Services), &services); err != nil {
			continue
		}
		var stopped []string
		for _, svc := range services {
			if svc.Status == ServiceFailed ||
				(svc.Status == ServiceStopped && svc.Enabled != nil && *svc.Enabled) {
				stopped = append(stopped, svc.DisplayName)
			}
		}
		if len(stopped) > 0 {
			s.triggerAlert(ctx, rule, r.ServerID, nil,
				"Service stopped: "+strings.Join(stopped, ", "))
		} else {
			s.resolveAlert(ctx, rule.ID, r.ServerID)
		}
	}
}

// triggerAlert creates the alert if no active one exists; the partial unique
// index makes alert storms impossible at the storage layer. Repeated passes
// refresh last_breached_at + metric_value without creating new rows.
func (s *AlertService) triggerAlert(ctx context.Context, rule AlertRule, serverID string, value *float64, message string) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO alerts (rule_id, server_id, status, severity, metric_value, threshold, message, last_breached_at)
		VALUES ($1, $2, 'triggered', $3, $4, $5, $6, now())
		ON CONFLICT (rule_id, server_id) WHERE status IN ('triggered','acknowledged')
		DO UPDATE SET last_breached_at = now(),
		              metric_value = COALESCE(EXCLUDED.metric_value, alerts.metric_value)`,
		rule.ID, serverID, rule.Severity, value, rule.Threshold, message)
	if err != nil {
		s.log.Warn("alert trigger failed", "rule", rule.Name, "err", err)
		return
	}
	if tag.RowsAffected() == 1 {
		s.audit.Log(ctx, audit.Entry{
			ActorType: "system", Label: "monitoring",
			Action: "alerts.triggered", Resource: "alert", ResourceID: rule.Name,
			Metadata: map[string]any{"server_id": serverID, "message": message},
		})
		if s.Notifier != nil {
			s.Notifier(ctx, "triggered", rule.Name, rule.RuleType, rule.Severity,
				serverID, s.serverNameOf(ctx, serverID), message, value, rule.Threshold,
				time.Now().UTC().Format(time.RFC3339))
		}
	}
}

// serverNameOf resolves a display name for the notifier payload (best effort).
func (s *AlertService) serverNameOf(ctx context.Context, serverID string) string {
	var name string
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(label,''), hostname, '') FROM servers WHERE id = $1`,
		serverID).Scan(&name)
	return name
}

// resolveAlert resolves an active alert when the condition clears.
func (s *AlertService) resolveAlert(ctx context.Context, ruleID, serverID string) {
	rule, err := s.ruleByID(ctx, ruleID)
	if err != nil {
		rule = &AlertRule{ID: ruleID, RuleType: "unknown"}
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE alerts SET status = 'resolved', resolved_at = now(),
		       resolved_reason = 'condition returned to normal'
		WHERE rule_id = $1 AND server_id = $2 AND status IN ('triggered','acknowledged')`,
		ruleID, serverID)
	if err != nil {
		s.log.Warn("alert resolve failed", "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		s.audit.Log(ctx, audit.Entry{
			ActorType: "system", Label: "monitoring",
			Action: "alerts.resolved", Resource: "alert", ResourceID: ruleID,
			Metadata: map[string]any{"server_id": serverID},
		})
		if s.Notifier != nil {
			s.Notifier(ctx, "resolved", rule.Name, rule.RuleType, rule.Severity,
				serverID, s.serverNameOf(ctx, serverID),
				"Alert resolved — condition returned to normal", nil, nil,
				time.Now().UTC().Format(time.RFC3339))
		}
	}
}

// ruleByID fetches a rule for notification payloads (best effort).
func (s *AlertService) ruleByID(ctx context.Context, id string) (*AlertRule, error) {
	var r AlertRule
	err := s.pool.QueryRow(ctx, `
		SELECT id, name, rule_type, threshold, duration_seconds, severity, enabled
		FROM alert_rules WHERE id = $1`, id).
		Scan(&r.ID, &r.Name, &r.RuleType, &r.Threshold, &r.DurationSeconds, &r.Severity, &r.Enabled)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

var evalRunning atomic.Bool

// Start launches the evaluation loop (every 30s, never overlapping).
func (s *AlertService) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !evalRunning.CompareAndSwap(false, true) {
					continue
				}
				if err := s.Evaluate(ctx); err != nil {
					s.log.Warn("alert evaluation failed", "err", err)
				}
				evalRunning.Store(false)
			}
		}
	}()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// ThresholdOr treats a nil threshold with the given default (offline rules
// store minutes-as-threshold by seed convention).
func (r AlertRule) ThresholdOr(def float64) float64 {
	if r.Threshold == nil {
		return def
	}
	return *r.Threshold
}

func humanDuration(seconds int) string {
	switch {
	case seconds%86400 == 0 && seconds >= 86400:
		return strconv.Itoa(seconds/86400) + "d"
	case seconds%3600 == 0 && seconds >= 3600:
		return strconv.Itoa(seconds/3600) + "h"
	case seconds%60 == 0:
		return strconv.Itoa(seconds/60) + " minutes"
	default:
		return strconv.Itoa(seconds) + "s"
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
