package apitokens

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

// PlatformKeysHandler manages platform admin API keys (epa_).
//
// Listing is allowed for any principal that passed the admin gate (admin
// session or epa_ key with admin:read). Creation and revocation are
// SESSION-ONLY by design: a leaked key must never be able to mint new
// privileges for itself or revoke other keys to cover its tracks — rotate
// keys from the admin UI / an admin session instead.
type PlatformKeysHandler struct {
	Tokens *Store
	Audit  *audit.Store
}

func (h *PlatformKeysHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/api-keys", httpapi.RequireAdmin(h.List))
	mux.HandleFunc("POST /v1/admin/api-keys", h.requireAdminSession(h.Create))
	mux.HandleFunc("DELETE /v1/admin/api-keys/{key_id}", h.requireAdminSession(h.Revoke))
}

// requireAdminSession is stricter than httpapi.RequireAdmin: platform admin
// BROWSER SESSION only (no epa_ keys, no org tokens).
func (h *PlatformKeysHandler) requireAdminSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := httpapi.UserFrom(r.Context())
		if !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if user.Role != "admin" || httpapi.IsAPIToken(r.Context()) {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform admin session required"))
			return
		}
		next(w, r)
	}
}

// GET /v1/admin/api-keys
func (h *PlatformKeysHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.Tokens.ListAdminKeys(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []AdminKey{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"admin_api_keys": list})
}

// POST /v1/admin/api-keys  {"name": "provisioning-bot", "scopes": ["*"], "expires_in_days": 90}
// The raw key is returned exactly once. Scope "*" expands to every valid
// scope (full-control key).
func (h *PlatformKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
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
		httpapi.RespondError(w, httpapi.ErrValidation("at least one scope is required (use \"*\" for full control)"))
		return
	}
	seen := map[string]bool{}
	for _, sc := range req.Scopes {
		if sc == "*" {
			continue
		}
		if !ValidAdminScope(sc) {
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

	k, raw, err := h.Tokens.CreateAdminKey(r.Context(), createdBy, req.Name, req.Scopes, expiresAt)
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
			ActorUserID:  actorID,
			ActorType:    audit.ActorUser,
			Action:       "admin_api_key.created",
			ResourceType: "admin_api_key",
			ResourceID:   k.ID.String(),
			Metadata:     map[string]any{"name": req.Name, "scopes": req.Scopes},
			IP:           httpapi.ClientIP(r),
		})
	}

	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"admin_api_key": k,
		"raw_token":     raw,
		"note":          "store this key now; it is not retrievable later",
	})
}

// DELETE /v1/admin/api-keys/{key_id}
func (h *PlatformKeysHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	keyID, err := uuid.Parse(r.PathValue("key_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid key id"))
		return
	}
	if err := h.Tokens.RevokeAdminKey(r.Context(), keyID); err == ErrAdminKeyNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("admin api key not found"))
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
			ActorUserID:  actorID,
			ActorType:    audit.ActorUser,
			Action:       "admin_api_key.revoked",
			ResourceType: "admin_api_key",
			ResourceID:   keyID.String(),
			IP:           httpapi.ClientIP(r),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}
