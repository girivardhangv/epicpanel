// Package agentclient is the panel's HTTP client for the agent ops channel.
// The agent exposes a fixed set of typed operations (no shell, no arbitrary
// file paths); every call authenticates with the per-server ops token that is
// established at registration and never returned by any panel API.
package agentclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// Client talks to one agent endpoint per call; base URL and token are passed
// in because the panel resolves them from the servers table.
type Client struct {
	HTTP *http.Client
	// HTTPLong is used for long-running operations (software installs /
	// removals that download and compile large archives). The default HTTP
	// client's 20s timeout would abort a multi-minute MariaDB/PHP install.
	HTTPLong *http.Client
}

func New() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 20 * time.Second},
		HTTPLong: &http.Client{Timeout: 60 * time.Minute},
	}
}

type request struct {
	method string
	path   string
	body   any
	query  urlValues
}

type urlValues []struct{ k, v string }

func (c *Client) do(ctx context.Context, baseURL, opsToken string, r request, out any) error {
	return c.doWith(ctx, baseURL, opsToken, c.HTTP, r, out)
}

// doLong is like do but uses the long-timeout client for operations that can
// take minutes (software installs/removals with downloads + compilation).
func (c *Client) doLong(ctx context.Context, baseURL, opsToken string, r request, out any) error {
	return c.doWith(ctx, baseURL, opsToken, c.HTTPLong, r, out)
}

func (c *Client) doWith(ctx context.Context, baseURL, opsToken string, client *http.Client, r request, out any) error {
	if baseURL == "" {
		return errUnreachable(fmt.Errorf("agent endpoint is not configured (agent must be enrolled with ops support)"))
	}
	if opsToken == "" {
		return errUnreachable(fmt.Errorf("agent ops credentials are missing on the panel; re-enroll the agent"))
	}

	var reader io.Reader
	if r.body != nil {
		raw, err := json.Marshal(r.body)
		if err != nil {
			return fmt.Errorf("encode agent request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}
	url := joinURL(baseURL, r.path)
	if len(r.query) > 0 {
		url += "?"
		for i, kv := range r.query {
			if i > 0 {
				url += "&"
			}
			url += kv.k + "=" + kv.v
		}
	}
	req, err := http.NewRequestWithContext(ctx, r.method, url, reader)
	if err != nil {
		return errUnreachable(err)
	}
	req.Header.Set("Accept", "application/json")
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+opsToken)

	resp, err := client.Do(req)
	if err != nil {
		return errUnreachable(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("agent returned HTTP %d", resp.StatusCode)
		}
		return &AgentError{Status: resp.StatusCode, Code: firstNonEmpty(e.Error.Code, "AGENT_ERROR"), Message: msg}
	}
	if out != nil && len(body) > 0 {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode agent response: %w", err)
		}
	}
	return nil
}

// AgentError is a structured failure reported by the agent.
type AgentError struct {
	Status  int
	Code    string
	Message string
}

func (e *AgentError) Error() string { return e.Message }

// UnreachableError marks connectivity problems distinctly from rejections.
type UnreachableError struct{ err error }

func (e *UnreachableError) Error() string { return e.err.Error() }
func (e *UnreachableError) Unwrap() error { return e.err }

func errUnreachable(err error) error { return &UnreachableError{err: err} }

// IsUnreachable classifies an error as "agent could not be contacted".
func IsUnreachable(err error) bool {
	for err != nil {
		if _, ok := err.(*UnreachableError); ok {
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ---------------------------------------------------------------------------
// Typed operations
// ---------------------------------------------------------------------------

// Ping verifies the ops channel end to end.
func (c *Client) Ping(ctx context.Context, baseURL, token string) error {
	return c.do(ctx, baseURL, token, request{method: http.MethodGet, path: "/agent/v1/ping"}, nil)
}

// SystemInfo mirrors the agent's platform snapshot plus runtime facts.
type SystemInfo struct {
	OS        string `json:"os"`
	OSName    string `json:"os_name"`
	Version   string `json:"os_version"`
	Arch      string `json:"arch"`
	Hostname  string `json:"hostname"`
	Agent     string `json:"agent_version"`
	PanelUser string `json:"panel_user,omitempty"`
}

func (c *Client) SystemInfo(ctx context.Context, baseURL, token string) (*SystemInfo, error) {
	var out SystemInfo
	err := c.do(ctx, baseURL, token, request{method: http.MethodGet, path: "/agent/v1/system/info"}, &out)
	return &out, err
}

// NginxStatus reports detection results. Installed=false is a normal state,
// not an error.
type NginxStatus struct {
	Installed  bool   `json:"installed"`
	Version    string `json:"version"`
	ConfigPath string `json:"config_path"`
	Running    bool   `json:"running"`
	Style      string `json:"style"` // sites-enabled | conf.d | custom (windows)
}

func (c *Client) NginxStatus(ctx context.Context, baseURL, token string) (*NginxStatus, error) {
	var out NginxStatus
	err := c.do(ctx, baseURL, token, request{method: http.MethodGet, path: "/agent/v1/nginx/status"}, &out)
	return &out, err
}

// DeploySiteRequest installs (or replaces) a managed site config atomically:
// the agent writes the candidate, validates the full configuration, and rolls
// back to the previous file when validation fails.
type DeploySiteRequest struct {
	Name    string `json:"name"`    // filesystem-safe site slug
	Content string `json:"content"` // rendered server block
	Enable  bool   `json:"enable"`
}

type DeploySiteResult struct {
	Deployed     bool   `json:"deployed"`
	Validated    bool   `json:"validated"`
	ValidationOut string `json:"validation_output,omitempty"`
	RolledBack   bool   `json:"rolled_back,omitempty"`
}

func (c *Client) NginxDeploySite(ctx context.Context, baseURL, token string, req DeploySiteRequest) (*DeploySiteResult, error) {
	var out DeploySiteResult
	err := c.do(ctx, baseURL, token, request{method: http.MethodPost, path: "/agent/v1/nginx/deploy-site", body: req}, &out)
	return &out, err
}

type RemoveSiteRequest struct {
	Name string `json:"name"`
}

func (c *Client) NginxRemoveSite(ctx context.Context, baseURL, token, name string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/nginx/remove-site",
		body: RemoveSiteRequest{Name: name},
	}, nil)
}

// NginxSetEnabled toggles a previously deployed site in/out of the active
// configuration without rewriting its file.
func (c *Client) NginxSetEnabled(ctx context.Context, baseURL, token, name string, enable bool) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/nginx/set-enabled",
		body:   map[string]any{"name": name, "enable": enable},
	}, nil)
}

func (c *Client) NginxReload(ctx context.Context, baseURL, token string) error {
	return c.do(ctx, baseURL, token, request{method: http.MethodPost, path: "/agent/v1/nginx/reload"}, nil)
}

// ServiceReload restarts/reloads a service from a strict allowlist (nginx,
// php{version}-fpm). The agent rejects anything else.
func (c *Client) ServiceReload(ctx context.Context, baseURL, token, name string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/service/reload",
		body: map[string]string{"name": name},
	}, nil)
}

// PHPVersion describes one discovered runtime.
type PHPVersion struct {
	Version       string `json:"version"`
	BinaryPath    string `json:"binary_path"`
	ConfigPath    string `json:"config_path,omitempty"`
	HandlerType   string `json:"handler_type"` // fpm | fastcgi
	Status        string `json:"status"`       // available | running | stopped
}

func (c *Client) PHPVersions(ctx context.Context, baseURL, token string) ([]PHPVersion, error) {
	var out struct {
		Versions []PHPVersion `json:"versions"`
	}
	err := c.do(ctx, baseURL, token, request{method: http.MethodGet, path: "/agent/v1/php/versions"}, &out)
	return out.Versions, err
}

// PHPPoolRequest configures the per-site PHP runtime. Linux: writes the FPM
// pool and reloads it. Windows: assigns a loopback port and (re)starts
// php-cgi. Remove=true tears the pool down.
type PHPPoolRequest struct {
	SiteSlug string            `json:"site_slug"`
	Version  string            `json:"version"`
	User     string            `json:"user,omitempty"` // per-site OS user the pool runs as (Linux)
	Settings map[string]string `json:"settings,omitempty"`
	Remove   bool              `json:"remove,omitempty"`
}

type PHPPoolResult struct {
	Address string `json:"address"` // unix:/run/php/epicpanel-<slug>.sock | 127.0.0.1:<port>
}

func (c *Client) PHPPool(ctx context.Context, baseURL, token string, req PHPPoolRequest) (*PHPPoolResult, error) {
	var out PHPPoolResult
	err := c.do(ctx, baseURL, token, request{method: http.MethodPost, path: "/agent/v1/php/pool", body: req}, &out)
	return &out, err
}

// FSMkdir creates directories (MkdirAll semantics — idempotent). Paths must
// resolve inside the agent's sites root.
func (c *Client) FSMkdir(ctx context.Context, baseURL, token string, paths []string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/fs/mkdir",
		body: map[string]any{"paths": paths},
	}, nil)
}

// FSWrite atomically writes a file inside the sites root.
func (c *Client) FSWrite(ctx context.Context, baseURL, token, path string, content []byte) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/fs/write",
		body: map[string]string{
			"path":           path,
			"content_base64": base64.StdEncoding.EncodeToString(content),
		},
	}, nil)
}

// FSRemove deletes a file or directory tree inside the sites root. The agent
// refuses to remove the sites root itself or anything outside it.
func (c *Client) FSRemove(ctx context.Context, baseURL, token, path string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/fs/remove",
		body:   map[string]string{"path": path},
	}, nil)
}

// FSEntry is one directory listing item returned by the agent.
type FSEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode,omitempty"`
	ModTime string `json:"mod_time,omitempty"`
}

// FSListResult is the agent's directory listing response.
type FSListResult struct {
	Entries []FSEntry `json:"entries"`
	Path    string    `json:"path"`
}

// FSList lists a directory inside the sites root.
func (c *Client) FSList(ctx context.Context, baseURL, token, path string) (*FSListResult, error) {
	var out FSListResult
	err := c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/fs/list",
		body: map[string]string{"path": path},
	}, &out)
	return &out, err
}

// FSReadResult is the agent's bounded file-read response.
type FSReadResult struct {
	ContentBase64 string `json:"content_base64"`
	SizeBytes     int64  `json:"size_bytes"`
	Truncated     bool   `json:"truncated"`
}

// FSRead reads up to maxBytes of a file inside the sites root.
func (c *Client) FSRead(ctx context.Context, baseURL, token, path string, maxBytes int64) (*FSReadResult, error) {
	if maxBytes <= 0 {
		maxBytes = 8 << 20
	}
	var out FSReadResult
	err := c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/fs/read",
		body: map[string]any{"path": path, "max_bytes": maxBytes},
	}, &out)
	return &out, err
}

// FSRename moves/renames a file or directory inside the sites root.
func (c *Client) FSRename(ctx context.Context, baseURL, token, oldPath, newPath string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/fs/rename",
		body: map[string]string{"old_path": oldPath, "new_path": newPath},
	}, nil)
}

// FSUser ensures the shared site system user exists (Linux only; the agent
// reports unsupported on Windows and callers treat that as success).
func (c *Client) FSUser(ctx context.Context, baseURL, token, username string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/fs/user",
		body:   map[string]string{"username": username},
	}, nil)
}

// FSChown changes ownership of a path tree to a user (Linux only). Used to
// hand a website's directory tree to its isolated account. The path must
// resolve inside the agent's sites root.
func (c *Client) FSChown(ctx context.Context, baseURL, token, path, username string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/fs/chown",
		body:   map[string]string{"path": path, "username": username},
	}, nil)
}

// SetLimits applies per-site CPU/memory limits. Linux: cgroup v2 (systemd
// slice per site). Windows: Job Object. Zero values mean "unlimited".
func (c *Client) SetLimits(ctx context.Context, baseURL, token, slug string, cpuPct, memMB int) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/limits/set",
		body: map[string]any{
			"slug": slug, "cpu_limit_pct": cpuPct, "memory_limit_mb": memMB,
		},
	}, nil)
}

// ResourceLimit represents a per-website CPU/memory ceiling.
type ResourceLimit struct {
	CPULimitPct   int `json:"cpu_limit_pct"`
	MemoryLimitMB int `json:"memory_limit_mb"`
}

// LogsRead returns a bounded tail of a log file inside the sites root.
type LogsResult struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content"`
}

func (c *Client) LogsRead(ctx context.Context, baseURL, token, path string, maxBytes int64) (*LogsResult, error) {
	if maxBytes <= 0 || maxBytes > 512*1024 {
		maxBytes = 512 * 1024
	}
	var out LogsResult
	err := c.do(ctx, baseURL, token, request{
		method: http.MethodGet, path: "/agent/v1/logs/read",
		query: urlValues{{"path", path}, {"max_bytes", fmt.Sprintf("%d", maxBytes)}},
	}, &out)
	return &out, err
}

// InstallNginx triggers an explicit, operator-requested download + install
// of the official Windows Nginx build into the agent's configured directory.
func (c *Client) InstallNginx(ctx context.Context, baseURL, token string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/install/nginx",
	}, nil)
}

// InstallPHP triggers an explicit, operator-requested download + install of
// a PHP Windows build. Empty version installs the agent's pinned default.
func (c *Client) InstallPHP(ctx context.Context, baseURL, token, version string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/install/php",
		body: map[string]string{"version": version},
	}, nil)
}

// ---------------------------------------------------------------------------
// SSL / ACME (Phase 4)
// ---------------------------------------------------------------------------

// SSLOrderRequest asks the agent to obtain a certificate for a website.
type SSLOrderRequest struct {
	SiteSlug string   `json:"site_slug"`
	Domains  []string `json:"domains"` // SAN list (validated upstream)
	WebRoot  string   `json:"webroot"` // directory used for HTTP-01 challenge
	Mode     string   `json:"mode"`    // production | staging | mock
	Email    string   `json:"email,omitempty"`
}

type SSLOrderResult struct {
	CertPath  string   `json:"cert_path"`
	KeyPath   string   `json:"key_path"`
	Domains   []string `json:"domains"`
	ExpiresAt *string  `json:"expires_at"`
	Provider  string   `json:"provider"`
}

// SSLOrder performs the full issuance round-trip on the agent.
func (c *Client) SSLOrder(ctx context.Context, baseURL, token string, req SSLOrderRequest) (*SSLOrderResult, error) {
	var out SSLOrderResult
	err := c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/ssl/order", body: req,
	}, &out)
	return &out, err
}

// SSLRemove tears down the agent-side certificate files for a site.
func (c *Client) SSLRemove(ctx context.Context, baseURL, token, siteSlug, provider string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/ssl/remove",
		body: map[string]string{"site_slug": siteSlug, "provider": provider},
	}, nil)
}

// ---------------------------------------------------------------------------
// Managed databases (Phase 6)
// ---------------------------------------------------------------------------

// DBEngineStatus reports one engine's availability on the agent.
type DBEngineStatus struct {
	Configured bool   `json:"configured"`
	Available  bool   `json:"available"`
	Version    string `json:"version,omitempty"`
	Error      string `json:"error,omitempty"`
}

type DBEnginesResult struct {
	MySQL    DBEngineStatus `json:"mysql"`
	Postgres DBEngineStatus `json:"postgres"`
}

func (c *Client) DBEngines(ctx context.Context, baseURL, token string) (*DBEnginesResult, error) {
	var out DBEnginesResult
	err := c.do(ctx, baseURL, token, request{
		method: http.MethodGet, path: "/agent/v1/db/engines",
	}, &out)
	return &out, err
}

func (c *Client) DBCreate(ctx context.Context, baseURL, token, engine, name string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/db/create",
		body: map[string]string{"engine": engine, "name": name},
	}, nil)
}

func (c *Client) DBDrop(ctx context.Context, baseURL, token, engine, name string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/db/drop",
		body: map[string]string{"engine": engine, "name": name},
	}, nil)
}

type DBUserResult struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c *Client) DBUserCreate(ctx context.Context, baseURL, token, engine, database, username string) (*DBUserResult, error) {
	var out DBUserResult
	err := c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/db/user/create",
		body: map[string]string{"engine": engine, "database": database, "username": username},
	}, &out)
	return &out, err
}

func (c *Client) DBUserDrop(ctx context.Context, baseURL, token, engine, username string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/db/user/drop",
		body: map[string]string{"engine": engine, "username": username},
	}, nil)
}

func (c *Client) DBUserPassword(ctx context.Context, baseURL, token, engine, username string) (*DBUserResult, error) {
	var out DBUserResult
	err := c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/db/user/password",
		body: map[string]string{"engine": engine, "username": username},
	}, &out)
	return &out, err
}

// ---------------------------------------------------------------------------
// Software manager (Phase 7)
// ---------------------------------------------------------------------------

// SoftwareComponent is one detected/managed piece of server software.
type SoftwareComponent struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
	Category    string `json:"category"`
	Installed   bool   `json:"installed"`
	Managed     bool   `json:"managed"`
	Location    string `json:"location,omitempty"`
	Version     string `json:"version,omitempty"`
	Service     string `json:"service,omitempty"`
	Running     bool   `json:"running"`
	Supported   bool   `json:"supported"`
	Source      string `json:"source,omitempty"`
}

type SoftwareOS struct {
	Distro         string `json:"Distro"`
	Family         string `json:"Family"`
	Arch           string `json:"Arch"`
	PackageManager string `json:"PackageManager"`
}

type SoftwareListResult struct {
	OS         SoftwareOS          `json:"os"`
	Components []SoftwareComponent `json:"components"`
	Dir        string              `json:"dir"`
}

func (c *Client) SoftwareList(ctx context.Context, baseURL, token string) (*SoftwareListResult, error) {
	var out SoftwareListResult
	err := c.do(ctx, baseURL, token, request{
		method: http.MethodGet, path: "/agent/v1/software/list",
	}, &out)
	return &out, err
}

func (c *Client) SoftwareInstall(ctx context.Context, baseURL, token, name string) error {
	// Long timeout: downloads and compiles can take many minutes.
	return c.doLong(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/software/install",
		body: map[string]string{"name": name},
	}, nil)
}

func (c *Client) SoftwareRemove(ctx context.Context, baseURL, token, name string) error {
	// Long timeout: teardown of self-contained installs can take a while.
	return c.doLong(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/software/remove",
		body: map[string]string{"name": name},
	}, nil)
}

func (c *Client) SoftwareService(ctx context.Context, baseURL, token, name, action string) error {
	return c.do(ctx, baseURL, token, request{
		method: http.MethodPost, path: "/agent/v1/software/service",
		body: map[string]string{"name": name, "action": action},
	}, nil)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func joinURL(base, path string) string {
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + path
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// FreeTCPPort is exported for tests; the agent picks FastCGI ports itself.
func FreeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
