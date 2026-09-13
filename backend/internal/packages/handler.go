package packages

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

type Handler struct {
	Packages   *Store
	Jobs       *jobs.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
	// EnforceForSite enqueues the unified Phase 9 enforce_limits job for one
	// site (implemented by the api layer over the resource engine). When nil
	// the legacy apply_quota payload is enqueued (backward compatible).
	EnforceForSite func(ctx context.Context, orgID, websiteID uuid.UUID) error
}

func (h *Handler) Register(mux *http.ServeMux) {
	// Package catalog: admin-only management.
	mux.HandleFunc("GET /v1/admin/packages", h.requireAdmin(h.List))
	mux.HandleFunc("POST /v1/admin/packages", h.requireAdmin(h.Create))
	mux.HandleFunc("PATCH /v1/admin/packages/{pkg_id}", h.requireAdmin(h.Update))
	mux.HandleFunc("DELETE /v1/admin/packages/{pkg_id}", h.requireAdmin(h.Delete))
	mux.HandleFunc("POST /v1/admin/organizations/{org_id}/package", h.requireAdmin(h.AssignOrg))
	// Org members can read their own plan + usage.
	mux.HandleFunc("GET /v1/organizations/{org_id}/package", h.requireOrg(organizations.RoleBilling, h.OrgPackage))
}

func (h *Handler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := httpapi.UserFrom(r.Context())
		if !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if user.Role != "admin" {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform administrator access required"))
			return
		}
		next(w, r)
	}
}

func (h *Handler) requireOrg(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if _, apiErr := h.RequireOrg(r, r.PathValue("org_id"), min); apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r)
	}
}

func (h *Handler) auditAdmin(r *http.Request, action, resourceID string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	user, _ := httpapi.UserFrom(r.Context())
	var actorID *uuid.UUID
	if user != nil {
		if uid, err := uuid.Parse(user.ID); err == nil {
			actorID = &uid
		}
	}
	h.Audit.RecordBestEffort(r.Context(), audit.Entry{
		ActorUserID:  actorID,
		ActorType:    audit.ActorUser,
		Action:       action,
		ResourceType: "package",
		ResourceID:   resourceID,
		Metadata:     meta,
		IP:           clientIP(r),
	})
}

func clientIP(r *http.Request) string { return httpapi.ClientIP(r) }

// GET /v1/admin/packages
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.Packages.List(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Package{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"packages": list})
}

type packageRequest struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	MaxWebsites     int      `json:"max_websites"`
	MaxDatabases    int      `json:"max_databases"`
	MaxDiskMB       int      `json:"max_disk_mb"`
	MemoryLimitMB   int      `json:"memory_limit_mb"`
	CPUCores        float64  `json:"cpu_cores"`
	MaxAddonDomains int      `json:"max_addon_domains"`
	MaxSubdomains   int      `json:"max_subdomains"`
	AllowedRuntimes []string `json:"allowed_runtimes"`
	MaxBandwidthMB  int64    `json:"max_bandwidth_mb"`
	IOWeight        int      `json:"io_weight"`
	MaxProcesses    int      `json:"max_processes"`
	MaxPorts        int      `json:"max_ports"`
	MaxBackups      int      `json:"max_backups"`
	PriceMonthly    int      `json:"price_monthly_cents"`
}

func (h *Handler) parseAndValidate(r *http.Request) (*packageRequest, *httpapi.APIError) {
	var req packageRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		return nil, apiErr
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) < 2 || len(req.Name) > 60 {
		return nil, httpapi.ErrValidation("name must be 2-60 characters")
	}
	if req.MaxWebsites <= 0 {
		req.MaxWebsites = 1
	}
	if req.MaxDatabases <= 0 {
		req.MaxDatabases = 1
	}
	if req.MaxDiskMB <= 0 {
		req.MaxDiskMB = 1024
	}
	if req.MemoryLimitMB <= 0 {
		req.MemoryLimitMB = 128
	}
	if req.CPUCores <= 0 {
		req.CPUCores = 1.0
	}
	switch req.Kind {
	case "web":
	default:
		req.Kind = "web"
	}
	if req.IOWeight < 0 || req.IOWeight > 10000 {
		req.IOWeight = 0
	}
	if req.MaxAddonDomains < 0 {
		req.MaxAddonDomains = 0
	}
	if req.MaxSubdomains < 0 {
		req.MaxSubdomains = 0
	}
	if req.MaxAddonDomains+req.MaxSubdomains == 0 && req.MaxWebsites > 0 {
		// sensible defaults when the caller omits domain counts
		req.MaxAddonDomains = req.MaxWebsites
		req.MaxSubdomains = req.MaxWebsites
	}
	// allowed_runtimes is advisory metadata for the UI only; it no longer
	// gates website creation (packages specify resources, not software).
	if len(req.AllowedRuntimes) == 0 {
		req.AllowedRuntimes = []string{"static", "php"}
	}
	if req.PriceMonthly < 0 {
		req.PriceMonthly = 0
	}
	return &req, nil
}

// POST /v1/admin/packages
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	req, apiErr := h.parseAndValidate(r)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	p, err := h.Packages.Create(r.Context(), CreateInput{
		Name: req.Name, Kind: req.Kind, MaxWebsites: req.MaxWebsites, MaxDatabases: req.MaxDatabases,
		MaxDiskMB: req.MaxDiskMB, MemoryLimitMB: req.MemoryLimitMB, CPUCores: req.CPUCores,
		MaxAddonDomains: req.MaxAddonDomains, MaxSubdomains: req.MaxSubdomains,
		AllowedRuntimes: req.AllowedRuntimes, MaxBandwidthMB: req.MaxBandwidthMB, IOWeight: req.IOWeight,
		MaxProcesses: req.MaxProcesses, MaxPorts: req.MaxPorts, MaxBackups: req.MaxBackups,
		PriceMonthly: req.PriceMonthly,
	})
	if err == ErrNameTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("package name already exists"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditAdmin(r, "package.created", p.ID.String(), map[string]any{"name": p.Name})
	httpapi.WriteJSON(w, http.StatusCreated, p)
}

// PATCH /v1/admin/packages/{pkg_id}
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	pkgID, err := uuid.Parse(r.PathValue("pkg_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid package id"))
		return
	}
	req, apiErr := h.parseAndValidate(r)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	p, err := h.Packages.Update(r.Context(), pkgID, CreateInput{
		Name: req.Name, Kind: req.Kind, MaxWebsites: req.MaxWebsites, MaxDatabases: req.MaxDatabases,
		MaxDiskMB: req.MaxDiskMB, MemoryLimitMB: req.MemoryLimitMB, CPUCores: req.CPUCores,
		MaxAddonDomains: req.MaxAddonDomains, MaxSubdomains: req.MaxSubdomains,
		AllowedRuntimes: req.AllowedRuntimes, MaxBandwidthMB: req.MaxBandwidthMB, IOWeight: req.IOWeight,
		MaxProcesses: req.MaxProcesses, MaxPorts: req.MaxPorts, MaxBackups: req.MaxBackups,
		PriceMonthly: req.PriceMonthly,
	})
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("package not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditAdmin(r, "package.updated", p.ID.String(), map[string]any{"name": p.Name})
	httpapi.WriteJSON(w, http.StatusOK, p)
}

// DELETE /v1/admin/packages/{pkg_id}
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	pkgID, err := uuid.Parse(r.PathValue("pkg_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid package id"))
		return
	}
	err = h.Packages.Delete(r.Context(), pkgID)
	switch err {
	case ErrNotFound:
		httpapi.RespondError(w, httpapi.ErrNotFound("package not found"))
		return
	case ErrDefaultStays:
		httpapi.RespondError(w, httpapi.ErrConflict("the default package cannot be deleted"))
		return
	case ErrInUse:
		httpapi.RespondError(w, httpapi.ErrConflict("package is assigned to organizations"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditAdmin(r, "package.deleted", pkgID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// POST /v1/admin/organizations/{org_id}/package  {"package_id": "..."}
func (h *Handler) AssignOrg(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PackageID string `json:"package_id"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	pkgID, err := uuid.Parse(req.PackageID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid package_id"))
		return
	}
	if _, err := h.Packages.GetByID(r.Context(), pkgID); err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("package not found"))
		return
	}
	affected, err := h.Packages.Assign(r.Context(), orgID_param(r), pkgID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("organization not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// Reconcile limits on the affected sites. Phase 9: the unified engine's
	// enforce_limits job (full plan matrix) when wired; legacy apply_quota
	// otherwise.
	for _, a := range affected {
		var enqueued bool
		if h.EnforceForSite != nil {
			if err := h.EnforceForSite(r.Context(), orgID_param(r), a.ID); err == nil {
				enqueued = true
			}
		}
		if !enqueued {
			payload := QuotaPayload{
				WebsiteID:     a.ID,
				MaxDiskMB:     a.MaxDiskMB,
				MemoryLimitMB: a.MemoryLimitMB,
				CPUCores:      a.CPUCores,
			}
			if _, err := h.Jobs.EnqueueForWebsite(r.Context(), a.ID, "apply_quota", payload); err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
		}
	}
	h.auditAdmin(r, "package.assigned", pkgID.String(), map[string]any{"org": orgID_param(r).String(), "sites_reconciled": len(affected)})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"assigned": true, "sites_reconciled": len(affected)})
}

// GET /v1/organizations/{org_id}/package — plan + usage for org members.
func (h *Handler) OrgPackage(w http.ResponseWriter, r *http.Request) {
	orgID, apiErr := h.RequireOrg(r, r.PathValue("org_id"), organizations.RoleBilling)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	p, err := h.Packages.ForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	u, err := h.Packages.UsageForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"package": p, "usage": u})
}

func orgID_param(r *http.Request) uuid.UUID {
	id, _ := uuid.Parse(r.PathValue("org_id"))
	return id
}
