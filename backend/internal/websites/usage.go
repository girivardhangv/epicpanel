package websites

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

func contextWithTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// usageJobPayload matches agent.SiteUsageJobPayload.
type usageJobPayload struct {
	WebsiteID string `json:"website_id"`
}

// usageOutcome mirrors agent.SiteUsageOutcome (subset persisted).
type usageOutcome struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemBytes   int64   `json:"memory_bytes"`
	DiskMB     int64   `json:"disk_used_mb"`
	Processes  int     `json:"processes"`
}

// LimitsFor resolves (memory cap bytes, cpu cores, disk allowance MB) for a
// site from its organization's package. Optional api wiring.
type LimitsForFunc func(ctx context.Context, orgID, websiteID uuid.UUID) (memBytes int64, cpuCores float64, diskMB int64)

// GET .../websites/{id}/usage — latest persisted usage snapshot. A fresh
// sample job is enqueued when the last one is older than ~45s so polling
// clients get near-live numbers without hammering the agent.
func (h *Handler) GetUsage(w http.ResponseWriter, r *http.Request) {
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

	stale := ws.UsageSampledAt == nil || time.Since(*ws.UsageSampledAt) > 45*time.Second
	if stale && ws.Status == StatusReady {
		payload := usageJobPayload{WebsiteID: ws.ID.String()}
		_, _ = h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeSiteUsage, payload)
	}

	// Plan limits come from an optional PackageLimitsLookup hook (api wiring):
	// memory cap + disk allowance for meaningful percentages.
	var memLimit, diskLimit int64
	var cpuLimitCores float64
	if h.LimitsFor != nil {
		memLimit, cpuLimitCores, diskLimit = h.LimitsFor(r.Context(), orgID, ws.ID)
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"cpu_percent":     ws.UsageCPUPercent,
		"memory_bytes":    ws.UsageMemoryBytes,
		"disk_mb":         ws.UsageDiskMB,
		"processes":       ws.UsageProcesses,
		"sampled_at":      ws.UsageSampledAt,
		"memory_limit":    memLimit,
		"cpu_limit_cores": cpuLimitCores,
		"disk_limit_mb":   diskLimit,
	})
}

// ApplySiteUsageOutcome persists a finished site_usage job's snapshot.
// Wired from the api job fanout.
func (h *Handler) ApplySiteUsageOutcome(job *jobs.Job, result json.RawMessage) {
	if job.Type != jobs.TypeSiteUsage || job.Status != jobs.StatusSuccess || job.WebsiteID == nil {
		return
	}
	var o usageOutcome
	if err := json.Unmarshal(result, &o); err != nil {
		return
	}
	ctx, cancel := contextWithTimeout()
	defer cancel()
	if err := h.Websites.StoreUsage(ctx, *job.WebsiteID, o.CPUPercent, o.MemBytes, o.DiskMB, o.Processes); err != nil {
		// best-effort: a missed snapshot is invisible, the next poll fixes it
		_ = err
	}
}
