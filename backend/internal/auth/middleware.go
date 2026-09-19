package auth

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apitokens"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/users"
)

// SessionMiddleware authenticates requests via Bearer session token, session
// cookie, epk_ API token (scoped to its organization) or epa_ platform admin
// key (platform-wide, scope-checked) and stores the authenticated identity
// in the request context.
func SessionMiddleware(userStore *users.Store, sessions *SessionStore, tokens TokenResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(r)
		if token != "" {
			// epk_ org token / epa_ platform key resolve to their identity.
			if tokens != nil && (strings.HasPrefix(token, apitokens.TokenPrefix) || strings.HasPrefix(token, apitokens.AdminKeyPrefix)) {
				resolved, err := tokens.Resolve(r.Context(), token)
				if err == nil && resolved.Platform {
					// Platform admin API key: machine principal with the
					// platform-admin role, no bound org (org scope comes
					// from the request path; scopes enforced per route).
					ctx := httpapi.WithUser(r.Context(), &httpapi.User{
						ID:    resolved.UserID.String(),
						Email: resolved.Email,
						Role:  "admin",
					})
					ctx = httpapi.WithPlatformKeyAuth(ctx, resolved.Scopes)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				if err == nil {
					// Org token: confined to its issuing organization and
					// never inherits platform-admin (ADR-027/ADR-043).
					ctx := httpapi.WithUser(r.Context(), &httpapi.User{
						ID:    resolved.UserID.String(),
						Email: resolved.Email,
						Role:  "user",
					})
					ctx = httpapi.WithTokenAuth(ctx, resolved.Scopes, resolved.OrgID.String())
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			} else {
				sess, err := sessions.Get(r.Context(), token)
				if err == nil {
					u, err := userStore.GetByID(r.Context(), sess.UserID)
					if err == nil && u != nil && u.Status == "active" {
						role := "user"
						if u.IsAdmin {
							role = "admin"
						}
						ctx := httpapi.WithUser(r.Context(), &httpapi.User{
							ID:    u.ID.String(),
							Email: u.Email,
							Role:  role,
						})
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}
		}
		// Unauthenticated requests proceed without a user; protected routes
		// enforce authentication via httpapi.RequireUser.
		next.ServeHTTP(w, r)
	})
}

var _ = context.Background
var _ = time.Now

// TokenResolver resolves raw API tokens (kept as an interface to avoid
// importing the apitokens package from auth).
type TokenResolver interface {
	Resolve(ctx context.Context, raw string) (*apitokens.Resolved, error)
}
