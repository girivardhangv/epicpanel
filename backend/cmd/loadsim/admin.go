package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

func newCookieJar() http.CookieJar {
	jar, _ := cookiejar.New(nil)
	return jar
}

// adminClient handles the panel-side REST surface the simulator needs:
// first-user bootstrap (or login), org create, servers create, agent enroll.
// Everything is the exact same flow a real operator would click through.
type adminClient struct {
	base string
	hc   *http.Client
	csrf bool // X-EpicPanel header required once a session cookie exists
}

func newAdminClient(base, email, pass string) (*adminClient, error) {
	c := &adminClient{base: trimSlash(base), hc: &http.Client{
		Timeout: 15 * time.Second,
		// Session auth is cookie-based; the jar stores the epicpanel_session
		// cookie so every subsequent call is authenticated.
		Jar: newCookieJar(),
	}}
	// Try login first (repeat runs), then first-user register (fresh DBs).
	// The login response carries is_platform_admin (role is per-org and only
	// appears on /v1/auth/me); either marks an account able to see the fleet.
	if code, body, err := c.post("/v1/auth/login", map[string]string{"email": email, "password": pass}); err == nil && code == 200 {
		var out struct {
			User struct {
				IsPlatformAdmin bool `json:"is_platform_admin"`
			} `json:"user"`
		}
		if json.Unmarshal(body, &out) == nil && out.User.IsPlatformAdmin {
			return c, nil
		}
		return nil, fmt.Errorf("login ok but account is not a platform admin")
	}
	code, body, err := c.post("/v1/auth/register", map[string]string{
		"email": email, "password": pass, "name": "loadsim",
	})
	if err != nil {
		return nil, err
	}
	if code != 201 {
		return nil, fmt.Errorf("bootstrap failed (login and register): register status %d: %s", code, truncate(body, 300))
	}
	return c, nil
}

func (c *adminClient) post(path string, payload any) (int, []byte, error) {
	return c.do(http.MethodPost, path, payload)
}

func (c *adminClient) do(method, path string, payload any) (int, []byte, error) {
	var rd io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// CSRF double-check header — the API refuses cookie-auth mutations
	// without it (Phase 2 hardening).
	req.Header.Set("X-EpicPanel", "1")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func (c *adminClient) get(path string) (int, []byte, error) {
	return c.do(http.MethodGet, path, nil)
}

// ensureOrg creates the fleet org or reuses the existing one (409 on dup).
func (c *adminClient) ensureOrg(name string) (string, error) {
	code, body, err := c.post("/v1/organizations", map[string]string{"name": name})
	if err != nil {
		return "", err
	}
	if code == 201 {
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
			return "", fmt.Errorf("org create response missing id")
		}
		return out.ID, nil
	}
	// Duplicate or otherwise: list and find by name.
	code, body, err = c.get("/v1/organizations")
	if err != nil {
		return "", err
	}
	if code != 200 {
		return "", fmt.Errorf("org create status %d and list status %d: %s", code, code, truncate(body, 200))
	}
	var out struct {
		Organizations []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"organizations"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", err
	}
	for _, o := range out.Organizations {
		if o.Name == name {
			return o.ID, nil
		}
	}
	return "", fmt.Errorf("org create status %d and no existing org named %q", code, name)
}

type nodeToken struct {
	ServerID   string `json:"server_id"`
	AgentToken string `json:"agent_token"`
	Hostname   string
}

// enrollFleet registers nNodes servers and enrolls an agent token for each.
// Idempotent across runs by hostname: reuse the registered server's most
// recent valid registration token when present.
func (c *adminClient) enrollFleet(orgID string, n int, quiet bool) ([]nodeToken, error) {
	out := make([]nodeToken, 0, n)
	for i := 0; i < n; i++ {
		hostname := fmt.Sprintf("loadsim-%03d", i)
		tok, err := c.enrollOne(orgID, hostname)
		if err != nil {
			return nil, fmt.Errorf("node %s: %w", hostname, err)
		}
		tok.Hostname = hostname
		out = append(out, tok)
		if !quiet && (i+1)%25 == 0 {
			fmt.Printf("[loadsim] enrolled %d/%d nodes\n", i+1, n)
		}
	}
	return out, nil
}

func (c *adminClient) enrollOne(orgID, hostname string) (nodeToken, error) {
	var tok nodeToken
	code, body, err := c.post("/v1/organizations/"+orgID+"/servers", map[string]string{"name": hostname})
	if err != nil {
		return tok, err
	}
	if code == 201 {
		var out struct {
			Server            map[string]any `json:"server"`
			RegistrationToken string         `json:"registration_token"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return tok, fmt.Errorf("decode create: %w", err)
		}
		id, _ := out.Server["id"].(string)
		if id == "" || out.RegistrationToken == "" {
			return tok, fmt.Errorf("create response missing id/registration_token")
		}
		return c.enrollWithToken(out.RegistrationToken, hostname)
	}
	if code == 409 || strings.Contains(string(body), "already") || strings.Contains(string(body), "taken") {
		// Find the existing server, mint a fresh registration token.
		code, body, err = c.get("/v1/organizations/" + orgID + "/servers")
		if err != nil {
			return tok, err
		}
		if code != 200 {
			return tok, fmt.Errorf("server list status %d", code)
		}
		var list struct {
			Servers []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"servers"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			return tok, err
		}
		for _, s := range list.Servers {
			if s.Name == hostname {
				code, body, err = c.post("/v1/organizations/"+orgID+"/servers/"+s.ID+"/registration-token", nil)
				if err != nil {
					return tok, err
				}
				if code != 201 && code != 200 {
					return tok, fmt.Errorf("token rotate status %d: %s", code, truncate(body, 200))
				}
				var rot struct {
					RegistrationToken string `json:"registration_token"`
				}
				if err := json.Unmarshal(body, &rot); err != nil || rot.RegistrationToken == "" {
					return tok, fmt.Errorf("rotate response missing registration_token")
				}
				tok.ServerID = s.ID
				return c.enrollWithToken(rot.RegistrationToken, hostname)
			}
		}
		return tok, fmt.Errorf("existing server %q not found in list", hostname)
	}
	return tok, fmt.Errorf("server create status %d: %s", code, truncate(body, 200))
}

func (c *adminClient) enrollWithToken(regToken, hostname string) (nodeToken, error) {
	var tok nodeToken
	code, body, err := c.post("/v1/agent/enroll", map[string]string{
		"registration_token": regToken,
		"hostname":           hostname,
		"os_info":            "loadsim",
		"agent_version":      version,
	})
	if err != nil {
		return tok, err
	}
	if code != 201 {
		return tok, fmt.Errorf("enroll status %d: %s", code, truncate(body, 200))
	}
	var out struct {
		ServerID   string `json:"server_id"`
		AgentToken string `json:"agent_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.AgentToken == "" {
		return tok, fmt.Errorf("enroll response missing agent_token")
	}
	tok.ServerID = out.ServerID
	tok.AgentToken = out.AgentToken
	return tok, nil
}

// dialBrowserWS opens /v1/ws with the admin session cookie, mirroring the
// browser (the hub filters by org but admins receive the full fleet).
func (c *adminClient) dialBrowserWS(ctx context.Context) (*websocket.Conn, error) {
	u := "ws" + strings.TrimPrefix(c.base, "http") + "/v1/ws"
	hdr := http.Header{}
	uu, err := urlFromString(c.base)
	if err != nil {
		return nil, err
	}
	for _, ck := range c.hc.Jar.Cookies(uu) {
		hdr.Add("Cookie", ck.Name+"="+ck.Value)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := dialer.DialContext(ctx, u, hdr)
	return conn, err
}

// fleetFleetMetrics GETs the live fleet projection (in-memory, no DB).
func (c *adminClient) fleetFleetMetrics(orgID string) (int, []byte, error) {
	return c.get("/v1/organizations/" + orgID + "/servers/metrics")
}

// serverMetrics GETs one node's live projection.
func (c *adminClient) serverMetrics(orgID, serverID string) (int, []byte, error) {
	return c.get("/v1/organizations/" + orgID + "/servers/" + serverID + "/metrics")
}

// serverState reads node_state + freshness.state for one node from the live
// fleet projection.
func (c *adminClient) serverState(orgID, serverID string) (nodeState, freshState string) {
	code, body, err := c.fleetFleetMetrics(orgID)
	if err != nil || code != 200 {
		return "", ""
	}
	var out struct {
		Metrics []struct {
			ServerID  string `json:"server_id"`
			NodeState string `json:"node_state"`
			Freshness struct {
				State string `json:"state"`
			} `json:"freshness"`
		} `json:"metrics"`
	}
	if json.Unmarshal(body, &out) != nil {
		return "", ""
	}
	for _, m := range out.Metrics {
		if m.ServerID == serverID {
			return m.NodeState, m.Freshness.State
		}
	}
	return "", ""
}

func urlFromString(raw string) (*url.URL, error) { return url.Parse(raw) }

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n]) + "..."
	}
	return string(b)
}
