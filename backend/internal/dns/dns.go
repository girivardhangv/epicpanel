// Package dns implements panel-side DNS zone + record management. The panel
// stores zones and records as its own source of truth and syncs them to a
// configured provider (Cloudflare first). Providers are pluggable behind a
// small interface so self-hosted authoritative DNS can be added later.
package dns

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Record types supported (subset covers A/AAAA/CNAME/MX/TXT/NS/SRV).
var supportedTypes = map[string]bool{
	"A": true, "AAAA": true, "CNAME": true, "MX": true, "TXT": true, "NS": true, "SRV": true,
}

const (
	StatusPending = "pending"
	StatusSynced  = "synced"
	StatusError   = "error"
)

// Provider is the DNS sync backend. Implementations live in provider_*.go.
type Provider interface {
	// EnsureZone creates the zone if missing and returns its provider ID.
	EnsureZone(ctx context.Context, zone Zone) (string, error)
	// UpsertRecord creates or updates one record; returns provider record ID.
	UpsertRecord(ctx context.Context, zone Zone, rec Record) (string, error)
	// DeleteRecord removes a record by its provider ID.
	DeleteRecord(ctx context.Context, zone Zone, providerRecordID string) error
}

// Zone is the API view of a DNS zone.
type Zone struct {
	ID            string `json:"id"`
	ServerID      *string `json:"server_id"`
	Domain        string `json:"domain"`
	Provider      string `json:"provider"`
	Status        string `json:"status"`
	ProviderZoneID string `json:"provider_zone_id,omitempty"`
	Error         string `json:"error,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// Record is the API view of a DNS record.
type Record struct {
	ID               string `json:"id"`
	ZoneID           string `json:"zone_id"`
	Name             string `json:"name"`
	Type             string `json:"type"`
	Value            string `json:"value"`
	Priority         int    `json:"priority"`
	TTL              int    `json:"ttl"`
	Proxied          bool   `json:"proxied"`
	Status           string `json:"status"`
	ProviderRecordID string `json:"provider_record_id,omitempty"`
	Error            string `json:"error,omitempty"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

// RecordInput is the accepted create payload for a DNS record.
type RecordInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	TTL      int    `json:"ttl"`
	Proxied  bool   `json:"proxied"`
}

type Deps struct {
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Settings *settings.Service
	Audit    *audit.Service
}

type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps} }

// ValidateDomain enforces a normalized, zone-safe domain.
func ValidateDomain(domain string) error {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	if d == "" || len(d) > 253 || strings.ContainsAny(d, " *\\/;") {
		return apierror.BadRequest("invalid domain")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return apierror.BadRequest("domain must have at least two labels")
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return apierror.BadRequest("invalid domain label")
		}
		for _, r := range l {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			default:
				return apierror.BadRequest("invalid domain character")
			}
		}
	}
	return nil
}

// ValidateRecordType returns an error for unsupported record types.
func ValidateRecordType(t string) error {
	if !supportedTypes[strings.ToUpper(t)] {
		return apierror.BadRequest("unsupported record type (A/AAAA/CNAME/MX/TXT/NS/SRV)")
	}
	return nil
}

// ValidateName normalizes a record name ('' = root, else a label path).
func ValidateName(name string) error {
	n := strings.TrimSpace(strings.ToLower(name))
	if n == "@" || n == "" || n == "." {
		return nil // root record
	}
	if len(n) > 200 || strings.ContainsAny(n, " *\\/;") {
		return apierror.BadRequest("invalid record name")
	}
	return nil
}

func (s *Service) normalizeName(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	if n == "@" || n == "" {
		return ""
	}
	return strings.TrimSuffix(n, ".")
}

func (s *Service) zoneSelect() string {
	return `SELECT id, server_id, domain, provider, status, COALESCE(provider_zone_id,''),
		COALESCE(error,''), created_at::text, updated_at::text FROM dns_zones`
}

func scanZone(row pgx.Row) (*Zone, error) {
	var z Zone
	var serverID *string
	if err := row.Scan(&z.ID, &serverID, &z.Domain, &z.Provider, &z.Status,
		&z.ProviderZoneID, &z.Error, &z.CreatedAt, &z.UpdatedAt); err != nil {
		return nil, err
	}
	z.ServerID = serverID
	return &z, nil
}

func (s *Service) recordSelect() string {
	return `SELECT id, zone_id, name, type, value, priority, ttl, proxied, status,
		COALESCE(provider_record_id,''), COALESCE(error,''), created_at::text, updated_at::text FROM dns_records`
}

func scanRecord(row pgx.Row) (*Record, error) {
	var r Record
	if err := row.Scan(&r.ID, &r.ZoneID, &r.Name, &r.Type, &r.Value, &r.Priority,
		&r.TTL, &r.Proxied, &r.Status, &r.ProviderRecordID, &r.Error,
		&r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// ListZones returns all zones, newest first.
func (s *Service) ListZones(ctx context.Context) ([]Zone, error) {
	rows, err := s.deps.Pool.Query(ctx, s.zoneSelect()+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []Zone{}
	for rows.Next() {
		z, err := scanZone(rows)
		if err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, *z)
	}
	return out, rows.Err()
}

// GetZone fetches one zone.
func (s *Service) GetZone(ctx context.Context, id string) (*Zone, error) {
	z, err := scanZone(s.deps.Pool.QueryRow(ctx, s.zoneSelect()+` WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, apierror.NotFound("dns zone")
		}
		return nil, apierror.From(err)
	}
	return z, nil
}

// CreateZone validates and inserts a zone (provider sync deferred to SyncZone).
func (s *Service) CreateZone(ctx context.Context, actor Actor, domain string) (*Zone, error) {
	if err := ValidateDomain(domain); err != nil {
		return nil, err
	}
	domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
	var id string
	err := s.deps.Pool.QueryRow(ctx,
		`INSERT INTO dns_zones (domain, provider) VALUES ($1, 'cloudflare') RETURNING id`, domain).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "unique") {
			return nil, apierror.Conflict("a zone already exists for this domain")
		}
		return nil, apierror.From(err)
	}
	s.audit(ctx, actor, "dns.zone_created", id, map[string]any{"domain": domain})
	return s.GetZone(ctx, id)
}

// DeleteZone removes a zone and its records locally; provider cleanup is
// best-effort (the zone remains orphaned remotely if the provider is down).
func (s *Service) DeleteZone(ctx context.Context, actor Actor, id string) error {
	z, err := s.GetZone(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.deps.Pool.Exec(ctx, `DELETE FROM dns_zones WHERE id = $1`, id); err != nil {
		return apierror.From(err)
	}
	s.audit(ctx, actor, "dns.zone_deleted", id, map[string]any{"domain": z.Domain})
	return nil
}

// ListRecords returns records for a zone in a stable order.
func (s *Service) ListRecords(ctx context.Context, zoneID string) ([]Record, error) {
	if _, err := s.GetZone(ctx, zoneID); err != nil {
		return nil, err
	}
	rows, err := s.deps.Pool.Query(ctx,
		s.recordSelect()+` WHERE zone_id = $1 ORDER BY name, type, value`, zoneID)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []Record{}
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// CreateRecord validates + inserts a record (provider sync via SyncZone).
func (s *Service) CreateRecord(ctx context.Context, actor Actor, zoneID string, in RecordInput) (*Record, error) {
	if _, err := s.GetZone(ctx, zoneID); err != nil {
		return nil, err
	}
	if err := ValidateRecordType(in.Type); err != nil {
		return nil, err
	}
	if err := ValidateName(in.Name); err != nil {
		return nil, err
	}
	name := s.normalizeName(in.Name)
	typ := strings.ToUpper(in.Type)
	value := strings.TrimSpace(in.Value)
	if value == "" {
		return nil, apierror.BadRequest("record value is required")
	}
	if typ == "CNAME" && name == "" {
		return nil, apierror.BadRequest("CNAME record name is required")
	}
	ttl := in.TTL
	if ttl <= 0 {
		ttl = 300
	}
	if ttl > 86400 {
		return nil, apierror.BadRequest("ttl must be between 1 and 86400")
	}

	var id string
	err := s.deps.Pool.QueryRow(ctx, `
		INSERT INTO dns_records (zone_id, name, type, value, priority, ttl, proxied)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		zoneID, name, typ, value, in.Priority, ttl, in.Proxied).Scan(&id)
	if err != nil {
		return nil, apierror.From(err)
	}
	s.audit(ctx, actor, "dns.record_created", id, map[string]any{
		"zone_id": zoneID, "name": name, "type": typ,
	})
	return s.GetRecord(ctx, id)
}

// GetRecord fetches one record.
func (s *Service) GetRecord(ctx context.Context, id string) (*Record, error) {
	r, err := scanRecord(s.deps.Pool.QueryRow(ctx, s.recordSelect()+` WHERE id = $1`, id))
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, apierror.NotFound("dns record")
		}
		return nil, apierror.From(err)
	}
	return r, nil
}

// DeleteRecord removes a record locally; provider cleanup is best-effort.
func (s *Service) DeleteRecord(ctx context.Context, actor Actor, id string) error {
	r, err := s.GetRecord(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.deps.Pool.Exec(ctx, `DELETE FROM dns_records WHERE id = $1`, id); err != nil {
		return apierror.From(err)
	}
	s.audit(ctx, actor, "dns.record_deleted", id, map[string]any{"zone_id": r.ZoneID, "type": r.Type})
	return nil
}

// SyncZone pushes a zone + its records to the configured provider. It is
// idempotent and safe to call repeatedly; provider errors are persisted on the
// rows and surfaced to the operator (never silently dropped).
func (s *Service) SyncZone(ctx context.Context, zoneID string) error {
	z, err := s.GetZone(ctx, zoneID)
	if err != nil {
		return err
	}
	prov, err := s.providerFor(ctx, z.Provider)
	if err != nil {
		return err
	}

	zoneIDOut, zerr := prov.EnsureZone(ctx, *z)
	if zerr != nil {
		s.markZoneError(ctx, zoneID, zerr)
		return apierror.New(502, "DNS_PROVIDER_ERROR", "zone sync failed: "+zerr.Error())
	}
	_, _ = s.deps.Pool.Exec(ctx,
		`UPDATE dns_zones SET provider_zone_id = $2, status = 'synced', error = '', updated_at = now() WHERE id = $1`,
		zoneID, zoneIDOut)

	rows, err := s.deps.Pool.Query(ctx, s.recordSelect()+` WHERE zone_id = $1 AND status <> 'synced'`, zoneID)
	if err != nil {
		return apierror.From(err)
	}
	defer rows.Close()
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return apierror.From(err)
		}
		s.syncOne(ctx, *z, *r, prov)
	}
	return rows.Err()
}

func (s *Service) syncOne(ctx context.Context, zone Zone, rec Record, prov Provider) {
	pid, err := prov.UpsertRecord(ctx, zone, rec)
	if err != nil {
		_, _ = s.deps.Pool.Exec(ctx,
			`UPDATE dns_records SET status='error', error=$3, updated_at=now() WHERE id=$1 AND zone_id=$2`,
			rec.ID, zone.ID, truncate(err.Error(), 300))
		return
	}
	_, _ = s.deps.Pool.Exec(ctx,
		`UPDATE dns_records SET provider_record_id=$3, status='synced', error='', updated_at=now() WHERE id=$1 AND zone_id=$2`,
		rec.ID, zone.ID, pid)
}

func (s *Service) markZoneError(ctx context.Context, zoneID string, err error) {
	_, _ = s.deps.Pool.Exec(ctx,
		`UPDATE dns_zones SET status='error', error=$2, updated_at=now() WHERE id=$1`,
		zoneID, truncate(err.Error(), 300))
}

// providerFor builds the configured provider from system settings.
func (s *Service) providerFor(ctx context.Context, name string) (Provider, error) {
	switch name {
	case "cloudflare":
		token := s.deps.Settings.String(ctx, "dns.cloudflare_token", "")
		if token == "" {
			return nil, apierror.New(409, "DNS_PROVIDER_NOT_CONFIGURED",
				"DNS provider 'cloudflare' is not configured: set dns.cloudflare_token in Settings")
		}
		return NewCloudflare(token, s.deps.Log), nil
	default:
		return nil, apierror.BadRequest("unsupported DNS provider: " + name)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Actor identifies who triggered an operation.
type Actor struct {
	Label string
	IP    string
}

func (s *Service) audit(ctx context.Context, actor Actor, action, resourceID string, meta map[string]any) {
	entry := audit.Entry{
		ActorType: "user", Label: actor.Label,
		Action: action, Resource: "dns", ResourceID: resourceID,
		Metadata: meta,
	}
	if actor.Label == "" {
		entry.ActorType = "system"
		entry.Label = "dns manager"
	}
	s.deps.Audit.Log(ctx, entry)
}

var _ = time.Now // reserved for future scheduling of auto-sync
var _ = fmt.Sprintf
