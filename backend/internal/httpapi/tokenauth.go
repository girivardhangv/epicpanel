package httpapi

import (
	"context"
	"net/http"
	"strings"
)

// Token auth context keys: scopes + the org a token belongs to.
type tokenScopesKey struct{}
type tokenOrgKey struct{}
type tokenIsAPIKey struct{}
type tokenIsPlatformKey struct{}

func WithTokenAuth(ctx context.Context, scopes map[string]bool, orgID string) context.Context {
	ctx = context.WithValue(ctx, tokenScopesKey{}, scopes)
	ctx = context.WithValue(ctx, tokenOrgKey{}, orgID)
	return context.WithValue(ctx, tokenIsAPIKey{}, true)
}

// WithPlatformKeyAuth marks a platform admin API key (epa_): a machine
// principal that acts as the platform admin across all organizations. It
// carries no bound org — org scope comes from the request path and every
// route is still scope-checked via ScopeEnforce.
func WithPlatformKeyAuth(ctx context.Context, scopes map[string]bool) context.Context {
	ctx = context.WithValue(ctx, tokenScopesKey{}, scopes)
	ctx = context.WithValue(ctx, tokenIsAPIKey{}, true)
	return context.WithValue(ctx, tokenIsPlatformKey{}, true)
}

func IsAPIToken(ctx context.Context) bool {
	v, _ := ctx.Value(tokenIsAPIKey{}).(bool)
	return v
}

// IsPlatformKey reports whether the request authenticated via a platform
// admin API key (epa_) rather than an org token (epk_). Org tokens can
// never set this flag, so admin-surface gates may rely on it.
func IsPlatformKey(ctx context.Context) bool {
	v, _ := ctx.Value(tokenIsPlatformKey{}).(bool)
	return v
}

func TokenScopes(ctx context.Context) map[string]bool {
	v, _ := ctx.Value(tokenScopesKey{}).(map[string]bool)
	return v
}

func TokenOrgID(ctx context.Context) string {
	v, _ := ctx.Value(tokenOrgKey{}).(string)
	return v
}

// RequireAdmin gates the platform-admin surface. It accepts:
//   - a platform-admin browser session (cookie or session bearer token), or
//   - a platform admin API key (epa_, IsPlatformKey).
//
// Org-scoped tokens (epk_) are always rejected — they never inherit
// platform-admin (ADR-027/ADR-043). This is the single shared replacement
// for the per-package requireAdminSession/requirePlatformAdmin copies so the
// API-key policy cannot drift between features.
func RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := UserFrom(r.Context())
		if !ok {
			RespondError(w, ErrUnauthorized("authentication required"))
			return
		}
		if user.Role != "admin" {
			RespondError(w, ErrForbidden("platform administrator access required"))
			return
		}
		if IsAPIToken(r.Context()) && !IsPlatformKey(r.Context()) {
			RespondError(w, ErrForbidden("org api tokens cannot access the admin surface"))
			return
		}
		next(w, r)
	}
}

// scopeResource maps a path's resource segment to a scope group.
// Deny-by-default: resources without a mapping are rejected for API tokens
// (previously they passed unchecked — audit S2).
func scopeResource(resource string) string {
	switch resource {
	case "websites":
		return "websites"
	case "servers":
		return "servers"
	case "runtimes":
		return "runtimes"
	case "databases":
		return "databases"
	case "domains":
		return "domains"
	case "api-tokens":
		return "org"
	case "service-accounts":
		return "org"
	case "alerts":
		return "alerts"
	case "audit-logs":
		return "audit"
	case "members":
		return "org"
	case "package":
		return "org"
	case "billing":
		return "billing"
	case "ssh-keys":
		return "websites"
	case "crons":
		return "websites"
	case "ftp-accounts":
		return "websites"
	case "redirects", "dns-zones", "dns-records":
		return "domains"
	case "files", "files/content", "files/upload", "files/download":
		return "websites"
	case "config":
		return "websites"
	case "application":
		return "websites"
	case "wordpress":
		return "websites"
	case "usage":
		return "websites"
	case "events":
		return "events"
	}
	return ""
}

// requiredScope derives the scope needed for a method+path on an
// org-scoped route. Second-level path segments refine the mapping
// (deployments/backups/monitoring live under websites).
func requiredScope(method, path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// Expect: v1 / organizations / {org} / resource...
	if len(parts) < 2 || parts[0] != "v1" || parts[1] != "organizations" {
		return "", false
	}
	// v1/organizations            -> list orgs / create org
	// v1/organizations/{org}      -> org details / rename
	if len(parts) <= 3 {
		if method == http.MethodGet {
			return "org:read", true
		}
		return "org:write", true
	}
	resource := parts[3]
	// Refinements for sub-resources under websites.
	if resource == "websites" && len(parts) >= 6 {
		switch parts[5] {
		case "deploy", "rollback", "deployment-config", "deployments", "promote", "staging":
			if method == http.MethodGet {
				return "deployments:read", true
			}
			return "deployments:write", true
		case "backups":
			if method == http.MethodGet {
				return "backups:read", true
			}
			return "backups:write", true
		case "domains":
			if method == http.MethodGet {
				return "domains:read", true
			}
			return "domains:write", true
		case "redirects":
			if method == http.MethodGet {
				return "domains:read", true
			}
			return "domains:write", true
		case "ftp-accounts":
			if method == http.MethodGet {
				return "websites:read", true
			}
			return "websites:write", true
		case "suspend", "resume", "terminate", "purge":
			return "websites:write", true
		case "bandwidth":
			// bandwidth + bandwidth/history are read-only surfaces.
			return "websites:read", true
		case "quota":
			if method == http.MethodGet {
				return "websites:read", true
			}
			return "websites:write", true
		case "dns-zone":
			if method == http.MethodGet {
				return "domains:read", true
			}
			return "domains:write", true
		case "health":
			return "monitoring:read", true
		case "jobs":
			return "websites:read", true
		case "backup-config":
			return "backups:write", true
		}
	}
	// Top-level special: backup restore + alerts resolve.
	if resource == "backups" && method == http.MethodPost {
		return "backups:write", true
	}

	group := scopeResource(resource)
	if group == "" {
		return "", false
	}
	if method == http.MethodGet {
		return group + ":read", true
	}
	return group + ":write", true
}

// tokenExempt paths are reachable with any valid token (identity-level, no
// resource scope applies).
func tokenExempt(path string) bool {
	switch path {
	case "/v1/auth/me", "/v1/ws":
		return true
	}
	return false
}

// requiredAdminScope derives the scope needed for a method+path on the
// platform-admin surface: the cross-organization read models
// (/v1/adminview, /v1/admin), the job console and platform settings. Only
// platform admin keys (epa_) can hold admin scopes, so org tokens are
// excluded from these routes by construction.
func requiredAdminScope(method, path string) (string, bool) {
	admin := func() (string, bool) {
		if method == http.MethodGet || method == http.MethodHead {
			return "admin:read", true
		}
		return "admin:write", true
	}
	switch {
	case strings.HasPrefix(path, "/v1/adminview/"),
		strings.HasPrefix(path, "/v1/admin/"):
		return admin()
	case path == "/v1/jobs",
		path == "/v1/settings",
		path == "/v1/audit-logs":
		return admin()
	}
	return "", false
}

// ScopeEnforce rejects token-authenticated requests whose scopes don't cover
// the route. Session-authenticated users are unaffected. Deny-by-default:
// an unmapped path is refused for API tokens rather than allowed.
func ScopeEnforce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsAPIToken(r.Context()) {
			if tokenExempt(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			// Platform admin surface first: only epa_ keys can satisfy it.
			if scope, ok := requiredAdminScope(r.Method, r.URL.Path); ok {
				scopes := TokenScopes(r.Context())
				if !scopes[scope] {
					RespondError(w, ErrForbidden("token missing required scope: "+scope))
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			scope, ok := requiredScope(r.Method, r.URL.Path)
			if !ok {
				RespondError(w, ErrForbidden("api tokens cannot access this route"))
				return
			}
			scopes := TokenScopes(r.Context())
			if !scopes[scope] {
				RespondError(w, ErrForbidden("token missing required scope: "+scope))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RateLimit applies a token bucket per key (token id or client IP). Auth
// endpoints use a stricter budget.
func RateLimit(limiter *TokenBucket, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/v1/auth/") {
			if !limiter.Allow("auth:"+clientKey(r), 10.0/60.0, 20) {
				w.Header().Set("Retry-After", "10")
				RespondError(w, &APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "too many authentication attempts"})
				return
			}
		} else if strings.HasPrefix(path, "/v1/") {
			key := clientKey(r)
			if IsPlatformKey(r.Context()) {
				key = "tok:platform:" + key
			} else if IsAPIToken(r.Context()) {
				key = "tok:" + TokenOrgID(r.Context()) + ":" + key
			}
			if !limiter.Allow(key, 300.0/60.0, 600) {
				w.Header().Set("Retry-After", "1")
				RespondError(w, &APIError{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "rate limit exceeded"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func clientKey(r *http.Request) string {
	return ClientIP(r)
}
