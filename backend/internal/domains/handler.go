package domains

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

const maxAliasesPerWebsite = 20

type Handler struct {
	Domains    *Store
	Jobs       *jobs.Store
	Orgs       *organizations.Store
	Websites   *websites.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
	// OnDomainsChanged is invoked after any change affecting a website's
	// serving configuration (vhost reconcile is the subscriber).
	OnDomainsChanged func(ctx context.Context, websiteID, orgID, serverID uuid.UUID)
}

// OnSSLEvent is the Phase-2 event bus hook (wired in api/server.go; nil-safe
// at all call sites). Events: "ssl.issued", "ssl.failed",
// "ssl.renewal_started" with meta domain/expires_at/error.
var OnSSLEvent func(event string, domain string, meta map[string]any)

// emitSSLEvent invokes the hook, never panicking the caller.
func emitSSLEvent(event, domain string, meta map[string]any) {
	if OnSSLEvent == nil {
		return
	}
	OnSSLEvent(event, domain, meta)
}

var domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/domains", h.requireOrg(organizations.RoleBilling, h.List))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/domains", h.requireOrg(organizations.RoleDeveloper, h.Create))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/domains/{domain_id}", h.requireOrg(organizations.RoleDeveloper, h.UpdateDomain))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/websites/{website_id}/domains/{domain_id}", h.requireOrg(organizations.RoleAdmin, h.Delete))
	mux.HandleFunc("POST /v1/organizations/{org_id}/domains/{domain_id}/ssl", h.requireOrg(organizations.RoleDeveloper, h.SetSSL))
	mux.HandleFunc("POST /v1/organizations/{org_id}/domains/{domain_id}/verify-dns", h.requireOrg(organizations.RoleDeveloper, h.VerifyDNS))
	mux.HandleFunc("GET /v1/organizations/{org_id}/domains", h.requireOrg(organizations.RoleBilling, h.ListOrg))
	(&redirectsHandler{h}).Register(mux)
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

func (h *Handler) audit(r *http.Request, orgID *uuid.UUID, action, resourceType, resourceID string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	var actorID *uuid.UUID
	if usr, ok := httpapi.UserFrom(r.Context()); ok {
		if uid, err := uuid.Parse(usr.ID); err == nil {
			actorID = &uid
		}
	}
	h.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		Metadata:       meta,
		IP:             clientIP(r),
	})
}

func clientIP(r *http.Request) string { return httpapi.ClientIP(r) }

var errOrgContext = errStr("org id missing from request context")

type errStr string

func (e errStr) Error() string { return string(e) }

func (h *Handler) websiteFromPath(r *http.Request, orgID uuid.UUID) (*websites.Website, *httpapi.APIError) {
	websiteID, err := uuid.Parse(r.PathValue("website_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid website id")
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, websiteID)
	if err == websites.ErrNotFound {
		return nil, httpapi.ErrNotFound("website not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return ws, nil
}

// GET .../domains
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
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
	list, err := h.Domains.ListForWebsite(r.Context(), ws.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Domain{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"domains": list})
}

// POST .../domains  {"domain": "www.example.com", "kind": "alias"}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
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
	if ws.Status == websites.StatusDeleting || ws.Status == websites.StatusDeleted {
		httpapi.RespondError(w, httpapi.ErrConflict("website is being deleted"))
		return
	}

	var req struct {
		Domain        string `json:"domain"`
		Kind          string `json:"kind"`
		DocrootSuffix string `json:"docroot_suffix"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Domain = strings.ToLower(strings.TrimSpace(req.Domain))
	if len(req.Domain) > 253 || !domainRe.MatchString(req.Domain) {
		httpapi.RespondError(w, httpapi.ErrValidation("domain is not a valid hostname"))
		return
	}
	kind := KindAlias
	if req.Kind != "" {
		if req.Kind != string(KindPrimary) && req.Kind != string(KindAlias) {
			httpapi.RespondError(w, httpapi.ErrValidation("kind must be primary or alias"))
			return
		}
		kind = Kind(req.Kind)
	}

	// Only one primary per website; websites always have exactly one.
	if kind == KindPrimary {
		httpapi.RespondError(w, httpapi.ErrConflict("website already has a primary domain; add aliases instead"))
		return
	}
	count, err := h.Domains.CountByWebsite(r.Context(), ws.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if count >= maxAliasesPerWebsite {
		httpapi.RespondError(w, httpapi.ErrConflict("domain limit reached for this website"))
		return
	}

	d, err := h.Domains.Create(r.Context(), orgID, ws.ID, req.Domain, kind)
	if err == ErrTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("domain is already in use by another website"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// Optional per-domain running directory (addon domains can serve any
	// relative path of the site tree).
	suffix := strings.Trim(strings.TrimSpace(req.DocrootSuffix), "/")
	if suffix != "" {
		// Strict charset (rendered into nginx root / pool directives).
		if !validDocrootRe.MatchString(suffix) || strings.Contains(suffix, "..") || strings.Count(suffix, "/") > 3 || len(suffix) > 100 {
			_ = h.Domains.Delete(r.Context(), orgID, d.ID)
			httpapi.RespondError(w, httpapi.ErrValidation("docroot_suffix allows only letters, digits, dot, underscore, hyphen and / (max 4 levels)"))
			return
		}
		if err := h.Domains.SetDocrootSuffix(r.Context(), d.ID, suffix); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}

	h.audit(r, &orgID, "domain.added", "domain", d.ID.String(), map[string]any{"domain": req.Domain})
	h.notifyChanged(orgID, ws)
	httpapi.WriteJSON(w, http.StatusCreated, d)
}

// DELETE .../domains/{domain_id} (aliases only)
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
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
	domainID, err := uuid.Parse(r.PathValue("domain_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid domain id"))
		return
	}
	d, err := h.Domains.GetByID(r.Context(), orgID, domainID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("domain not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if d.WebsiteID != ws.ID {
		httpapi.RespondError(w, httpapi.ErrNotFound("domain not found"))
		return
	}
	if err := h.Domains.Delete(r.Context(), orgID, domainID); err != nil {
		if err == ErrNotFound {
			httpapi.RespondError(w, httpapi.ErrConflict("primary domain cannot be deleted; delete the website instead"))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "domain.removed", "domain", d.ID.String(), map[string]any{"domain": d.Domain})
	h.notifyChanged(orgID, ws)
	w.WriteHeader(http.StatusNoContent)
}

// GET /v1/organizations/{org_id}/domains — org-wide SSL dashboard list.
func (h *Handler) ListOrg(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	list, err := h.Domains.ListForOrganization(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Domain{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"domains": list})
}

// PATCH /v1/organizations/{org_id}/domains/{domain_id}
// {"docroot_suffix": "forum/public"} — per-domain running directory.
func (h *Handler) UpdateDomain(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	domainID, err := uuid.Parse(r.PathValue("domain_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid domain id"))
		return
	}
	d, err := h.Domains.GetByID(r.Context(), orgID, domainID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("domain not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var req struct {
		DocrootSuffix string `json:"docroot_suffix"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	suffix := strings.Trim(strings.TrimSpace(req.DocrootSuffix), "/")
	if suffix != "" {
		if strings.Contains(suffix, "..") || strings.Count(suffix, "/") > 3 || len(suffix) > 100 {
			httpapi.RespondError(w, httpapi.ErrValidation("docroot_suffix must be a short relative path"))
			return
		}
	}
	if err := h.Domains.SetDocrootSuffix(r.Context(), domainID, suffix); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "domain.docroot_updated", "domain", d.ID.String(), map[string]any{"docroot_suffix": suffix})
	if ws, err := h.Websites.GetByIDAny(r.Context(), d.WebsiteID); err == nil {
		h.notifyChanged(orgID, ws)
	}
	updated, _ := h.Domains.GetByID(r.Context(), orgID, domainID)
	httpapi.WriteJSON(w, http.StatusOK, updated)
}

// POST .../ssl  {"mode": "none"|"selfsigned"|"letsencrypt"}
func (h *Handler) SetSSL(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	domainID, err := uuid.Parse(r.PathValue("domain_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid domain id"))
		return
	}
	d, err := h.Domains.GetByID(r.Context(), orgID, domainID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("domain not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	var req struct {
		Mode string `json:"mode"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	mode := SSLMode(req.Mode)
	switch mode {
	case SSLNone, SSLSelfSigned, SSLLetsEnc:
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("mode must be one of: none, selfsigned, letsencrypt"))
		return
	}

	updated, err := h.Domains.SetSSLMode(r.Context(), domainID, mode)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	// Vhost reconcile MUST be enqueued before certificate issuance: jobs run
	// in order per server, and the ACME challenge is served by the freshly
	// rendered :80 vhost — issuing against the stale vhost fails the first
	// attempt (and on older agents, forever).
	if ws, err := h.Websites.GetByIDAny(r.Context(), d.WebsiteID); err == nil {
		h.notifyChanged(orgID, ws)
	}

	if mode != SSLNone {
		payload := CertPayload{DomainID: updated.ID, Domain: updated.Domain, Mode: string(mode)}
		if _, err := h.Jobs.EnqueueForWebsite(r.Context(), updated.WebsiteID, jobs.TypeIssueCertificate, payload); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}

	h.audit(r, &orgID, "domain.ssl_mode_set", "domain", d.ID.String(), map[string]any{"mode": req.Mode})
	httpapi.WriteJSON(w, http.StatusAccepted, updated)
}

// POST .../verify-dns — enqueues a DNS verification job on the server.
func (h *Handler) VerifyDNS(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	domainID, err := uuid.Parse(r.PathValue("domain_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid domain id"))
		return
	}
	d, err := h.Domains.GetByID(r.Context(), orgID, domainID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("domain not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := VerifyDNSPayload{DomainID: d.ID, Domain: d.Domain}
	if _, err := h.Jobs.EnqueueForWebsite(r.Context(), d.WebsiteID, jobs.TypeVerifyDomain, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "domain.dns_check_requested", "domain", d.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "verification queued"})
}

func (h *Handler) notifyChanged(orgID uuid.UUID, ws *websites.Website) {
	if h.OnDomainsChanged == nil {
		return
	}
	if ws != nil {
		h.OnDomainsChanged(context.Background(), ws.ID, orgID, ws.ServerID)
	}
}

// --- job payloads (shared with agent) ---

type CertPayload struct {
	DomainID uuid.UUID `json:"domain_id"`
	Domain   string    `json:"domain"`
	Mode     string    `json:"mode"`
}

type VerifyDNSPayload struct {
	DomainID uuid.UUID `json:"domain_id"`
	Domain   string    `json:"domain"`
}

// validDocrootRe is the strict charset for per-domain running directories.
var validDocrootRe = regexp.MustCompile(`^[a-zA-Z0-9._/-]+$`)
