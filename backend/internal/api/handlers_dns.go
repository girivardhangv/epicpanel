package api

import (
	"net/http"

	"github.com/epicbyte/epicpanel/backend/internal/httpx"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/dns"
	"github.com/go-chi/chi/v5"
)

type dnsZoneInput struct {
	Domain string `json:"domain"`
}

func (s *Server) handleDNSZonesList(w http.ResponseWriter, r *http.Request) {
	zones, err := s.deps.DNS.ListZones(r.Context())
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"zones": zones})
}

func (s *Server) handleDNSZoneCreate(w http.ResponseWriter, r *http.Request) {
	var req dnsZoneInput
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	zone, err := s.deps.DNS.CreateZone(r.Context(), s.dnsActor(r), req.Domain)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusCreated, map[string]any{"zone": zone})
}

func (s *Server) handleDNSZoneGet(w http.ResponseWriter, r *http.Request) {
	zone, err := s.deps.DNS.GetZone(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"zone": zone})
}

func (s *Server) handleDNSZoneDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.DNS.DeleteZone(r.Context(), s.dnsActor(r), chi.URLParam(r, "id")); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) handleDNSZoneSync(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.DNS.SyncZone(r.Context(), chi.URLParam(r, "id")); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"synced": true})
}

func (s *Server) handleDNSRecordsList(w http.ResponseWriter, r *http.Request) {
	records, err := s.deps.DNS.ListRecords(r.Context(), chi.URLParam(r, "zoneId"))
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"records": records})
}

func (s *Server) handleDNSRecordCreate(w http.ResponseWriter, r *http.Request) {
	var req dns.RecordInput
	if err := Decode(r, &req); err != nil {
		Error(w, r, err)
		return
	}
	record, err := s.deps.DNS.CreateRecord(r.Context(), s.dnsActor(r), chi.URLParam(r, "zoneId"), req)
	if err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusCreated, map[string]any{"record": record})
}

func (s *Server) handleDNSRecordDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.deps.DNS.DeleteRecord(r.Context(), s.dnsActor(r), chi.URLParam(r, "id")); err != nil {
		Error(w, r, err)
		return
	}
	JSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

func (s *Server) dnsActor(r *http.Request) dns.Actor {
	id := auth.IdentityFrom(r.Context())
	return dns.Actor{
		Label: id.Username,
		IP:    httpx.ClientIP(r, s.cfg.Server.TrustedProxy),
	}
}
