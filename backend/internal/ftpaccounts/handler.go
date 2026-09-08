package ftpaccounts

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

const TypeSyncFTPAccounts = jobs.Type("sync_ftp_accounts")

const (
	minPasswordLen = 6
	maxPasswordLen = 256
)

type Handler struct {
	Accounts   *Store
	Jobs       *jobs.Store
	Websites   *websites.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/ftp-accounts", h.requireOrg(organizations.RoleDeveloper, h.Create))
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/ftp-accounts", h.requireOrg(organizations.RoleDeveloper, h.List))
	mux.HandleFunc("POST /v1/organizations/{org_id}/ftp-accounts/{account_id}/password", h.requireOrg(organizations.RoleDeveloper, h.ChangePassword))
	mux.HandleFunc("POST /v1/organizations/{org_id}/ftp-accounts/{account_id}/reveal", h.requireOrg(organizations.RoleDeveloper, h.Reveal))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/ftp-accounts/{account_id}", h.requireOrg(organizations.RoleAdmin, h.Delete))
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

func (h *Handler) audit(r *http.Request, orgID *uuid.UUID, action, resourceID string, meta map[string]any) {
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
		OrganizationID: orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "ftp_account",
		ResourceID:     resourceID,
		Metadata:       meta,
		IP:             httpapi.ClientIP(r),
	})
}

// List lists a website's accounts. Tenant scoping: the website must belong
// to the path org (the IDOR fix — org check required on every website route).
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	accounts, err := h.Accounts.List(r.Context(), orgID, ws.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if accounts == nil {
		accounts = []Account{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"ftp_accounts": accounts})
}

// Create validates and stores the account, then enqueues the website-wide
// sync. The plaintext password is returned exactly once in this response.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Protocol   string `json:"protocol"`
		Label      string `json:"label"`
		Password   string `json:"password"`
		HomeSubdir string `json:"home_subdir"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Label = strings.TrimSpace(req.Label)
	if len(req.Label) < 1 || len(req.Label) > 60 {
		httpapi.RespondError(w, httpapi.ErrValidation("label must be 1-60 characters"))
		return
	}
	if !ValidProtocol(req.Protocol) {
		httpapi.RespondError(w, httpapi.ErrValidation("protocol must be ftp or sftp"))
		return
	}
	if _, err := ValidateHomeSubdir(req.HomeSubdir); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	password := req.Password
	if password == "" {
		var genErr error
		if password, genErr = CreatePassword(); genErr != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(genErr))
			return
		}
	}
	if err := validatePassword(password); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	acct, err := h.Accounts.Create(r.Context(), orgID, ws.ID, req.Protocol, req.Label, password, req.HomeSubdir)
	switch {
	case errors.Is(err, ErrTooMany):
		httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
		return
	case errors.Is(err, ErrTaken):
		httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
		return
	case errors.Is(err, ErrBadProto), errors.Is(err, ErrBadDir):
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	case err != nil:
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// Surface sync-enqueue failures on create: without the job the account
	// would sit pending forever (the hourly safety net still converges it).
	if err := h.enqueueSync(r.Context(), ws); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "ftp.account_created", acct.ID.String(), map[string]any{
		"protocol":  acct.Protocol,
		"label":     acct.Label,
		"user_name": acct.UserName,
	})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"account": acct, "password": password})
}

// ChangePassword re-encrypts the credential and re-syncs. The new plaintext
// is returned exactly once in this response.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	acct, apiErr := h.accountFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, acct.WebsiteID)
	if err == websites.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("website not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if err := validatePassword(req.Password); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	updated, err := h.Accounts.UpdatePassword(r.Context(), orgID, acct.ID, req.Password)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("ftp account not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.enqueueSync(r.Context(), ws); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "ftp.password_changed", acct.ID.String(), map[string]any{"user_name": acct.UserName})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"account": updated, "password": req.Password})
}

// Reveal decrypts and returns the credential. Explicitly audited.
func (h *Handler) Reveal(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	acct, apiErr := h.accountFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	password, err := h.Accounts.RevealPassword(r.Context(), acct.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
		return
	}
	h.audit(r, &orgID, "ftp.password_revealed", acct.ID.String(), map[string]any{"user_name": acct.UserName})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"protocol":  acct.Protocol,
		"user_name": acct.UserName,
		"password":  password,
	})
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	acct, apiErr := h.accountFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, acct.WebsiteID)
	if err == websites.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("website not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Accounts.Delete(r.Context(), orgID, acct.ID); err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("ftp account not found"))
		return
	}
	// Best-effort on delete: the hourly safety net converges the agent.
	_ = h.enqueueSync(r.Context(), ws)
	h.audit(r, &orgID, "ftp.account_deleted", acct.ID.String(), map[string]any{
		"protocol":  acct.Protocol,
		"user_name": acct.UserName,
	})
	w.WriteHeader(http.StatusNoContent)
}

// enqueueSync enqueues the website-wide FTP sync job on the site's server
// with the full desired account list (SHA-512-crypt hashes only).
func (h *Handler) enqueueSync(ctx context.Context, ws *websites.Website) error {
	if h.Jobs == nil {
		return nil
	}
	accounts, err := h.Accounts.DesiredForWebsite(ctx, ws.ID)
	if err != nil {
		return err
	}
	_, err = h.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, TypeSyncFTPAccounts, SyncPayload{
		WebsiteID: ws.ID.String(),
		Accounts:  accounts,
	}, "ftp_"+ws.ID.String())
	return err
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

func (h *Handler) accountFromPath(r *http.Request, orgID uuid.UUID) (*Account, *httpapi.APIError) {
	accountID, err := uuid.Parse(r.PathValue("account_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid account id")
	}
	acct, err := h.Accounts.Get(r.Context(), orgID, accountID)
	if err == ErrNotFound {
		return nil, httpapi.ErrNotFound("ftp account not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return acct, nil
}

// validatePassword guards the credential value itself: printable, no control
// characters that would break agent-side chpasswd/vsftpd handling.
func validatePassword(password string) error {
	if len(password) < minPasswordLen {
		return errors.New("password must be at least 6 characters")
	}
	if len(password) > maxPasswordLen {
		return errors.New("password must be at most 256 characters")
	}
	if strings.ContainsAny(password, "\x00\n\r") {
		return errors.New("password must not contain NUL or newline characters")
	}
	return nil
}
