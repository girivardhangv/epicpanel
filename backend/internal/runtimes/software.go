package runtimes

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// The types matching frontend/src/lib/software.ts
type SoftwareItem struct {
	Name              string             `json:"name"`
	DisplayName       string             `json:"display_name"`
	Category          string             `json:"category"`
	Description       string             `json:"description,omitempty"`
	Singleton         bool               `json:"singleton,omitempty"`
	Installed         []InstalledEntry   `json:"installed"`
	AvailableVersions []AvailableVersion `json:"available_versions"`
	DefaultVersion    string             `json:"default_version,omitempty"`
}

type InstalledEntry struct {
	Version string `json:"version"`
	Status  string `json:"status,omitempty"`
}

type AvailableVersion struct {
	Version string   `json:"version"`
	Methods []string `json:"methods,omitempty"`
}

// SoftwareCatalog returns the catalog of available and installed software.
func (h *Handler) SoftwareCatalog(w http.ResponseWriter, r *http.Request) {
	serverID, err := uuid.Parse(r.PathValue("server_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid server id"))
		return
	}

	// Fetch installed runtimes
	installedRuntimes, err := h.Runtimes.ListForServer(r.Context(), serverID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	// Group installed by type
	installedByType := make(map[Type][]InstalledEntry)
	for _, rt := range installedRuntimes {
		installedByType[rt.Type] = append(installedByType[rt.Type], InstalledEntry{
			Version: rt.Version,
			Status:  string(rt.Status),
		})
	}

	// Also check if dbtools is installed
	var hasDBTools bool
	// Try to query the most recent install_database_tools job that succeeded
	// Or maybe the agent detects it via detect_software? 
	// For now, let's just query jobs.
	var count int
	err = h.Jobs.Pool.QueryRow(r.Context(), `SELECT count(*) FROM jobs WHERE server_id = $1 AND type = $2 AND status = 'success'`, serverID, jobs.TypeDBTools).Scan(&count)
	if err == nil && count > 0 {
		hasDBTools = true
	}

	// Build the catalog from the same source as setupCatalog, but mapped to SoftwareItem
	catalog := []SoftwareItem{
		{
			Name: "php", DisplayName: "PHP", Category: "language",
			Description: "Required for PHP sites, WordPress and phpMyAdmin.",
			// Supported-upstream versions only (8.1 went EOL 2025-12); the
			// agent still installs 8.1 if a legacy site explicitly asks.
			AvailableVersions: []AvailableVersion{{Version: "8.2"}, {Version: "8.3"}, {Version: "8.4"}, {Version: "8.5"}},
			DefaultVersion:    "8.4",
			Installed:         installedByType[TypePHP],
		},
		{
			Name: "node", DisplayName: "Node.js", Category: "language",
			Description: "Official Node.js builds, one managed install per major.",
			// LTS lines only (21/23 were odd-numbered, now EOL).
			AvailableVersions: []AvailableVersion{{Version: "22"}, {Version: "24"}},
			DefaultVersion:    "24",
			Installed:         installedByType[TypeNode],
		},
		{
			Name: "python", DisplayName: "Python", Category: "language",
			Description: "System Python with venv support.",
			AvailableVersions: []AvailableVersion{{Version: "3.12"}, {Version: "3.13"}},
			DefaultVersion: "3.13",
			Installed: installedByType[TypePython],
		},
		{
			Name: "go", DisplayName: "Go", Category: "language",
			Description: "Official toolchain for Go applications.",
			// Go supports the two most recent minors (1.24 dropped when 1.26
			// shipped).
			AvailableVersions: []AvailableVersion{{Version: "1.26"}, {Version: "1.27"}},
			DefaultVersion:    "1.27",
			Installed:         installedByType[TypeGo],
		},
		{
			Name: "java", DisplayName: "Java", Category: "language",
			Description: "OpenJDK for Java applications.",
			AvailableVersions: []AvailableVersion{{Version: "21"}, {Version: "25"}},
			DefaultVersion: "21",
			Installed: installedByType[TypeJava],
		},
		{
			Name: "openlitespeed", DisplayName: "OpenLiteSpeed", Category: "webserver",
			Description: "High-performance web server with LSPHP.",
			Singleton: true,
			AvailableVersions: []AvailableVersion{{Version: "1.8"}},
			DefaultVersion: "1.8",
			Installed: installedByType[TypeOpenLiteSpd],
		},
		{
			Name: "redis", DisplayName: "Redis", Category: "cache",
			Description: "In-memory cache and message broker (127.0.0.1 only).",
			Singleton: true,
			AvailableVersions: []AvailableVersion{{Version: "latest"}},
			DefaultVersion: "latest",
			Installed: installedByType[TypeRedis],
		},
		{
			Name: "dbtools", DisplayName: "phpMyAdmin & Adminer", Category: "database",
			Description: "Database web tools on port 8081 (SSO-ready).",
			Singleton: true,
			AvailableVersions: []AvailableVersion{{Version: "latest"}},
			DefaultVersion: "latest",
		},
	}

	if hasDBTools {
		catalog[6].Installed = []InstalledEntry{{Version: "latest", Status: "available"}}
	}

	for i := range catalog {
		if catalog[i].Installed == nil {
			catalog[i].Installed = []InstalledEntry{}
		}
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"data": catalog})
}

// InstallSoftware handles the POST request to install software.
func (h *Handler) InstallSoftware(w http.ResponseWriter, r *http.Request) {
	serverID, err := uuid.Parse(r.PathValue("server_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid server id"))
		return
	}

	var req struct {
		Name    string `json:"name"`
		Version string `json:"version"`
		Method  string `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid JSON"))
		return
	}

	if req.Name == "dbtools" {
		// Panel tools follow the operator's global PHP choice when set.
		var phpPref string
		_ = h.Jobs.Pool.QueryRow(r.Context(),
			`SELECT value FROM system_settings WHERE key = 'global_php_version'`).Scan(&phpPref)
		payload := map[string]string{"php_version": phpPref}
		job, err := h.Jobs.EnqueueIdempotent(r.Context(), serverID, nil, jobs.TypeDBTools, payload, "install_dbtools_"+serverID.String())
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		orgID, _ := OrgIDFromRequest(r)
		h.auditUser(r, &orgID, "server.software.install", "server", serverID.String(), map[string]any{"software": req.Name})
		httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"task_id": job.ID})
		return
	}

	// Otherwise, it's a runtime
	if req.Version == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("version is required"))
		return
	}

	if !ValidVersionForType(Type(req.Name), req.Version) && req.Version != "latest" {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid version format for this type"))
		return
	}

	payload := map[string]string{"type": req.Name, "version": req.Version}
	if req.Method != "" && req.Method != "auto" {
		payload["method"] = req.Method
	}

	job, err := h.Jobs.EnqueueIdempotent(r.Context(), serverID, nil, jobs.TypeInstallRuntime, payload, "install_rt_"+serverID.String()+"_"+req.Name+"_"+req.Version)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	orgID, _ := OrgIDFromRequest(r)
	h.auditUser(r, &orgID, "server.software.install", "server", serverID.String(), map[string]any{"software": req.Name, "version": req.Version})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"task_id": job.ID})
}

func (h *Handler) RemoveSoftware(w http.ResponseWriter, r *http.Request) {
	httpapi.RespondError(w, httpapi.ErrValidation("software removal not fully supported yet"))
}
