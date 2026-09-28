package websites

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/nginxcfg"
)

// maxConfigJSON caps the structured config document (well above any sane
// configuration; the model is structured, not free-form).
const maxConfigJSON = 64 * 1024

// siteRulesFor builds the validation context for a website: what the
// managed vhost looks like (PHP handler? reverse proxy?) and which
// loopback ports belong to THIS site.
func (h *Handler) siteRulesFor(ctx context.Context, ws *Website) nginxcfg.SiteRules {
	rules := nginxcfg.SiteRules{PHP: ws.Runtime == RuntimePHP}
	if isAppRuntime(ws.Runtime) {
		rules.Proxy = true
	}
	own := map[int]bool{}
	if ws.AppPort > 0 {
		own[ws.AppPort] = true
		rules.OwnPorts = append(rules.OwnPorts, ws.AppPort)
	}
	if ws.BackendPort > 0 {
		own[ws.BackendPort] = true
		rules.OwnPorts = append(rules.OwnPorts, ws.BackendPort)
	}
	// PortOwner: a loopback port allocated to a DIFFERENT website on the
	// same server must not be a proxy target (multi-tenant routing, ADR-068).
	if ws.ServerID != uuid.Nil {
		other := map[int]bool{}
		if appPorts, err := h.Websites.UsedAppPorts(ctx, ws.ServerID); err == nil {
			for p := range appPorts {
				if !own[p] {
					other[p] = true
				}
			}
		}
		if backendPorts, err := h.Websites.UsedBackendPorts(ctx, ws.ServerID); err == nil {
			for p := range backendPorts {
				if !own[p] {
					other[p] = true
				}
			}
		}
		if len(other) > 0 {
			rules.PortOwner = func(port int) string {
				if other[port] {
					return "another-website"
				}
				return ""
			}
		}
	}
	return rules
}

// mapConfigErr converts an nginxcfg validation error to a 422 with the
// friendly, actionable message (spec §25: never a bare "unsupported
// directive").
func mapConfigErr(err error) *httpapi.APIError {
	if errors.Is(err, nginxcfg.ErrValidation) {
		return httpapi.ErrValidation(err.Error())
	}
	return httpapi.ErrInternal(err)
}

// GET .../websites/{id}/config
// Returns the legacy rewrite_rules string (still consumed by older
// clients) plus the structured config document and its version.
func (h *Handler) GetConfig(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	cfg, err := h.Configs.Get(r.Context(), ws.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, cfg)
}

// PUT .../websites/{id}/config {"config": {...SiteConfig}}
// Structured, context-aware replacement of the whole customization
// document. Validated strictly (save-time), versioned, then the desired
// state converges via the idempotent provision job.
func (h *Handler) SetSiteConfig(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	cfg, apiErr := readSiteConfig(r)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rules := h.siteRulesFor(r.Context(), ws)
	if err := nginxcfg.ValidateSiteConfig(cfg, rules); err != nil {
		httpapi.RespondError(w, mapConfigErr(err))
		return
	}
	actor := actorIDFrom(r)
	saved, err := h.Configs.SetConfig(r.Context(), ws.ID, cfg, actor)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.site_config_updated", "website", ws.ID.String(), map[string]any{"version": saved.Version})
	h.convergeConfig(w, r, ws, orgID, saved)
}

// POST .../websites/{id}/config/validate {"config": {...}}
// Dry-run: full validation, nothing stored, nothing converged.
func (h *Handler) ValidateSiteConfig(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	cfg, apiErr := readSiteConfig(r)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rules := h.siteRulesFor(r.Context(), ws)
	if err := nginxcfg.ValidateSiteConfig(cfg, rules); err != nil {
		httpapi.RespondError(w, mapConfigErr(err))
		return
	}
	// Render check: report what the user layer would silently drop, so
	// the UI can surface drop candidates as explicit warnings.
	_, dropped := nginxcfg.BuildUserLayer(cfg, rules)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"valid": true, "warnings": dropped})
}

// GET .../websites/{id}/config/versions
func (h *Handler) ListConfigVersions(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	versions, err := h.Configs.ListVersions(r.Context(), ws.ID, 20)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

// POST .../websites/{id}/config/rollback {"to_version": N}
// Rollback is a NEW version containing the old document (append-only
// history; version numbers never decrease).
func (h *Handler) RollbackConfig(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		ToVersion int `json:"to_version"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	cfg, err := h.Configs.GetVersion(r.Context(), ws.ID, req.ToVersion)
	if errors.Is(err, ErrConfigNotFound) {
		httpapi.RespondError(w, httpapi.ErrNotFound("config version not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// The site's serving shape may have changed since the version was
	// stored (runtime switch, app removed) — re-validate before applying.
	rules := h.siteRulesFor(r.Context(), ws)
	if err := nginxcfg.ValidateSiteConfig(cfg, rules); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("version no longer compatible with this site: "+err.Error()))
		return
	}
	saved, err := h.Configs.SetConfig(r.Context(), ws.ID, cfg, actorIDFrom(r))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.site_config_rollback", "website", ws.ID.String(), map[string]any{"from_version": req.ToVersion, "version": saved.Version})
	h.convergeConfig(w, r, ws, orgID, saved)
}

// readSiteConfig parses and size-caps the request's config document.
func readSiteConfig(r *http.Request) (*nginxcfg.SiteConfig, *httpapi.APIError) {
	var req struct {
		Config json.RawMessage `json:"config"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		return nil, apiErr
	}
	if len(req.Config) > maxConfigJSON {
		return nil, httpapi.ErrValidation("configuration document too large (max 64KB)")
	}
	cfg := &nginxcfg.SiteConfig{}
	if err := json.Unmarshal(req.Config, cfg); err != nil {
		return nil, httpapi.ErrValidation("invalid configuration document: " + err.Error())
	}
	cfg.Schema = 2
	return cfg, nil
}

// actorIDFrom resolves the acting user for version attribution.
func actorIDFrom(r *http.Request) *uuid.UUID {
	if usr, ok := httpapi.UserFrom(r.Context()); ok {
		if uid, err := uuid.Parse(usr.ID); err == nil {
			return &uid
		}
	}
	return nil
}

// convergeConfig re-enqueues the idempotent provision job so the agent
// re-renders the vhost with the new config (ready sites only).
func (h *Handler) convergeConfig(w http.ResponseWriter, r *http.Request, ws *Website, orgID uuid.UUID, cfg *WebsiteConfig) {
	if ws.Status == StatusReady || ws.Status == StatusFailed {
		payload, apiErr := h.buildDesiredPayload(r.Context(), ws, orgID, ws.UnixUser, ws.RuntimeVersion)
		if apiErr == nil {
			_, _ = h.Jobs.EnqueueIdempotent(r.Context(), ws.ServerID, &ws.ID, jobs.TypeProvisionWebsite, payload, "provision_website_"+ws.ID.String())
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, cfg)
}

// POST .../websites/{id}/reconcile (admin+)
// On-demand desired-state converge: re-enqueues the idempotent provision
// job so the agent re-renders the vhost/FPM pool from stored state. The
// same path the hourly sweep and every config save use — exposed for ops
// recovery (e.g. after manual server work or a failed deploy converge).
func (h *Handler) Reconcile(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	payload, apiErr := h.buildDesiredPayload(r.Context(), ws, orgID, ws.UnixUser, ws.RuntimeVersion)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	job, err := h.Jobs.EnqueueIdempotent(r.Context(), ws.ServerID, &ws.ID, jobs.TypeProvisionWebsite, payload, "provision_website_"+ws.ID.String())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.reconcile_requested", "website", ws.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

// PUT .../websites/{id}/config/rewrite {"rewrite_rules": "..."}
// LEGACY endpoint, contract unchanged: a raw server-context snippet.
// Validation now runs through the context-aware core (superset of the old
// flat allowlist — every previously accepted snippet still passes). The
// string is stored in rewrite_rules and rendered when no structured
// config document exists for the site.
func (h *Handler) SetRewriteRules(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		RewriteRules string `json:"rewrite_rules"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if len(req.RewriteRules) > 16384 {
		httpapi.RespondError(w, httpapi.ErrValidation("rewrite rules too large (max 16KB)"))
		return
	}
	if err := nginxcfg.ValidateSnippet(req.RewriteRules, nginxcfg.CtxServer, h.siteRulesFor(r.Context(), ws)); err != nil {
		httpapi.RespondError(w, mapConfigErr(err))
		return
	}
	cfg, err := h.Configs.SetRules(r.Context(), ws.ID, req.RewriteRules)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.rewrite_rules_updated", "website", ws.ID.String(), nil)
	h.convergeConfig(w, r, ws, orgID, cfg)
}
