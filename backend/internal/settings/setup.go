package settings

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

// phpMinorRe validates PHP major.minor versions for the global setting.
var phpMinorRe = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// setupTokenTTL is how long the installer-generated bootstrap link stays
// valid. After that the link is dead and a new one must be minted on the
// server (epicpanel-api setup-token).
const setupTokenTTL = time.Hour

type SetupHandler struct {
	Settings *Store
	// Optional collaborators for the pre-auth software step. When nil the
	// software endpoints degrade to catalog-only responses.
	Servers *servers.Store
	Jobs    *jobs.Store
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// MintSetupToken creates a one-time bootstrap token (returned raw; only its
// SHA-256 hash is stored) valid for 1 hour. Used by the installer flow and
// the CLI (`epicpanel-api setup-token`).
func (h *SetupHandler) MintSetupToken(ctx context.Context) (raw string, expiresAt time.Time, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", time.Time{}, err
	}
	raw = hex.EncodeToString(b)
	expiresAt = time.Now().Add(setupTokenTTL)
	_, err = h.Settings.Pool.Exec(ctx, `
		INSERT INTO setup_tokens (token_hash, expires_at) VALUES ($1, $2)
	`, hashToken(raw), expiresAt)
	return raw, expiresAt, err
}

// consumeSetupToken validates a raw token: must exist, be unexpired and
// unused. Marks it used (single use) on success.
func (h *SetupHandler) consumeSetupToken(ctx context.Context, raw string) bool {
	tag, err := h.Settings.Pool.Exec(ctx, `
		UPDATE setup_tokens SET used_at = now()
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
	`, hashToken(raw))
	return err == nil && tag.RowsAffected() == 1
}

// GET /v1/setup/status — public (no auth). Reports whether setup is needed.
// When a candidate token is supplied (?token=), reports its validity without
// consuming it.
func (h *SetupHandler) Status(w http.ResponseWriter, r *http.Request) {
	completed, _ := h.Settings.Get(r.Context(), "setup_completed")
	hostname, _ := h.Settings.Get(r.Context(), "panel_hostname")
	hasUsers := false
	var count int
	if err := h.Settings.Pool.QueryRow(r.Context(), `SELECT count(*) FROM users`).Scan(&count); err == nil {
		hasUsers = count > 0
	}
	resp := map[string]any{
		"setup_completed": completed == "true",
		"hostname":        hostname,
		"has_users":       hasUsers,
	}
	if tok := strings.TrimSpace(r.URL.Query().Get("token")); tok != "" {
		var ok bool
		_ = h.Settings.Pool.QueryRow(r.Context(), `
			SELECT EXISTS (
				SELECT 1 FROM setup_tokens
				WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
			)`, hashToken(tok)).Scan(&ok)
		resp["token_valid"] = ok
	}
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// POST /v1/setup/verify-hostname {"hostname": "..."} — public during setup.
// Confirms the hostname resolves to one of this server's IP addresses, so
// users do not bind a panel hostname pointing somewhere else.
func (h *SetupHandler) VerifyHostname(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Hostname string `json:"hostname"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Hostname = strings.ToLower(strings.TrimSpace(req.Hostname))
	if req.Hostname == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("hostname is required"))
		return
	}
	if ip := net.ParseIP(req.Hostname); ip != nil {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"matched": true, "resolved": []string{ip.String()}, "note": "raw IP — always matches"})
		return
	}
	if apiErr := validateHostname(req.Hostname); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	ips, err := net.LookupHost(req.Hostname)
	if err != nil || len(ips) == 0 {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"matched": false, "resolved": []string{}, "error": "hostname does not resolve"})
		return
	}
	local, err := localAddresses()
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	match := false
	for _, rip := range ips {
		for _, lip := range local {
			if subtle.ConstantTimeCompare([]byte(rip), []byte(lip)) == 1 {
				match = true
			}
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"matched": match, "resolved": ips})
}

func localAddresses() ([]string, error) {
	var out []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			if ipnet, ok := addr.(*net.IPNet); ok {
				out = append(out, ipnet.IP.String())
			}
		}
	}
	return out, nil
}

// POST /v1/setup — first-boot setup. REQUIRES a valid one-time setup token
// (from the installer link). Creates the platform-admin account and records
// the panel hostname. One-shot: once setup is completed, this is closed.
func (h *SetupHandler) Complete(w http.ResponseWriter, r *http.Request) {
	completed, _ := h.Settings.Get(r.Context(), "setup_completed")
	if completed == "true" {
		httpapi.RespondError(w, httpapi.ErrForbidden("setup has already been completed"))
		return
	}

	var req struct {
		Token    string `json:"token"`
		Hostname string `json:"hostname"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}

	// Token gate: the person completing setup must hold the secret printed
	// by the installer on the server itself.
	if strings.TrimSpace(req.Token) == "" {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("setup token required — open the link printed by the installer"))
		return
	}
	if !h.consumeSetupToken(r.Context(), strings.TrimSpace(req.Token)) {
		httpapi.RespondError(w, httpapi.ErrForbidden("invalid, expired or already-used setup link — generate a new one with `epicpanel-api setup-token`"))
		return
	}

	req.Hostname = strings.ToLower(strings.TrimSpace(req.Hostname))
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	req.Name = strings.TrimSpace(req.Name)

	// Hostname is optional at first boot; it can be changed later from the
	// admin dashboard (PATCH /v1/settings/hostname). Defaults to localhost.
	if req.Hostname == "" {
		req.Hostname = "localhost"
	}
	if apiErr := validateHostname(req.Hostname); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if len(req.Password) < 10 {
		httpapi.RespondError(w, httpapi.ErrValidation("password must be at least 10 characters"))
		return
	}
	if req.Name == "" {
		req.Name = "Admin"
	}

	// Create admin user + flip setup flags ATOMICALLY (audit: the two Set
	// calls were fire-and-forget; a failed write let setup re-run and mint
	// extra platform admins).
	var userID string
	hash, hashErr := hashPasswordLocal(req.Password)
	if hashErr != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(hashErr))
		return
	}
	tx, txErr := h.Settings.Pool.Begin(r.Context())
	if txErr != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(txErr))
		return
	}
	defer tx.Rollback(r.Context())

	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtext('epicpanel:setup-complete'))`); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	completed2, _ := h.Settings.Get(r.Context(), "setup_completed")
	if completed2 == "true" {
		httpapi.RespondError(w, httpapi.ErrForbidden("setup has already been completed"))
		return
	}
	err := tx.QueryRow(r.Context(), `
		INSERT INTO users (email, password_hash, name, is_platform_admin)
		VALUES ($1, $2, $3, TRUE)
		RETURNING id::text
	`, req.Email, hash, req.Name).Scan(&userID)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			httpapi.RespondError(w, httpapi.ErrConflict("email already registered"))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	for _, kv := range [][2]string{{"panel_hostname", req.Hostname}, {"setup_completed", "true"}} {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO system_settings (key, value) VALUES ($1, $2)
			ON CONFLICT (key) DO UPDATE SET value = $2, updated_at = now()
		`, kv[0], kv[1]); err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
	}
	if err := tx.Commit(r.Context()); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"hostname": req.Hostname,
		"email":    req.Email,
		"note":     "setup complete; you can now log in",
	})
}

// GET /v1/settings — authenticated (admin only) — panel settings.
func (h *SetupHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	user, ok := httpapi.UserFrom(r.Context())
	if !ok || user.Role != "admin" {
		httpapi.RespondError(w, httpapi.ErrForbidden("platform administrator access required"))
		return
	}
	hostname, _ := h.Settings.Get(r.Context(), "panel_hostname")
	globalPHP, _ := h.Settings.Get(r.Context(), "global_php_version")
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"hostname":           hostname,
		"global_php_version": globalPHP,
	})
}

// PATCH /v1/settings/global-php — admin-only: which PHP minor the panel's
// own tooling uses (DB Tools pool today). Empty = auto (highest installed).
func (h *SetupHandler) SetGlobalPHP(w http.ResponseWriter, r *http.Request) {
	user, ok := httpapi.UserFrom(r.Context())
	if !ok || user.Role != "admin" {
		httpapi.RespondError(w, httpapi.ErrForbidden("platform administrator access required"))
		return
	}
	var req struct {
		Version string `json:"version"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Version = strings.TrimSpace(req.Version)
	if req.Version != "" && !phpMinorRe.MatchString(req.Version) {
		httpapi.RespondError(w, httpapi.ErrValidation("version must be major.minor (e.g. 8.4), or empty for auto"))
		return
	}
	if err := h.Settings.Set(r.Context(), "global_php_version", req.Version); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	// Auto-migrate: every server that already runs DB Tools gets a fresh
	// install job immediately — the operator changes ONE setting and the
	// tools' pool moves to the new PHP without touching the Software page
	// (operator request: "it gets changed automatically").
	migrated := 0
	if h.Jobs != nil {
		rows, qerr := h.Settings.Pool.Query(r.Context(),
			`SELECT DISTINCT server_id FROM jobs WHERE type = $1 AND status = 'success'`,
			jobs.TypeDBTools)
		if qerr == nil {
			defer rows.Close()
			for rows.Next() {
				var sid uuid.UUID
				if serr := rows.Scan(&sid); serr != nil {
					continue
				}
				if _, jerr := h.Jobs.EnqueueIdempotent(r.Context(), sid, nil, jobs.TypeDBTools,
					map[string]string{"php_version": req.Version}, "install_dbtools_"+sid.String()); jerr != nil {
					slog.Warn("global-php auto-migration enqueue failed", "server", sid, "err", jerr)
					continue
				}
				migrated++
			}
		}
	}

	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"global_php_version": req.Version, "dbtools_migrations_queued": migrated})
}

// PATCH /v1/settings/hostname — admin-only hostname update.
func (h *SetupHandler) SetHostname(w http.ResponseWriter, r *http.Request) {
	user, ok := httpapi.UserFrom(r.Context())
	if !ok || user.Role != "admin" {
		httpapi.RespondError(w, httpapi.ErrForbidden("platform administrator access required"))
		return
	}
	var req struct {
		Hostname string `json:"hostname"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.Hostname = strings.ToLower(strings.TrimSpace(req.Hostname))
	if req.Hostname == "" || len(req.Hostname) > 253 {
		httpapi.RespondError(w, httpapi.ErrValidation("hostname is required"))
		return
	}
	if apiErr := validateHostname(req.Hostname); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if err := h.Settings.Set(r.Context(), "panel_hostname", req.Hostname); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"hostname": req.Hostname})
}

// validateHostname enforces RFC 1123 hostname syntax (letters, digits,
// hyphen; dot-separated labels of at most 63 chars).
func validateHostname(hostname string) *httpapi.APIError {
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 {
			return httpapi.ErrValidation("invalid hostname")
		}
		for _, c := range label {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
				return httpapi.ErrValidation("invalid hostname")
			}
		}
	}
	return nil
}

func hashPasswordLocal(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(b), err
}

// --- pre-auth software step (token-gated, non-consuming) ---

// SetupCatalogItem is one managed software offering shown during setup.
type SetupCatalogItem struct {
	Type        string   `json:"type"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Versions    []string `json:"versions"`
	Recommended bool     `json:"recommended"`
}

// setupCatalog is the curated offering list; the agent maps type+version to
// concrete OS packages (with repo fallbacks), so this stays portable.
var setupCatalog = []SetupCatalogItem{
	{Type: "php", Label: "PHP (FPM + extensions)", Description: "Required for PHP sites, WordPress and phpMyAdmin.", Versions: []string{"8.3", "8.4", "8.5", "8.2"}, Recommended: true},
	{Type: "node", Label: "Node.js", Description: "NodeSource builds for JavaScript apps.", Versions: []string{"22", "20", "24"}},
	{Type: "python", Label: "Python", Description: "System Python with venv support.", Versions: []string{"3.12", "3.11", "3.13"}},
	{Type: "go", Label: "Go", Description: "Official toolchain for Go applications.", Versions: []string{"1.22", "1.23"}},
	{Type: "apache", Label: "Apache HTTP Server", Description: "Alternative web server (per-site selection).", Versions: []string{"2.4"}},
	{Type: "openlitespeed", Label: "OpenLiteSpeed", Description: "High-performance web server with LSPHP.", Versions: []string{"1.8"}},
	{Type: "dbtools", Label: "phpMyAdmin + Adminer", Description: "Database web tools on port 8081 (SSO-ready).", Versions: []string{"latest"}, Recommended: true},
}

func (h *SetupHandler) checkSetupToken(ctx context.Context, raw string) bool {
	var ok bool
	_ = h.Settings.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM setup_tokens
			WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
		)`, hashToken(strings.TrimSpace(raw))).Scan(&ok)
	return ok
}

// GET /v1/setup/software?token= — catalog + install state (from jobs).
func (h *SetupHandler) Software(w http.ResponseWriter, r *http.Request) {
	if !h.checkSetupToken(r.Context(), r.URL.Query().Get("token")) {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("valid setup token required"))
		return
	}
	resp := map[string]any{"catalog": setupCatalog, "jobs": []any{}}
	if h.Jobs == nil || h.Servers == nil {
		httpapi.WriteJSON(w, http.StatusOK, resp)
		return
	}
	srvID, _, err := h.Servers.FirstOnline(r.Context())
	if err != nil {
		resp["server"] = nil
		httpapi.WriteJSON(w, http.StatusOK, resp)
		return
	}
	resp["server"] = srvID
	list, err := h.Jobs.ListRecentForServer(r.Context(), srvID, []string{string(jobs.TypeInstallRuntime), string(jobs.TypeDBTools), string(jobs.TypeDetectSoftware)}, 25)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	resp["jobs"] = list
	httpapi.WriteJSON(w, http.StatusOK, resp)
}

// POST /v1/setup/software {"token": "...", "items": [{"type": "php", "version": "8.3"}]}
func (h *SetupHandler) SoftwareInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
		Items []struct {
			Type    string `json:"type"`
			Version string `json:"version"`
		} `json:"items"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if !h.checkSetupToken(r.Context(), req.Token) {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("valid setup token required"))
		return
	}
	if h.Jobs == nil || h.Servers == nil {
		httpapi.RespondError(w, httpapi.ErrInternal(errors.New("software install not wired on this control plane")))
		return
	}
	srvID, _, err := h.Servers.FirstOnline(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrConflict("no online server — the agent may still be starting; retry in a moment"))
		return
	}
	valid := map[string]bool{"php": true, "node": true, "python": true, "go": true, "apache": true, "openlitespeed": true, "dbtools": true}
	var queued []map[string]any
	for _, item := range req.Items {
		if !valid[item.Type] {
			httpapi.RespondError(w, httpapi.ErrValidation("unsupported software type "+item.Type))
			return
		}
		payload := map[string]string{"type": item.Type, "version": item.Version}
		var jt jobs.Type = jobs.TypeInstallRuntime
		if item.Type == "dbtools" {
			jt = jobs.TypeDBTools
		}
		job, err := h.Jobs.Enqueue(r.Context(), srvID, nil, jt, payload)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		queued = append(queued, map[string]any{"type": item.Type, "version": item.Version, "job_id": job.ID})
	}
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"queued": queued})
}

// GET /v1/setup/jobs?token= — live job progress for the setup UI.
func (h *SetupHandler) SetupJobs(w http.ResponseWriter, r *http.Request) {
	if !h.checkSetupToken(r.Context(), r.URL.Query().Get("token")) {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("valid setup token required"))
		return
	}
	if h.Jobs == nil || h.Servers == nil {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": []any{}})
		return
	}
	srvID, _, err := h.Servers.FirstOnline(r.Context())
	if err != nil {
		httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": []any{}})
		return
	}
	list, err := h.Jobs.ListRecentForServer(r.Context(), srvID, nil, 30)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"jobs": list})
}
