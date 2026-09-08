package websites

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// StagingDatabases resolves the databases attached to a website (api layer).
type StagingDatabases func(ctx context.Context, websiteID uuid.UUID) ([]StagingDBRef, error)

type StagingDBRef struct {
	Engine string
	Name   string
	User   string
}

// HandlerExtensions carries the staging-specific collaborators.
type HandlerExtensions struct {
	Jobs      *jobs.Store
	Databases StagingDatabases
}

var _ = strings.TrimSpace

// StagingPayload is the wire form for the clone/promote jobs.
type StagingPayload struct {
	SourceWebsiteID string          `json:"source_website_id"`
	TargetWebsiteID string          `json:"target_website_id"`
	Databases       []StagingDBPair `json:"databases,omitempty"`
}

type StagingDBPair struct {
	Engine     string `json:"engine"`
	SourceName string `json:"source_name"`
	TargetName string `json:"target_name"`
	User       string `json:"user"`
}

// CreateStaging creates a staging copy website (same server, name suffixed
// -staging, runtime + web server copied) and enqueues a clone job.
func (h *Handler) CreateStaging(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromStagingContext(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if ws.IsStaging {
		httpapi.RespondError(w, httpapi.ErrConflict("this website is already a staging environment"))
		return
	}
	if ws.Status == StatusSuspended {
		httpapi.RespondError(w, httpapi.ErrConflict("website is suspended (resume it first)"))
		return
	}
	user, _ := httpapi.UserFrom(r.Context())
	createdBy, _ := uuid.Parse(user.ID)

	staging, err := h.Websites.CreateStagingCopy(r.Context(), orgID, ws, createdBy)
	if err == ErrNameTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("a staging website already exists for this site (delete it first)"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	payload := StagingPayload{SourceWebsiteID: ws.ID.String(), TargetWebsiteID: staging.ID.String()}
	if h.Ext != nil && h.Ext.Databases != nil {
		if refs, err := h.Ext.Databases(r.Context(), ws.ID); err == nil {
			for _, ref := range refs {
				payload.Databases = append(payload.Databases, StagingDBPair{
					Engine: ref.Engine, SourceName: ref.Name, TargetName: stagingStagingName(ref.Name), User: "",
				})
			}
		}
	}
	if _, err := h.JobsForStaging().Enqueue(r.Context(), ws.ServerID, &staging.ID, "clone_staging", payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.staging_created", "website", staging.ID.String(), map[string]any{"of": ws.ID.String()})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"staging": staging})
}

// PromoteStaging copies staging content + databases back to production (admin+).
func (h *Handler) PromoteStaging(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromStagingContext(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if !ws.IsStaging || ws.StagingOf == nil {
		httpapi.RespondError(w, httpapi.ErrValidation("website is not a staging environment"))
		return
	}
	payload := StagingPayload{SourceWebsiteID: ws.ID.String(), TargetWebsiteID: ws.StagingOf.String()}
	if h.Ext != nil && h.Ext.Databases != nil {
		if refs, err := h.Ext.Databases(r.Context(), *ws.StagingOf); err == nil {
			for _, ref := range refs {
				payload.Databases = append(payload.Databases, StagingDBPair{
					Engine: ref.Engine, SourceName: stagingStagingName(ref.Name), TargetName: ref.Name, User: "",
				})
			}
		}
	}
	if _, err := h.JobsForStaging().Enqueue(r.Context(), ws.ServerID, &ws.ID, "promote_staging", payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditUser(r, &orgID, "website.staging_promoted", "website", ws.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "promotion queued"})
}

// stagingStagingName derives the staging-side database name (suffix swap).
// Panel db names are ep_<org8>_<label>; staging clones get _stg appended.
func stagingStagingName(name string) string {
	if strings.HasSuffix(name, "_stg") {
		return name
	}
	if len(name) > 59 {
		name = name[:59]
	}
	return name + "_stg"
}

func (h *Handler) JobsForStaging() *jobs.Store {
	if h.Ext != nil && h.Ext.Jobs != nil {
		return h.Ext.Jobs
	}
	return h.Jobs
}

var _ = context.Background

// OrgKeyType is the context key type carrying the resolved org id
// (set by the api layer wrappers, distinct from orgIDCtxKey used by requireOrg).
type OrgKeyType struct{}

// OrgIDFromStagingContext extracts the org id set by the api wrapper.
func OrgIDFromStagingContext(r *http.Request) (uuid.UUID, bool) {
	id, ok := r.Context().Value(OrgKeyType{}).(uuid.UUID)
	return id, ok
}
