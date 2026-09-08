package monitoring

import (
	"context"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

type Handler struct {
	Pool       *pgxpool.Pool
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/alerts", h.requireOrg(organizations.RoleBilling, h.ListAlerts))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/health", h.requireOrg(organizations.RoleBilling, h.WebsiteHealth))
	mux.HandleFunc("GET /v1/organizations/{org_id}/servers/{server_id}/metrics/history", h.requireOrg(organizations.RoleBilling, h.ServerMetricsHistory))
}

func (h *Handler) requireOrg(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		orgID, apiErr := h.RequireOrg(r, r.PathValue("org_id"), min)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r.WithContext(withOrgID(r.Context(), orgID)))
	}
}

type orgIDCtxKey struct{}

func withOrgID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, orgIDCtxKey{}, id)
}

func OrgIDFromRequest(r *http.Request) (uuid.UUID, bool) {
	id, ok := r.Context().Value(orgIDCtxKey{}).(uuid.UUID)
	return id, ok
}

func (h *Handler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	showResolved := r.URL.Query().Get("resolved") == "true"
	query := `
		SELECT id, type, severity, resource_type, resource_id, resource_name, message, resolved_at, created_at
		FROM alerts WHERE organization_id = $1`
	if !showResolved {
		query += ` AND resolved_at IS NULL`
	}
	query += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := h.Pool.Query(r.Context(), query, orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	defer rows.Close()
	alerts := []map[string]any{}
	for rows.Next() {
		var id, resType, resID, resName, typ, severity, message string
		var resolved, created *time.Time
		if err := rows.Scan(&id, &typ, &severity, &resType, &resID, &resName, &message, &resolved, &created); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		entry := map[string]any{
			"id": id, "type": typ, "severity": severity,
			"resource_type": resType, "resource_id": resID, "resource_name": resName,
			"message": message, "created_at": created,
		}
		if resolved != nil {
			entry["resolved_at"] = resolved
		}
		alerts = append(alerts, entry)
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (h *Handler) WebsiteHealth(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	websiteID, err := uuid.Parse(r.PathValue("website_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid website id"))
		return
	}
	rows, err := h.Pool.Query(r.Context(), `
		SELECT status_code, latency_ms, up, error, checked_at
		FROM http_checks WHERE website_id = $1 AND organization_id = $2
		ORDER BY checked_at DESC LIMIT 50
	`, websiteID, orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	defer rows.Close()
	checks := []map[string]any{}
	for rows.Next() {
		var code, latency int
		var up bool
		var errMsg string
		var checked time.Time
		if err := rows.Scan(&code, &latency, &up, &errMsg, &checked); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		checks = append(checks, map[string]any{
			"status_code": code, "latency_ms": latency, "up": up,
			"error": errMsg, "checked_at": checked,
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"checks": checks})
}

func (h *Handler) ServerMetricsHistory(w http.ResponseWriter, r *http.Request) {
	// Membership auth already applied by requireOrg; the fleet is shared.
	if _, ok := OrgIDFromRequest(r); !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	serverID, err := uuid.Parse(r.PathValue("server_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid server id"))
		return
	}
	// Servers are shared across organizations; only membership auth is applied.
	// (Audit fix: this used Pool.Query with unclosed rows — one leaked pooled
	// connection per call. QueryRow has no rows to close.)
	var exists bool
	if err := h.Pool.QueryRow(r.Context(), `SELECT true FROM servers WHERE id = $1`, serverID).Scan(&exists); err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("server not found"))
		return
	}
	// range=24h (default) reads raw 10s rows; 7d/30d read the 5-minute
	// rollup table (raw rows are pruned after 24h).
	rng := r.URL.Query().Get("range")
	var rows pgx.Rows
	if rng == "7d" || rng == "30d" {
		rows, err = h.Pool.Query(r.Context(), `
			SELECT cpu_max, mem_used_avg, mem_total_max, load1_avg, rx_avg, tx_avg, bucket
			FROM server_metrics_rollup_5m WHERE server_id = $1
			ORDER BY bucket DESC LIMIT 500
		`, serverID)
	} else {
		rows, err = h.Pool.Query(r.Context(), `
			SELECT cpu_percent, memory_used_bytes, memory_total_bytes, disk_used_bytes, disk_total_bytes, load1, collected_at
			FROM server_metrics WHERE server_id = $1
			ORDER BY collected_at DESC LIMIT 200
		`, serverID)
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	defer rows.Close()
	points := []map[string]any{}
	for rows.Next() {
		if rng == "7d" || rng == "30d" {
			var cpu, memU, memT, l1, rx, tx float64
			var at time.Time
			if err := rows.Scan(&cpu, &memU, &memT, &l1, &rx, &tx, &at); err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			points = append(points, map[string]any{
				"cpu_percent": cpu, "memory_used": memU, "memory_total": memT,
				"load1": l1, "rx_bps": rx, "tx_bps": tx, "collected_at": at,
			})
			continue
		}
		var cpu, l1 float64
		var memU, memT, diskU, diskT int64
		var at time.Time
		if err := rows.Scan(&cpu, &memU, &memT, &diskU, &diskT, &l1, &at); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		points = append(points, map[string]any{
			"cpu_percent": cpu, "memory_used": memU, "memory_total": memT,
			"disk_used": diskU, "disk_total": diskT, "load1": l1, "collected_at": at,
		})
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"metrics": points})
}

var errOrgContext = errStr("org id missing from request context")

type errStr string

func (e errStr) Error() string { return string(e) }
