package packages

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("package not found")
	ErrNameTaken    = errors.New("package name already exists")
	ErrInUse        = errors.New("package is assigned to organizations")
	ErrDefaultStays = errors.New("the default package cannot be deleted")
)

type Package struct {
	ID              uuid.UUID `json:"id"`
	Name            string    `json:"name"`
	Kind            string    `json:"kind"`
	MaxWebsites     int       `json:"max_websites"`
	MaxDatabases    int       `json:"max_databases"`
	MaxDiskMB       int       `json:"max_disk_mb"`
	MemoryLimitMB   int       `json:"memory_limit_mb"`
	CPUCores        float64   `json:"cpu_cores"`
	MaxAddonDomains int       `json:"max_addon_domains"`
	MaxSubdomains   int       `json:"max_subdomains"`
	AllowedRuntimes []string  `json:"allowed_runtimes"`
	MaxBandwidthMB  int64     `json:"max_bandwidth_mb"`
	IOWeight        int       `json:"io_weight"`
	MaxProcesses    int       `json:"max_processes"`
	MaxPorts        int       `json:"max_ports"`
	MaxBackups      int       `json:"max_backups"`
	PriceMonthly    int       `json:"price_monthly_cents"`
	IsDefault       bool      `json:"is_default"`
	CreatedAt       time.Time `json:"created_at"`
}

// OrgUsage summarizes current consumption against the package.
type OrgUsage struct {
	Websites     int `json:"websites"`
	AddonDomains int `json:"addon_domains"`
	Subdomains   int `json:"subdomains"`
	Databases    int `json:"databases"`
	DiskUsedMB   int `json:"disk_used_mb"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
	max_addon_domains, max_subdomains, allowed_runtimes, max_bandwidth_mb, io_weight, max_processes,
	max_ports, max_backups, price_monthly_cents, is_default, created_at`

func scanRow(row pgx.Row) (*Package, error) {
	var p Package
	err := row.Scan(&p.ID, &p.Name, &p.Kind, &p.MaxWebsites, &p.MaxDatabases, &p.MaxDiskMB, &p.MemoryLimitMB,
		&p.CPUCores, &p.MaxAddonDomains, &p.MaxSubdomains, &p.AllowedRuntimes,
		&p.MaxBandwidthMB, &p.IOWeight, &p.MaxProcesses, &p.MaxPorts, &p.MaxBackups,
		&p.PriceMonthly, &p.IsDefault, &p.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) List(ctx context.Context) ([]Package, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM hosting_packages ORDER BY is_default DESC, price_monthly_cents ASC, name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Package
	for rows.Next() {
		p, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func (s *Store) GetByID(ctx context.Context, id uuid.UUID) (*Package, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM hosting_packages WHERE id = $1`, id)
	p, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

// DefaultPackage returns the fallback package used when an org has none assigned.
func (s *Store) DefaultPackage(ctx context.Context) (*Package, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM hosting_packages WHERE is_default = TRUE LIMIT 1`)
	p, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

type CreateInput struct {
	Name            string
	Kind            string
	MaxWebsites     int
	MaxDatabases    int
	MaxDiskMB       int
	MemoryLimitMB   int
	CPUCores        float64
	MaxAddonDomains int
	MaxSubdomains   int
	AllowedRuntimes []string
	MaxBandwidthMB  int64
	IOWeight        int
	MaxProcesses    int
	MaxPorts        int
	MaxBackups      int
	PriceMonthly    int
}

func normKind(k string) string {
	switch k {
	case "web":
		return k
	default:
		return "web"
	}
}

func (s *Store) Create(ctx context.Context, in CreateInput) (*Package, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO hosting_packages
			(name, kind, max_websites, max_databases, max_disk_mb, memory_limit_mb, cpu_cores,
			 max_addon_domains, max_subdomains, allowed_runtimes, max_bandwidth_mb, io_weight,
			 max_processes, max_ports, max_backups, price_monthly_cents)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		RETURNING `+cols,
		in.Name, normKind(in.Kind), in.MaxWebsites, in.MaxDatabases, in.MaxDiskMB, in.MemoryLimitMB, in.CPUCores,
		in.MaxAddonDomains, in.MaxSubdomains, in.AllowedRuntimes, in.MaxBandwidthMB, in.IOWeight,
		in.MaxProcesses, in.MaxPorts, in.MaxBackups, in.PriceMonthly,
	)
	p, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	return p, nil
}

func (s *Store) Update(ctx context.Context, id uuid.UUID, in CreateInput) (*Package, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE hosting_packages SET name = $2, kind = $3, max_websites = $4, max_databases = $5, max_disk_mb = $6,
		       memory_limit_mb = $7, cpu_cores = $8, max_addon_domains = $9, max_subdomains = $10,
		       allowed_runtimes = $11, max_bandwidth_mb = $12, io_weight = $13, max_processes = $14,
		       max_ports = $15, max_backups = $16, price_monthly_cents = $17, updated_at = now()
		WHERE id = $1
		RETURNING `+cols,
		id, in.Name, normKind(in.Kind), in.MaxWebsites, in.MaxDatabases, in.MaxDiskMB, in.MemoryLimitMB, in.CPUCores,
		in.MaxAddonDomains, in.MaxSubdomains, in.AllowedRuntimes, in.MaxBandwidthMB, in.IOWeight,
		in.MaxProcesses, in.MaxPorts, in.MaxBackups, in.PriceMonthly,
	)
	p, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return p, err
}

func (s *Store) Delete(ctx context.Context, id uuid.UUID) error {
	var isDefault bool
	err := s.Pool.QueryRow(ctx, `SELECT is_default FROM hosting_packages WHERE id = $1`, id).Scan(&isDefault)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if isDefault {
		return ErrDefaultStays
	}
	tag, err := s.Pool.Exec(ctx, `DELETE FROM hosting_packages WHERE id = $1 AND NOT EXISTS (SELECT 1 FROM organizations WHERE package_id = $1)`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInUse
	}
	return nil
}

// Assign sets the package on an organization. Returns the affected websites
// (id + server) so the caller can enqueue quota jobs for existing sites.
func (s *Store) Assign(ctx context.Context, orgID, packageID uuid.UUID) ([]AffectedSite, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE organizations SET package_id = $2 WHERE id = $1`, orgID, packageID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	p, err := s.GetByID(ctx, packageID)
	if err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, server_id FROM websites
		WHERE organization_id = $1 AND status NOT IN ('deleted','deleting')`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AffectedSite
	for rows.Next() {
		var w AffectedSite
		if err := rows.Scan(&w.ID, &w.ServerID); err != nil {
			return nil, err
		}
		w.MaxDiskMB = p.MaxDiskMB
		w.MemoryLimitMB = p.MemoryLimitMB
		w.CPUCores = p.CPUCores
		out = append(out, w)
	}
	return out, rows.Err()
}

type AffectedSite struct {
	ID            uuid.UUID
	ServerID      uuid.UUID
	MaxDiskMB     int
	MemoryLimitMB int
	CPUCores      float64
}

// UsageForOrg counts current consumption.
func (s *Store) UsageForOrg(ctx context.Context, orgID uuid.UUID) (*OrgUsage, error) {
	u := &OrgUsage{}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM websites WHERE organization_id = $1 AND status NOT IN ('deleted','deleting')`, orgID).Scan(&u.Websites); err != nil {
		return nil, err
	}
	// Domain-style counting: the primary domain of each site is either an
	// addon domain (root/registered domain: example.com) or a subdomain
	// (panel.example.com). Aliases count against the same buckets.
	if err := s.Pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE array_length(string_to_array(d.domain, '.'), 1) <= 2),
			count(*) FILTER (WHERE array_length(string_to_array(d.domain, '.'), 1) > 2)
		FROM domains d
		JOIN websites w ON w.id = d.website_id
		WHERE w.organization_id = $1 AND w.status NOT IN ('deleted','deleting')
	`, orgID).Scan(&u.AddonDomains, &u.Subdomains); err != nil {
		u.AddonDomains, u.Subdomains = 0, 0
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM databases WHERE organization_id = $1`, orgID).Scan(&u.Databases); err != nil {
		return nil, err
	}
	if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(SUM((metadata->>'disk_bytes')::bigint), 0) FROM websites WHERE organization_id = $1`, orgID).Scan(&u.DiskUsedMB); err != nil {
		u.DiskUsedMB = 0
	}
	u.DiskUsedMB = 0 // disk accounting lands with the quota op results
	return u, nil
}

// ForOrg resolves the effective package (assigned or default).
func (s *Store) ForOrg(ctx context.Context, orgID uuid.UUID) (*Package, error) {
	var pkgID *uuid.UUID
	err := s.Pool.QueryRow(ctx, `SELECT package_id FROM organizations WHERE id = $1`, orgID).Scan(&pkgID)
	if err != nil {
		return nil, err
	}
	if pkgID != nil {
		return s.GetByID(ctx, *pkgID)
	}
	return s.DefaultPackage(ctx)
}

type QuotaPayload struct {
	WebsiteID     uuid.UUID `json:"website_id"`
	MaxDiskMB     int       `json:"max_disk_mb"`
	MemoryLimitMB int       `json:"memory_limit_mb"`
	CPUCores      float64   `json:"cpu_cores"`
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

var _ = json.Marshal
