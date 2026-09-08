// Package adminview is the Phase 6 fleet read-model: cross-organization
// aggregates for the admin WHM experience (dashboard, nodes, accounts,
// domains, DNS, databases, backups, jobs, ports, alerts, users).
//
// Rules it honors:
//   - Live metrics NEVER come from the historical DB — instantaneous values
//     are read from the in-memory LiveStore (Phase 3) with LIVE/STALE/OFFLINE
//     state and age; DB rows only supply counts and identity.
//   - Read-only: every mutation (suspend/resume accounts, retry/cancel jobs)
//     lives in the api layer (phase6_adminview.go) so authorization, audit
//     and event publication happen in exactly one place.
package adminview

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/metrics"
	"github.com/epicbyte/epicpanel/backend/internal/ports"
)

type Service struct {
	Pool *pgxpool.Pool
	// Live is the Phase 3 instantaneous store; nil-safe (tests without a
	// stream simply report zero live nodes).
	Live *metrics.LiveStore
}

// ---------------------------------------------------------------------------
// rows
// ---------------------------------------------------------------------------

// ServerRow is one node in the fleet roster (DB facts only; live state is
// merged client-side from the WS frames / LiveFrames endpoint).
type ServerRow struct {
	ID              uuid.UUID `json:"id"`
	OrganizationID  uuid.UUID `json:"organization_id"`
	Name            string    `json:"name"`
	Hostname        string    `json:"hostname"`
	OSInfo          string    `json:"os_info"`
	AgentVersion    string    `json:"agent_version"`
	Status          string    `json:"status"`
	MaintenanceMode bool      `json:"maintenance_mode"`
	EnrolledAt      *string   `json:"enrolled_at,omitempty"`
	LastSeenAt      *string   `json:"last_seen_at,omitempty"`
	CreatedAt       string    `json:"created_at"`
	Websites        int64     `json:"websites"`
	Databases       int64     `json:"databases"`
	JobsActive      int64     `json:"jobs_active"`
	Runtimes        []RtRow   `json:"runtimes"`
}

type RtRow struct {
	Type    string `json:"type"`
	Version string `json:"version"`
	Status  string `json:"status"`
}

// AccountRow is one cross-org hosting account (= website).
type AccountRow struct {
	ID           uuid.UUID `json:"id"`
	Organization uuid.UUID `json:"organization_id"`
	OrgName      string    `json:"org_name"`
	ServerID     uuid.UUID `json:"server_id"`
	ServerName   string    `json:"server_name"`
	Name         string    `json:"name"`
	Primary      string    `json:"primary_domain"`
	Runtime      string    `json:"runtime"`
	WebServer    string    `json:"web_server"`
	Plan         *string   `json:"plan,omitempty"`
	Status       string    `json:"status"`
	BackendPort  int       `json:"backend_port"`
	UsageCPU     float64   `json:"usage_cpu_percent"`
	UsageMem     int64     `json:"usage_memory_bytes"`
	UsageDiskMB  int64     `json:"usage_disk_mb"`
	CreatedAt    string    `json:"created_at"`
}

type OrgRow struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Plan      *string   `json:"plan,omitempty"`
	Websites  int64     `json:"websites"`
	Databases int64     `json:"databases"`
	Domains   int64     `json:"domains"`
	Owners    string    `json:"owners"`
	CreatedAt string    `json:"created_at"`
}

type DomainRow struct {
	ID         uuid.UUID `json:"id"`
	OrgName    string    `json:"org_name"`
	WebsiteID  uuid.UUID `json:"website_id"`
	Website    string    `json:"website_name"`
	ServerName string    `json:"server_name"`
	Domain     string    `json:"domain"`
	Kind       string    `json:"kind"`
	SSLMode    string    `json:"ssl_mode"`
	SSLState   string    `json:"ssl_state"`
	SSLExpires *string   `json:"ssl_expires_at,omitempty"`
	CreatedAt  string    `json:"created_at"`
}

type DatabaseRow struct {
	ID        uuid.UUID `json:"id"`
	OrgName   string    `json:"org_name"`
	Server    string    `json:"server_name"`
	Website   *string   `json:"website_name,omitempty"`
	Engine    string    `json:"engine"`
	Name      string    `json:"name"`
	DBUser    string    `json:"db_user"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"created_at"`
}

type BackupRow struct {
	ID        uuid.UUID `json:"id"`
	OrgName   string    `json:"org_name"`
	Website   string    `json:"website_name"`
	Type      string    `json:"type"`
	Status    string    `json:"status"`
	Trigger   string    `json:"trigger_type"`
	SizeBytes int64     `json:"size_bytes"`
	Error     string    `json:"error"`
	CreatedAt string    `json:"created_at"`
	Finished  *string   `json:"finished_at,omitempty"`
}

type ZoneRow struct {
	ID        uuid.UUID `json:"id"`
	OrgName   string    `json:"org_name"`
	WebsiteID uuid.UUID `json:"website_id"`
	Website   string    `json:"website_name"`
	Domain    string    `json:"domain"`
	TTL       int       `json:"ttl"`
	Serial    int64     `json:"serial"`
	Status    string    `json:"status"`
	Records   int64     `json:"records"`
	UpdatedAt string    `json:"updated_at"`
}

type AlertRow struct {
	ID           string  `json:"id"`
	OrgName      string  `json:"org_name"`
	Type         string  `json:"type"`
	Severity     string  `json:"severity"`
	ResourceType string  `json:"resource_type"`
	ResourceID   string  `json:"resource_id"`
	ResourceName string  `json:"resource_name"`
	Message      string  `json:"message"`
	CreatedAt    string  `json:"created_at"`
	ResolvedAt   *string `json:"resolved_at,omitempty"`
}

type UserRow struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Platform  bool      `json:"is_platform_admin"`
	Status    string    `json:"status"`
	MFA       bool      `json:"mfa_enabled"`
	Orgs      string    `json:"orgs"`
	CreatedAt string    `json:"created_at"`
}

// ---------------------------------------------------------------------------
// queries
// ---------------------------------------------------------------------------

func (s *Service) Servers(ctx context.Context) ([]ServerRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT sv.id, sv.organization_id, sv.name, coalesce(sv.hostname, ''), coalesce(sv.os_info, ''),
		       coalesce(sv.agent_version, ''), sv.status, sv.maintenance_mode, sv.enrolled_at, sv.last_seen_at, sv.created_at,
		       (SELECT count(*) FROM websites w WHERE w.server_id = sv.id AND w.status NOT IN ('deleted','deleting')),
		       (SELECT count(*) FROM databases d WHERE d.server_id = sv.id AND d.status <> 'deleting'),
		       (SELECT count(*) FROM jobs j WHERE j.server_id = sv.id AND j.status IN ('pending','running'))
		FROM servers sv
		ORDER BY sv.name ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ServerRow{}
	for rows.Next() {
		var r ServerRow
		var enrolled, lastSeen *time.Time
		var created time.Time
		if err := rows.Scan(&r.ID, &r.OrganizationID, &r.Name, &r.Hostname, &r.OSInfo, &r.AgentVersion,
			&r.Status, &r.MaintenanceMode, &enrolled, &lastSeen, &created, &r.Websites, &r.Databases, &r.JobsActive); err != nil {
			return nil, err
		}
		r.EnrolledAt, r.LastSeenAt, r.CreatedAt = isoPtr(enrolled), isoPtr(lastSeen), iso(created)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		rt, err := s.runtimesFor(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Runtimes = rt
	}
	return out, nil
}

func (s *Service) runtimesFor(ctx context.Context, serverID uuid.UUID) ([]RtRow, error) {
	rows, err := s.Pool.Query(ctx, `SELECT type::text, version, status::text FROM runtimes WHERE server_id = $1 ORDER BY type, version`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RtRow{}
	for rows.Next() {
		var r RtRow
		if err := rows.Scan(&r.Type, &r.Version, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type AccountFilter struct {
	OrgID    string // optional uuid string
	ServerID string
	Status   string
	Search   string
	Limit    int
}

func (s *Service) Accounts(ctx context.Context, f AccountFilter) ([]AccountRow, error) {
	q := `
		SELECT w.id, w.organization_id, o.name, w.server_id, sv.name, w.name, w.primary_domain,
		       w.runtime::text, w.web_server::text, p.name, w.status::text, w.backend_port,
		       w.usage_cpu_percent, w.usage_memory_bytes, w.usage_disk_mb, w.created_at
		FROM websites w
		JOIN organizations o ON o.id = w.organization_id
		JOIN servers sv ON sv.id = w.server_id
		LEFT JOIN hosting_packages p ON p.id = o.package_id
		WHERE w.status NOT IN ('deleted','deleting')`
	args := []any{}
	if f.OrgID != "" {
		args = append(args, f.OrgID)
		q += ` AND w.organization_id = $` + itoa(len(args))
	}
	if f.ServerID != "" {
		args = append(args, f.ServerID)
		q += ` AND w.server_id = $` + itoa(len(args))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		q += ` AND w.status = $` + itoa(len(args))
	}
	if f.Search != "" {
		args = append(args, "%"+f.Search+"%")
		q += ` AND (w.name ILIKE $` + itoa(len(args)) + ` OR w.primary_domain ILIKE $` + itoa(len(args)) + ` OR o.name ILIKE $` + itoa(len(args)) + `)`
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	args = append(args, f.Limit)
	q += ` ORDER BY w.created_at DESC LIMIT $` + itoa(len(args))
	rows, err := s.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountRow{}
	for rows.Next() {
		var r AccountRow
		var created time.Time
		if err := rows.Scan(&r.ID, &r.Organization, &r.OrgName, &r.ServerID, &r.ServerName, &r.Name,
			&r.Primary, &r.Runtime, &r.WebServer, &r.Plan, &r.Status, &r.BackendPort,
			&r.UsageCPU, &r.UsageMem, &r.UsageDiskMB, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = iso(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Organizations(ctx context.Context) ([]OrgRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT o.id, o.name, o.slug, p.name,
		       (SELECT count(*) FROM websites w WHERE w.organization_id = o.id AND w.status NOT IN ('deleted','deleting')),
		       (SELECT count(*) FROM databases d WHERE d.organization_id = o.id AND d.status <> 'deleting'),
		       (SELECT count(*) FROM domains dm WHERE dm.organization_id = o.id),
		       coalesce((SELECT string_agg(u.email, ', ') FROM organization_members m JOIN users u ON u.id = m.user_id WHERE m.organization_id = o.id), ''),
		       o.created_at
		FROM organizations o
		LEFT JOIN hosting_packages p ON p.id = o.package_id
		ORDER BY o.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OrgRow{}
	for rows.Next() {
		var r OrgRow
		var created time.Time
		if err := rows.Scan(&r.ID, &r.Name, &r.Slug, &r.Plan, &r.Websites, &r.Databases, &r.Domains, &r.Owners, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = iso(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Domains(ctx context.Context) ([]DomainRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT d.id, o.name, w.id, w.name, sv.name, d.domain, d.kind::text, d.ssl_mode::text, d.ssl_state::text, d.ssl_expires_at, d.created_at
		FROM domains d
		JOIN organizations o ON o.id = d.organization_id
		JOIN websites w ON w.id = d.website_id
		JOIN servers sv ON sv.id = w.server_id
		ORDER BY d.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DomainRow{}
	for rows.Next() {
		var r DomainRow
		var created time.Time
		var sslExpires *time.Time
		if err := rows.Scan(&r.ID, &r.OrgName, &r.WebsiteID, &r.Website, &r.ServerName, &r.Domain, &r.Kind,
			&r.SSLMode, &r.SSLState, &sslExpires, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = iso(created)
		r.SSLExpires = isoPtr(sslExpires)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Databases(ctx context.Context) ([]DatabaseRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT db.id, o.name, sv.name, w.name, db.engine::text, db.name, db.db_user, db.status::text, db.created_at
		FROM databases db
		JOIN organizations o ON o.id = db.organization_id
		JOIN servers sv ON sv.id = db.server_id
		LEFT JOIN websites w ON w.id = db.website_id
		WHERE db.status <> 'deleting'
		ORDER BY db.created_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DatabaseRow{}
	for rows.Next() {
		var r DatabaseRow
		var created time.Time
		var wname *string
		if err := rows.Scan(&r.ID, &r.OrgName, &r.Server, &wname, &r.Engine, &r.Name, &r.DBUser, &r.Status, &created); err != nil {
			return nil, err
		}
		r.Website = wname
		r.CreatedAt = iso(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Backups(ctx context.Context) ([]BackupRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT b.id, o.name, w.name, b.type::text, b.status::text, b.trigger_type::text, b.size_bytes, b.error, b.created_at, b.finished_at
		FROM backups b
		JOIN organizations o ON o.id = b.organization_id
		JOIN websites w ON w.id = b.website_id
		ORDER BY b.created_at DESC
		LIMIT 200
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupRow{}
	for rows.Next() {
		var r BackupRow
		var created time.Time
		var finished *time.Time
		if err := rows.Scan(&r.ID, &r.OrgName, &r.Website, &r.Type, &r.Status, &r.Trigger, &r.SizeBytes, &r.Error, &created, &finished); err != nil {
			return nil, err
		}
		r.CreatedAt = iso(created)
		r.Finished = isoPtr(finished)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Zones(ctx context.Context) ([]ZoneRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT z.id, o.name, w.id, w.name, z.domain, z.ttl, z.serial, z.status::text,
		       (SELECT count(*) FROM dns_records r WHERE r.zone_id = z.id), z.updated_at
		FROM dns_zones z
		JOIN organizations o ON o.id = z.organization_id
		JOIN websites w ON w.id = z.website_id
		ORDER BY z.updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ZoneRow{}
	for rows.Next() {
		var r ZoneRow
		var updated time.Time
		if err := rows.Scan(&r.ID, &r.OrgName, &r.WebsiteID, &r.Website, &r.Domain, &r.TTL, &r.Serial, &r.Status, &r.Records, &updated); err != nil {
			return nil, err
		}
		r.UpdatedAt = iso(updated)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Alerts(ctx context.Context, onlyUnresolved bool) ([]AlertRow, error) {
	q := `
		SELECT a.id::text, coalesce(o.name, ''), a.type, a.severity, a.resource_type, a.resource_id,
		       a.resource_name, a.message, a.created_at, a.resolved_at
		FROM alerts a
		LEFT JOIN organizations o ON o.id = a.organization_id`
	if onlyUnresolved {
		q += ` WHERE a.resolved_at IS NULL`
	}
	q += ` ORDER BY a.created_at DESC LIMIT 100`
	rows, err := s.Pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AlertRow{}
	for rows.Next() {
		var r AlertRow
		var created time.Time
		var resolved *time.Time
		if err := rows.Scan(&r.ID, &r.OrgName, &r.Type, &r.Severity, &r.ResourceType, &r.ResourceID,
			&r.ResourceName, &r.Message, &created, &resolved); err != nil {
			return nil, err
		}
		r.CreatedAt = iso(created)
		r.ResolvedAt = isoPtr(resolved)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Users(ctx context.Context) ([]UserRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT u.id, u.email, u.name, u.is_platform_admin, u.status, u.mfa_enabled,
		       coalesce((SELECT string_agg(o.name, ', ') FROM organization_members m JOIN organizations o ON o.id = m.organization_id WHERE m.user_id = u.id), ''),
		       u.created_at
		FROM users u
		ORDER BY u.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var r UserRow
		var created time.Time
		if err := rows.Scan(&r.ID, &r.Email, &r.Name, &r.Platform, &r.Status, &r.MFA, &r.Orgs, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = iso(created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// PortPools reports the backend proxy-port allocation per node plus the
// configured ranges from internal/ports. Honest scope: the platform has no
// dedicated-IP pool table — allocation today is per-node private backend
// ports (proxy mode) and node hostnames; the payload says so explicitly.
func (s *Service) PortPools(ctx context.Context) (map[string]any, error) {
	type allocRow struct {
		Server  string `json:"server"`
		Port    int    `json:"port"`
		Website string `json:"website"`
		Org     string `json:"org"`
		Status  string `json:"status"`
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT sv.name, w.backend_port, w.name, o.name, w.status::text
		FROM websites w
		JOIN servers sv ON sv.id = w.server_id
		JOIN organizations o ON o.id = w.organization_id
		WHERE w.backend_port > 0 AND w.status NOT IN ('deleted','deleting')
		ORDER BY sv.name, w.backend_port
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allocs := []allocRow{}
	apacheUsed, olsUsed := 0, 0
	for rows.Next() {
		var a allocRow
		if err := rows.Scan(&a.Server, &a.Port, &a.Website, &a.Org, &a.Status); err != nil {
			return nil, err
		}
		if ports.Apache().Contains(a.Port) {
			apacheUsed++
		} else if ports.OLS().Contains(a.Port) {
			olsUsed++
		}
		allocs = append(allocs, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var nodeHostnames []map[string]any
	nrows, err := s.Pool.Query(ctx, `SELECT name, coalesce(hostname, ''), status FROM servers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer nrows.Close()
	for nrows.Next() {
		var name, hostname, status string
		if err := nrows.Scan(&name, &hostname, &status); err != nil {
			return nil, err
		}
		nodeHostnames = append(nodeHostnames, map[string]any{"name": name, "hostname": hostname, "status": status})
	}
	ap, op := ports.Apache(), ports.OLS()
	return map[string]any{
		"apache":      map[string]any{"range": ap.String(), "total": ap.Max - ap.Min + 1, "allocated": apacheUsed},
		"ols":         map[string]any{"range": op.String(), "total": op.Max - op.Min + 1, "allocated": olsUsed},
		"allocations": allocs,
		"nodes":       nodeHostnames,
		"note":        "Dedicated-IP pooling is not implemented; allocation is per-node backend ports (proxy mode) plus enrolled node hostnames.",
	}, nil
}

// LiveFrames exposes the instantaneous store for one-shot WS seeding — the
// admin app then relies on the live WebSocket stream (never polling).
func (s *Service) LiveFrames() []metrics.SnapshotFrame {
	if s.Live == nil {
		return []metrics.SnapshotFrame{}
	}
	return s.Live.Frames()
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func iso(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func isoPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := iso(*t)
	return &s
}
