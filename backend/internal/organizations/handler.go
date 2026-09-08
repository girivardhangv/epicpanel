package organizations

import (
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

type Handler struct {
	Store *Store
	Audit *audit.Store
}

// auditOrg records an organization-scoped event; failures are logged, never fatal.
func (h *Handler) auditOrg(r *http.Request, action, resourceType, resourceID string, orgID *uuid.UUID, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	var actorID *uuid.UUID
	if usr, ok := httpapi.UserFrom(r.Context()); ok {
		if uid, err := uuid.Parse(usr.ID); err == nil {
			actorID = &uid
		}
	}
	h.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		Metadata:       meta,
		IP:             clientIP(r),
	})
}

func clientIP(r *http.Request) string { return httpapi.ClientIP(r) }

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/organizations", h.Create)
	mux.HandleFunc("GET /v1/organizations", h.ListMine)
	mux.HandleFunc("GET /v1/organizations/{org_id}", h.RequireMinimumRole(RoleBilling, h.Get))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}", h.RequireMinimumRole(RoleAdmin, h.Update))
	mux.HandleFunc("GET /v1/organizations/{org_id}/members", h.RequireMinimumRole(RoleBilling, h.ListMembers))
	mux.HandleFunc("POST /v1/organizations/{org_id}/members", h.RequireMinimumRole(RoleAdmin, h.AddMember))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/members/{user_id}", h.RequireMinimumRole(RoleAdmin, h.UpdateMember))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/members/{user_id}", h.RequireMinimumRole(RoleAdmin, h.RemoveMember))
}

type createRequest struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	user, ok := httpapi.UserFrom(r.Context())
	if !ok {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
		return
	}

	var req createRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) < 2 || len(req.Name) > 100 {
		httpapi.RespondError(w, httpapi.ErrValidation("name must be 2-100 characters"))
		return
	}

	slug := NormalizeSlug(req.Slug)
	if slug == "" {
		slug = Slugify(req.Name)
	}
	if !ValidSlug(slug) {
		httpapi.RespondError(w, httpapi.ErrValidation("slug must match [a-z0-9-] (2-64 chars, start/end alphanumeric)"))
		return
	}

	uid, err := parseUser(user.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	org, err := h.Store.Create(r.Context(), req.Name, slug, uid)
	if err == ErrSlugTaken {
		httpapi.RespondError(w, httpapi.ErrConflict("slug already in use"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditOrg(r, "organization.created", "organization", org.ID.String(), &org.ID, map[string]any{"name": org.Name, "slug": org.Slug})
	httpapi.WriteJSON(w, http.StatusCreated, org)
}

func (h *Handler) ListMine(w http.ResponseWriter, r *http.Request) {
	user, ok := httpapi.UserFrom(r.Context())
	if !ok {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
		return
	}
	uid, err := parseUser(user.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	orgs, err := h.Store.ListForUser(r.Context(), uid)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"organizations": orgs})
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	orgID, _ := parseOrg(r.PathValue("org_id"))
	org, err := h.Store.GetByID(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, org)
}

type updateRequest struct {
	Name string `json:"name"`
}

func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	orgID, _ := parseOrg(r.PathValue("org_id"))
	var req updateRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if len(req.Name) < 2 || len(req.Name) > 100 {
		httpapi.RespondError(w, httpapi.ErrValidation("name must be 2-100 characters"))
		return
	}
	org, err := h.Store.Update(r.Context(), orgID, req.Name)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditOrg(r, "organization.updated", "organization", org.ID.String(), &org.ID, map[string]any{"name": org.Name})
	httpapi.WriteJSON(w, http.StatusOK, org)
}

type addMemberRequest struct {
	Email string `json:"email"`
	Role  Role   `json:"role"`
}

func (h *Handler) AddMember(w http.ResponseWriter, r *http.Request) {
	orgID, _ := parseOrg(r.PathValue("org_id"))
	var req addMemberRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !ValidRole(req.Role) {
		httpapi.RespondError(w, httpapi.ErrValidation("role must be one of: owner, admin, reseller, developer, billing, support"))
		return
	}
	target, err := h.Store.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if target == nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("no user with that email"))
		return
	}
	// Only owners (or platform admins) may grant the owner role.
	if req.Role == RoleOwner {
		ok, apiErr := h.actorIsOwner(r, orgID)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		if !ok {
			httpapi.RespondError(w, httpapi.ErrForbidden("only an organization owner can grant the owner role"))
			return
		}
	}
	if err := h.Store.SetMemberRole(r.Context(), orgID, target.ID, req.Role); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditOrg(r, "member.added", "member", target.ID.String(), &orgID, map[string]any{"email": target.Email, "role": req.Role})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"user_id": target.ID,
		"email":   target.Email,
		"role":    req.Role,
	})
}

type updateMemberRequest struct {
	Role Role `json:"role"`
}

func (h *Handler) UpdateMember(w http.ResponseWriter, r *http.Request) {
	orgID, _ := parseOrg(r.PathValue("org_id"))
	userID, ok := parseOptionalUser(r.PathValue("user_id"))
	if !ok {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid user id"))
		return
	}
	var req updateMemberRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if !ValidRole(req.Role) {
		httpapi.RespondError(w, httpapi.ErrValidation("role must be one of: owner, admin, reseller, developer, billing, support"))
		return
	}
	// Owner changes require an owner actor; demotion protects the last owner.
	current, err := h.Store.RoleFor(r.Context(), orgID, userID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if current == "" {
		httpapi.RespondError(w, httpapi.ErrNotFound("member not found"))
		return
	}
	ownerChange := req.Role == RoleOwner || current == RoleOwner
	if ownerChange {
		ok, apiErr := h.actorIsOwner(r, orgID)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		if !ok {
			httpapi.RespondError(w, httpapi.ErrForbidden("only an organization owner can change owner membership"))
			return
		}
	}
	if current == RoleOwner && req.Role != RoleOwner {
		owners, err := h.Store.CountOwners(r.Context(), orgID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if owners <= 1 {
			httpapi.RespondError(w, httpapi.ErrConflict("cannot demote the last owner of an organization"))
			return
		}
	}
	if err := h.Store.SetMemberRole(r.Context(), orgID, userID, req.Role); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditOrg(r, "member.role_changed", "member", userID.String(), &orgID, map[string]any{"role": req.Role})
	w.WriteHeader(http.StatusNoContent)
}

// actorIsOwner reports whether the authenticated principal is an organization
// owner. Platform admins bypass this via session only — API tokens never
// inherit platform-admin (RBAC v2 rule).
func (h *Handler) actorIsOwner(r *http.Request, orgID uuid.UUID) (bool, *httpapi.APIError) {
	user, ok := httpapi.UserFrom(r.Context())
	if !ok {
		return false, httpapi.ErrUnauthorized("authentication required")
	}
	if user.Role == "admin" && !httpapi.IsAPIToken(r.Context()) {
		return true, nil
	}
	uid, err := uuid.Parse(user.ID)
	if err != nil {
		return false, httpapi.ErrInternal(err)
	}
	role, err := h.Store.RoleFor(r.Context(), orgID, uid)
	if err != nil {
		return false, httpapi.ErrInternal(err)
	}
	return role == RoleOwner, nil
}

func (h *Handler) RemoveMember(w http.ResponseWriter, r *http.Request) {
	orgID, _ := parseOrg(r.PathValue("org_id"))
	userID, ok := parseOptionalUser(r.PathValue("user_id"))
	if !ok {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid user id"))
		return
	}
	role, err := h.Store.RoleFor(r.Context(), orgID, userID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if role == "" {
		httpapi.RespondError(w, httpapi.ErrNotFound("member not found"))
		return
	}
	if role == RoleOwner {
		owners, err := h.Store.CountOwners(r.Context(), orgID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if owners <= 1 {
			httpapi.RespondError(w, httpapi.ErrConflict("cannot remove the last owner of an organization"))
			return
		}
	}
	if err := h.Store.RemoveMember(r.Context(), orgID, userID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditOrg(r, "member.removed", "member", userID.String(), &orgID, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListMembers(w http.ResponseWriter, r *http.Request) {
	orgID, _ := parseOrg(r.PathValue("org_id"))
	members, err := h.Store.Members(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"members": members})
}
