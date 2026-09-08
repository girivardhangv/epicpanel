package websites

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

// DBProvisioner creates the WordPress database (implemented by api layer
// over the databases store, avoiding a package cycle).
type DBProvisioner interface {
	CreateWPSiteDB(ctx context.Context, orgID, serverID, websiteID uuid.UUID, label string) (DBInfo, error)
}

type DBInfo struct {
	ID     uuid.UUID
	Name   string
	User   string
	Status string
}

type WPRequest struct {
	AdminUser  string `json:"admin_user"`
	AdminEmail string `json:"admin_email"`
	Title      string `json:"title"`
}

// InstallWordPress enqueues the one-click WordPress flow for a site:
// creates a fresh MariaDB database (pipeline-managed creds) + install job.
func (h *Handler) InstallWordPress(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if ws.Status != StatusReady {
		httpapi.RespondError(w, httpapi.ErrConflict("website must be ready before installing WordPress"))
		return
	}
	if ws.Runtime != RuntimePHP {
		httpapi.RespondError(w, httpapi.ErrValidation("WordPress requires a PHP site"))
		return
	}

	var req WPRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.AdminUser = strings.TrimSpace(req.AdminUser)
	req.Title = strings.TrimSpace(req.Title)
	if len(req.AdminUser) < 3 || strings.ContainsAny(req.AdminUser, " '\";") {
		httpapi.RespondError(w, httpapi.ErrValidation("admin_user must be 3+ chars (letters/digits, no quotes)"))
		return
	}
	if !strings.Contains(req.AdminEmail, "@") {
		httpapi.RespondError(w, httpapi.ErrValidation("admin_email is required"))
		return
	}

	// 1. Fresh MariaDB database for this site (pipeline handles creds).
	// The install job is NOT enqueued here: it chains off the database-ready
	// event (api wiring) so wp-cli receives the real generated password.
	label := "wp_" + ws.Name
	if len(label) > 30 {
		label = label[:30]
	}
	db, err := h.DBs.CreateWPSiteDB(r.Context(), orgID, ws.ServerID, ws.ID, label)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
		return
	}
	dbName, dbUser := db.Name, db.User

	// 2. WordPress install job (queued by the database-ready chain once the
	// agent reports the credential; the agent receives it encrypted).
	payload := map[string]any{
		"website_id":    ws.ID.String(),
		"unix_user":     ws.UnixUser,
		"document_root": ws.DocumentRoot,
		"site_url":      "http://" + ws.PrimaryDomain,
		"title":         req.Title,
		"admin_user":    req.AdminUser,
		"admin_email":   req.AdminEmail,
		"db_name":       dbName,
		"db_user":       dbUser,
		"db_host":       "localhost",
	}
	_ = payload // install job is assembled by the DB-ready chain (api wiring)
	if h.WPPending != nil {
		if err := h.WPPending.Create(r.Context(), WPPending{
			DBID: db.ID, WebsiteID: ws.ID, Title: req.Title,
			AdminUser: req.AdminUser, AdminEmail: req.AdminEmail,
		}); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}
	if h.Audit != nil {
		usr, _ := httpapi.UserFrom(r.Context())
		var actorID *uuid.UUID
		if usr != nil {
			if uid, err := uuid.Parse(usr.ID); err == nil {
				actorID = &uid
			}
		}
		h.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &orgID,
			ActorUserID:    actorID,
			ActorType:      audit.ActorUser,
			Action:         "wordpress.install_requested",
			ResourceType:   "website",
			ResourceID:     ws.ID.String(),
			Metadata:       map[string]any{"db": dbName},
		})
	}

	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{
		"database": db,
		"note":     "Installation starts automatically once the database is ready; the WordPress admin password is returned in the job result",
	})
}
