package runtimes

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

var extNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,30}$`)

// phpExtensionCatalog is the friendly offering list for the UI.
var phpExtensionCatalog = []map[string]string{
	{"name": "mysql", "label": "MySQL / MariaDB (mysqli + pdo_mysql)"},
	{"name": "pgsql", "label": "PostgreSQL (pgsql + pdo_pgsql)"},
	{"name": "curl", "label": "cURL"},
	{"name": "gd", "label": "GD (images)"},
	{"name": "mbstring", "label": "Multibyte strings"},
	{"name": "xml", "label": "XML / SimpleXML / DOM"},
	{"name": "zip", "label": "Zip"},
	{"name": "intl", "label": "Internationalization"},
	{"name": "opcache", "label": "OPcache"},
	{"name": "imagick", "label": "ImageMagick"},
	{"name": "redis", "label": "Redis"},
	{"name": "sqlite3", "label": "SQLite3"},
	{"name": "bcmath", "label": "BCMath"},
	{"name": "soap", "label": "SOAP"},
	{"name": "xsl", "label": "XSL"},
	{"name": "ldap", "label": "LDAP"},
}

// extensionJobPayload matches the agent's ExtensionJobPayload.
type extensionJobPayload struct {
	ExtensionID string `json:"extension_id"`
	RuntimeID   string `json:"runtime_id"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Name        string `json:"name"`
}

// GET .../runtimes/{id}/extensions — managed extensions + catalog.
func (h *Handler) ListExtensions(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srvID, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rtID, err := uuid.Parse(r.PathValue("runtime_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid runtime id"))
		return
	}
	rt, err := h.Runtimes.GetByID(r.Context(), srvID, rtID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("runtime not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	list, err := h.Extensions.ListForRuntime(r.Context(), rt.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Extension{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"extensions": list, "catalog": phpExtensionCatalog, "runtime_type": rt.Type, "runtime_version": rt.Version})
}

// POST .../runtimes/{id}/extensions {"name": "redis", "action": "install"|"remove"}
func (h *Handler) ManageExtension(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	srvID, apiErr := h.serverFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	rtID, err := uuid.Parse(r.PathValue("runtime_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid runtime id"))
		return
	}
	rt, err := h.Runtimes.GetByID(r.Context(), srvID, rtID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("runtime not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if rt.Type != TypePHP && rt.Type != TypeOpenLiteSpd {
		httpapi.RespondError(w, httpapi.ErrValidation("extensions are managed for PHP and OpenLiteSpeed runtimes"))
		return
	}
	if rt.Status != StatusAvailable {
		httpapi.RespondError(w, httpapi.ErrConflict("runtime is not ready"))
		return
	}
	var req struct {
		Name   string `json:"name"`
		Action string `json:"action"`
		// PhpVersion selects the LSPHP version for OpenLiteSpeed extensions.
		PhpVersion string `json:"php_version"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Name = strings.ToLower(strings.TrimSpace(req.Name))
	req.PhpVersion = strings.TrimSpace(req.PhpVersion)
	if !extNameRe.MatchString(req.Name) {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid extension name"))
		return
	}
	if req.Action != "install" && req.Action != "remove" {
		httpapi.RespondError(w, httpapi.ErrValidation("action must be install or remove"))
		return
	}
	if rt.Type == TypeOpenLiteSpd && req.PhpVersion == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("php_version is required for OpenLiteSpeed extensions (LSPHP version, e.g. 8.3)"))
		return
	}

	extVersion := rt.Version
	if rt.Type == TypeOpenLiteSpd {
		// Extensions target the LSPHP runtime; the agent maps the version to
		// lsphpXY packages from the LiteSpeed repo.
		extVersion = req.PhpVersion
	}

	ext, err := h.Extensions.UpsertInstalling(r.Context(), rt.ID, req.Name, req.PhpVersion)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := extensionJobPayload{
		ExtensionID: ext.ID.String(), RuntimeID: rt.ID.String(),
		Type: string(rt.Type), Version: extVersion, Name: req.Name,
	}
	jt := jobs.TypeInstallExtension
	if req.Action == "remove" {
		jt = jobs.TypeRemoveExtension
		if err := h.Extensions.SetStatus(r.Context(), ext.ID, string(ExtRemoving), ""); err != nil && err != ErrExtNotFound {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}
	if _, err := h.Jobs.Enqueue(r.Context(), srvID, nil, jt, payload); err != nil {
		_ = h.Extensions.SetStatus(r.Context(), ext.ID, string(ExtFailed), "enqueue failed: "+err.Error())
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "php.extension_"+req.Action+"_requested", "runtime", rt.ID.String(), map[string]any{"extension": req.Name})
	httpapi.WriteJSON(w, http.StatusAccepted, ext)
}

// ApplyExtensionJobOutcome advances the extension state machine for finished
// extension jobs. Wired from the api fanout.
func (h *Handler) ApplyExtensionJobOutcome(ctx context.Context, job *jobs.Job) {
	if job.Type != jobs.TypeInstallExtension && job.Type != jobs.TypeRemoveExtension {
		return
	}
	var p extensionJobPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return
	}
	extID, err := uuid.Parse(p.ExtensionID)
	if err != nil {
		return
	}
	switch job.Status {
	case jobs.StatusSuccess:
		if job.Type == jobs.TypeRemoveExtension {
			_ = h.Extensions.Delete(ctx, extID)
		} else {
			_ = h.Extensions.SetStatus(ctx, extID, string(ExtAvailable), "")
		}
	case jobs.StatusFailed:
		_ = h.Extensions.SetStatus(ctx, extID, string(ExtFailed), job.Error)
	}
}
