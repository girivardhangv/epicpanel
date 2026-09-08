package organizations

import (
	"net/http"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

// RequireMinimumRole enforces that the authenticated user is a member of the
// organization with at least the given role rank. Membership is resolved
// server-side; non-members receive 404 to avoid leaking org existence.
func (h *Handler) RequireMinimumRole(min Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := httpapi.UserFrom(r.Context())
		if !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}

		orgID, err := uuid.Parse(r.PathValue("org_id"))
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
			return
		}

		// Platform admins operate across all organizations (cPanel root model).
		// API tokens never inherit platform-admin — they are org-confined
		// automation identities (RBAC v2).
		if user.Role == "admin" && !httpapi.IsAPIToken(r.Context()) {
			next(w, r)
			return
		}

		uid, err := uuid.Parse(user.ID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}

		role, err := h.Store.RoleFor(r.Context(), orgID, uid)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if role == "" {
			httpapi.RespondError(w, httpapi.ErrNotFound("organization not found"))
			return
		}
		if RoleRank[role] < RoleRank[min] {
			httpapi.RespondError(w, httpapi.ErrForbidden("insufficient organization role"))
			return
		}

		next(w, r)
	}
}
