package databases

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

// DBLimits is the package quota gate for databases.
type DBLimits interface {
	CheckDatabaseAllowed(ctx context.Context, orgID uuid.UUID) error
}

type Handler struct {
	Databases *Store
	Jobs      *jobs.Store
	// SSOKey fetches the dbadmin SSO key (hex, 32 bytes) — nil when database
	// tools are not installed. Implemented by the api layer over settings.
	SSOKey func(ctx context.Context) (string, error)
	// SettingsStore persists the SSO key reported by db tools install jobs.
	SettingsStore SettingsSetter
	// OnDatabaseReady fires when a create_database job succeeds and the
	// credential is available (WordPress chaining). Payload is the plaintext
	// password in memory only — never persisted by the subscriber.
	OnDatabaseReady func(ctx context.Context, dbID uuid.UUID, password string)
	// PackageChecker enforces the org's max_databases limit.
	PackageChecker DBLimits
	Orgs           *organizations.Store
	Servers        *servers.Store
	Audit          *audit.Store
	RequireOrg     func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

// SettingsSetter is the minimal settings persistence interface used by the
// db tools job fanout (avoids importing the settings package).
type SettingsSetter interface {
	Set(ctx context.Context, key, value string) error
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/organizations/{org_id}/databases", h.requireOrg(organizations.RoleDeveloper, h.Create))
	mux.HandleFunc("GET /v1/organizations/{org_id}/databases", h.requireOrg(organizations.RoleBilling, h.List))
	mux.HandleFunc("GET /v1/organizations/{org_id}/databases/{db_id}", h.requireOrg(organizations.RoleBilling, h.Get))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/databases/{db_id}", h.requireOrg(organizations.RoleAdmin, h.Delete))
	mux.HandleFunc("GET /v1/organizations/{org_id}/databases/{db_id}/credentials", h.requireOrg(organizations.RoleDeveloper, h.Reveal))
	mux.HandleFunc("GET /v1/organizations/{org_id}/databases/{db_id}/pma-sso", h.requireOrg(organizations.RoleDeveloper, h.PmaSSO))
}

func (h *Handler) requireOrg(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		orgID, apiErr := h.RequireOrg(r, r.PathValue("org_id"), min)
		if apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r.WithContext(withOrgID(r.Context(), orgID)))
	}
}

type orgIDCtxKey struct{}

func withOrgID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, orgIDCtxKey{}, id)
}

func OrgIDFromRequest(r *http.Request) (uuid.UUID, bool) {
	id, ok := r.Context().Value(orgIDCtxKey{}).(uuid.UUID)
	return id, ok
}

func (h *Handler) audit(r *http.Request, orgID *uuid.UUID, action, resourceType, resourceID string, meta map[string]any) {
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

var errOrgContext = errStr("org id missing from request context")

type errStr string

func (e errStr) Error() string { return string(e) }

// POST /v1/organizations/{org_id}/databases
// {"name": "shop_db", "server_id": "...", "engine": "mariadb", "website_id": "..."}
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	var req struct {
		Name      string `json:"name"`
		ServerID  string `json:"server_id"`
		Engine    string `json:"engine"`
		WebsiteID string `json:"website_id"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if !ValidEngine(req.Engine) {
		httpapi.RespondError(w, httpapi.ErrValidation("engine must be one of: mysql, mariadb, postgresql"))
		return
	}
	if h.PackageChecker != nil {
		if err := h.PackageChecker.CheckDatabaseAllowed(r.Context(), orgID); err != nil {
			httpapi.RespondError(w, httpapi.ErrForbidden(err.Error()))
			return
		}
	}
	var serverID uuid.UUID
	if req.ServerID == "" {
		picked, pickErr := h.Servers.AutoPickServer(r.Context(), "", "")
		if pickErr != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("could not select a server automatically: "+pickErr.Error()))
			return
		}
		serverID = picked
	} else {
		parsed, err := uuid.Parse(req.ServerID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("server_id must be a UUID"))
			return
		}
		serverID = parsed
	}
	if _, err := h.Servers.GetByID(r.Context(), serverID); err == servers.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("server not found"))
		return
	} else if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	var websiteID *uuid.UUID
	if req.WebsiteID != "" {
		wid, err := uuid.Parse(req.WebsiteID)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("website_id must be a UUID"))
			return
		}
		ok, err := h.lookupWebsite(r.Context(), orgID, wid)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if !ok {
			httpapi.RespondError(w, httpapi.ErrNotFound("website not found"))
			return
		}
		websiteID = &wid
	}

	dbName, err := DerivedName(orgID, strings.ToLower(req.Name))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	// db user: shorter form to respect the MySQL 32-char user limit.
	dbUser, err := DerivedUser(orgID, strings.ToLower(req.Name))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}

	user, _ := httpapi.UserFrom(r.Context())
	createdBy, err := uuid.Parse(user.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	db, err := h.Databases.Create(r.Context(), orgID, serverID, &createdBy, websiteID, Engine(req.Engine), dbName, dbUser)
	if err == ErrDuplicate {
		httpapi.RespondError(w, httpapi.ErrConflict("a database with that name already exists on this server"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	payload := CreatePayload{DatabaseID: db.ID, Engine: string(db.Engine), Name: db.Name, DbUser: db.DBUser}
	// Set status before responding so the client never observes 'pending'.
	if err := h.Databases.SetStatus(r.Context(), db.ID, StatusCreating, ""); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if _, err := h.Jobs.Enqueue(r.Context(), serverID, nil, jobs.TypeCreateDatabase, payload); err != nil {
		_ = h.Databases.SetStatus(r.Context(), db.ID, StatusFailed, "enqueue failed: "+err.Error())
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	h.audit(r, &orgID, "database.created", "database", db.ID.String(), map[string]any{"engine": req.Engine, "name": dbName})
	db.Status = StatusCreating
	httpapi.WriteJSON(w, http.StatusAccepted, db)
}

func (h *Handler) lookupWebsite(ctx context.Context, orgID, websiteID uuid.UUID) (bool, error) {
	// Audit S4: Exec on a SELECT reported success for ANY website id (row
	// count ignored) — cross-org attach. Use QueryRow and treat ErrNoRows.
	var one int
	err := h.Databases.Pool.QueryRow(ctx, `SELECT 1 FROM websites WHERE id = $1 AND organization_id = $2`, websiteID, orgID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	list, err := h.Databases.ListForOrg(r.Context(), orgID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if list == nil {
		list = []Database{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"databases": list})
}

func (h *Handler) dbFromPath(r *http.Request, orgID uuid.UUID) (*Database, *httpapi.APIError) {
	dbID, err := uuid.Parse(r.PathValue("db_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid database id")
	}
	db, err := h.Databases.GetByID(r.Context(), orgID, dbID)
	if err == ErrNotFound {
		return nil, httpapi.ErrNotFound("database not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return db, nil
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	db, apiErr := h.dbFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, db)
}

func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	db, apiErr := h.dbFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if db.Status == StatusDeleting {
		httpapi.RespondError(w, httpapi.ErrConflict("database deletion already in progress"))
		return
	}

	payload := CreatePayload{DatabaseID: db.ID, Engine: string(db.Engine), Name: db.Name, DbUser: db.DBUser}
	if _, err := h.Jobs.Enqueue(r.Context(), db.ServerID, nil, jobs.TypeDeleteDatabase, payload); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Databases.SetStatus(r.Context(), db.ID, StatusDeleting, ""); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "database.delete_requested", "database", db.ID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// Reveal decrypts and returns the database credential. Explicitly audited.
func (h *Handler) Reveal(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	db, apiErr := h.dbFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	password, err := h.Databases.RevealPassword(r.Context(), db.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
		return
	}
	h.audit(r, &orgID, "database.credentials_revealed", "database", db.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"engine":   db.Engine,
		"database": db.Name,
		"user":     db.DBUser,
		"password": password,
	})
}

// --- job payload (shared shape with the agent) ---

type CreatePayload struct {
	DatabaseID uuid.UUID `json:"database_id"`
	Engine     string    `json:"engine"`
	Name       string    `json:"name"`
	DbUser     string    `json:"db_user"`
}

// DerivedUser builds the MySQL-safe user name (<= 32 chars total).
func DerivedUser(orgID uuid.UUID, label string) (string, error) {
	name, err := DerivedName(orgID, label)
	if err != nil {
		return "", err
	}
	// ep_<org8>_<label> can exceed 32 chars for MySQL users; truncate keeping uniqueness.
	if len(name) > 32 {
		return name[:32], nil
	}
	return name, nil
}

// ApplyJobOutcome advances the database registry for finished db jobs:
// on create success it encrypts the generated password (and scrubs the
// plaintext from the job result); terminal failures mark the row failed.
func (h *Handler) ApplyJobOutcome(ctx context.Context, job *jobs.Job, result json.RawMessage) {
	switch job.Type {
	case jobs.TypeCreateDatabase, jobs.TypeDeleteDatabase:
	case jobs.TypeDBTools:
		// Persist the dbadmin SSO key reported by the agent so the panel can
		// mint one-time phpMyAdmin tokens.
		if job.Status == jobs.StatusSuccess && h.SettingsStore != nil {
			var out struct {
				SSOKey string `json:"sso_key"`
			}
			if err := json.Unmarshal(result, &out); err == nil && len(out.SSOKey) == 64 {
				_ = h.SettingsStore.Set(ctx, "dbadmin_sso_key", out.SSOKey)
			}
		}
		return
	default:
		return
	}
	var p CreatePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.DatabaseID == uuid.Nil {
		slog.Error("database job payload missing database_id", "job", job.ID)
		return
	}

	if job.Type == jobs.TypeDeleteDatabase {
		if job.Status == jobs.StatusSuccess {
			if err := h.Databases.DeleteByServer(ctx, job.ServerID, p.DatabaseID); err != nil && err != ErrNotFound {
				slog.Error("database delete row failed", "database", p.DatabaseID, "err", err)
			}
		} else if job.Status == jobs.StatusFailed {
			if err := h.Databases.SetStatus(ctx, p.DatabaseID, StatusFailed, job.Error); err != nil && err != ErrNotFound {
				slog.Error("database mark failed", "database", p.DatabaseID, "err", err)
			}
		}
		return
	}

	if job.Status == jobs.StatusSuccess {
		var outcome struct {
			Password string `json:"password"`
		}
		if err := json.Unmarshal(result, &outcome); err != nil || outcome.Password == "" {
			slog.Error("database create result missing password", "job", job.ID)
			_ = h.Databases.SetStatus(ctx, p.DatabaseID, StatusFailed, "agent returned no credential")
			return
		}
		encrypted, err := secretbox.Encrypt(outcome.Password)
		if err != nil {
			slog.Error("credential encryption failed", "err", err)
			_ = h.Databases.SetStatus(ctx, p.DatabaseID, StatusFailed, "credential encryption failed")
			return
		}
		fingerprint := secretbox.Fingerprint(outcome.Password)
		if err := h.Databases.MarkReady(ctx, p.DatabaseID, encrypted, fingerprint); err != nil && err != ErrNotFound {
			slog.Error("database mark ready failed", "database", p.DatabaseID, "err", err)
		}
		if err := h.Jobs.ScrubResult(ctx, job.ID); err != nil {
			slog.Error("job result scrub failed", "job", job.ID, "err", err)
		}
		// WordPress one-click chain: when this database was created for a WP
		// install, enqueue the install job now — the credential exists and is
		// delivered to the agent encrypted (never in plaintext at rest).
		if h.OnDatabaseReady != nil {
			h.OnDatabaseReady(ctx, p.DatabaseID, outcome.Password)
		}
		return
	}
	if job.Status == jobs.StatusFailed {
		if err := h.Databases.SetStatus(ctx, p.DatabaseID, StatusFailed, job.Error); err != nil && err != ErrNotFound {
			slog.Error("database mark failed", "database", p.DatabaseID, "err", err)
		}
	}
}
