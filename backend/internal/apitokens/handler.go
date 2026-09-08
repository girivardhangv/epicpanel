package apitokens

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

type Handler struct {
	Tokens     *Store
	Orgs       *organizations.Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/api-tokens", h.requireOrg(organizations.RoleAdmin, h.List))
	mux.HandleFunc("POST /v1/organizations/{org_id}/api-tokens", h.requireOrg(organizations.RoleAdmin, h.Create))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/api-tokens/{token_id}", h.requireOrg(organizations.RoleAdmin, h.Revoke))
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

// POST .../api-tokens  {"name": "billing-automation", "scopes": ["websites:read"], "expires_in_days": 90}
// The raw token is returned exactly once.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	var req struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays int      `json:"expires_in_days"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		httpapi.RespondError(w, httpapi.ErrValidation("name is required (1-100 chars)"))
		return
	}
	if len(req.Scopes) == 0 {
		httpapi.RespondError(w, httpapi.ErrValidation("at least one scope is required"))
		return
	}
	seen := map[string]bool{}
	for _, sc := range req.Scopes {
		if !ValidScope(sc) {
			httpapi.RespondError(w, httpapi.ErrValidation("unknown scope: "+sc))
			return
		}
		if seen[sc] {
			httpapi.RespondError(w, httpapi.ErrValidation("duplicate scope: "+sc))
			return
		}
		seen[sc] = true
	}
	var expiresAt *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		expiresAt = &t
	}

	user, _ := httpapi.UserFrom(r.Context())
	createdBy, _ := uuid.Parse(user.ID)

	t, raw, err := h.Tokens.Create(r.Context(), orgID, createdBy, req.Name, req.Scopes, expiresAt)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	if h.Audit != nil {
		var actorID *uuid.UUID
		if uid, err := uuid.Parse(user.ID); err == nil {
			actorID = &uid
		}
		h.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &orgID,
			ActorUserID:    actorID,
			ActorType:      audit.ActorUser,
			Action:         "api_token.created",
			ResourceType:   "api_token",
			ResourceID:     t.ID.String(),
			Metadata:       map[string]any{"name": req.Name, "scopes": req.Scopes},
			IP:             clientIP(r),
		})
	}

	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"token":     t,
		"raw_token": raw,
		"note":      "store this token now; it is not retrievable later",
	})
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	list, err := h.Tokens.ListForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Token{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"api_tokens": list})
}

func (h *Handler) Revoke(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	tokenID, err := uuid.Parse(r.PathValue("token_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid token id"))
		return
	}
	if err := h.Tokens.Revoke(r.Context(), orgID, tokenID); err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("token not found"))
		return
	} else if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if h.Audit != nil {
		user, _ := httpapi.UserFrom(r.Context())
		var actorID *uuid.UUID
		if uid, err := uuid.Parse(user.ID); err == nil {
			actorID = &uid
		}
		h.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &orgID,
			ActorUserID:    actorID,
			ActorType:      audit.ActorUser,
			Action:         "api_token.revoked",
			ResourceType:   "api_token",
			ResourceID:     tokenID.String(),
			IP:             clientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func clientIP(r *http.Request) string { return httpapi.ClientIP(r) }

var errOrgContext = errStr("org id missing from request context")

type errStr string

func (e errStr) Error() string { return string(e) }
