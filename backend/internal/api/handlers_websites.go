package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
	"github.com/go-chi/chi/v5"
)

func (s *Server) actorFrom(r *http.Request) websites.Actor {
	a := websites.Actor{IP: httpx.ClientIP(r, s.cfg.Server.TrustedProxy)}
	if idt := auth.IdentityFrom(r.Context()); idt != nil {
		a.Label = idt.Username
		uid := idt.UserID
		a.ID = &uid
	}
	return a
}

// identityHasPermission performs an in-handler permission check for fields
// that need different codes within one endpoint. Denials are audited.
func (s *Server) identityHasPermission(r *http.Request, code string) bool {
	idt := auth.IdentityFrom(r.Context())
	if idt == nil {
		return false
	}
	if idt.HasPermission(code) {
		return true
	}
	s.deps.Audit.Log(r.Context(), auditEntryForIdentity(idt, "authz.denied", "permission", code,
		httpx.ClientIP(r, s.cfg.Server.TrustedProxy), r.UserAgent()))
	return false
}

type createWebsiteRequest struct {
	ServerID     string   `json:"server_id"`
	DomainID     string   `json:"domain_id"`
	AliasIDs     []string `json:"alias_domain_ids"`
	PHPVersion   string   `json:"php_version"`
	DocumentRoot string   `json:"document_root"`
}

func (s *Server) handleWebsitesCreate(w http.ResponseWriter, r *http.Request) {
	var req createWebsiteRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	res, err := s.deps.Websites.Create(r.Context(), s.actorFrom(r), websites.CreateInput{
		ServerID:     req.ServerID,
		DomainID:     req.DomainID,
		AliasIDs:     req.AliasIDs,
		PHPVersion:   req.PHPVersion,
		DocumentRoot: req.DocumentRoot,
	})
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"website": res.Website, "job": res.Job})
}

func (s *Server) handleWebsitesList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Websites.List(r.Context(),
		r.URL.Query().Get("search"),
		r.URL.Query().Get("status"),
		r.URL.Query().Get("server_id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"websites": out})
}

func (s *Server) handleWebsitesGet(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Websites.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

type patchWebsiteRequest struct {
	PHPVersion  *string           `json:"php_version"`
	PHPSettings map[string]string `json:"php_settings"`
	AliasIDs    *[]string         `json:"alias_domain_ids"`
}

// PATCH enforces per-field permissions: changing the PHP runtime requires
// websites.php.manage, aliases and PHP settings require websites.config.manage.
func (s *Server) handleWebsitesUpdate(w http.ResponseWriter, r *http.Request) {
	var req patchWebsiteRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if req.PHPVersion == nil && req.PHPSettings == nil && req.AliasIDs == nil {
		Error(w, r, apierror.BadRequest("nothing to update"))
		return
	}
	if req.PHPVersion != nil {
		if !s.identityHasPermission(r, "websites.php.manage") {
			Error(w, r, apierror.Forbidden)
			return
		}
	}
	if req.PHPSettings != nil || req.AliasIDs != nil {
		if !s.identityHasPermission(r, "websites.config.manage") {
			Error(w, r, apierror.Forbidden)
			return
		}
	}
	job, err := s.deps.Websites.Update(r.Context(), s.actorFrom(r), chi.URLParam(r, "id"), websites.UpdateInput{
		PHPVersion:  req.PHPVersion,
		PHPSettings: req.PHPSettings,
		AliasIDs:    req.AliasIDs,
	})
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"job": job})
}

type deleteWebsiteRequest struct {
	DeleteFiles bool `json:"delete_files"`
}

func (s *Server) handleWebsitesDelete(w http.ResponseWriter, r *http.Request) {
	var req deleteWebsiteRequest
	if r.ContentLength > 0 {
		if err := Decode(r, &req); err != nil {
			Error(w, r, err)
			return
		}
	}
	if v := r.URL.Query().Get("delete_files"); v != "" {
		req.DeleteFiles = v == "true" || v == "1"
	}
	job, err := s.deps.Websites.RequestDelete(r.Context(), s.actorFrom(r), chi.URLParam(r, "id"), req.DeleteFiles)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handleWebsitesEnable(w http.ResponseWriter, r *http.Request) {
	s.websiteStateChange(w, r, true)
}

func (s *Server) handleWebsitesDisable(w http.ResponseWriter, r *http.Request) {
	s.websiteStateChange(w, r, false)
}

func (s *Server) websiteStateChange(w http.ResponseWriter, r *http.Request, enable bool) {
	id := chi.URLParam(r, "id")
	if err := s.deps.Websites.SetEnabled(r.Context(), s.actorFrom(r), id, enable); err != nil {
		Error(w, r, err)
		return
	}
	website, err := s.deps.Websites.Get(r.Context(), id)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"status": website.Status})
}

func (s *Server) handleWebsitesReload(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.deps.Websites.Reload(r.Context(), s.actorFrom(r), id); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"reloaded": true})
}

type updateLimitsRequest struct {
	CPULimitPct   int `json:"cpu_limit_pct"`
	MemoryLimitMB int `json:"memory_limit_mb"`
}

func (s *Server) handleWebsitesLimits(w http.ResponseWriter, r *http.Request) {
	var req updateLimitsRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if err := s.deps.Websites.UpdateLimits(r.Context(), s.actorFrom(r),
		chi.URLParam(r, "id"), req.CPULimitPct, req.MemoryLimitMB); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{
		"cpu_limit_pct":   req.CPULimitPct,
		"memory_limit_mb": req.MemoryLimitMB,
		"applied":         true,
	})
}

func (s *Server) handleWebsitesRetry(w http.ResponseWriter, r *http.Request) {
	job, err := s.deps.Websites.Retry(r.Context(), s.actorFrom(r), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handleWebsitesLogs(w http.ResponseWriter, r *http.Request) {
	maxBytes := int64(128 * 1024)
	if v := r.URL.Query().Get("max_bytes"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 && n <= 512*1024 {
			maxBytes = n
		}
	}
	out, err := s.deps.Websites.Logs(r.Context(), chi.URLParam(r, "id"),
		strings.ToLower(r.URL.Query().Get("type")), maxBytes)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

// handleWebsitesHealth derives website health (spec §30) from provisioning
// state + the server's newest telemetry; no external probes.
func (s *Server) handleWebsitesHealth(w http.ResponseWriter, r *http.Request) {
	ws, err := s.deps.Websites.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	health, err := s.deps.MonitoringQuery.WebsiteHealth(r.Context(), ws.ServerID, ws.Status, ws.PHPVersion)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, health)
}

func (s *Server) handleJobsGet(w http.ResponseWriter, r *http.Request) {
	job, err := s.deps.Jobs.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, job)
}
