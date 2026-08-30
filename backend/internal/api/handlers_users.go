package api

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/rbac"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/epicbyte/epicpanel/backend/internal/users"
)

/* ------------------------------ helpers ---------------------------------- */

func actorIDFromString(s string) *uuid.UUID {
	if u, err := uuid.Parse(s); err == nil {
		return &u
	}
	return nil
}

// minLengthSetting / classesSetting read operator-tunable password policy;
// both are cache-backed so the per-request cost is one mutex check.
func (s *Server) minLengthSetting(ctx context.Context) int {
	return s.deps.Settings.Int(ctx, settings.KeyPasswordMinLength,
		s.cfg.Security.PasswordMinLength, 10, 128)
}

func (s *Server) classesSetting(ctx context.Context) int {
	return s.deps.Settings.Int(ctx, settings.KeyPasswordRequireClasses,
		s.cfg.Security.PasswordRequireClasses, 3, 4)
}

type auditMeta = map[string]any

func (s *Server) auditUserAction(r *http.Request, id *auth.Identity, action, resourceID string, meta auditMeta) {
	var label string
	if id != nil {
		label = id.Username
	} else {
		label = "unknown"
	}
	s.deps.Audit.Log(r.Context(), audit.Entry{
		ActorType: "user", Label: label,
		Action: action, Resource: "user", ResourceID: resourceID,
		IP:        httpx.ClientIP(r, s.cfg.Server.TrustedProxy),
		UserAgent: r.UserAgent(),
		Metadata:  meta,
	})
}

/* ------------------------------- users ----------------------------------- */

type createUserRequest struct {
	Username    string   `json:"username"`
	Email       string   `json:"email"`
	DisplayName string   `json:"display_name"`
	Password    string   `json:"password"`
	Roles       []string `json:"roles"`
}

type updateUserRequest struct {
	DisplayName *string   `json:"display_name"`
	Email       *string   `json:"email"`
	IsActive    *bool     `json:"is_active"`
	Roles       *[]string `json:"roles"`
	NewPassword string    `json:"new_password,omitempty"` // optional admin-side reset
}

func (s *Server) handleUsersList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.UserManager.List(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) handleUsersCreate(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	id := auth.IdentityFrom(r.Context())

	created, err := s.deps.UserManager.Create(r.Context(), users.CreateInput{
		Username:    req.Username,
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Password:    req.Password,
		RoleNames:   req.Roles,
		MinLength:   s.minLengthSetting(r.Context()),
		Classes:     s.classesSetting(r.Context()),
	})
	if err != nil {
		Error(w, r, err)
		return
	}
	s.auditUserAction(r, id, audit.ActionUserCreated, created.ID, auditMeta{
		"username": created.Username,
		"roles":    created.Roles,
	})
	JSON(w, r, http.StatusCreated, created)
}

func (s *Server) handleUsersGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.deps.UserManager.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, v)
}

func (s *Server) handleUsersUpdate(w http.ResponseWriter, r *http.Request) {
	var req updateUserRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	target := chi.URLParam(r, "id")
	id := auth.IdentityFrom(r.Context())

	updated, err := s.deps.UserManager.Update(r.Context(), id.UserID, target, users.UpdateInput{
		DisplayName: req.DisplayName,
		Email:       req.Email,
		IsActive:    req.IsActive,
		RoleNames:   req.Roles,
	})
	if err != nil {
		Error(w, r, err)
		return
	}
	meta := auditMeta{}
	if req.DisplayName != nil {
		meta["display_name"] = true
	}
	if req.Email != nil {
		meta["email"] = true
	}
	if req.IsActive != nil {
		meta["is_active"] = *req.IsActive
	}
	if req.Roles != nil {
		meta["roles"] = *req.Roles
	}
	s.auditUserAction(r, id, audit.ActionUserUpdated, target, meta)

	// The route already required users.edit; an optional administrator-side
	// password rotation may ride along on the same update.
	if req.NewPassword != "" {
		err := s.deps.UserManager.AdminSetPassword(r.Context(), target, req.NewPassword,
			s.minLengthSetting(r.Context()), s.classesSetting(r.Context()))
		if err != nil {
			Error(w, r, err)
			return
		}
		s.auditUserAction(r, id, audit.ActionPasswordChanged, target,
			auditMeta{"via": "admin_reset"})
	}
	JSON(w, r, http.StatusOK, updated)
}

func (s *Server) handleUsersDelete(w http.ResponseWriter, r *http.Request) {
	target := chi.URLParam(r, "id")
	id := auth.IdentityFrom(r.Context())
	if err := s.deps.UserManager.Delete(r.Context(), id.UserID, target); err != nil {
		Error(w, r, err)
		return
	}
	s.auditUserAction(r, id, audit.ActionUserDeleted, target, nil)
	JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

/* ------------------------------- roles ------------------------------------ */

type roleUpsertRequest struct {
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	// Name only applies to creation; updates intentionally cannot rename roles.
	Name string `json:"name"`
}

func (s *Server) handleRolesListDetail(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.RBAC.ListRolesDetail(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"roles": out})
}

func (s *Server) handleRolesCreate(w http.ResponseWriter, r *http.Request) {
	var req roleUpsertRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	id := auth.IdentityFrom(r.Context())
	created, err := s.deps.RBAC.CreateRole(r.Context(), rbac.UpsertRoleInput{
		Name:        req.Name,
		Description: req.Description,
		Permissions: req.Permissions,
	})
	if err != nil {
		Error(w, r, err)
		return
	}
	actorID := actorIDFromString(id.UserID)
	s.deps.Audit.Log(r.Context(), audit.Entry{
		ActorType: "user", ActorID: actorID, Label: id.Username,
		Action: audit.ActionRoleCreated, Resource: "role", ResourceID: created.ID,
		IP:       httpx.ClientIP(r, s.cfg.Server.TrustedProxy),
		UserAgent: r.UserAgent(),
		Metadata:  map[string]any{"name": created.Name, "permissions": created.Permissions},
	})
	JSON(w, r, http.StatusCreated, created)
}

func (s *Server) handleRolesUpdate(w http.ResponseWriter, r *http.Request) {
	var req roleUpsertRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	id := auth.IdentityFrom(r.Context())
	roleID := chi.URLParam(r, "id")
	updated, err := s.deps.RBAC.UpdateRole(r.Context(), roleID, rbac.UpsertRoleInput{
		Name:        "",
		Description: req.Description,
		Permissions: req.Permissions,
	})
	if err != nil {
		Error(w, r, err)
		return
	}
	actorID := actorIDFromString(id.UserID)
	s.deps.Audit.Log(r.Context(), audit.Entry{
		ActorType: "user", ActorID: actorID, Label: id.Username,
		Action: audit.ActionRoleUpdated, Resource: "role", ResourceID: roleID,
		IP:        httpx.ClientIP(r, s.cfg.Server.TrustedProxy),
		UserAgent: r.UserAgent(),
		Metadata:  map[string]any{"permissions": updated.Permissions},
	})
	JSON(w, r, http.StatusOK, updated)
}

func (s *Server) handleRolesDelete(w http.ResponseWriter, r *http.Request) {
	id := auth.IdentityFrom(r.Context())
	roleID := chi.URLParam(r, "id")
	if err := s.deps.RBAC.DeleteRole(r.Context(), roleID); err != nil {
		Error(w, r, err)
		return
	}
	actorID := actorIDFromString(id.UserID)
	s.deps.Audit.Log(r.Context(), audit.Entry{
		ActorType: "user", ActorID: actorID, Label: id.Username,
		Action: audit.ActionRoleDeleted, Resource: "role", ResourceID: roleID,
		IP:        httpx.ClientIP(r, s.cfg.Server.TrustedProxy),
		UserAgent: r.UserAgent(),
	})
	JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

/* ------------------------- public password-reset flow --------------------- */

type forgotPasswordRequest struct {
	Identifier string `json:"identifier"`
}

// always answers identically regardless of account existence
func (s *Server) handleForgotPassword(w http.ResponseWriter, r *http.Request) {
	var req forgotPasswordRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	ip := httpx.ClientIP(r, s.cfg.Server.TrustedProxy)
	s.deps.Auth.StartPasswordReset(r.Context(), req.Identifier, ip)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte(`{"accepted":true}`))
}

type resetPasswordRequest struct {
	Token           string `json:"token"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirm_password"`
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	ip := httpx.ClientIP(r, s.cfg.Server.TrustedProxy)
	if err := s.deps.Auth.CompletePasswordReset(r.Context(), req.Token,
		req.Password, req.ConfirmPassword, ip); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"reset": true})
}
