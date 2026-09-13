package alerts

// Phase 13 — alert rule engine. Async only: evaluation happens on bus event
// subscriptions and periodic sweep ticks, NEVER in API request handlers
// (Phase 3 rule). Verbatim alert list (master doc):
//   CPU > threshold | RAM > threshold | Disk > threshold | Node offline
//   Service down | Backup failed | Provisioning failed | SSL expiration
//   Container crashed

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/events"
)

// Alert is one row of the shared alerts feed (0010 + 0032 extensions).
type Alert struct {
	ID             uuid.UUID  `json:"id"`
	Organization   *uuid.UUID `json:"organization_id,omitempty"`
	Type           string     `json:"type"`
	Severity       string     `json:"severity"`
	ResourceType   string     `json:"resource_type"`
	ResourceID     string     `json:"resource_id"`
	ResourceName   string     `json:"resource_name"`
	Message        string     `json:"message"`
	RuleID         *uuid.UUID `json:"rule_id,omitempty"`
	Occurrences    int        `json:"occurrences"`
	LastSeenAt     *time.Time `json:"last_seen_at,omitempty"`
	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	ResolvedAt     *time.Time `json:"resolved_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// Rule is one alert_rules row.
type Rule struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	Class           string    `json:"rule_class"` // threshold | state | time
	Metric          string    `json:"metric"`
	Scope           string    `json:"scope"` // node | account | plan | fleet
	ScopeID         string    `json:"scope_id"`
	Comparison      string    `json:"comparison"` // gt | lt
	Threshold       float64   `json:"threshold"`
	DurationSeconds int       `json:"duration_seconds"`
	RecoveryMargin  float64   `json:"recovery_margin"`
	WindowDays      int       `json:"window_days"`
	Severity        string    `json:"severity"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

const alertCols = `id, organization_id, type, severity, resource_type, resource_id,
	resource_name, message, rule_id, occurrences, last_seen_at, acknowledged_at,
	resolved_at, created_at`

// Store persists rules + the alerts feed.
type Store struct {
	Pool *pgxpool.Pool
}

// ---------------------------------------------------------------------------
// Rules CRUD
// ---------------------------------------------------------------------------

// CreateRule inserts one rule.
func (s *Store) CreateRule(ctx context.Context, r *Rule) (*Rule, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO alert_rules (name, description, rule_class, metric, scope, scope_id,
			comparison, threshold, duration_seconds, recovery_margin, window_days, severity, enabled)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id, name, description, rule_class, metric, scope, scope_id, comparison,
			threshold, duration_seconds, recovery_margin, window_days, severity, enabled,
			created_at, updated_at`,
		r.Name, r.Description, r.Class, r.Metric, r.Scope, r.ScopeID,
		r.Comparison, r.Threshold, r.DurationSeconds, r.RecoveryMargin,
		r.WindowDays, r.Severity, r.Enabled)
	return scanRule(row)
}

// UpdateRule mutates tunable fields of one rule.
func (s *Store) UpdateRule(ctx context.Context, id uuid.UUID, r *Rule) (*Rule, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE alert_rules SET description=$2, comparison=$3, threshold=$4,
			duration_seconds=$5, recovery_margin=$6, window_days=$7, severity=$8,
			enabled=$9, updated_at=now()
		WHERE id=$1
		RETURNING id, name, description, rule_class, metric, scope, scope_id, comparison,
			threshold, duration_seconds, recovery_margin, window_days, severity, enabled,
			created_at, updated_at`,
		id, r.Description, r.Comparison, r.Threshold, r.DurationSeconds,
		r.RecoveryMargin, r.WindowDays, r.Severity, r.Enabled)
	out, err := scanRule(row)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteRule removes one rule.
func (s *Store) DeleteRule(ctx context.Context, id uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM alert_rules WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListRules lists all rules (platform-wide; WHM admin surface).
func (s *Store) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, name, description, rule_class, metric, scope, scope_id,
		comparison, threshold, duration_seconds, recovery_margin, window_days, severity, enabled,
		created_at, updated_at FROM alert_rules ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Feed queries (admin surface)
// ---------------------------------------------------------------------------

// ListAlerts returns the feed filtered by state (active|ack|resolved|all).
func (s *Store) ListAlerts(ctx context.Context, state string, limit int) ([]Alert, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	where := ""
	switch state {
	case "active":
		where = " WHERE resolved_at IS NULL AND acknowledged_at IS NULL"
	case "ack":
		where = " WHERE resolved_at IS NULL AND acknowledged_at IS NOT NULL"
	case "resolved":
		where = " WHERE resolved_at IS NOT NULL"
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+alertCols+` FROM alerts`+where+
		` ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// Acknowledge marks one alert acknowledged (admin actor).
func (s *Store) Acknowledge(ctx context.Context, id, byUser uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE alerts SET acknowledged_at = now(), acknowledged_by = $2, updated_at = now()
		WHERE id = $1 AND resolved_at IS NULL`, id, byUser)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Resolve marks one alert resolved (admin actor).
func (s *Store) Resolve(ctx context.Context, id, byUser uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE alerts SET resolved_at = now(), resolved_by = $2, updated_at = now()
		WHERE id = $1 AND resolved_at IS NULL`, id, byUser)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Engine
// ---------------------------------------------------------------------------

// Engine evaluates rules asynchronously. One breach counter per (rule,
// subject) in memory; the feed table carries the durable alert lifecycle.
type Engine struct {
	Store *Store
	Bus   *events.Bus

	mu      sync.Mutex
	breach  map[string]int // ruleID|subject -> consecutive breach count
	openMtx sync.Mutex
	open    map[string]uuid.UUID // ruleID|subject -> open alert id
}

// NewEngine builds the engine (breach state is process-local; the feed table
// dedups durably via the unique open-alert index from 0010).
func NewEngine(st *Store, bus *events.Bus) *Engine {
	return &Engine{Store: st, Bus: bus, breach: map[string]int{}, open: map[string]uuid.UUID{}}
}

// EvaluateThreshold consumes one live metric sample point for one subject.
// Called from the sweep loop, not from request handlers.
func (e *Engine) EvaluateThreshold(ctx context.Context, r Rule, subject string, value float64) {
	if r.Class != "threshold" {
		return
	}
	breaching := (r.Comparison == "gt" && value > r.Threshold) ||
		(r.Comparison == "lt" && value < r.Threshold)
	key := r.ID.String() + "|" + subject
	e.mu.Lock()
	if breaching {
		e.breach[key]++
	} else {
		e.breach[key] = 0
	}
	consecutive := e.breach[key]
	e.mu.Unlock()

	if breaching && consecutive*r.sweepUnit() >= r.DurationSeconds {
		// Hysteresis satisfied — raise (deduped by rule+subject).
		msg := fmt.Sprintf("%s breached %.1f %s %.1f for %ds (now %.1f)",
			r.Name, value, r.Comparison, r.Threshold, consecutive*r.sweepUnit(), value)
		e.raise(ctx, r, subject, msg, map[string]any{"value": value, "consecutive": consecutive})
		return
	}
	if !breaching {
		// Recovery: clear threshold = threshold - recovery_margin.
		if r.Comparison == "gt" && value <= r.Threshold-r.RecoveryMargin {
			e.resolveAuto(ctx, r, subject)
		} else if r.Comparison == "lt" && value >= r.Threshold+r.RecoveryMargin {
			e.resolveAuto(ctx, r, subject)
		}
	}
}

// EvaluateState raises/resolves a state-based alert (offline/down/crashed/failed).
func (e *Engine) EvaluateState(ctx context.Context, r Rule, subject string, bad bool, detail string) {
	if r.Class != "state" || !r.matchSubject(subject) {
		return
	}
	if bad {
		e.raise(ctx, r, subject, detail, nil)
		return
	}
	e.resolveAuto(ctx, r, subject)
}

// EvaluateTimeWindow raises time-based alerts (SSL expiration windows).
func (e *Engine) EvaluateTimeWindow(ctx context.Context, r Rule, subject string, daysLeft int) {
	if r.Class != "time" || r.WindowDays <= 0 {
		return
	}
	if daysLeft <= r.WindowDays {
		e.raise(ctx, r, subject, fmt.Sprintf("%s expires in %d day(s)", r.Name, daysLeft),
			map[string]any{"days_left": daysLeft})
		return
	}
	e.resolveAuto(ctx, r, subject)
}

// raise inserts (or increments) the deduped open alert and notifies.
func (e *Engine) raise(ctx context.Context, r Rule, subject, msg string, payload map[string]any) {
	openKey := r.ID.String() + "|" + subject
	e.openMtx.Lock()
	if alertID, ok := e.open[openKey]; ok {
		e.openMtx.Unlock()
		if _, err := e.Store.Pool.Exec(ctx,
			`UPDATE alerts SET occurrences = occurrences + 1, last_seen_at = now(), updated_at = now() WHERE id = $1`,
			alertID); err != nil {
			slog.Warn("alert bump failed", "alert", alertID, "err", err)
		}
		return
	}
	e.openMtx.Unlock()

	orgID := orgForSubject(subject)
	aid := uuid.New()
	_, err := e.Store.Pool.Exec(ctx, `
		INSERT INTO alerts (id, organization_id, type, severity, resource_type, resource_id,
			resource_name, message, rule_id, occurrences, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,now())
		ON CONFLICT (type, resource_id) WHERE resolved_at IS NULL DO NOTHING`,
		aid, orgID, "rule."+r.Name, r.Severity, r.Scope, subject, subject, msg, r.ID)
	if err != nil {
		slog.Warn("alert insert failed", "rule", r.Name, "err", err)
		return
	}
	e.openMtx.Lock()
	e.open[openKey] = aid
	e.openMtx.Unlock()

	if e.Bus != nil {
		b, _ := json.Marshal(payload)
		e.Bus.Publish(ctx, events.Event{
			Type:         "alert.raised",
			Organization: orgID,
			ActorType:    "system",
			ResourceType: r.Scope,
			ResourceID:   subject,
			Payload:      json.RawMessage(b),
		})
	}
	slog.Info("alert raised", "rule", r.Name, "subject", subject, "msg", msg)
}

// resolveAuto closes the open alert on recovery and publishes the event.
func (e *Engine) resolveAuto(ctx context.Context, r Rule, subject string) {
	openKey := r.ID.String() + "|" + subject
	e.openMtx.Lock()
	alertID, ok := e.open[openKey]
	if !ok {
		e.openMtx.Unlock()
		return
	}
	delete(e.open, openKey)
	e.openMtx.Unlock()
	if _, err := e.Store.Pool.Exec(ctx,
		`UPDATE alerts SET resolved_at = now(), updated_at = now() WHERE id = $1 AND resolved_at IS NULL`,
		alertID); err != nil {
		slog.Warn("alert resolve failed", "alert", alertID, "err", err)
		return
	}
	if e.Bus != nil {
		e.Bus.Publish(ctx, events.Event{
			Type:         "alert.resolved",
			ActorType:    "system",
			ResourceType: r.Scope,
			ResourceID:   subject,
		})
	}
}

// sweepUnit is the assumed sweep cadence (seconds) for hysteresis math.
func (r Rule) sweepUnit() int {
	if r.DurationSeconds <= 0 {
		return 1
	}
	if r.DurationSeconds < 30 {
		return r.DurationSeconds
	}
	return 30
}

// matchSubject: state rules carry the verbatim metric names; scope matching
// is exact-subject (node id, service key, or workload kind).
func (r Rule) matchSubject(subject string) bool {
	if r.ScopeID == "" {
		return true
	}
	return r.ScopeID == subject
}

// orgForSubject: fleet/node/state alerts are platform-wide (nil org);
// account-scoped alerts carry the org in their subject prefix "org:<uuid>:".
func orgForSubject(subject string) *uuid.UUID {
	const prefix = "org:"
	if len(subject) > len(prefix) && subject[:len(prefix)] == prefix {
		rest := subject[len(prefix):]
		for i := 0; i < len(rest); i++ {
			if rest[i] == '|' || rest[i] == ':' {
				if id, err := uuid.Parse(rest[:i]); err == nil {
					return &id
				}
				return nil
			}
		}
		if id, err := uuid.Parse(rest); err == nil {
			return &id
		}
	}
	return nil
}

func scanRule(row pgxRow) (*Rule, error) {
	var r Rule
	err := row.Scan(&r.ID, &r.Name, &r.Description, &r.Class, &r.Metric, &r.Scope,
		&r.ScopeID, &r.Comparison, &r.Threshold, &r.DurationSeconds,
		&r.RecoveryMargin, &r.WindowDays, &r.Severity, &r.Enabled,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func scanAlert(row pgxRow) (*Alert, error) {
	var a Alert
	var org []byte
	var ruleID []byte
	err := row.Scan(&a.ID, &org, &a.Type, &a.Severity, &a.ResourceType, &a.ResourceID,
		&a.ResourceName, &a.Message, &ruleID, &a.Occurrences, &a.LastSeenAt,
		&a.AcknowledgedAt, &a.ResolvedAt, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	a.Organization = nullUUID(org)
	a.RuleID = nullUUID(ruleID)
	return &a, nil
}
