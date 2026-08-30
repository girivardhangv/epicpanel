package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/epicbyte/epicpanel/backend/internal/websites"

	"github.com/go-chi/chi/v5"
)

func (s *Server) offlineThreshold(r *http.Request) int {
	return s.deps.Settings.Int(r.Context(), settings.KeyServerOfflineMinutes, 5, 1, 10080)
}

func (s *Server) handleServersList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Servers.List(r.Context(), s.offlineThreshold(r))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"servers": out})
}

func (s *Server) handleServersGet(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	srv, err := s.deps.Servers.Get(r.Context(), id, s.offlineThreshold(r))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, srv)
}

func (s *Server) handleServersRevoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	idt := auth.IdentityFrom(r.Context())
	var label string
	if idt != nil {
		label = idt.Username
	}
	err := s.deps.Servers.Revoke(r.Context(), id)
	if err != nil {
		Error(w, r, err)
		return
	}
	s.deps.Audit.Log(r.Context(), audit.Entry{
		ActorType: "user", Label: label,
		Action: audit.ActionServerRevoked, Resource: "server", ResourceID: id,
		IP: httpx.ClientIP(r, s.cfg.Server.TrustedProxy), UserAgent: r.UserAgent(),
	})
	JSON(w, r, http.StatusOK, map[string]any{"revoked": true})
}

// ---------------------------------------------------------------------------
// Registration tokens
// ---------------------------------------------------------------------------

type createTokenRequest struct {
	Label        string `json:"label"`
	ExpiresHours int    `json:"expires_hours"` // optional; default 24h, max 168h
}

// Creating a token is the moment a new server is onboarded, hence
// servers.create; listing requires only view; revoking servers.delete.
func (s *Server) handleRegistrationTokenCreate(w http.ResponseWriter, r *http.Request) {
	var req createTokenRequest
	if r.ContentLength > 0 {
		if err := Decode(r, &req); err != nil {
			Error(w, r, err)
			return
		}
	}
	var createdBy *string
	if idt := auth.IdentityFrom(r.Context()); idt != nil && idt.UserID != "" {
		uid := idt.UserID
		createdBy = &uid
	}
	ttl := time.Duration(req.ExpiresHours) * time.Hour
	token, plaintext, err := s.deps.Servers.CreateRegistrationToken(r.Context(), createdBy, req.Label, ttl)
	if err != nil {
		Error(w, r, err)
		return
	}
	s.auditWithIdentity(r, audit.ActionServerTokenCreated, "registration_token", token.ID,
		map[string]any{"label": token.Label, "expires_at": token.ExpiresAt})
	JSON(w, r, http.StatusCreated, map[string]any{"token": token, "registration_token": plaintext})
}

func (s *Server) handleRegistrationTokenList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Servers.ListRegistrationTokens(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) handleRegistrationTokenRevoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.deps.Servers.RevokeRegistrationToken(r.Context(), id); err != nil {
		Error(w, r, err)
		return
	}
	s.auditWithIdentity(r, audit.ActionServerTokenRevoked, "registration_token", id, nil)
	JSON(w, r, http.StatusOK, map[string]any{"revoked": true})
}

// ---------------------------------------------------------------------------
// Capabilities (nginx + PHP probe)
// ---------------------------------------------------------------------------

// probeCapabilities contacts the agent and stores the feature matrix. All
// results are honest: a failure yields reachable=false with the safe reason.
func (s *Server) probeCapabilities(w http.ResponseWriter, r *http.Request, serverID string) {
	target, err := s.deps.Servers.OpsTarget(r.Context(), serverID)
	if err != nil {
		Error(w, r, err)
		return
	}
	caps := websites.Capabilities{
		Reachable:    false,
		Provisioning: false,
		LogAccess:    false,
	}
	if !target.Manageable {
		caps.Error = "Server has no management channel; re-enroll the agent with ops support."
	} else if uerr := s.deps.Agent.Ping(r.Context(), target.AgentURL, target.OpsToken); uerr != nil {
		caps.Error = safeAgentError(uerr)
	} else {
		caps.Reachable = true
		if st, err := s.deps.Agent.NginxStatus(r.Context(), target.AgentURL, target.OpsToken); err == nil {
			caps.Nginx = st
		} else {
			s.deps.Log.Warn("nginx probe failed", "server", serverID, "err", err)
		}
		if pv, err := s.deps.Agent.PHPVersions(r.Context(), target.AgentURL, target.OpsToken); err == nil {
			caps.PHP = pv
		} else {
			s.deps.Log.Warn("php probe failed", "server", serverID, "err", err)
		}
		caps.Provisioning = caps.Nginx != nil && caps.Nginx.Installed
		caps.LogAccess = caps.Reachable
		caps.ProbedAt = time.Now().UTC().Format(time.RFC3339)
	}

	raw, _ := capsAsMap(caps)
	if err := s.deps.Servers.SaveCapabilities(r.Context(), serverID, raw); err != nil {
		Error(w, r, err)
		return
	}
	s.auditWithIdentity(r, audit.ActionServerProbed, "server", serverID, map[string]any{
		"reachable": caps.Reachable, "nginx": caps.Nginx != nil && caps.Nginx.Installed,
	})
	JSON(w, r, http.StatusOK, caps)
}

func (s *Server) handleServerCapabilities(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.deps.Servers.Get(r.Context(), id, s.offlineThreshold(r)); err != nil {
		Error(w, r, err)
		return
	}
	if r.URL.Query().Get("refresh") == "1" {
		s.probeCapabilities(w, r, id)
		return
	}
	caps, err := s.deps.Servers.Capabilities(r.Context(), id)
	if err != nil {
		Error(w, r, err)
		return
	}
	if len(caps) == 0 {
		// Never fabricated: no probe has happened yet.
		s.probeCapabilities(w, r, id)
		return
	}
	JSON(w, r, http.StatusOK, caps)
}

// handleServerCapabilitiesProbe forces a fresh probe (POST variant).
func (s *Server) handleServerCapabilitiesProbe(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.deps.Servers.Get(r.Context(), id, s.offlineThreshold(r)); err != nil {
		Error(w, r, err)
		return
	}
	s.probeCapabilities(w, r, id)
}

// handleServerPHPVersions returns the live PHP discovery result; falls back
// to the cached capability list when the agent is unreachable so the wizard
// remains usable during brief outages (clearly labeled).
func (s *Server) handleServerPHPVersions(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	target, err := s.deps.Servers.OpsTarget(r.Context(), id)
	if err != nil {
		Error(w, r, err)
		return
	}
	if target.Manageable {
		if pv, err := s.deps.Agent.PHPVersions(r.Context(), target.AgentURL, target.OpsToken); err == nil {
			JSON(w, r, http.StatusOK, map[string]any{"versions": pv, "source": "live"})
			return
		}
	}
	caps, cerr := s.deps.Servers.Capabilities(r.Context(), id)
	if cerr != nil {
		Error(w, r, cerr)
		return
	}
	type phpEntry struct {
		Version     string `json:"version"`
		HandlerType string `json:"handler_type"`
		Status      string `json:"status"`
	}
	versions := []phpEntry{}
	if list, ok := caps["php"].([]any); ok {
		for _, item := range list {
			if m, ok := item.(map[string]any); ok {
				v, _ := m["version"].(string)
				h, _ := m["handler_type"].(string)
				st, _ := m["status"].(string)
				versions = append(versions, phpEntry{Version: v, HandlerType: h, Status: st})
			}
		}
	}
	JSON(w, r, http.StatusOK, map[string]any{"versions": versions, "source": "cached"})
}

func (s *Server) clientIPFn() func(*http.Request) string {
	return func(req *http.Request) string { return httpx.ClientIP(req, s.cfg.Server.TrustedProxy) }
}

// safeAgentError converts agent errors into a user-safe message.
func safeAgentError(err error) string {
	if agentclient.IsUnreachable(err) {
		return "The agent could not be reached. Check that it is running and the management port is reachable."
	}
	var ae *agentclient.AgentError
	if ok := asAgentError(err, &ae); ok {
		return ae.Message
	}
	return "The agent reported an unexpected error."
}

func asAgentError(err error, target **agentclient.AgentError) bool {
	for err != nil {
		if e, ok := err.(*agentclient.AgentError); ok {
			*target = e
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

func capsAsMap(c websites.Capabilities) (map[string]any, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out, nil
}
