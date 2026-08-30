package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/notifier"
	"github.com/go-chi/chi/v5"
)

// ---------------------------------------------------------------------------
// SSL / certificates (Phase 4)
// ---------------------------------------------------------------------------

func (s *Server) handleCertificateGet(w http.ResponseWriter, r *http.Request) {
	cert, err := s.deps.Websites.CertificateInfo(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	if cert == nil {
		JSON(w, r, http.StatusOK, map[string]any{"certificate": nil})
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"certificate": cert})
}

type requestCertificateRequest struct {
	AutoRenew bool `json:"auto_renew"`
}

// Issuance is explicit and runs as a background job (long ACME round).
func (s *Server) handleCertificateRequest(w http.ResponseWriter, r *http.Request) {
	var req requestCertificateRequest
	if r.ContentLength > 0 {
		if err := Decode(r, &req); err != nil {
			Error(w, r, err)
			return
		}
	}
	job, err := s.deps.Websites.RequestCertificate(r.Context(), s.actorFrom(r), chi.URLParam(r, "id"), req.AutoRenew)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handleCertificateRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Websites.RemoveCertificate(r.Context(), s.actorFrom(r), chi.URLParam(r, "id")); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"removed": true})
}

// ---------------------------------------------------------------------------
// Notification channels (Phase 5)
// ---------------------------------------------------------------------------

type channelRequest struct {
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Config   map[string]any `json:"config"`
	Severity string         `json:"severity"`
	Enabled  *bool          `json:"enabled"`
}

func (s *Server) handleChannelsList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Notifier.ListChannels(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	// Secrets are never returned; redact the config payloads.
	JSON(w, r, http.StatusOK, map[string]any{"channels": redactChannels(out)})
}

func (s *Server) handleChannelCreate(w http.ResponseWriter, r *http.Request) {
	var req channelRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	ch, err := s.deps.Notifier.CreateChannel(r.Context(), req.Name, req.Type, req.Config, req.Severity, enabled)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusCreated, map[string]any{"channel": redactChannel(*ch)})
}

func (s *Server) handleChannelUpdate(w http.ResponseWriter, r *http.Request) {
	var req channelRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	name := req.Name
	var namePtr *string
	if name != "" {
		namePtr = &name
	}
	ch, err := s.deps.Notifier.UpdateChannel(r.Context(), chi.URLParam(r, "id"),
		namePtr, req.Config, ptrStr(req.Severity), req.Enabled)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"channel": redactChannel(*ch)})
}

func (s *Server) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.Notifier.DeleteChannel(r.Context(), chi.URLParam(r, "id")); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

// handleChannelTest sends a sample alert to a channel.
func (s *Server) handleChannelTest(w http.ResponseWriter, r *http.Request) {
	ch, err := s.deps.Notifier.GetChannel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	if !ch.Enabled {
		Error(w, r, apierror.BadRequest("channel is disabled"))
		return
	}
	err = s.deps.Notifier.Deliver(r.Context(), *ch, notifier.AlertPayload{
		Event: "test", Severity: ch.Severity,
		ServerName: "EpicPanel", Rule: "Test notification",
		RuleType: "test", Message: "This is a test notification from EpicPanel.",
		PanelURL: s.cfg.Server.PublicURL,
	})
	if err != nil {
		Error(w, r, apierror.New(502, "DELIVERY_FAILED", "Delivery failed: "+err.Error()))
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"sent": true})
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func ptrStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func redactChannel(c notifier.Channel) notifier.Channel {
	cfg := map[string]any{}
	if c.Config != nil {
		for k, v := range c.Config {
			switch k {
			case "webhook_url":
				cfg[k] = redactURL(v.(string))
			case "smtp_password":
				cfg[k] = "••••••••"
			default:
				cfg[k] = v
			}
		}
	}
	c.Config = cfg
	return c
}

func redactURL(u string) string {
	if len(u) > 8 {
		return u[:8] + "••••"
	}
	return "••••"
}

func redactChannels(in []notifier.Channel) []notifier.Channel {
	out := make([]notifier.Channel, 0, len(in))
	for _, c := range in {
		out = append(out, redactChannel(c))
	}
	return out
}

var _ = auth.IdentityFrom
