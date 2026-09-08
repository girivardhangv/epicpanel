// Package apitokens — service accounts: org-owned machine principals backed
// by a dedicated API token (kind='service'). They never inherit platform
// privileges and are confined to their organization like every other token.
package apitokens

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

var ErrServiceAccountNotFound = errors.New("service account not found")

type ServiceAccount struct {
	ID           uuid.UUID  `json:"id"`
	Organization uuid.UUID  `json:"organization_id"`
	Name         string     `json:"name"`
	TokenID      *uuid.UUID `json:"token_id,omitempty"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type ServiceAccountStore struct {
	Pool *pgxpool.Pool
}

const saCols = `id, organization_id, name, token_id, revoked_at, created_at`

func scanSA(row pgx.Row) (*ServiceAccount, error) {
	var sa ServiceAccount
	err := row.Scan(&sa.ID, &sa.Organization, &sa.Name, &sa.TokenID, &sa.RevokedAt, &sa.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &sa, nil
}

// Create registers the service account and returns its id for token binding.
func (s *ServiceAccountStore) Create(ctx context.Context, orgID, createdBy uuid.UUID, name string) (*ServiceAccount, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO service_accounts (organization_id, name, created_by)
		VALUES ($1, $2, $3)
		RETURNING `+saCols,
		orgID, name, createdBy,
	)
	return scanSA(row)
}

// BindToken links the freshly minted API token to the account.
func (s *ServiceAccountStore) BindToken(ctx context.Context, saID, tokenID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE service_accounts SET token_id = $2 WHERE id = $1`, saID, tokenID)
	return err
}

func (s *ServiceAccountStore) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]ServiceAccount, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+saCols+` FROM service_accounts WHERE organization_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceAccount
	for rows.Next() {
		sa, err := scanSA(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sa)
	}
	return out, rows.Err()
}

// Revoke disables the account and its token in one transaction.
func (s *ServiceAccountStore) Revoke(ctx context.Context, orgID, saID uuid.UUID) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `
		UPDATE service_accounts SET revoked_at = now() WHERE id = $1 AND organization_id = $2 AND revoked_at IS NULL
	`, saID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrServiceAccountNotFound
	}
	if _, err := tx.Exec(ctx, `
		UPDATE api_tokens SET revoked_at = now()
		WHERE service_account_id = $1 AND revoked_at IS NULL
	`, saID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ServiceAccountHandler — org-scoped admin endpoints.
type ServiceAccountHandler struct {
	Accounts   *ServiceAccountStore
	Tokens     *Store
	Audit      *audit.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *ServiceAccountHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/organizations/{org_id}/service-accounts", h.requireAdmin(h.Create))
	mux.HandleFunc("GET /v1/organizations/{org_id}/service-accounts", h.requireAdmin(h.List))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/service-accounts/{sa_id}", h.requireAdmin(h.Revoke))
}

func (h *ServiceAccountHandler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		orgID, apiErr := h.RequireOrg(r, r.PathValue("org_id"), organizations.RoleAdmin)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r)
		_ = orgID
	}
}

func (h *ServiceAccountHandler) auditOrg(r *http.Request, orgID uuid.UUID, action, id string, meta map[string]any) {
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
		OrganizationID: &orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "service_account",
		ResourceID:     id,
		Metadata:       meta,
		IP:             httpapi.ClientIP(r),
	})
}

func (h *ServiceAccountHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	var req struct {
		Name          string   `json:"name"`
		Scopes        []string `json:"scopes"`
		ExpiresInDays int      `json:"expires_in_days,omitempty"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Name = trimSpace(req.Name)
	if len(req.Name) < 2 || len(req.Name) > 100 {
		httpapi.RespondError(w, httpapi.ErrValidation("name must be 2-100 characters"))
		return
	}
	if len(req.Scopes) == 0 {
		httpapi.RespondError(w, httpapi.ErrValidation("at least one scope is required"))
		return
	}
	for _, sc := range req.Scopes {
		if !ValidScope(sc) {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid scope: "+sc))
			return
		}
	}
	var expires *time.Time
	if req.ExpiresInDays > 0 {
		t := time.Now().Add(time.Duration(req.ExpiresInDays) * 24 * time.Hour)
		expires = &t
	}

	user, _ := httpapi.UserFrom(r.Context())
	createdBy, _ := uuid.Parse(user.ID)

	sa, err := h.Accounts.Create(r.Context(), orgID, createdBy, req.Name)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	token, raw, err := h.Tokens.CreateKind(r.Context(), orgID, createdBy, req.Name, req.Scopes, expires, KindService, sa.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Accounts.BindToken(r.Context(), sa.ID, token.ID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditOrg(r, orgID, "service_account.created", sa.ID.String(), map[string]any{"name": sa.Name, "scopes": req.Scopes})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"service_account": sa,
		"token":           token,
		"raw_token":       raw, // shown exactly once
	})
}

func (h *ServiceAccountHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	list, err := h.Accounts.ListForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []ServiceAccount{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"service_accounts": list})
}

func (h *ServiceAccountHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	saID, err := uuid.Parse(r.PathValue("sa_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid service account id"))
		return
	}
	if err := h.Accounts.Revoke(r.Context(), orgID, saID); err != nil {
		if errors.Is(err, ErrServiceAccountNotFound) {
			httpapi.RespondError(w, httpapi.ErrNotFound("service account not found"))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.auditOrg(r, orgID, "service_account.revoked", saID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

func trimSpace(s string) string {
	out := []rune(s)
	start, end := 0, len(out)-1
	for start <= end && (out[start] == ' ' || out[start] == '\t') {
		start++
	}
	for end >= start && (out[end] == ' ' || out[end] == '\t') {
		end--
	}
	return string(out[start : end+1])
}
