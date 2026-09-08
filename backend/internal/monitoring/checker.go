package monitoring

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Checker struct {
	Pool     *pgxpool.Pool
	Client   *http.Client
	interval time.Duration
	stop     chan struct{}
	once     sync.Once
}

func NewChecker(pool *pgxpool.Pool, interval time.Duration) *Checker {
	return &Checker{
		Pool:     pool,
		Client:   &http.Client{Timeout: 10 * time.Second},
		interval: interval,
		stop:     make(chan struct{}),
	}
}

func (c *Checker) Stop() { c.once.Do(func() { close(c.stop) }) }

type checkTarget struct {
	WebsiteID uuid.UUID
	OrgID     uuid.UUID
	Domain    string
}

// Run loops health checks until stopped. Each pass: pick target domains
// (primary of ready websites), issue HTTP GET, record result, manage alerts.
func (c *Checker) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stop:
			return
		case <-ticker.C:
			c.Pass(ctx)
		}
	}
}

// Pass performs one round of checks over all ready websites' primary domains.
func (c *Checker) Pass(ctx context.Context) {
	rows, err := c.Pool.Query(ctx, `
		SELECT w.id, w.organization_id, d.domain
		FROM websites w
		JOIN domains d ON d.website_id = w.id AND d.kind = 'primary'
		WHERE w.status = 'ready'
		LIMIT 500
	`)
	if err != nil {
		slog.Warn("health check query failed", "err", err)
		return
	}
	var targets []checkTarget
	for rows.Next() {
		var t checkTarget
		if err := rows.Scan(&t.WebsiteID, &t.OrgID, &t.Domain); err != nil {
			rows.Close()
			return
		}
		targets = append(targets, t)
	}
	rows.Close()

	for _, t := range targets {
		code, latency, err := c.check(ctx, t.Domain)
		up := err == nil && code >= 200 && code < 400
		if _, err := c.Pool.Exec(ctx, `
			INSERT INTO http_checks (website_id, organization_id, domain, status_code, latency_ms, up, error)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, t.WebsiteID, t.OrgID, t.Domain, code, latency.Milliseconds(), up, errString(err)); err != nil {
			slog.Warn("http check insert failed", "err", err)
		}
		if up {
			c.resolveAlert(ctx, t.OrgID, "website.down", t.WebsiteID.String())
		} else {
			c.raiseAlert(ctx, t.OrgID, "website.down", "critical", "website", t.WebsiteID.String(), t.Domain,
				"site is not responding"+errSuffix(err))
		}
	}
}

func (c *Checker) check(ctx context.Context, domain string) (int, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+domain+"/", nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("User-Agent", "EpicPanel-HealthCheck/1.0")
	start := time.Now()
	resp, err := c.Client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return 0, latency, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, latency, nil
}

// raiseAlert inserts an alert if no unresolved one exists for (type, resource).
func (c *Checker) raiseAlert(ctx context.Context, orgID uuid.UUID, typ, severity, resType, resID, resName, message string) {
	tag, err := c.Pool.Exec(ctx, `
		INSERT INTO alerts (organization_id, type, severity, resource_type, resource_id, resource_name, message)
		SELECT $1, $2, $3, $4, $5, $6, $7
		WHERE NOT EXISTS (
			SELECT 1 FROM alerts WHERE type = $2 AND resource_id = $5 AND resolved_at IS NULL
		)
	`, orgID, typ, severity, resType, resID, resName, message)
	if err != nil {
		slog.Warn("alert insert failed", "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Warn("alert raised", "type", typ, "resource", resName, "message", message)
	}
}

func (c *Checker) resolveAlert(ctx context.Context, orgID uuid.UUID, typ, resID string) {
	tag, err := c.Pool.Exec(ctx, `
		UPDATE alerts SET resolved_at = now()
		WHERE organization_id = $1 AND type = $2 AND resource_id = $3 AND resolved_at IS NULL
	`, orgID, typ, resID)
	if err != nil {
		slog.Warn("alert resolve failed", "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("alert resolved", "type", typ, "resource", resID)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func errSuffix(err error) string {
	if err == nil {
		return ""
	}
	return ": " + err.Error()
}
