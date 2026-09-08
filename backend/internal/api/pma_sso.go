package api

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"html"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/databases"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

// pmaGateState tracks consumed one-time SSO tokens (defense in depth on top
// of the 60s token TTL; the PHP shim enforces single use as well).
var (
	pmaGateMu       sync.Mutex
	pmaGateConsumed = map[string]time.Time{}
)

func consumePmaGateToken(token string) bool {
	pmaGateMu.Lock()
	defer pmaGateMu.Unlock()
	now := time.Now()
	for k, at := range pmaGateConsumed {
		if now.Sub(at) > 5*time.Minute {
			delete(pmaGateConsumed, k)
		}
	}
	if _, used := pmaGateConsumed[token]; used {
		return false
	}
	pmaGateConsumed[token] = now
	return true
}

// pmaTargetHost validates the redirect target host:port the browser is
// allowed to be sent to (phpMyAdmin/Adminer vhost).
func pmaTargetHost(h string) (string, bool) {
	if h == "" {
		return "", false
	}
	host, port, err := net.SplitHostPort(h)
	if err != nil {
		return "", false
	}
	if port != "8081" && port != "8080" {
		return "", false
	}
	if host == "" || len(host) > 253 {
		return "", false
	}
	for _, c := range host {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == ':' || c == '_') {
			return "", false
		}
	}
	return host + ":" + port, true
}

// ssoDecrypt opens the dbadmin SSO token (nonce | ct | tag, AES-256-GCM).
func ssoDecrypt(keyHex, token string) (*databases.SSOPayload, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return nil, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize()+16 {
		return nil, err
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return nil, err
	}
	var p databases.SSOPayload
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// dbadminSSOKey reads the hex SSO key the agent provisioned alongside the
// database tools (stored in system_settings by the install job fanout).
func (s *Server) dbadminSSOKey(ctx context.Context) (string, error) {
	return s.Settings.Get(ctx, "dbadmin_sso_key")
} // GET /v1/pma-gate?t=<token>&h=<host:port> — public: the encrypted token is
// the auth. MySQL/MariaDB → 302 to the phpMyAdmin signon shim; PostgreSQL →
// self-submitting form into Adminer. Either way the password is never shown.
func (s *Server) pmaGate(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("t"))
	if token == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("missing token"))
		return
	}
	if !consumePmaGateToken(token) {
		http.Error(w, gateMsg("already used"), http.StatusConflict)
		return
	}
	keyHex, _ := s.dbadminSSOKey(r.Context())
	if keyHex == "" {
		http.Error(w, gateMsg("expired"), http.StatusUnauthorized)
		return
	}
	p, err := ssoDecrypt(keyHex, token)
	if err != nil || time.Now().Unix() > p.Exp {
		http.Error(w, gateMsg("expired"), http.StatusUnauthorized)
		return
	}

	target := r.URL.Query().Get("h")
	if target == "" {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			target = h + ":8081"
		} else {
			target = host + ":8081"
		}
	} else if _, ok := pmaTargetHost(target); !ok {
		http.Error(w, "Invalid target host", http.StatusBadRequest)
		return
	}

	switch strings.ToLower(p.Engine) {
	case "postgres", "postgresql":
		renderAdminerForm(w, target, p)
	default:
		// phpMyAdmin signon mode: the shim validates the token, sets the
		// signon session and redirects into phpMyAdmin.
		u := url.URL{Scheme: "http", Host: target, Path: "/phpmyadmin/epicpanel-sso.php"}
		http.Redirect(w, r, u.String()+"?t="+url.QueryEscape(token), http.StatusFound)
	}
}

func gateMsg(kind string) string {
	if kind == "already used" {
		return "This one-time login link was already used. Close this tab and open the database again from the panel."
	}
	return "This one-time login link has expired. Open the database again from the panel."
}

func renderAdminerForm(w http.ResponseWriter, target string, p *databases.SSOPayload) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	u := url.URL{Scheme: "http", Host: target, Path: "/adminer.php"}
	_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><title>Opening ` + html.EscapeString(p.Database) + `…</title></head>
<body style="font-family:system-ui;background:#0f172a;color:#e2e8f0;display:flex;align-items:center;justify-content:center;height:100vh;margin:0">
<div style="text-align:center"><div style="font-size:15px;font-weight:600;margin-bottom:8px">Signing you in to ` + html.EscapeString(p.Database) + `…</div>
<div style="font-size:12px;opacity:.6">If nothing happens, click Continue.</div></div>
<form method="post" action="` + html.EscapeString(u.String()) + `" id="f">
<input type="hidden" name="auth[driver]" value="pgsql">
<input type="hidden" name="auth[server]" value="localhost">
<input type="hidden" name="auth[username]" value="` + html.EscapeString(p.User) + `">
<input type="hidden" name="auth[password]" value="` + html.EscapeString(p.Password) + `">
<input type="hidden" name="auth[db]" value="` + html.EscapeString(p.Database) + `">
<noscript><button type="submit">Continue</button></noscript></form>
<script>document.getElementById('f').submit()</script></body></html>`))
}
