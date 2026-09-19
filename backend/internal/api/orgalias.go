package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

// Invisible tenancy (ADR-060): organizations remain the isolation engine,
// but callers never need to know their org id. Every org-scoped route
// registered as /v1/organizations/{org_id}/X is ALSO served at the short
// form /v1/X: this middleware (running right after session auth) resolves
// the caller's ACTIVE ORGANIZATION and rewrites the path to the canonical
// form before routing. Zero route duplication — one authoritative route
// table, mirroring how APIPrefixRewrite keeps /api/v1 and /v1 in sync.
//
// Active-org resolution, in priority order:
//  1. Org tokens (epk_): always their bound organization — the header below
//     is ignored so a token can never escape its confinement (ADR-027).
//  2. X-EpicPanel-Org header: explicit org selection (useful for admins and
//     platform keys operating several orgs). Handlers still enforce
//     membership/role, so this can only ever widen what you may target, not
//     what you may touch.
//  3. Otherwise: the user's primary organization — the earliest-created
//     membership. For customers (one auto-created personal org) this is
//     always exactly their account.
//
// A caller with no organization gets a structured 404 ("no_organization");
// unauthenticated callers get the standard 401 before any rewriting.

const orgAliasHeader = "X-EpicPanel-Org"

// orgAliasSkip lists /v1 top-level segments that carry their own non-org
// routes; they are never treated as org-scoped resources. Anything NOT in
// this set is assumed to be an org-scoped resource (websites, databases,
// domains, ...) and gets the alias rewrite.
var orgAliasSkip = map[string]bool{
	"auth":          true,
	"organizations": true,
	"admin":         true,
	"adminview":     true,
	"agent":         true,
	"setup":         true,
	"settings":      true,
	"jobs":          true,
	"audit-logs":    true,
	"ws":            true,
	"openapi.json":  true,
	"billing":       true,
	"pma-gate":      true,
}

// ResolveOrgAlias implements the short-form org paths (see doc comment).
func (s *Server) ResolveOrgAlias(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/v1/")
		seg := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			seg = rest[:i]
		}
		if seg == "" || orgAliasSkip[seg] {
			next.ServeHTTP(w, r)
			return
		}

		user, ok := httpapi.UserFrom(r.Context())
		if !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}

		orgID := ""
		if httpapi.IsAPIToken(r.Context()) && !httpapi.IsPlatformKey(r.Context()) {
			// Org tokens are pinned to their issuing organization regardless
			// of any header (Phase 11 tenant confinement).
			orgID = httpapi.TokenOrgID(r.Context())
		} else if header := r.Header.Get(orgAliasHeader); header != "" {
			if _, err := uuid.Parse(header); err != nil {
				httpapi.RespondError(w, httpapi.ErrValidation("invalid "+orgAliasHeader+" header: must be an organization id"))
				return
			}
			orgID = header
		} else {
			uid, err := uuid.Parse(user.ID)
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			primary, err := s.Orgs.PrimaryOrgForUser(r.Context(), uid)
			if errors.Is(err, organizations.ErrNoMembership) {
				httpapi.RespondError(w, httpapi.ErrNotFound("no organization for this account: create one via POST /v1/organizations, pass "+orgAliasHeader+", or use the /v1/organizations/{org_id}/... paths"))
				return
			} else if err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			orgID = primary.String()
		}

		r.URL.Path = "/v1/organizations/" + orgID + "/" + rest
		next.ServeHTTP(w, r)
	})
}
