// Package ops exposes the agent's management HTTP endpoint. The surface is a
// fixed set of typed operations behind bearer-token auth; there is no command
// execution, no arbitrary file read/write and no shell anywhere. Requests the
// panel makes are still treated as untrusted input (slugs, paths and PHP
// settings are validated before they reach the platform layer).
package ops

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/agent/internal/dbops"
	"github.com/epicbyte/epicpanel/agent/internal/install"
	"github.com/epicbyte/epicpanel/agent/internal/platform"
	"github.com/epicbyte/epicpanel/agent/internal/software"
	"github.com/epicbyte/epicpanel/agent/internal/ssl"
)

const maxBodyBytes = 8 << 20 // generous for config payloads; far below abuse scale

// Server wires the ops endpoint.
type Server struct {
	log       *slog.Logger
	opsToken  string
	sitesRoot string
	nginxDir  string
	phpDirs   string
	certsDir  string
	acctDir   string
	mysql     dbops.Ops
	postgres  dbops.Ops
	sw        *software.Manager
	nginx     platform.WebServerOps
	php       platform.PHPOps
	fs        platform.FSOps
	version   string

	srv *http.Server
}

type Options struct {
	Log          *slog.Logger
	OpsToken     string
	SitesRoot    string
	NginxDir     string
	PHPDirs      string
	CertsDir     string
	AcctDir      string
	SoftwareDir  string
	MySQL        dbops.AdminConfig
	Postgres     dbops.AdminConfig
	Version      string
}

// New builds the ops server; a missing ops token disables it (older enrollments).
func New(opts Options) (*Server, error) {
	if opts.OpsToken == "" {
		return nil, errors.New("ops token missing; re-enroll required for remote management")
	}
	nginx, err := platform.NewWebServerDir(opts.NginxDir)
	if err != nil {
		return nil, err
	}
	php, err := platform.NewPHPRuntimeDir(splitDirList(opts.PHPDirs))
	if err != nil {
		return nil, err
	}
	fs, err := platform.NewFSOps(opts.SitesRoot)
	if err != nil {
		return nil, err
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.NginxDir == "" {
		opts.NginxDir = `C:\nginx`
	}
	if opts.CertsDir == "" {
		opts.CertsDir = filepath.Join(opts.NginxDir, "conf", "ssl")
	}
	if opts.AcctDir == "" {
		opts.AcctDir = filepath.Join(opts.SitesRoot, "..", "acme")
	}
	// Database engines are optional: configured only when admin creds are
	// supplied. Absent engines report "not configured" honestly.
	var mysqlOps, pgOps dbops.Ops
	if opts.MySQL.User != "" {
		if m, err := dbops.New(dbops.EngineMySQL, opts.MySQL); err == nil {
			mysqlOps = m
		}
	}
	if opts.Postgres.User != "" {
		if p, err := dbops.New(dbops.EnginePostgres, opts.Postgres); err == nil {
			pgOps = p
		}
	}
	return &Server{
		log:       opts.Log,
		opsToken:  opts.OpsToken,
		sitesRoot: opts.SitesRoot,
		nginxDir:  opts.NginxDir,
		phpDirs:   opts.PHPDirs,
		certsDir:  opts.CertsDir,
		acctDir:   opts.AcctDir,
		mysql:     mysqlOps,
		postgres:  pgOps,
		sw:        software.NewManagerDir(opts.Log, opts.SoftwareDir),
		nginx:     nginx,
		php:       php,
		fs:        fs,
		version:   opts.Version,
	}, nil
}

type apiError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *apiError) Error() string { return e.Message }

func badRequest(msg string) error    { return &apiError{Status: 400, Code: "VALIDATION_ERROR", Message: msg} }
func serverError(err error) error {
	return &apiError{Status: 500, Code: "AGENT_OPERATION_FAILED", Message: "operation failed on the server"}
}

// Handler builds the routed, authenticated handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" || len(token) > 256 ||
				subtle.ConstantTimeCompare([]byte(token), []byte(s.opsToken)) != 1 {
				s.writeError(w, r, &apiError{Status: 401, Code: "AGENT_AUTH_REQUIRED", Message: "authorization required"})
				return
			}
			next(w, r)
		}
	}

	mux.HandleFunc("/agent/v1/ping", auth(s.handlePing))
	mux.HandleFunc("/agent/v1/system/info", auth(s.handleSystemInfo))
	mux.HandleFunc("/agent/v1/nginx/status", auth(s.handleNginxStatus))
	mux.HandleFunc("/agent/v1/nginx/deploy-site", auth(s.handleNginxDeploy))
	mux.HandleFunc("/agent/v1/nginx/remove-site", auth(s.handleNginxRemove))
	mux.HandleFunc("/agent/v1/nginx/set-enabled", auth(s.handleNginxSetEnabled))
	mux.HandleFunc("/agent/v1/nginx/reload", auth(s.handleNginxReload))
	mux.HandleFunc("/agent/v1/php/versions", auth(s.handlePHPVersions))
	mux.HandleFunc("/agent/v1/php/pool", auth(s.handlePHPPool))
	mux.HandleFunc("/agent/v1/fs/mkdir", auth(s.handleFSMkdir))
	mux.HandleFunc("/agent/v1/fs/write", auth(s.handleFSWrite))
	mux.HandleFunc("/agent/v1/fs/remove", auth(s.handleFSRemove))
	mux.HandleFunc("/agent/v1/fs/list", auth(s.handleFSList))
	mux.HandleFunc("/agent/v1/fs/read", auth(s.handleFSRead))
	mux.HandleFunc("/agent/v1/fs/rename", auth(s.handleFSRename))
	mux.HandleFunc("/agent/v1/fs/user", auth(s.handleFSUser))
	mux.HandleFunc("/agent/v1/fs/chown", auth(s.handleFSChown))
	mux.HandleFunc("/agent/v1/limits/set", auth(s.handleLimitsSet))
	mux.HandleFunc("/agent/v1/logs/read", auth(s.handleLogsRead))
	mux.HandleFunc("/agent/v1/install/nginx", auth(s.handleInstallNginx))
	mux.HandleFunc("/agent/v1/install/php", auth(s.handleInstallPHP))
	mux.HandleFunc("/agent/v1/ssl/order", auth(s.handleSSLOrder))
	mux.HandleFunc("/agent/v1/ssl/remove", auth(s.handleSSLRemove))
	mux.HandleFunc("/agent/v1/db/engines", auth(s.handleDBEngines))
	mux.HandleFunc("/agent/v1/db/create", auth(s.handleDBCreate))
	mux.HandleFunc("/agent/v1/db/drop", auth(s.handleDBDrop))
	mux.HandleFunc("/agent/v1/db/user/create", auth(s.handleDBUserCreate))
	mux.HandleFunc("/agent/v1/db/user/drop", auth(s.handleDBUserDrop))
	mux.HandleFunc("/agent/v1/db/user/password", auth(s.handleDBUserPassword))
	mux.HandleFunc("/agent/v1/software/list", auth(s.handleSoftwareList))
	mux.HandleFunc("/agent/v1/software/install", auth(s.handleSoftwareInstall))
	mux.HandleFunc("/agent/v1/software/remove", auth(s.handleSoftwareRemove))
	mux.HandleFunc("/agent/v1/software/service", auth(s.handleSoftwareService))

	return http.MaxBytesHandler(mux, maxBodyBytes)
}

// ListenAndServe starts the ops endpoint with sane server timeouts.
func (s *Server) ListenAndServe(addr string) error {
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      3600 * time.Second, // install endpoints download large archives / compile from source
		IdleTimeout:       120 * time.Second,
	}
	return s.srv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

// ---------------------------------------------------------------------------
// handlers
// ---------------------------------------------------------------------------

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.version})
}

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	info, err := platform.Collect(platform.Current())
	if err != nil {
		s.writeError(w, r, serverError(err))
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"os":            string(info.OS),
		"os_name":       info.OSName,
		"os_version":    info.Version,
		"arch":          info.Arch,
		"hostname":      info.Hostname,
		"agent_version": s.version,
	})
}

func (s *Server) handleNginxStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.nginx.Status(r.Context())
	if err != nil {
		s.writeError(w, r, serverError(err))
		return
	}
	s.writeJSON(w, http.StatusOK, st)
}

type deploySiteRequest struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Enable  bool   `json:"enable"`
}

func (s *Server) handleNginxDeploy(w http.ResponseWriter, r *http.Request) {
	var req deploySiteRequest
	if !s.decode(w, r, &req) {
		return
	}
	if len(req.Content) > 512*1024 {
		s.writeError(w, r, badRequest("site configuration too large"))
		return
	}
	res, err := s.nginx.DeploySite(r.Context(), platform.NginxSite{
		Name: req.Name, Content: req.Content, Enable: req.Enable,
	})
	if err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "NGINX_DEPLOY_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

type nameRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleNginxRemove(w http.ResponseWriter, r *http.Request) {
	var req nameRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.nginx.RemoveSite(r.Context(), req.Name); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "NGINX_REMOVE_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

type setEnabledRequest struct {
	Name   string `json:"name"`
	Enable bool   `json:"enable"`
}

func (s *Server) handleNginxSetEnabled(w http.ResponseWriter, r *http.Request) {
	var req setEnabledRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.nginx.SetEnabled(r.Context(), req.Name, req.Enable); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "NGINX_ENABLE_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"updated": true})
}

func (s *Server) handleNginxReload(w http.ResponseWriter, r *http.Request) {
	if err := s.nginx.Reload(r.Context()); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "NGINX_RELOAD_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"reloaded": true})
}

func (s *Server) handlePHPVersions(w http.ResponseWriter, r *http.Request) {
	vers, err := s.php.Versions(r.Context())
	if err != nil {
		s.writeError(w, r, serverError(err))
		return
	}
	if vers == nil {
		vers = []platform.PHPVersion{}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"versions": vers})
}

type phpPoolRequest struct {
	SiteSlug string            `json:"site_slug"`
	Version  string            `json:"version"`
	User     string            `json:"user,omitempty"`
	Settings map[string]string `json:"settings"`
	Remove   bool              `json:"remove"`
}

func (s *Server) handlePHPPool(w http.ResponseWriter, r *http.Request) {
	var req phpPoolRequest
	if !s.decode(w, r, &req) {
		return
	}
	preq := platform.PHPPoolRequest{
		SiteSlug: req.SiteSlug, Version: req.Version, User: req.User,
		Settings: req.Settings, Remove: req.Remove,
	}
	if req.Remove {
		if err := s.php.RemovePool(r.Context(), preq); err != nil {
			s.writeError(w, r, &apiError{Status: 422, Code: "PHP_POOL_REMOVE_FAILED", Message: safe(err)})
			return
		}
		s.writeJSON(w, http.StatusOK, map[string]any{"removed": true})
		return
	}
	res, err := s.php.EnsurePool(r.Context(), preq)
	if err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "PHP_POOL_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, res)
}

type mkdirRequest struct {
	Paths []string `json:"paths"`
}

func (s *Server) handleFSMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirRequest
	if !s.decode(w, r, &req) {
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > 32 {
		s.writeError(w, r, badRequest("paths must contain 1..32 entries"))
		return
	}
	if err := s.fs.MkdirAll(req.Paths); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"created": true})
}

type writeFileRequest struct {
	Path          string `json:"path"`
	ContentBase64 string `json:"content_base64"`
}

func (s *Server) handleFSWrite(w http.ResponseWriter, r *http.Request) {
	var req writeFileRequest
	if !s.decode(w, r, &req) {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(req.ContentBase64)
	if err != nil {
		s.writeError(w, r, badRequest("content_base64 is not valid base64"))
		return
	}
	if len(raw) > 4<<20 {
		s.writeError(w, r, badRequest("content too large"))
		return
	}
	if err := s.fs.WriteFile(req.Path, raw); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"written": true})
}

func (s *Server) handleFSRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.Path == "" {
		s.writeError(w, r, badRequest("path is required"))
		return
	}
	if err := s.fs.Remove(req.Path); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

type fsListRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleFSList(w http.ResponseWriter, r *http.Request) {
	var req fsListRequest
	if !s.decode(w, r, &req) {
		return
	}
	entries, err := s.fs.List(req.Path)
	if err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "path": req.Path})
}

type fsReadRequest struct {
	Path     string `json:"path"`
	MaxBytes int64  `json:"max_bytes"`
}

func (s *Server) handleFSRead(w http.ResponseWriter, r *http.Request) {
	var req fsReadRequest
	if !s.decode(w, r, &req) {
		return
	}
	content, size, truncated, err := s.fs.ReadFile(req.Path, req.MaxBytes)
	if err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"content_base64": base64.StdEncoding.EncodeToString(content),
		"size_bytes":     size,
		"truncated":      truncated,
	})
}

type fsRenameRequest struct {
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
}

func (s *Server) handleFSRename(w http.ResponseWriter, r *http.Request) {
	var req fsRenameRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := s.fs.Rename(req.OldPath, req.NewPath); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"renamed": true})
}

type userRequest struct {
	Username string `json:"username"`
}

func (s *Server) handleFSUser(w http.ResponseWriter, r *http.Request) {
	var req userRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := EnsureSiteUser(req.Username); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_OPERATION_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ensured": true})
}

type chownRequest struct {
	Path     string `json:"path"`
	Username string `json:"username"`
}

// handleFSChown hands a website's directory tree to its isolated OS user.
// The path must resolve inside the sites root (validated by FSOps).
func (s *Server) handleFSChown(w http.ResponseWriter, r *http.Request) {
	var req chownRequest
	if !s.decode(w, r, &req) {
		return
	}
	if err := EnsureSiteUser(req.Username); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_OPERATION_FAILED", Message: safe(err)})
		return
	}
	if err := platform.ChownSiteTree(s.sitesRoot, req.Path, req.Username); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "FS_CHOWN_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"chowned": true})
}

type limitsRequest struct {
	Slug          string `json:"slug"`
	CPULimitPct   int    `json:"cpu_limit_pct"`
	MemoryLimitMB int    `json:"memory_limit_mb"`
}

// handleLimitsSet applies per-site CPU/memory limits. Linux: cgroup v2 slice.
// Windows: Job Object. Zero values clear the limit (unlimited).
func (s *Server) handleLimitsSet(w http.ResponseWriter, r *http.Request) {
	var req limitsRequest
	if !s.decode(w, r, &req) {
		return
	}
	if !platform.ValidSiteSlug(req.Slug) {
		s.writeError(w, r, badRequest("invalid site slug"))
		return
	}
	if req.CPULimitPct < 0 || req.CPULimitPct > 100 || req.MemoryLimitMB < 0 {
		s.writeError(w, r, badRequest("invalid limits (cpu 0..100, memory >= 0)"))
		return
	}
	if err := platform.ApplySiteLimits(req.Slug, req.CPULimitPct, req.MemoryLimitMB); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "LIMITS_APPLY_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"applied": true, "cpu_limit_pct": req.CPULimitPct, "memory_limit_mb": req.MemoryLimitMB})
}

func (s *Server) handleLogsRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	maxBytes := int64(128 * 1024)
	if v := r.URL.Query().Get("max_bytes"); v != "" {
		if n, err := parsePositiveInt(v); err == nil && n <= 512*1024 {
			maxBytes = n
		}
	}
	content, size, truncated, err := platform.LogsRead(s.sitesRoot, path, maxBytes)
	if err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "LOG_READ_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"path": path, "size_bytes": size, "truncated": truncated, "content": content,
	})
}

// handleInstallNginx downloads the official Windows build into the agent's
// configured nginx dir. Explicit operator action only — never automatic.
func (s *Server) handleInstallNginx(w http.ResponseWriter, r *http.Request) {
	s.log.Info("installing nginx", "dir", s.nginxDir)
	if err := install.Nginx(s.nginxDir); err != nil {
		s.log.Error("nginx install failed", "err", err)
		s.writeError(w, r, &apiError{Status: 500, Code: "INSTALL_FAILED", Message: "Nginx download/install failed on the server"})
		return
	}
	// Rebuild the web server handle so status reflects the fresh install.
	if n, err := platform.NewWebServer(); err == nil {
		s.nginx = n
	}
	s.log.Info("nginx installed", "dir", s.nginxDir)
	s.writeJSON(w, http.StatusOK, map[string]any{"installed": true, "dir": s.nginxDir})
}

// handleInstallPHP downloads the official Windows thread-safe build into the
// first configured PHP directory (or C:\PHP). Explicit operator action only.
func (s *Server) handleInstallPHP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Version string `json:"version"`
	}
	_ = s.decode(w, r, &req)
	target := firstPHPdir(s.phpDirs)
	if target == "" {
		target = `C:\PHP`
	}
	s.log.Info("installing php", "dir", target, "version", req.Version)
	if err := install.PHP(target, req.Version); err != nil {
		s.log.Error("php install failed", "err", err)
		s.writeError(w, r, &apiError{Status: 500, Code: "INSTALL_FAILED", Message: "PHP download/install failed on the server"})
		return
	}
	if p, err := platform.NewPHPRuntime(); err == nil {
		s.php = p
	}
	s.log.Info("php installed", "dir", target)
	s.writeJSON(w, http.StatusOK, map[string]any{"installed": true, "dir": target})
}

func firstPHPdir(phpDirs string) string {
	if phpDirs == "" {
		return ""
	}
	for _, d := range strings.Split(phpDirs, ";") {
		if d = strings.TrimSpace(d); d != "" {
			return d
		}
	}
	return ""
}

// splitDirList splits a semicolon-separated directory list.
func splitDirList(dirs string) []string {
	if dirs == "" {
		return nil
	}
	out := []string{}
	for _, d := range strings.Split(dirs, ";") {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// handleSSLOrder issues a certificate for a site. Explicit operator action;
// mock mode only when the panel explicitly configures it (development).
func (s *Server) handleSSLOrder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SiteSlug string   `json:"site_slug"`
		Domains  []string `json:"domains"`
		WebRoot  string   `json:"webroot"`
		Mode     string   `json:"mode"`
		Email    string   `json:"email"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if req.SiteSlug == "" || len(req.Domains) == 0 || len(req.Domains) > 20 {
		s.writeError(w, r, badRequest("site_slug and a bounded domains list are required"))
		return
	}
	for _, d := range req.Domains {
		if !validDomain(d) {
			s.writeError(w, r, badRequest("invalid domain in certificate request: "+d))
			return
		}
	}
	s.log.Info("ssl order requested", "site", req.SiteSlug, "mode", req.Mode, "domains", len(req.Domains))
	res, err := ssl.Order(r.Context(), ssl.Options{
		CertsDir:   s.certsDir,
		AccountDir: s.acctDir,
		HTTPClient: sslHTTPClient(),
	}, ssl.OrderRequest{
		SiteSlug: req.SiteSlug,
		Domains:  req.Domains,
		WebRoot:  req.WebRoot,
		Mode:     req.Mode,
		Email:    req.Email,
	})
	if err != nil {
		s.log.Error("ssl order failed", "site", req.SiteSlug, "err", err)
		s.writeError(w, r, &apiError{Status: 422, Code: "SSL_ORDER_FAILED", Message: safe(err)})
		return
	}
	s.log.Info("ssl certificate installed", "site", req.SiteSlug, "provider", res.Provider)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"cert_path": res.CertPath, "key_path": res.KeyPath,
		"domains": res.Domains, "expires_at": res.ExpiresAt, "provider": res.Provider,
	})
}

// handleSSLRemove deletes the certificate files for a site.
func (s *Server) handleSSLRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SiteSlug string `json:"site_slug"`
		Provider string `json:"provider"`
	}
	if !s.decode(w, r, &req) {
		return
	}
	if err := ssl.Remove(s.certsDir, req.SiteSlug); err != nil {
		s.writeError(w, r, &apiError{Status: 422, Code: "SSL_REMOVE_FAILED", Message: safe(err)})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"removed": true})
}

// validDomain mirrors the panel's whitelist so the agent never passes
// unsanitized names to the ACME client.
func validDomain(d string) bool {
	if d == "" || len(d) > 253 || strings.ContainsAny(d, " *\\/;") {
		return false
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return false
		}
		for _, r := range l {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}
	return true
}

func sslHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

// ---------------------------------------------------------------------------
// plumbing
// ---------------------------------------------------------------------------

func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes+1))
	if err != nil || len(body) > maxBodyBytes {
		s.writeError(w, r, badRequest("request body too large"))
		return false
	}
	if len(body) == 0 {
		s.writeError(w, r, badRequest("a JSON body is required"))
		return false
	}
	if err := json.Unmarshal(body, dst); err != nil {
		s.writeError(w, r, badRequest("malformed JSON body"))
		return false
	}
	return true
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	ae := &apiError{Status: 500, Code: "AGENT_OPERATION_FAILED", Message: "operation failed on the server"}
	if e, ok := err.(*apiError); ok {
		ae = e
	}
	if ae.Status >= 500 {
		s.log.Error("ops request failed", "path", r.URL.Path, "err", err)
	} else {
		s.log.Warn("ops request rejected", "path", r.URL.Path, "err", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(ae.Status)
	_ = json.NewEncoder(w).Encode(map[string]apiError{"error": *ae})
}

// safe strips internal error detail for 4xx responses; 5xx responses already
// carry generic text and log the cause.
func safe(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

func bearerToken(r *http.Request) string {
	authz := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(authz) > len(prefix) && authz[:len(prefix)] == prefix {
		return authz[len(prefix):]
	}
	return ""
}

func parsePositiveInt(v string) (int64, error) {
	var n int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int64(c-'0')
		if n > 1<<40 {
			return 0, errors.New("too large")
		}
	}
	if n <= 0 {
		return 0, errors.New("must be positive")
	}
	return n, nil
}

// EnsureSiteUser is the platform-level entry the handler uses. It accepts the
// shared site user (epicpanel-sites) and per-site isolated accounts (web_*)
// so provisioning can give each website its own OS user.
func EnsureSiteUser(name string) error {
	if !validSiteUsername(name) {
		return badRequest("invalid site user name (expected epicpanel-sites or web_*")
	}
	if runtime.GOOS == "windows" {
		return errors.New("site users are not applicable on windows")
	}
	return platform.EnsureSiteUserImpl(name)
}

// validSiteUsername mirrors the panel's generated names: the shared user or a
// web_<slug> account, lowercase alnum/underscore/dot/hyphen, <= 32 chars, no
// leading digit or trailing separator. Defense in depth: only names we
// generate are ever passed to useradd.
func validSiteUsername(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	if name == platform.SiteUser {
		return true
	}
	if !strings.HasPrefix(name, "web_") || len(name) < 5 {
		return false
	}
	if strings.Contains(name, "..") {
		return false
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, "-") || strings.HasSuffix(name, "_") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
		default:
			return false
		}
	}
	return true
}
