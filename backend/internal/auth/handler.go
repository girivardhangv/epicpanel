package auth

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/users"
)

var emailRe = regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`)

type Handler struct {
	Users    *users.Store
	Sessions *SessionStore
	MFA      *MFAStore
	Audit    *audit.Store
	Cfg      config.Config
	// SetupDone reports whether first-boot setup completed; once true,
	// public registration is disabled (accounts are created by admins,
	// cPanel model). Tests/bootstrap leave it unset.
	SetupDone func(ctx context.Context) bool
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type authResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      any       `json:"user"`
}

func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	// Registration lockdown (audit S3): after setup completes, only platform
	// admins create accounts (POST /v1/admin/users). The first-user bootstrap
	// and integration tests bypass this via the unset SetupDone hook.
	if h.SetupDone != nil && h.SetupDone(r.Context()) {
		httpapi.RespondError(w, httpapi.ErrForbidden("registration is disabled; ask the administrator to create an account"))
		return
	}
	var req registerRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Name = strings.TrimSpace(req.Name)

	var details []string
	if !emailRe.MatchString(req.Email) {
		details = append(details, "email is not a valid address")
	}
	if len(req.Password) < 10 {
		details = append(details, "password must be at least 10 characters")
	}
	if len(req.Name) < 1 {
		details = append(details, "name is required")
	}
	if len(details) > 0 {
		httpapi.RespondError(w, httpapi.ErrValidationDetails("validation failed", details))
		return
	}

	hash, err := HashPassword(req.Password)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	u, _, err := h.Users.Create(r.Context(), req.Email, hash, req.Name)
	if err == users.ErrEmailTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("email already registered"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	token, sess, err := h.Sessions.Create(r.Context(), u.ID, h.Cfg.SessionTTL, r.UserAgent(), clientIP(r))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.setSessionCookie(w, r, token)
	h.audit(r, "user.registered", &u.ID, nil, map[string]any{"is_first_user": u.IsAdmin})
	httpapi.WriteJSON(w, http.StatusCreated, authResponse{Token: token, ExpiresAt: sess.ExpiresAt, User: u})
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	u, err := h.Users.GetByEmail(r.Context(), req.Email)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if u == nil || !CheckPassword(u.PasswordHash, req.Password) {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("invalid email or password"))
		return
	}
	if u.Status != "active" {
		httpapi.RespondError(w, httpapi.ErrForbidden("account is disabled"))
		return
	}

	// 2FA foundation: MFA-enabled accounts do not get a session until the
	// second factor verifies (POST /v1/auth/mfa/verify).
	if h.MFA != nil {
		on, err := h.MFA.MFAEnabled(r.Context(), u.ID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if on {
			challenge, err := h.MFA.BeginChallenge(r.Context(), u.ID, mfaChallengeTTL)
			if err != nil {
				httpapi.RespondError(w, httpapi.ErrInternal(err))
				return
			}
			h.audit(r, "auth.mfa_challenge", &u.ID, nil, nil)
			httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{
				"mfa_required": true,
				"mfa_token":    challenge,
			})
			return
		}
	}

	token, sess, err := h.Sessions.Create(r.Context(), u.ID, h.Cfg.SessionTTL, r.UserAgent(), clientIP(r))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.setSessionCookie(w, r, token)
	h.audit(r, "auth.login", &u.ID, nil, nil)
	httpapi.WriteJSON(w, http.StatusOK, authResponse{Token: token, ExpiresAt: sess.ExpiresAt, User: u})
}

func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	token := sessionToken(r)
	if token != "" {
		if err := h.Sessions.Revoke(r.Context(), token); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if usr, ok := httpapi.UserFrom(r.Context()); ok {
			if uid, err := uuid.Parse(usr.ID); err == nil {
				h.audit(r, "auth.logout", &uid, nil, nil)
			}
		}
	}
	clearSessionCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// audit records an authentication event; failures are logged, never fatal.
func (h *Handler) audit(r *http.Request, action string, userID *uuid.UUID, orgID *uuid.UUID, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	h.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: orgID,
		ActorUserID:    userID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "user",
		Metadata:       meta,
		IP:             clientIP(r),
	})
}

func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	usr, ok := httpapi.UserFrom(r.Context())
	if !ok {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
		return
	}
	id, err := uuid.Parse(usr.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	u, err := h.Users.GetByID(r.Context(), id)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if u == nil {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("user no longer exists"))
		return
	}
	// API/service tokens resolve to a synthetic identity: the token is
	// org-confined and NEVER carries the creator's platform-admin flag.
	if httpapi.IsAPIToken(r.Context()) {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{
			"id": u.ID, "email": u.Email, "name": u.Name,
			"is_platform_admin": false, "status": u.Status,
			"api_token": true, "organization_id": httpapi.TokenOrgID(r.Context()),
		})
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, u)
}

const cookieName = "epicpanel_session"

// requestIsHTTPS reports whether THIS request arrived over TLS — directly
// (r.TLS) or through a proxy that set X-Forwarded-Proto (trusted proxies
// are enforced upstream in the chain). A global Secure flag breaks plain-
// HTTP installs: browsers silently drop Secure cookies over http://, so
// login "succeeds" but every following request is 401. Per-request is the
// boring correct behavior: Secure when HTTPS, not when HTTP.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}

func writeSessionCookie(w http.ResponseWriter, r *http.Request, ttl time.Duration, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	writeSessionCookie(w, r, h.Cfg.SessionTTL, token)
}

func sessionToken(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	if strings.HasPrefix(authz, "Bearer ") {
		return strings.TrimPrefix(authz, "Bearer ")
	}
	if c, err := r.Cookie(cookieName); err == nil {
		return c.Value
	}
	return ""
}

func clientIP(r *http.Request) string { return httpapi.ClientIP(r) }
