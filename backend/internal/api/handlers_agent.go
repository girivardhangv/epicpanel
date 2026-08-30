package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
)

// Agent enrollment requires a single-use registration token created in the
// panel (servers.create). Tokens expire, are revocable and are consumed
// atomically during registration; only their SHA-256 digests are stored.
func (s *Server) agentRegistrationGate() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("X-Agent-Key")
			if key == "" || len(key) > 256 {
				s.deps.Audit.Log(r.Context(), audit.Entry{
					ActorType: "system", Action: "servers.enroll_rejected",
					IP: httpx.ClientIP(r, s.cfg.Server.TrustedProxy), UserAgent: r.UserAgent(),
				})
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"AGENT_KEY_INVALID","message":"Registration token invalid"}}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type registerRequest struct {
	Label        string         `json:"label"`
	Hostname     string         `json:"hostname"`
	OS           string         `json:"os"`
	OSVersion    string         `json:"os_version"`
	Arch         string         `json:"arch"`
	AgentVersion string         `json:"agent_version"`
	Specs        map[string]any `json:"specs"`
	OpsAddr      string         `json:"ops_addr"`
}

type registerResponse struct {
	Server     any    `json:"server"`
	AgentToken string `json:"agent_token"`
	OpsToken   string `json:"ops_token"`
}

// The single time the raw tokens are visible; agents persist them locally
// with restrictive file permissions.
func (s *Server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	ip := httpx.ClientIP(r, s.cfg.Server.TrustedProxy)

	// Consume the registration token first: it is single-use. If enrollment
	// subsequently fails the token stays burned — deliberate, so tokens
	// cannot be ground against the endpoint repeatedly.
	if err := s.deps.Servers.ConsumeRegistrationToken(r.Context(), r.Header.Get("X-Agent-Key")); err != nil {
		s.deps.Audit.Log(r.Context(), audit.Entry{
			ActorType: "system", Action: "servers.enroll_rejected",
			Resource: "registration_token",
			IP:       ip, UserAgent: r.UserAgent(),
		})
		Error(w, r, err)
		return
	}

	srv, token, opsToken, err := s.deps.Servers.Enroll(r.Context(), servers.RegisterInput{
		Label:        req.Label,
		Hostname:     req.Hostname,
		OS:           req.OS,
		OSVersion:    req.OSVersion,
		Arch:         req.Arch,
		AgentVersion: req.AgentVersion,
		Specs:        req.Specs,
		OpsAddr:      req.OpsAddr,
	}, ip)
	if err != nil {
		Error(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), audit.Entry{
		ActorType: "agent", Label: srv.Hostname,
		Action: audit.ActionServerRegistered, Resource: "server", ResourceID: srv.ID,
		IP: ip, UserAgent: r.UserAgent(),
	})
	JSON(w, r, http.StatusCreated, registerResponse{Server: srv, AgentToken: token, OpsToken: opsToken})
}

// agentTokenAuth authenticates heartbeat calls via their per-server bearer token.
func (s *Server) agentTokenAuth() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authz := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(authz, "Bearer ")
			if !ok || token == "" || len(token) > 256 {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"code":"AGENT_AUTH_REQUIRED","message":"Agent authorization required"}}`))
				return
			}
			next.ServeHTTP(w, r.WithContext(withAgentToken(r.Context(), token)))
		})
	}
}

type ctxKey int

const ctxAgentToken ctxKey = iota

func withAgentToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxAgentToken, token)
}

// Heartbeats update the server row keyed strictly by its secret token.
func (s *Server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	token := agentTokenFromContext(r)
	if token == "" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"AGENT_AUTH_REQUIRED","message":"Agent authorization required"}}`))
		return
	}
	err := s.deps.Servers.Heartbeat(r.Context(), token, req.OS, req.OSVersion, req.AgentVersion, req.OpsAddr, req.Specs)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"accepted": true})
}

func agentTokenFromContext(r *http.Request) string {
	if v, ok := r.Context().Value(ctxAgentToken).(string); ok {
		return v
	}
	return ""
}
