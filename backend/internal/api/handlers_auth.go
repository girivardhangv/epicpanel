package api

import (
	"net/http"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
)

type loginRequest struct {
	Identifier string `json:"identifier"`
	Password   string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	ip := httpx.ClientIP(r, s.cfg.Server.TrustedProxy)
	res, err := s.deps.Auth.Login(r.Context(), req.Identifier, req.Password, ip, r.UserAgent())
	if err != nil {
		Error(w, r, err)
		return
	}
	auth.WriteAuthCookies(w, res.Token, res.Identity.CSRFToken, res.LifetimeSec, s.sessionCookieOpts())
	JSON(w, r, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":           res.Identity.UserID,
			"username":     res.Identity.Username,
			"email":        res.Identity.Email,
			"display_name": res.Identity.DisplayName,
			"permissions":  res.Identity.Permissions,
		},
	})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	id := auth.IdentityFrom(r.Context())
	var req changePasswordRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if err := s.deps.Auth.ChangePassword(r.Context(), id, req.CurrentPassword, req.NewPassword, req.ConfirmPassword); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"changed": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := auth.TokenFromRequest(r)
	ip := httpx.ClientIP(r, s.cfg.Server.TrustedProxy)
	s.deps.Auth.Logout(r.Context(), token, ip, r.UserAgent(), auth.IdentityFrom(r.Context()))
	auth.ClearAuthCookies(w, s.sessionCookieOpts())
	JSON(w, r, http.StatusOK, map[string]any{"logged_out": true})
}

// refresh rotates the session token (sliding sessions with token rotation).
func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	rawToken := auth.TokenFromRequest(r)
	if rawToken == "" {
		Error(w, r, apierror.Unauthorized)
		return
	}
	lifetimeMin := s.deps.Settings.Int(r.Context(),
		settings.KeySessionLifetimeMinutes, int(s.cfg.Security.SessionLifetime.Minutes()), 30, 43200)

	newID, newToken, err := s.deps.Sessions.Refresh(r.Context(), rawToken,
		time.Now().Add(time.Duration(lifetimeMin)*time.Minute))
	if err != nil {
		auth.ClearAuthCookies(w, s.sessionCookieOpts())
		Error(w, r, apierror.SessionExpired)
		return
	}
	perms, perr := s.deps.RBAC.PermissionsForUser(r.Context(), newID.UserID)
	if perr == nil {
		newID.Permissions = perms
	}
	auth.WriteAuthCookies(w, newToken, newID.CSRFToken, lifetimeMin*60, s.sessionCookieOpts())
	JSON(w, r, http.StatusOK, map[string]any{
		"refreshed":   true,
		"permissions": newID.Permissions,
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	id := auth.IdentityFrom(r.Context())
	JSON(w, r, http.StatusOK, map[string]any{
		"user": map[string]any{
			"id":           id.UserID,
			"username":     id.Username,
			"email":        id.Email,
			"display_name": id.DisplayName,
			"permissions":  id.Permissions,
		},
	})
}
