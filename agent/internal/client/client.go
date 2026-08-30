// Package client implements the agent's authenticated communication with the
// EpicPanel API. Only two operations exist: enrollment and heartbeat. The
// per-server token is persisted with restrictive file permissions and never
// logged.
package client

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type Options struct {
	BaseURL   string // e.g. https://panel.example.com
	CAFile    string // optional PEM bundle for private CAs
	UserAgent string
	Timeout   time.Duration
}

func (o *Options) httpClient() (*http.Client, error) {
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	tr := &http.Transport{}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("no valid certificates found in CA file")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	} else {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	return &http.Client{Transport: tr, Timeout: o.Timeout}, nil
}

// EnrollRequest is the payload for first registration and heartbeats.
type EnrollRequest struct {
	Label        string         `json:"label"`
	Hostname     string         `json:"hostname"`
	OS           string         `json:"os"`
	OSVersion    string         `json:"os_version"`
	Arch         string         `json:"arch"`
	AgentVersion string         `json:"agent_version"`
	OpsAddr      string         `json:"ops_addr,omitempty"` // advertised management URL
	Specs        map[string]any `json:"specs"`
}

type enrollResponse struct {
	Server struct {
		ID       string `json:"id"`
		Hostname string `json:"hostname"`
	} `json:"server"`
	AgentToken string `json:"agent_token"`
	OpsToken   string `json:"ops_token"`
}

// Enroll registers this machine; returns server id + agent token + ops token.
// The ops token authenticates the panel against this agent's management
// endpoint; empty on older panels (ops channel disabled in that case).
func (o *Options) Enroll(registrationKey string, req EnrollRequest) (string, string, string, error) {
	hc, err := o.httpClient()
	if err != nil {
		return "", "", "", err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return "", "", "", err
	}
	httpReq, err := http.NewRequest(http.MethodPost,
		joinURL(o.BaseURL, "/api/v1/servers/register"), bytes.NewReader(raw))
	if err != nil {
		return "", "", "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Agent-Key", registrationKey)
	httpReq.Header.Set("User-Agent", o.UserAgent)

	resp, err := hc.Do(httpReq)
	if err != nil {
		return "", "", "", fmt.Errorf("enroll request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusCreated:
	case http.StatusUnauthorized:
		return "", "", "", errors.New("registration rejected: invalid X-Agent-Key")
	default:
		return "", "", "", fmt.Errorf("enroll failed: HTTP %d (%s)", resp.StatusCode, truncate(string(body), 300))
	}
	var er enrollResponse
	if err := json.Unmarshal(body, &er); err != nil || er.AgentToken == "" {
		return "", "", "", errors.New("malformed enrollment response")
	}
	return er.Server.ID, er.AgentToken, er.OpsToken, nil
}

// Heartbeat pushes a metrics snapshot using the saved bearer token.
func (o *Options) Heartbeat(agentToken string, req EnrollRequest) error {
	hc, err := o.httpClient()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost,
		joinURL(o.BaseURL, "/api/v1/servers/heartbeat"), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+agentToken)
	httpReq.Header.Set("User-Agent", o.UserAgent)

	resp, err := hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("heartbeat failed: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusUnauthorized:
		return ErrTokenInvalid
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("heartbeat rejected: HTTP %d (%s)", resp.StatusCode, truncate(string(body), 300))
	}
}

var ErrTokenInvalid = errors.New("agent: server rejected the stored agent token")

// TelemetryBatch is the ingestion wire format for POST /servers/telemetry.
// It reuses the per-server bearer token — no second authentication system.
type TelemetryBatch struct {
	Samples []json.RawMessage `json:"samples"`
}

// SendTelemetry delivers a bounded batch of monitoring samples. The caller
// (monitoring.Runner) already bounds batch size; the server enforces its own
// limits and answers 202 with per-sample outcomes.
func (o *Options) SendTelemetry(agentToken string, samples []json.RawMessage) error {
	if len(samples) == 0 {
		return nil
	}
	hc, err := o.httpClient()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(TelemetryBatch{Samples: samples})
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequest(http.MethodPost,
		joinURL(o.BaseURL, "/api/v1/servers/telemetry"), bytes.NewReader(raw))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+agentToken)
	httpReq.Header.Set("User-Agent", o.UserAgent)

	resp, err := hc.Do(httpReq)
	if err != nil {
		return fmt.Errorf("telemetry request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK, http.StatusAccepted:
		var outcome struct {
			Accepted   int `json:"accepted"`
			Duplicates int `json:"duplicates"`
			Rejected   int `json:"rejected"`
		}
		_ = json.Unmarshal(body, &outcome)
		if outcome.Accepted == 0 && outcome.Duplicates == 0 && outcome.Rejected == 0 && len(body) > 0 {
			// 2xx but nothing stored and nothing reported: surface it so the
			// sender logs and backs off rather than silently dropping data.
			return fmt.Errorf("telemetry accepted but stored nothing (response: %s)", truncate(string(body), 200))
		}
		return nil
	case http.StatusUnauthorized:
		return ErrTokenInvalid
	default:
		return fmt.Errorf("telemetry rejected: HTTP %d (%s)", resp.StatusCode, truncate(string(body), 300))
	}
}

func joinURL(base, path string) string {
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	return base + path
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Credentials persistence -----------------------------------------------------

type storedCredentials struct {
	ServerID   string `json:"server_id"`
	AgentToken string `json:"agent_token"`
	OpsToken   string `json:"ops_token,omitempty"` // panel -> agent management channel secret
}

func CredentialsPath(defaultDir string) string {
	dir := os.Getenv("EPICPANEL_AGENT_DIR")
	if dir == "" {
		dir = defaultDir
	}
	return filepath.Join(dir, "credentials.json")
}

// SaveCredentials writes credentials with 0600 permissions (best effort on
// platforms without POSIX perms, e.g. Windows ACL inheritance).
func SaveCredentials(path string, serverID, agentToken, opsToken string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(storedCredentials{
		ServerID: serverID, AgentToken: agentToken, OpsToken: opsToken,
	}, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Credentials is the loaded enrollment state.
type Credentials struct {
	ServerID   string
	AgentToken string
	OpsToken   string
}

// LoadCredentials reads previously enrolled credentials. Agents enrolled
// before the ops channel existed load fine but carry no OpsToken.
func LoadCredentials(path string) (Credentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var c storedCredentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credentials{}, fmt.Errorf("corrupt credentials file: %w", err)
	}
	if c.ServerID == "" || c.AgentToken == "" {
		return Credentials{}, errors.New("incomplete credentials file; re-enroll required")
	}
	return Credentials{ServerID: c.ServerID, AgentToken: c.AgentToken, OpsToken: c.OpsToken}, nil
}
