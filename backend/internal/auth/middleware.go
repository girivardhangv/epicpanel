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
// cookie, or epk_ API token (scoped to its organization) and stores the
// authenticated identity in the request context.
func SessionMiddleware(userStore *users.Store, sessions *SessionStore, tokens TokenResolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(r)
		if token != "" {
			// API token (epk_...) resolves to its org + creator identity.
			if strings.HasPrefix(token, apitokens.TokenPrefix) && tokens != nil {
				if resolved, err := tokens.Resolve(r.Context(), token); err == nil {
					role := "user"
					if resolved.IsAdmin {
						role = "admin"
					}
					ctx := httpapi.WithUser(r.Context(), &httpapi.User{
						ID:    resolved.UserID.String(),
						Email: resolved.Email,
						Role:  role,
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
