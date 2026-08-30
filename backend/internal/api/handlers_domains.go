package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/domains"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/google/uuid"
	"github.com/go-chi/chi/v5"
)

type createDomainRequest struct {
	ServerID string `json:"server_id"`
	Domain   string `json:"domain"`
	Type     string `json:"type"`
}

func (s *Server) handleDomainsCreate(w http.ResponseWriter, r *http.Request) {
	var req createDomainRequest
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	if req.Type == "" {
		req.Type = domains.TypePrimary
	}
	d, err := s.deps.Domains.Create(r.Context(), req.ServerID, req.Domain, req.Type)
	if err != nil {
		Error(w, r, err)
		return
	}
	s.auditWithIdentity(r, audit.ActionDomainCreated, "domain", d.ID, map[string]any{
		"domain": d.Domain, "type": d.Type,
	})
	JSON(w, r, http.StatusCreated, d)
}

func (s *Server) handleDomainsList(w http.ResponseWriter, r *http.Request) {
	out, err := s.deps.Domains.List(r.Context(), r.URL.Query().Get("server_id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"domains": out})
}

func (s *Server) handleDomainsGet(w http.ResponseWriter, r *http.Request) {
	d, err := s.deps.Domains.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, d)
}

func (s *Server) handleDomainsDelete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := s.deps.Domains.Get(r.Context(), id)
	if err != nil {
		Error(w, r, err)
		return
	}
	if err := s.deps.Domains.Delete(r.Context(), id); err != nil {
		Error(w, r, err)
		return
	}
	s.auditWithIdentity(r, audit.ActionDomainDeleted, "domain", id, map[string]any{"domain": d.Domain})
	JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

// auditWithIdentity writes an audit entry for the acting user.
func (s *Server) auditWithIdentity(r *http.Request, action, resource, resourceID string, meta map[string]any) {
	e := audit.Entry{
		ActorType:  "system",
		Action:     action,
		Resource:   resource,
		ResourceID: resourceID,
		IP:         httpx.ClientIP(r, s.cfg.Server.TrustedProxy),
		UserAgent:  r.UserAgent(),
		Metadata:   meta,
	}
	if idt := auth.IdentityFrom(r.Context()); idt != nil {
		e.ActorType = "user"
		e.Label = idt.Username
		if uid := idt.UserID; uid != "" {
			e.ActorID = uuidPtr(uid)
		}
	}
	s.deps.Audit.Log(r.Context(), e)
}

// auditEntryForIdentity builds an audit entry without a server receiver.
func auditEntryForIdentity(idt *auth.Identity, action, resource, resourceID, ip, ua string) audit.Entry {
	e := audit.Entry{
		ActorType: "system", Action: action,
		Resource: resource, ResourceID: resourceID, IP: ip, UserAgent: ua,
	}
	if idt != nil {
		e.ActorType = "user"
		e.Label = idt.Username
		if uid := idt.UserID; uid != "" {
			e.ActorID = uuidPtr(uid)
		}
	}
	return e
}

func uuidPtr(id string) *uuid.UUID {
	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil
	}
	return &parsed
}
