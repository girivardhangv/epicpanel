package runtimes

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// POST .../servers/{id}/detect-software — enqueues a detect_software job.
// When it finishes, the api fanout calls AdoptDetectedSoftware which inserts
// runtime rows for managed software already present on the machine (setup
// wizard installs and pre-existing tools converge into the panel state).
func (h *Handler) DetectSoftware(w http.ResponseWriter, r *http.Request) {
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
	job, err := h.Jobs.Enqueue(r.Context(), srvID, nil, jobs.TypeDetectSoftware, map[string]any{})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "server.detect_software_requested", "server", srvID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID})
}

type detectedSoftware struct {
	Type    string `json:"type"`
	Version string `json:"version"`
}

// AdoptDetectedSoftware inserts missing runtime rows for software the agent
// found on the machine. Called from the api fanout on detect_software success.
func (h *Handler) AdoptDetectedSoftware(ctx context.Context, job *jobs.Job, result json.RawMessage) {
	if job.Type != jobs.TypeDetectSoftware || job.Status != jobs.StatusSuccess {
		return
	}
	var items []detectedSoftware
	if err := json.Unmarshal(result, &items); err != nil {
		return
	}
	for _, it := range items {
		switch it.Type {
		case "php", "node", "python", "go", "apache", "openlitespeed":
		default:
			continue // mariadb/postgres/dbtools are not runtimes
		}
		version := normalizeRuntimeVersion(it.Type, it.Version)
		if _, err := h.Runtimes.GetByTypeVersionAny(ctx, job.ServerID, it.Type, version); err == ErrNotFound {
			_, _ = h.Runtimes.CreateAvailable(ctx, job.ServerID, it.Type, version)
		}
	}
}

// normalizeRuntimeVersion fits detected versions into the registry format.
func normalizeRuntimeVersion(rtType, version string) string {
	if version == "" || version == "latest" || version == "10.x" || version == "16" {
		if rtType == "apache" {
			return "2.4"
		}
		if rtType == "openlitespeed" {
			return "1.8"
		}
		return "0.0" // unknown; row exists but is not used for placement
	}
	if rtType == "node" || rtType == "go" || rtType == "apache" || rtType == "openlitespeed" {
		// major-only
		dot := -1
		for i, c := range version {
			if c == '.' {
				dot = i
				break
			}
		}
		if dot == -1 {
			return version + ".0"
		}
	}
	return version
}
