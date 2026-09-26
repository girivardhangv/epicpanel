package deployments

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

type Handler struct {
	Deployments *Store
	Jobs        *jobs.Store
	Websites    *websites.Store
	Audit       *audit.Store
	RequireOrg  func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
	// OnWebsiteConfigChanged fires after deploy config updates (vhost unaffected
	// but reserved for future hooks).
	OnWebsiteConfigChanged func(ctx context.Context, websiteID uuid.UUID)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/deployments", h.requireOrg(organizations.RoleBilling, h.List))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/websites/{website_id}/deployment-config", h.requireOrg(organizations.RoleDeveloper, h.UpdateConfig))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/deploy", h.requireOrg(organizations.RoleDeveloper, h.Deploy))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/rollback", h.requireOrg(organizations.RoleAdmin, h.Rollback))
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

func encryptToken(token string) ([]byte, error) {
	return secretbox.Encrypt(token)
}

// --- job payloads (shared with agent) ---

type DeployPayload struct {
	DeploymentID    uuid.UUID `json:"deployment_id"`
	WebsiteID       uuid.UUID `json:"website_id"`
	RepoURL         string    `json:"repo_url"`
	Branch          string    `json:"branch"`
	WebDir          string    `json:"web_dir,omitempty"`
	TokenEncrypted  []byte    `json:"token_encrypted,omitempty"`
	Runtime         string    `json:"runtime,omitempty"`
	RuntimeVersion  string    `json:"runtime_version,omitempty"`
	BuildCommand    string    `json:"build_command,omitempty"`
	UnixUser        string    `json:"unix_user,omitempty"`
	StartupCommand  string    `json:"app_startup_command,omitempty"`
	AppPort         int       `json:"app_port,omitempty"`
	AppDesiredState string    `json:"app_desired_state,omitempty"`
	AppEnvEnc       []byte    `json:"app_env_enc,omitempty"`
}

type RollbackPayload struct {
	DeploymentID     uuid.UUID `json:"deployment_id"`
	WebsiteID        uuid.UUID `json:"website_id"`
	TargetReleaseDir string    `json:"target_release_dir"`
}

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

// PATCH .../deployment-config {"repo_url": "...", "branch": "main", "deploy_token": "..."}
// The token (for private repos) is encrypted at rest and never returned.
func (h *Handler) UpdateConfig(w http.ResponseWriter, r *http.Request) {
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
	if ws.IsStaging {
		httpapi.RespondError(w, httpapi.ErrValidation("deployment config is managed on the production website"))
		return
	}

	var req struct {
		RepoURL     string  `json:"repo_url"`
		Branch      string  `json:"branch"`
		WebDir      *string `json:"web_dir"`
		DeployToken *string `json:"deploy_token"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.RepoURL = strings.TrimSpace(req.RepoURL)
	req.Branch = strings.TrimSpace(req.Branch)
	if req.RepoURL != "" && !strings.HasPrefix(req.RepoURL, "https://") && !strings.HasPrefix(req.RepoURL, "http://") {
		httpapi.RespondError(w, httpapi.ErrValidation("repo_url must be an http(s) git URL"))
		return
	}

	// Running directory inside the release (Forge-style web directory):
	// "public" for Laravel, "web" for legacy Symfony, "" = release root.
	// Relative path under the site's web root only — traversal is refused.
	webDir := ""
	if req.WebDir != nil {
		webDir = *req.WebDir
	}
	webDir = strings.Trim(strings.TrimSpace(webDir), "/")
	if webDir != "" {
		if strings.Contains(webDir, "..") || strings.Count(webDir, "/") > 3 || len(webDir) > 100 {
			httpapi.RespondError(w, httpapi.ErrValidation("web_dir must be a short relative path inside the repo"))
			return
		}
	}

	var tokenCipher []byte
	if req.DeployToken != nil && *req.DeployToken != "" {
		enc, err := encryptToken(*req.DeployToken)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		tokenCipher = enc
	}
	if err := h.Websites.SetDeployConfig(r.Context(), ws.ID, req.RepoURL, req.Branch, tokenCipher, webDir); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "website.deploy_config_updated", "website", ws.ID.String(), map[string]any{"repo_url": req.RepoURL, "branch": req.Branch, "web_dir": webDir})
	if h.OnWebsiteConfigChanged != nil {
		h.OnWebsiteConfigChanged(r.Context(), ws.ID)
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST .../deploy — enqueue deploy_website job with a fresh deployment row.
func (h *Handler) Deploy(w http.ResponseWriter, r *http.Request) {
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
	if ws.DeployRepoURL == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("deployment not configured; set deployment-config first"))
		return
	}
	if ws.IsStaging {
		httpapi.RespondError(w, httpapi.ErrConflict("staging websites are deployed via promotion"))
		return
	}
	branch := ws.DeployBranch
	if branch == "" {
		branch = "main"
	}

	user, _ := httpapi.UserFrom(r.Context())
	createdBy, _ := uuid.Parse(user.ID)
	dep, err := h.Deployments.Create(r.Context(), orgID, ws.ID, createdBy, branch, "manual", nil)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := DeployPayload{
		DeploymentID: dep.ID, WebsiteID: ws.ID, RepoURL: ws.DeployRepoURL, Branch: branch,
		WebDir: ws.DeployWebDir,
		Runtime: string(ws.Runtime), RuntimeVersion: ws.RuntimeVersion,
		BuildCommand: ws.AppBuildCommand, UnixUser: ws.UnixUser,
		StartupCommand: ws.AppStartupCommand, AppPort: ws.AppPort,
		AppDesiredState: ws.AppDesiredState,
	}
	if token, err := h.Deployments.GetWebsiteDeployToken(r.Context(), ws.ID); err == nil && token != nil {
		payload.TokenEncrypted = token
	}
	// App env travels ciphertext-only (agent decrypts with its key) — the
	// same trust model as the deploy token.
	if envBlob, err := h.Websites.GetAppEnv(r.Context(), ws.ID); err == nil && len(envBlob) > 0 {
		payload.AppEnvEnc = envBlob
	}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeDeployWebsite, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "deployment.triggered", "deployment", dep.ID.String(), map[string]any{"branch": branch})
	httpapi.WriteJSON(w, http.StatusAccepted, dep)
}

// POST .../rollback — redeploy the last successful release (admin+).
func (h *Handler) Rollback(w http.ResponseWriter, r *http.Request) {
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
	last, err := h.Deployments.LastSuccessful(r.Context(), ws.ID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrConflict("no successful deployment to roll back to"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	user, _ := httpapi.UserFrom(r.Context())
	createdBy, _ := uuid.Parse(user.ID)
	dep, err := h.Deployments.Create(r.Context(), orgID, ws.ID, createdBy, last.Branch, "rollback", &last.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := RollbackPayload{DeploymentID: dep.ID, WebsiteID: ws.ID, TargetReleaseDir: last.ReleaseDir}
	if _, err := h.Jobs.Enqueue(r.Context(), ws.ServerID, &ws.ID, jobs.TypeRollbackWebsite, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "deployment.rollback_triggered", "deployment", dep.ID.String(), map[string]any{"target": last.ReleaseDir})
	httpapi.WriteJSON(w, http.StatusAccepted, dep)
}

// GET .../deployments
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
	list, err := h.Deployments.ListForWebsite(r.Context(), ws.ID, 20)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Deployment{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"deployments": list})
}

// ApplyJobOutcome advances deployment rows for finished deploy/rollback jobs.
func (h *Handler) ApplyJobOutcome(job *jobs.Job, result json.RawMessage) {
	switch job.Type {
	case jobs.TypeDeployWebsite, jobs.TypeRollbackWebsite:
	default:
		return
	}
	var depID string
	if job.Type == jobs.TypeDeployWebsite {
		var p DeployPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return
		}
		depID = p.DeploymentID.String()
	} else {
		var p RollbackPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return
		}
		depID = p.DeploymentID.String()
	}
	id, err := uuid.Parse(depID)
	if err != nil {
		return
	}
	switch job.Status {
	case jobs.StatusSuccess:
		var outcome struct {
			CommitSHA  string `json:"commit_sha"`
			ReleaseDir string `json:"release_dir"`
			Log        string `json:"log"`
		}
		_ = json.Unmarshal(result, &outcome)
		if err := h.Deployments.MarkSuccessful(context.Background(), id, outcome.CommitSHA, outcome.ReleaseDir, outcome.Log); err != nil && err != ErrNotFound {
			slog.Error("deployment mark successful failed", "deployment", id, "err", err)
		}
	case jobs.StatusFailed:
		if err := h.Deployments.MarkFailed(context.Background(), id, "", job.Error); err != nil && err != ErrNotFound {
			slog.Error("deployment mark failed failed", "deployment", id, "err", err)
		}
	}
}

// MarkJobClaimed flips the deployment row to running when its job is claimed.
func (h *Handler) MarkJobClaimed(ctx context.Context, job *jobs.Job) {
	var depID string
	switch job.Type {
	case jobs.TypeDeployWebsite:
		var p DeployPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return
		}
		depID = p.DeploymentID.String()
	case jobs.TypeRollbackWebsite:
		var p RollbackPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return
		}
		depID = p.DeploymentID.String()
	}
	id, err := uuid.Parse(depID)
	if err != nil {
		return
	}
	if err := h.Deployments.MarkRunning(ctx, id); err != nil && err != ErrNotFound {
		slog.Error("deployment mark running failed", "deployment", id, "err", err)
	}
}
