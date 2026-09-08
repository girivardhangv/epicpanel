package users

import (
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

func hashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// AdminHandler provides platform-admin account management (cPanel-style:
// the hosting operator creates customer accounts; customers don't self-serve).
type AdminHandler struct {
	Users *Store
}

func (h *AdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/admin/users", h.requireAdmin(h.List))
	mux.HandleFunc("POST /v1/admin/users", h.requireAdmin(h.Create))
}

func (h *AdminHandler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := httpapi.UserFrom(r.Context())
		if !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if user.Role != "admin" {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform administrator access required"))
			return
		}
		next(w, r)
	}
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
	httpapi.WriteJSON(w, http.StatusCreated, u)
}
