package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/software"
	"github.com/go-chi/chi/v5"
)

func (s *Server) swActor(r *http.Request) software.Actor {
	a := software.Actor{IP: httpx.ClientIP(r, s.cfg.Server.TrustedProxy)}
	if idt := auth.IdentityFrom(r.Context()); idt != nil {
		a.Label = idt.Username
	}
	return a
}

// handleSoftwareList returns the live software inventory for a server.
func (s *Server) handleSoftwareList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Software.List(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, out)
}

type softwareNameRequest struct {
	Name string `json:"name"`
}

func (s *Server) handleSoftwareInstall(w http.ResponseWriter, r *http.Request) {
	var req softwareNameRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	job, err := s.deps.Software.Install(r.Context(), s.swActor(r), chi.URLParam(r, "id"), req.Name)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"job": job})
}

func (s *Server) handleSoftwareRemove(w http.ResponseWriter, r *http.Request) {
	var req softwareNameRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	job, err := s.deps.Software.Remove(r.Context(), s.swActor(r), chi.URLParam(r, "id"), req.Name)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusAccepted, map[string]any{"job": job})
}

type softwareServiceRequest struct {
	Name   string `json:"name"`
	Action string `json:"action"`
}

func (s *Server) handleSoftwareService(w http.ResponseWriter, r *http.Request) {
	var req softwareServiceRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if err := s.deps.Software.ServiceControl(r.Context(), s.swActor(r),
		chi.URLParam(r, "id"), req.Name, req.Action); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"ok": true})
}
