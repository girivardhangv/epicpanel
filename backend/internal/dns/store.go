package dns

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound      = errors.New("dns zone not found")
	ErrTaken         = errors.New("a zone for this domain already exists")
	ErrBadDomain     = errors.New("zone domain is not a valid hostname")
	ErrNotOwner      = errors.New("domain is not attached to this website")
	ErrCNAMEConflict = errors.New("a CNAME exists at this name; it cannot coexist with other records")
	ErrTooMany       = errors.New("dns record limit reached (100 per zone)")
)

// DomainChecker reports whether the domain is attached to the website (or is
// its primary domain). Injected to avoid an import cycle with domains.
type DomainChecker func(ctx context.Context, websiteID uuid.UUID, domain string) (bool, error)

type Zone struct {
	ID             uuid.UUID `json:"id"`
	WebsiteID      uuid.UUID `json:"website_id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Domain         string    `json:"domain"`
	TTL            int       `json:"ttl"`
	SOAPrimaryNS   string    `json:"soa_primary_ns"`
	SOAAdminEmail  string    `json:"soa_admin_email"`
	SOARefresh     int       `json:"soa_refresh"`
	SOARetry       int       `json:"soa_retry"`
	SOAExpire      int       `json:"soa_expire"`
	SOAMinimum     int       `json:"soa_minimum"`
	Serial         int64     `json:"serial"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Record struct {
	ID        uuid.UUID `json:"id"`
	ZoneID    uuid.UUID `json:"zone_id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Value     string    `json:"value"`
	TTL       int       `json:"ttl"`
	Priority  int       `json:"priority,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const zoneCols = `id, website_id, organization_id, domain, ttl, soa_primary_ns, soa_admin_email, soa_refresh, soa_retry, soa_expire, soa_minimum, serial, status, created_at, updated_at`

func scanZone(row pgx.Row) (*Zone, error) {
	var z Zone
	err := row.Scan(&z.ID, &z.WebsiteID, &z.OrganizationID, &z.Domain, &z.TTL,
		&z.SOAPrimaryNS, &z.SOAAdminEmail, &z.SOARefresh, &z.SOARetry, &z.SOAExpire, &z.SOAMinimum,
		&z.Serial, &z.Status, &z.CreatedAt, &z.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &z, nil
}

const recordCols = `id, zone_id, name, type, value, ttl, priority, created_at, updated_at`

func scanRecord(row pgx.Row) (*Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.ZoneID, &r.Name, &r.Type, &r.Value, &r.TTL, &r.Priority, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// CreateZone validates the apex, verifies it is attached to the website and
// inserts the zone. SOA defaults derive from the domain. Refuses duplicates.
func (s *Store) CreateZone(ctx context.Context, check DomainChecker, orgID, websiteID uuid.UUID, domain string, ttl int) (*Zone, error) {
	domain = NormalizeHostname(domain)
	if err := ValidateZone(domain); err != nil {
		return nil, ErrBadDomain
	}
	if ttl == 0 {
		ttl = 3600
	}
	if ttl < minTTL || ttl > maxTTL {
		return nil, errors.New("ttl must be between 60 and 604800")
	}
	ok, err := check(ctx, websiteID, domain)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotOwner
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO dns_zones (website_id, organization_id, domain, ttl, soa_primary_ns, soa_admin_email)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+zoneCols,
		websiteID, orgID, domain, ttl, "ns1."+domain+".", "hostmaster."+domain+".",
	)
	z, err := scanZone(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTaken
		}
		return nil, err
	}
	return z, nil
}

func (s *Store) GetZone(ctx context.Context, orgID, zoneID uuid.UUID) (*Zone, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+zoneCols+` FROM dns_zones WHERE id = $1 AND organization_id = $2`, zoneID, orgID)
	z, err := scanZone(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return z, err
}

// GetZoneByID resolves a zone without org scoping (internal fanout only).
func (s *Store) GetZoneByID(ctx context.Context, zoneID uuid.UUID) (*Zone, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+zoneCols+` FROM dns_zones WHERE id = $1`, zoneID)
	z, err := scanZone(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return z, err
}

// GetZoneByDomain resolves a zone by its apex (publish fanout).
func (s *Store) GetZoneByDomain(ctx context.Context, domain string) (*Zone, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+zoneCols+` FROM dns_zones WHERE domain = $1`, NormalizeHostname(domain))
	z, err := scanZone(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return z, err
}

func (s *Store) ListZones(ctx context.Context, orgID, websiteID uuid.UUID) ([]Zone, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+zoneCols+` FROM dns_zones WHERE organization_id = $1 AND website_id = $2 ORDER BY created_at ASC`, orgID, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collectZones(rows)
}

// ZoneWithWebsite is one row of the org-wide zone view for the UI.
type ZoneWithWebsite struct {
	Zone
	WebsiteName string `json:"website_name"`
	ServerID    string `json:"server_id"`
}

// ZonesWithWebsites lists every zone in the org with its site name (UI view).
func (s *Store) ZonesWithWebsites(ctx context.Context, orgID uuid.UUID) ([]ZoneWithWebsite, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT z.id, z.website_id, z.organization_id, z.domain, z.ttl, z.soa_primary_ns, z.soa_admin_email,
		       z.soa_refresh, z.soa_retry, z.soa_expire, z.soa_minimum, z.serial, z.status, z.created_at, z.updated_at,
		       w.name, w.server_id::text
		FROM dns_zones z
		JOIN websites w ON w.id = z.website_id
		WHERE z.organization_id = $1
		ORDER BY z.created_at ASC
	`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ZoneWithWebsite
	for rows.Next() {
		var zw ZoneWithWebsite
		if err := rows.Scan(&zw.ID, &zw.WebsiteID, &zw.OrganizationID, &zw.Domain, &zw.TTL,
			&zw.SOAPrimaryNS, &zw.SOAAdminEmail, &zw.SOARefresh, &zw.SOARetry, &zw.SOAExpire, &zw.SOAMinimum,
			&zw.Serial, &zw.Status, &zw.CreatedAt, &zw.UpdatedAt, &zw.WebsiteName, &zw.ServerID); err != nil {
			return nil, err
		}
		out = append(out, zw)
	}
	return out, rows.Err()
}

func (s *Store) DeleteZone(ctx context.Context, orgID, zoneID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM dns_zones WHERE id = $1 AND organization_id = $2`, zoneID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListRecordsForZone returns all records of a zone.
func (s *Store) ListRecordsForZone(ctx context.Context, zoneID uuid.UUID) ([]Record, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+recordCols+` FROM dns_records WHERE zone_id = $1 ORDER BY name, type`, zoneID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetRecord resolves a record through its zone so org scoping is enforced.
func (s *Store) GetRecord(ctx context.Context, orgID, recordID uuid.UUID) (*Record, *Zone, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT r.id, r.zone_id, r.name, r.type, r.value, r.ttl, r.priority, r.created_at, r.updated_at
		FROM dns_records r
		JOIN dns_zones z ON z.id = r.zone_id
		WHERE r.id = $1 AND z.organization_id = $2
	`, recordID, orgID)
	r, err := scanRecord(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	z, err := s.GetZoneByID(ctx, r.ZoneID)
	if err != nil {
		return nil, nil, err
	}
	return r, z, nil
}

// UpsertRecord inserts or replaces a record. Enforces CNAME exclusivity per
// name (a CNAME cannot coexist with any other record at the same name) and
// the per-zone record cap. Touches the zone serial on success.
func (s *Store) UpsertRecord(ctx context.Context, zone *Zone, name, typ, value string, ttl, priority int) (*Record, error) {
	if zone.Status != "active" {
		return nil, errors.New("zone is disabled")
	}
	normalized, err := ValidateRecordName(name, zone.Domain)
	if err != nil {
		return nil, err
	}
	if err := ValidateRecord(typ, normalized, value, ttl, priority); err != nil {
		return nil, err
	}
	if err := s.checkCNAMEExclusivity(ctx, zone.ID, normalized, typ, uuid.Nil); err != nil {
		return nil, err
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM dns_records WHERE zone_id = $1`, zone.ID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= MaxRecordsPerZone {
		return nil, ErrTooMany
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO dns_records (zone_id, name, type, value, ttl, priority)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (zone_id, name, type, value)
		DO UPDATE SET ttl = EXCLUDED.ttl, priority = EXCLUDED.priority, updated_at = now()
		RETURNING `+recordCols,
		zone.ID, normalized, typ, value, ttl, priority,
	)
	r, err := scanRecord(row)
	if err != nil {
		return nil, err
	}
	if err := s.TouchSerial(ctx, zone.ID); err != nil {
		return nil, err
	}
	return r, nil
}

// UpdateRecord changes value/ttl/priority only (name/type are immutable).
func (s *Store) UpdateRecord(ctx context.Context, r *Record, value string, ttl, priority int) (*Record, error) {
	if err := ValidateRecord(r.Type, r.Name, value, ttl, priority); err != nil {
		return nil, err
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE dns_records SET value = $2, ttl = $3, priority = $4, updated_at = now()
		WHERE id = $1
		RETURNING `+recordCols,
		r.ID, value, ttl, priority,
	)
	updated, err := scanRecord(row)
	if err != nil {
		return nil, err
	}
	if err := s.TouchSerial(ctx, r.ZoneID); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) DeleteRecord(ctx context.Context, orgID, recordID uuid.UUID) error {
	r, _, err := s.GetRecord(ctx, orgID, recordID)
	if err != nil {
		return err
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM dns_records WHERE id = $1`, r.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return s.TouchSerial(ctx, r.ZoneID)
}

// TouchSerial bumps the zone serial to the current unix time so publishes
// pick up changes (any zone/record mutation must call this).
func (s *Store) TouchSerial(ctx context.Context, zoneID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE dns_zones SET serial = EXTRACT(epoch FROM now())::bigint, updated_at = now() WHERE id = $1`, zoneID)
	return err
}

// checkCNAMEExclusivity: when adding typ at name, a CNAME at that name blocks
// everything, and adding a CNAME is blocked by any existing record there.
// excludeID lets updates skip the record being rewritten.
func (s *Store) checkCNAMEExclusivity(ctx context.Context, zoneID uuid.UUID, name, typ string, excludeID uuid.UUID) error {
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM dns_records
		WHERE zone_id = $1 AND name = $2 AND id <> $3
		  AND ($4 = 'CNAME' OR type = 'CNAME')
	`, zoneID, name, excludeID, typ).Scan(&n)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrCNAMEConflict
	}
	return nil
}

func collectZones(rows pgx.Rows) ([]Zone, error) {
	var out []Zone
	for rows.Next() {
		z, err := scanZone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *z)
	}
	return out, rows.Err()
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}
