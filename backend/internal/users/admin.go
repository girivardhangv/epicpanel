package users

import (
	"log/slog"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

func hashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// AdminHandler provides platform-admin account management (cPanel-style:
// the hosting operator creates customer accounts; customers don't self-serve).
type AdminHandler struct {
	Users *Store
	// Orgs powers the invisible-tenancy flow (ADR-060): admin-created
	// customers get a personal org automatically. Nil-safe (skips auto-org
	// when unset).
	Orgs *organizations.Store
}

func (h *AdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/users", httpapi.RequireAdmin(h.List))
	mux.HandleFunc("POST /v1/admin/users", httpapi.RequireAdmin(h.Create))
}

// GET /v1/admin/users
func (h *AdminHandler) List(w http.ResponseWriter, r *http.Request) {
	list, err := h.Users.List(r.Context(), 100, 0)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []User{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"users": list})
}

// POST /v1/admin/users  {"email", "name", "password"}
func (h *AdminHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) < 1 {
		httpapi.RespondError(w, httpapi.ErrValidation("name is required"))
		return
	}
	if len(req.Password) < 10 {
		httpapi.RespondError(w, httpapi.ErrValidation("password must be at least 10 characters"))
		return
	}
	hash, err := hashPassword(req.Password)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// Admin-created users are never platform admins (first-user rule only).
	u, _, err := h.Users.Create(r.Context(), req.Email, hash, req.Name)
	if err == ErrEmailTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("email already registered"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	// Invisible tenancy (ADR-060): the customer lands in their own personal
	// organization (created here, owned by them) instead of the admin having
	// to mint an org and add them as a member. Best-effort; admins can still
	// assign users to other orgs via membership endpoints.
	if h.Orgs != nil {
		if _, err := h.Orgs.EnsurePersonalOrg(r.Context(), u.ID, u.Name); err != nil {
			slog.Warn("personal org creation failed", "user", u.ID, "err", err)
		}
	}
	httpapi.WriteJSON(w, http.StatusCreated, u)
}
