package dns

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// TypeSyncDNSZone is the agent job that publishes a zone to the nameserver.
// Declared here (not in jobs) to keep the jobs package untouched.
const TypeSyncDNSZone = jobs.Type("sync_dns_zone")

// Handler registers the DNS zone/record endpoints.
type Handler struct {
	Store    *Store
	Jobs     *jobs.Store
	Websites *websites.Store
	Audit    *audit.Store
	// DomainCheck reports whether a domain is attached to a website; wire it
	// to domains.Store.DomainBelongsToWebsite in api/server.go.
	DomainCheck DomainChecker
	RequireOrg  func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/dns-zone", h.requireOrg(organizations.RoleBilling, h.GetZone))
	mux.HandleFunc("POST /v1/organizations/{org_id}/websites/{website_id}/dns-zone", h.requireOrg(organizations.RoleDeveloper, h.CreateZone))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/dns-zones/{zone_id}", h.requireOrg(organizations.RoleAdmin, h.DeleteZone))
	mux.HandleFunc("POST /v1/organizations/{org_id}/dns-zones/{zone_id}/records", h.requireOrg(organizations.RoleDeveloper, h.CreateRecord))
	mux.HandleFunc("PATCH /v1/organizations/{org_id}/dns-records/{record_id}", h.requireOrg(organizations.RoleDeveloper, h.UpdateRecord))
	mux.HandleFunc("DELETE /v1/organizations/{org_id}/dns-records/{record_id}", h.requireOrg(organizations.RoleAdmin, h.DeleteRecord))
	mux.HandleFunc("POST /v1/organizations/{org_id}/dns-zones/{zone_id}/publish", h.requireOrg(organizations.RoleDeveloper, h.PublishZone))
}

func (h *Handler) requireOrg(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if _, apiErr := h.RequireOrg(r, r.PathValue("org_id"), min); apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r)
	}
}

func (h *Handler) audit(r *http.Request, orgID *uuid.UUID, action, resourceID string, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	var actorID *uuid.UUID
	if user, ok := httpapi.UserFrom(r.Context()); ok {
		if uid, err := uuid.Parse(user.ID); err == nil {
			actorID = &uid
		}
	}
	h.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: orgID,
		ActorUserID:    actorID,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "dns_zone",
		ResourceID:     resourceID,
		Metadata:       meta,
	})
}

func (h *Handler) websiteFromPath(r *http.Request, orgID uuid.UUID) (*websites.Website, *httpapi.APIError) {
	websiteID, err := uuid.Parse(r.PathValue("website_id"))
	if err != nil {
		return nil, httpapi.ErrValidation("invalid website id")
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, websiteID)
	if err == websites.ErrNotFound {
		return nil, httpapi.ErrNotFound("website not found")
	}
	if err != nil {
		return nil, httpapi.ErrInternal(err)
	}
	return ws, nil
}

// GET .../dns-zone — zone + records; 404 until the zone is created.
func (h *Handler) GetZone(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	zones, err := h.Store.ListZones(r.Context(), orgID, ws.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if len(zones) == 0 {
		httpapi.RespondError(w, httpapi.ErrNotFound("no dns zone for this website yet; create one first"))
		return
	}
	z := zones[0]
	records, err := h.Store.ListRecordsForZone(r.Context(), z.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if records == nil {
		records = []Record{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"zone": z, "records": records})
}

// POST .../dns-zone {"domain": "example.com", "ttl": 3600}
func (h *Handler) CreateZone(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	ws, apiErr := h.websiteFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Domain string `json:"domain"`
		TTL    int    `json:"ttl"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if h.DomainCheck == nil {
		httpapi.RespondError(w, httpapi.ErrInternal(errNoDomainCheck))
		return
	}
	z, err := h.Store.CreateZone(r.Context(), h.DomainCheck, orgID, ws.ID, req.Domain, req.TTL)
	switch err {
	case nil:
	case ErrBadDomain:
		httpapi.RespondError(w, httpapi.ErrValidation("domain is not a valid hostname"))
		return
	case ErrTaken:
		httpapi.RespondError(w, httpapi.ErrConflict("a dns zone for this domain already exists"))
		return
	case ErrNotOwner:
		httpapi.RespondError(w, httpapi.ErrValidation("domain must be attached to this website"))
		return
	default:
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "dns_zone.created", z.ID.String(), map[string]any{"domain": z.Domain})
	httpapi.WriteJSON(w, http.StatusCreated, z)
}

// DELETE .../dns-zones/{zone_id}
func (h *Handler) DeleteZone(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	zoneID, err := uuid.Parse(r.PathValue("zone_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid zone id"))
		return
	}
	z, err := h.Store.GetZone(r.Context(), orgID, zoneID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("dns zone not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.Store.DeleteZone(r.Context(), orgID, zoneID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "dns_zone.deleted", z.ID.String(), map[string]any{"domain": z.Domain})
	w.WriteHeader(http.StatusNoContent)
}

// POST .../dns-zones/{zone_id}/records {"name":"www","type":"A","value":"1.2.3.4"}
func (h *Handler) CreateRecord(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	zoneID, err := uuid.Parse(r.PathValue("zone_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid zone id"))
		return
	}
	z, err := h.Store.GetZone(r.Context(), orgID, zoneID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("dns zone not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var req struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Value    string `json:"value"`
		TTL      int    `json:"ttl"`
		Priority int    `json:"priority"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.TTL == 0 {
		req.TTL = 3600
	}
	rec, err := h.Store.UpsertRecord(r.Context(), z, req.Name, strings.ToUpper(strings.TrimSpace(req.Type)), req.Value, req.TTL, req.Priority)
	if err != nil {
		respondRecordError(w, err)
		return
	}
	h.audit(r, &orgID, "dns_record.created", rec.ID.String(), map[string]any{"zone": z.Domain, "name": rec.Name, "type": rec.Type})
	httpapi.WriteJSON(w, http.StatusCreated, rec)
}

// PATCH .../dns-records/{record_id} {"value": "...", "ttl": 3600, "priority": 10}
func (h *Handler) UpdateRecord(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	recordID, err := uuid.Parse(r.PathValue("record_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid record id"))
		return
	}
	rec, z, err := h.Store.GetRecord(r.Context(), orgID, recordID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("dns record not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var req struct {
		Value    string `json:"value"`
		TTL      int    `json:"ttl"`
		Priority int    `json:"priority"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	updated, err := h.Store.UpdateRecord(r.Context(), rec, req.Value, req.TTL, req.Priority)
	if err != nil {
		respondRecordError(w, err)
		return
	}
	h.audit(r, &orgID, "dns_record.updated", rec.ID.String(), map[string]any{"zone": z.Domain, "type": rec.Type})
	httpapi.WriteJSON(w, http.StatusOK, updated)
}

// DELETE .../dns-records/{record_id}
func (h *Handler) DeleteRecord(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	recordID, err := uuid.Parse(r.PathValue("record_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid record id"))
		return
	}
	if err := h.Store.DeleteRecord(r.Context(), orgID, recordID); err != nil {
		if err == ErrNotFound {
			httpapi.RespondError(w, httpapi.ErrNotFound("dns record not found"))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "dns_record.deleted", recordID.String(), nil)
	w.WriteHeader(http.StatusNoContent)
}

// PublishPayload is what the agent's sync_dns_zone op receives: the desired
// zone header plus all records for one zone.
type PublishPayload struct {
	WebsiteID string       `json:"website_id"`
	Zone      ZoneFile     `json:"zone"`
	Records   []RecordFile `json:"records"`
}

// ZoneFile is the SOA-carrying zone header.
type ZoneFile struct {
	Domain     string `json:"domain"`
	TTL        int    `json:"ttl"`
	PrimaryNS  string `json:"primary_ns"`
	AdminEmail string `json:"admin_email"`
	Refresh    int    `json:"refresh"`
	Retry      int    `json:"retry"`
	Expire     int    `json:"expire"`
	Minimum    int    `json:"minimum"`
	Serial     int64  `json:"serial"`
}

// RecordFile is one record line in the published zone.
type RecordFile struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	TTL      int    `json:"ttl"`
	Priority int    `json:"priority,omitempty"`
}

// POST .../dns-zones/{zone_id}/publish — enqueues sync_dns_zone with the full
// desired zone. The idempotency key pins one publish per zone+serial so
// concurrent publishes of the same revision never duplicate the job.
func (h *Handler) PublishZone(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	zoneID, err := uuid.Parse(r.PathValue("zone_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid zone id"))
		return
	}
	z, err := h.Store.GetZone(r.Context(), orgID, zoneID)
	if err == ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("dns zone not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	records, err := h.Store.ListRecordsForZone(r.Context(), z.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	ws, err := h.Websites.GetByIDAny(r.Context(), z.WebsiteID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	payload := PublishPayload{
		WebsiteID: z.WebsiteID.String(),
		Zone: ZoneFile{
			Domain:     z.Domain,
			TTL:        z.TTL,
			PrimaryNS:  z.SOAPrimaryNS,
			AdminEmail: z.SOAAdminEmail,
			Refresh:    z.SOARefresh,
			Retry:      z.SOARetry,
			Expire:     z.SOAExpire,
			Minimum:    z.SOAMinimum,
			Serial:     z.Serial,
		},
		Records: make([]RecordFile, 0, len(records)),
	}
	for _, rec := range records {
		payload.Records = append(payload.Records, RecordFile{
			Name: rec.Name, Type: rec.Type, Value: rec.Value, TTL: rec.TTL, Priority: rec.Priority,
		})
	}
	key := "dnszone_" + z.ID.String() + "_" + strconv.FormatInt(z.Serial, 10)
	job, err := h.Jobs.EnqueueIdempotent(r.Context(), ws.ServerID, &z.WebsiteID, TypeSyncDNSZone, payload, key)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, &orgID, "dns_zone.published", z.ID.String(), map[string]any{"serial": z.Serial, "job": job.ID.String()})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "serial": z.Serial})
}

var errNoDomainCheck = errors.New("domain checker not wired")

func respondRecordError(w http.ResponseWriter, err error) {
	switch err {
	case ErrCNAMEConflict:
		httpapi.RespondError(w, httpapi.ErrConflict("a CNAME exists at this name; it cannot coexist with other records"))
	case ErrTooMany:
		httpapi.RespondError(w, httpapi.ErrConflict("dns record limit reached (100 per zone)"))
	case ErrNotFound:
		httpapi.RespondError(w, httpapi.ErrNotFound("dns record not found"))
	default:
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
	}
}

// zonePublishPayload is the subset of PublishPayload the fanout needs (the
// zone is resolved by apex to keep the wire payload exactly PublishPayload).
type zonePublishPayload struct {
	WebsiteID string `json:"website_id"`
	Zone      struct {
		Domain string `json:"domain"`
		Serial int64  `json:"serial"`
	} `json:"zone"`
}

// ApplyZonePublishOutcome is the fanout entry point: call it from the
// OnJobFinished chain for sync_dns_zone jobs (see api/server.go wiring).
// Zone rows are desired state, so outcomes are logged for operators; the
// returned error is informational and never fatal to the fanout.
func ApplyZonePublishOutcome(ctx context.Context, store *Store, job *jobs.Job, result json.RawMessage) error {
	if job.Type != TypeSyncDNSZone {
		return nil
	}
	var p zonePublishPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || p.Zone.Domain == "" {
		return nil
	}
	z, err := store.GetZoneByDomain(ctx, p.Zone.Domain)
	if err != nil {
		return err
	}
	if job.Status == jobs.StatusFailed {
		slog.Error("dns zone publish failed", "zone", z.Domain, "serial", p.Zone.Serial, "err", job.Error)
		return nil
	}
	if job.Status == jobs.StatusSuccess {
		var outcome struct {
			Serial int64 `json:"serial"`
		}
		_ = json.Unmarshal(result, &outcome)
		if outcome.Serial > 0 && outcome.Serial != p.Zone.Serial {
			// The agent applied a different revision; another publish is needed.
			slog.Warn("dns zone publish applied a different serial", "zone", z.Domain, "sent", p.Zone.Serial, "applied", outcome.Serial)
		}
	}
	return nil
}
