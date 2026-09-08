package domains

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

var (
	ErrNotFound   = errors.New("domain not found")
	ErrTaken      = errors.New("domain is already in use by another website")
	ErrPrimaryCap = errors.New("website already has a primary domain")
	ErrBadSSLOp   = errors.New("certificate operation not allowed in current state")
)

type Kind string

const (
	KindPrimary Kind = "primary"
	KindAlias   Kind = "alias"
)

type SSLMode string

const (
	SSLNone       SSLMode = "none"
	SSLSelfSigned SSLMode = "selfsigned"
	SSLLetsEnc    SSLMode = "letsencrypt"
)

type SSLState string

const (
	SSLStatePending SSLState = "pending"
	SSLStateIssuing SSLState = "issuing"
	SSLStateActive  SSLState = "active"
	SSLStateFailed  SSLState = "failed"
)

type Domain struct {
	ID            uuid.UUID  `json:"id"`
	Organization  uuid.UUID  `json:"organization_id"`
	WebsiteID     uuid.UUID  `json:"website_id"`
	Domain        string     `json:"domain"`
	Kind          Kind       `json:"kind"`
	SSLMode       SSLMode    `json:"ssl_mode"`
	SSLState      SSLState   `json:"ssl_state"`
	SSLIssuedAt   *time.Time `json:"ssl_issued_at,omitempty"`
	SSLExpiresAt  *time.Time `json:"ssl_expires_at,omitempty"`
	SSLError      string     `json:"ssl_error,omitempty"`
	DNSVerifiedAt *time.Time `json:"dns_verified_at,omitempty"`
	DocrootSuffix string     `json:"docroot_suffix,omitempty"`
	DNSMatches    *bool      `json:"dns_points_to_server,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, website_id, domain, kind, ssl_mode, ssl_state, ssl_issued_at, ssl_expires_at, ssl_error, dns_verified_at, dns_points_to_server, docroot_suffix, created_at`

func scanRow(row pgx.Row) (*Domain, error) {
	var d Domain
	err := row.Scan(&d.ID, &d.Organization, &d.WebsiteID, &d.Domain, &d.Kind, &d.SSLMode, &d.SSLState,
		&d.SSLIssuedAt, &d.SSLExpiresAt, &d.SSLError, &d.DNSVerifiedAt, &d.DNSMatches, &d.DocrootSuffix, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Create attaches a domain to a website. Domain uniqueness is GLOBAL: one
// domain can only ever belong to one website in the whole panel.
// SetDocrootSuffix persists a per-domain docroot override (relative path
// under the site tree; empty = the site's default running directory).
func (s *Store) SetDocrootSuffix(ctx context.Context, domainID uuid.UUID, suffix string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE domains SET docroot_suffix = $2, updated_at = now() WHERE id = $1`, domainID, suffix)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Create(ctx context.Context, orgID, websiteID uuid.UUID, domain string, kind Kind) (*Domain, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO domains (organization_id, website_id, domain, kind)
		VALUES ($1, $2, $3, $4)
		RETURNING `+cols,
		orgID, websiteID, domain, kind,
	)
	d, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTaken
		}
		return nil, err
	}
	return d, nil
}

func (s *Store) GetByID(ctx context.Context, orgID, domainID uuid.UUID) (*Domain, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM domains WHERE id = $1 AND organization_id = $2`, domainID, orgID)
	d, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// GetByIDAny resolves a domain without org scoping (internal job fanout only).
func (s *Store) GetByIDAny(ctx context.Context, domainID uuid.UUID) (*Domain, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM domains WHERE id = $1`, domainID)
	d, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return d, err
}

// ListForWebsite returns primary + aliases ordered primary-first.
func (s *Store) ListForWebsite(ctx context.Context, websiteID uuid.UUID) ([]Domain, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM domains WHERE website_id = $1 ORDER BY kind = 'primary' DESC, created_at ASC`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		d, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ListForOrganization returns all domains across the organization's websites
// (SSL dashboard view), primary domains first within each website.
func (s *Store) ListForOrganization(ctx context.Context, orgID uuid.UUID) ([]Domain, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM domains WHERE organization_id = $1 ORDER BY website_id, kind = 'primary' DESC, created_at ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		d, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, orgID, domainID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM domains WHERE id = $1 AND organization_id = $2 AND kind = 'alias'`, domainID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSSLMode transitions a domain's SSL configuration; resets state machine.
// Flipping active -> issuing emits "ssl.renewal_started" (the renewal
// scheduler uses this call; the API never sets letsencrypt/selfsigned while
// already active except through that same re-issue path).
func (s *Store) SetSSLMode(ctx context.Context, domainID uuid.UUID, mode SSLMode) (*Domain, error) {
	var state SSLState
	switch mode {
	case SSLNone:
		state = SSLStatePending
	case SSLSelfSigned, SSLLetsEnc:
		state = SSLStateIssuing
	}
	prev, err := s.GetByIDAny(ctx, domainID)
	if err != nil {
		return nil, err
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE domains SET ssl_mode = $2, ssl_state = $3, ssl_error = '', updated_at = now()
		WHERE id = $1
		RETURNING `+cols,
		domainID, mode, state,
	)
	d, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	if prev.SSLState == SSLStateActive && state == SSLStateIssuing {
		emitSSLEvent("ssl.renewal_started", d.Domain, map[string]any{"domain": d.Domain, "ssl_mode": string(d.SSLMode)})
	}
	return d, nil
}

// MarkSSLActive records a successful issuance with expiry.
func (s *Store) MarkSSLActive(ctx context.Context, domainID uuid.UUID, expiresAt time.Time) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE domains SET ssl_state = 'active', ssl_issued_at = now(), ssl_expires_at = $2, ssl_error = '', updated_at = now()
		WHERE id = $1
	`, domainID, expiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkSSLFailed(ctx context.Context, domainID uuid.UUID, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE domains SET ssl_state = 'failed', ssl_error = $2, updated_at = now() WHERE id = $1
	`, domainID, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkDNSVerified records DNS verification results.
func (s *Store) MarkDNSVerified(ctx context.Context, domainID uuid.UUID, matches bool) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE domains SET dns_verified_at = now(), dns_points_to_server = $2, updated_at = now() WHERE id = $1
	`, domainID, matches)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ExpiringForRenewal returns letsencrypt domains (active) whose certs expire
// within the given horizon, for the renewal scheduler.
func (s *Store) ExpiringForRenewal(ctx context.Context, horizon time.Duration, limit int) ([]Domain, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+cols+` FROM domains
		WHERE ssl_mode = 'letsencrypt' AND ssl_state = 'active'
		  AND ssl_expires_at IS NOT NULL AND ssl_expires_at < now() + $1::interval
		ORDER BY ssl_expires_at ASC
		LIMIT $2
	`, horizon, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Domain
	for rows.Next() {
		d, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// CountByWebsite is used to cap aliases per website.
func (s *Store) CountByWebsite(ctx context.Context, websiteID uuid.UUID) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM domains WHERE website_id = $1`, websiteID).Scan(&n)
	return n, err
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// ServingDomain mirrors websites.DomainServing without an import cycle.
type ServingDomain struct {
	Domain        string
	SSLMode       string
	CertPath      string
	KeyPath       string
	DocrootSuffix string
}

// ListForWebsiteServing returns the serving list for vhost rendering; cert
// paths are only included for domains with an ACTIVE certificate on disk.
func (s *Store) ListForWebsiteServing(ctx context.Context, websiteID uuid.UUID) ([]ServingDomain, error) {
	list, err := s.ListForWebsite(ctx, websiteID)
	if err != nil {
		return nil, err
	}
	out := make([]ServingDomain, 0, len(list))
	for _, d := range list {
		sd := ServingDomain{Domain: d.Domain, SSLMode: string(d.SSLMode), DocrootSuffix: d.DocrootSuffix}
		if d.SSLState == SSLStateActive && d.SSLMode != SSLNone {
			sd.CertPath = "/etc/epicpanel/ssl/" + d.Domain + "/fullchain.pem"
			sd.KeyPath = "/etc/epicpanel/ssl/" + d.Domain + "/privkey.pem"
		}
		out = append(out, sd)
	}
	return out, nil
}

// certJobPayload matches the issue_certificate job payload.
type certJobPayload struct {
	DomainID string `json:"domain_id"`
	Domain   string `json:"domain"`
	Mode     string `json:"mode"`
}

// dnsJobPayload matches the verify_domain job payload.
type dnsJobPayload struct {
	DomainID string `json:"domain_id"`
	Domain   string `json:"domain"`
}

// ApplyJobOutcome advances the SSL/DNS state machines for finished domain jobs.
// Returns the websiteID whose serving config changed (vhost reconcile), if any.
func (s *Store) ApplyJobOutcome(ctx context.Context, job *jobs.Job, result json.RawMessage) uuid.UUID {
	switch job.Type {
	case jobs.TypeIssueCertificate:
		var p certJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return uuid.Nil
		}
		domainID, err := uuid.Parse(p.DomainID)
		if err != nil {
			return uuid.Nil
		}
		if job.Status == jobs.StatusSuccess {
			var outcome struct {
				NotAfter time.Time `json:"not_after"`
			}
			_ = json.Unmarshal(result, &outcome)
			if err := s.MarkSSLActive(ctx, domainID, outcome.NotAfter); err != nil && err != ErrNotFound {
				slog.Error("domain mark ssl active failed", "domain", domainID, "err", err)
				return uuid.Nil
			}
			d, err := s.GetByIDAny(ctx, domainID)
			if err != nil {
				slog.Error("domain lookup after activation failed", "domain", domainID, "err", err)
				return uuid.Nil
			}
			emitSSLEvent("ssl.issued", d.Domain, map[string]any{"domain": d.Domain, "expires_at": outcome.NotAfter})
			return d.WebsiteID
		} else if job.Status == jobs.StatusFailed {
			if err := s.MarkSSLFailed(ctx, domainID, job.Error); err != nil && err != ErrNotFound {
				slog.Error("domain mark ssl failed", "domain", domainID, "err", err)
			}
			emitSSLEvent("ssl.failed", p.Domain, map[string]any{"domain": p.Domain, "error": job.Error})
			return uuid.Nil
		}
	case jobs.TypeVerifyDomain:
		var p dnsJobPayload
		if err := json.Unmarshal(job.Payload, &p); err != nil {
			return uuid.Nil
		}
		domainID, err := uuid.Parse(p.DomainID)
		if err != nil {
			return uuid.Nil
		}
		if job.Status == jobs.StatusSuccess {
			var outcome struct {
				Matches bool `json:"matches"`
			}
			_ = json.Unmarshal(result, &outcome)
			if err := s.MarkDNSVerified(ctx, domainID, outcome.Matches); err != nil && err != ErrNotFound {
				slog.Error("domain mark dns verified failed", "domain", domainID, "err", err)
			}
		}
	}
	return uuid.Nil
}
