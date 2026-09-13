package alerts

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a rule/alert row does not exist.
var ErrNotFound = errors.New("alerts: not found")

type pgxRow interface{ Scan(dest ...any) error }

func nullUUID(b []byte) *uuid.UUID {
	if len(b) != 16 {
		return nil
	}
	id, err := uuid.FromBytes(b)
	if err != nil {
		return nil
	}
	return &id
}

// Sweep runs one periodic evaluation pass. Sources (all async — never in the
// request path):
//   - threshold rules: latest live metrics sample per node/account
//   - state rules: node registry status, service status, backup/billing events
//     (those arrive via bus subscription; this sweep covers the periodic
//     sources: SSL expiry windows + service freshness from metrics history)
//   - time rules: SSL expiration windows from domains
func (e *Engine) Sweep(ctx context.Context, sweepInterval time.Duration) {
	rules, err := e.Store.ListRules(ctx)
	if err != nil {
		slog.Warn("alert sweep: rules unavailable", "err", err)
		return
	}
	enabled := make([]Rule, 0, len(rules))
	for _, r := range rules {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	if len(enabled) == 0 {
		return
	}
	e.sweepSSL(ctx, enabled)
}

// sweepSSL evaluates time-based SSL expiration rules from domains.
func (e *Engine) sweepSSL(ctx context.Context, rules []Rule) {
	rows, err := e.Store.Pool.Query(ctx, `
		SELECT d.id, d.domain, d.ssl_expires_at, d.organization_id
		FROM domains d
		WHERE d.ssl_expires_at IS NOT NULL
		LIMIT 5000`)
	if err != nil {
		return // domains table may not carry ssl columns in fresh DBs
	}
	defer rows.Close()
	type dom struct {
		id      uuid.UUID
		name    string
		expires time.Time
		org     uuid.UUID
	}
	var doms []dom
	for rows.Next() {
		var d dom
		var exp *time.Time
		if err := rows.Scan(&d.id, &d.name, &exp, &d.org); err != nil || exp == nil {
			continue
		}
		d.expires = *exp
		doms = append(doms, d)
	}
	for _, d := range doms {
		days := int(time.Until(d.expires).Hours() / 24)
		for _, r := range rules {
			if r.Class == "time" && r.Metric == "ssl_expiration" {
				e.EvaluateTimeWindow(ctx, r, "org:"+d.org.String()+"|domain:"+d.id.String(), days)
			}
		}
	}
}

// NodeSamplePoint is one threshold input from the metrics layer.
type NodeSamplePoint struct {
	ServerID uuid.UUID
	OrgID    *uuid.UUID
	CPUPct   float64
	RAMPct   float64
	DiskPct  float64
	Offline  bool
	Services map[string]bool // service -> up
}

// EvaluateNode feeds one node sample through every enabled rule.
func (e *Engine) EvaluateNode(ctx context.Context, r Rule, p NodeSamplePoint) {
	subject := "node:" + p.ServerID.String()
	if p.OrgID != nil {
		subject = "org:" + p.OrgID.String() + "|" + subject
	}
	switch r.Metric {
	case "cpu":
		e.EvaluateThreshold(ctx, r, subject, p.CPUPct)
	case "ram":
		e.EvaluateThreshold(ctx, r, subject, p.RAMPct)
	case "disk":
		e.EvaluateThreshold(ctx, r, subject, p.DiskPct)
	case "node_offline":
		e.EvaluateState(ctx, r, subject, p.Offline, "node offline")
	case "service_down":
		for svc, up := range p.Services {
			e.EvaluateState(ctx, r, subject+"|service:"+svc, !up, "service down: "+svc)
		}
	}
}

// StartSweeper runs the periodic sweep loop (call once from server startup).
func (e *Engine) StartSweeper(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				e.Sweep(ctx, interval)
			}
		}
	}()
}

var _ = pgx.ErrNoRows
