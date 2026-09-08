package servers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

// requireAgent authenticates the request via a server agent token
// (Authorization: Bearer agt_xxx). Agent endpoints never use session auth.
func (h *Handler) requireAgent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authz := r.Header.Get("Authorization")
		if !strings.HasPrefix(authz, "Bearer ") {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("agent token required"))
			return
		}
		token := strings.TrimPrefix(authz, "Bearer ")
		srv, err := h.Store.ServerForAgentToken(r.Context(), token)
		if errors.Is(err, ErrAgentForbidden) {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("agent token is invalid or revoked"))
			return
		}
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrInternal(err))
			return
		}
		if srv.Status == StatusDisabled {
			httpapi.RespondError(w, httpapi.ErrForbidden("server is disabled"))
			return
		}
		next(w, r.WithContext(withAgentServer(r.Context(), srv)))
	}
}

// POST /v1/agent/enroll — exchange a one-time registration token for a
// persistent agent token. This is the only agent endpoint that does not
// require an agent token.
func (h *Handler) AgentEnroll(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RegistrationToken string `json:"registration_token"`
		Hostname          string `json:"hostname"`
		OSInfo            string `json:"os_info"`
		AgentVersion      string `json:"agent_version"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	req.RegistrationToken = strings.TrimSpace(req.RegistrationToken)
	if req.RegistrationToken == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("registration_token is required"))
		return
	}

	srv, agentToken, err := h.Store.Enroll(r.Context(), req.RegistrationToken, req.Hostname, req.OSInfo, req.AgentVersion)
	if errors.Is(err, ErrTokenInvalid) {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("registration token is invalid or already used"))
		return
	}
	if errors.Is(err, ErrTokenExpired) {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("registration token has expired"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}

	if h.Audit != nil {
		h.Audit.RecordBestEffort(r.Context(), audit.Entry{
			OrganizationID: &srv.Organization,
			ActorType:      audit.ActorSystem,
			Action:         "agent.enrolled",
			ResourceType:   "server",
			ResourceID:     srv.ID.String(),
			Metadata:       map[string]any{"hostname": srv.Hostname, "os_info": srv.OSInfo, "agent_version": srv.AgentVersion},
			IP:             clientIP(r),
		})
	}

	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{
		"server_id":   srv.ID,
		"agent_token": agentToken,
		"server":      srv,
	})
}

// POST /v1/agent/heartbeat — liveness plus optional metrics payload.
// Body: {"metrics": {"cpu_percent":..., "memory_total_bytes":..., ...}}
func (h *Handler) AgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	srv, ok := ServerFromAgentContext(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("agent authentication required"))
		return
	}

	var req struct {
		Metrics *Metrics `json:"metrics,omitempty"`
	}
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		_ = dec.Decode(&req)
	}

	if err := h.Store.Heartbeat(r.Context(), srv.ID, req.Metrics); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"server_id": srv.ID,
	})
}

var errOrgContext = errStr("org id missing from request context")

type errStr string

func (e errStr) Error() string { return string(e) }

func validServerName(name string) bool {
	if len(name) == 0 || len(name) > 100 {
		return false
	}
	for _, c := range name {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-' || c == '_' || c == '.':
		default:
			return false
		}
	}
	return true
}
