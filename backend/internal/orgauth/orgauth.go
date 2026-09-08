// Package orgauth is the SHARED org-scoped authorization middleware (audit:
// requireOrg was copy-pasted into 10 packages and drifted into authz holes).
// It enforces granular permission strings from internal/permissions on top of
// the org membership model, preserves 404-cloaking, and keeps the API-token
// org confinement rules in ONE place. Phases 5/6 migrate the remaining
// handlers onto this package as they are touched.
package orgauth

import (
	"context"
	"net/http"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/permissions"
)

// Resolvers abstract the stores the middleware needs (set by the api wiring).
type Resolvers struct {
	RoleFor func(r *http.Request, orgID uuid.UUID) (organizations.Role, *httpapi.APIError)
}

// RequireOrgPermission resolves the org from the path, enforces that the
// authenticated principal holds the permission, and stores the org in context.
//
// Authorization rules, in order:
//  1. Platform-admin sessions bypass membership (cPanel root model).
//  2. API tokens are confined to their issuing org, then must carry the
//     mapped scope for the permission's resource group.
//  3. Org members must hold the permission (role -> permission map).
//  4. Non-members get 404 (existence cloaking), not 403.
func RequireOrgPermission(perm string, rs Resolvers, next http.HandlerFunc) http.HandlerFunc {
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

		// 1. Super Admin (session only).
		if user.Role == "admin" && !httpapi.IsAPIToken(r.Context()) {
			next(w, r.WithContext(withOrg(r.Context(), orgID)))
			return
		}

		// 2. API tokens: org confinement + scope equivalence.
		if httpapi.IsAPIToken(r.Context()) {
			if bound := httpapi.TokenOrgID(r.Context()); bound != orgID.String() {
				httpapi.RespondError(w, httpapi.ErrNotFound("organization not found"))
				return
			}
			if scope, ok := permissions.TokenScopeFor(perm); ok {
				scopes := httpapi.TokenScopes(r.Context())
				if !scopes[scope] {
					httpapi.RespondError(w, httpapi.ErrForbidden("token missing required scope: "+scope))
					return
				}
			}
			next(w, r.WithContext(withOrg(r.Context(), orgID)))
			return
		}

		// 3. Org membership -> permission string.
		role, apiErr := rs.RoleFor(r, orgID)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		if role == "" {
			// 4. 404-cloaking.
			httpapi.RespondError(w, httpapi.ErrNotFound("organization not found"))
			return
		}
		if !permissions.Can(role, perm) {
			httpapi.RespondError(w, httpapi.ErrForbidden("missing permission: "+perm))
			return
		}
		next(w, r.WithContext(withOrg(r.Context(), orgID)))
	}
}

// OrgFrom returns the resolved org id (set by RequireOrgPermission).
func OrgFrom(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(orgKey{}).(uuid.UUID)
	return id, ok
}

type orgKey struct{}
