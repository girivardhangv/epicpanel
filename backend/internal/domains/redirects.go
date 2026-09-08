package domains

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var redirectDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// normalizeDomain lowercases, trims and strips a trailing dot.
func normalizeDomain(in string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(in)), ".")
}

// isHostname checks the RFC-1123-ish hostname shape (mirrors domains domainRe).
func isHostname(s string) bool {
	return len(s) <= 253 && redirectDomainRe.MatchString(s)
}

var (
	ErrRedirectNotFound = errors.New("redirect not found")
	ErrRedirectTaken    = errors.New("a redirect for this source domain already exists")
	ErrRedirectTooMany  = errors.New("redirect limit reached (20 per website)")
)

const maxRedirectsPerWebsite = 20

// Redirect is one domain -> URL forwarding rule rendered into the vhost.
type Redirect struct {
	ID             uuid.UUID `json:"id"`
	WebsiteID      uuid.UUID `json:"website_id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	FromDomain     string    `json:"from_domain"`
	ToURL          string    `json:"to_url"`
	StatusCode     int       `json:"status_code"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// RedirectTarget is the wire shape inside the agent payload
// (redirects:[{from,to,status}]).
type RedirectTarget struct {
	Domain string `json:"from"`
	To     string `json:"to"`
	Status int    `json:"status"`
}

const redirectCols = `id, website_id, organization_id, from_domain, to_url, status_code, enabled, created_at, updated_at`

func scanRedirect(row pgx.Row) (*Redirect, error) {
	var r Redirect
	err := row.Scan(&r.ID, &r.WebsiteID, &r.OrganizationID, &r.FromDomain, &r.ToURL, &r.StatusCode, &r.Enabled, &r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ValidateRedirectToURL requires an absolute http(s) URL with a host.
func ValidateRedirectToURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("to_url is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("to_url must use the http or https scheme")
	}
	if u.Host == "" {
		return errors.New("to_url must be an absolute URL with a host")
	}
	return nil
}

// CreateRedirect validates and stores a redirect. The source must be a valid
// hostname attached to THIS website (or its primary domain).
func (s *Store) CreateRedirect(ctx context.Context, orgID, websiteID uuid.UUID, from, toURL string, statusCode int) (*Redirect, error) {
	from = normalizeDomain(from)
	if !isHostname(from) {
		return nil, errors.New("from_domain is not a valid hostname")
	}
	if statusCode == 0 {
		statusCode = 301
	}
	if statusCode != 301 && statusCode != 302 && statusCode != 307 && statusCode != 308 {
		return nil, errors.New("status_code must be one of 301, 302, 307, 308")
	}
	if err := ValidateRedirectToURL(toURL); err != nil {
		return nil, err
	}
	owned, err := s.domainOwnedByWebsite(ctx, websiteID, from)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, errors.New("from_domain must be attached to this website")
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM domain_redirects WHERE website_id = $1`, websiteID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxRedirectsPerWebsite {
		return nil, ErrRedirectTooMany
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO domain_redirects (website_id, organization_id, from_domain, to_url, status_code)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+redirectCols,
		websiteID, orgID, from, toURL, statusCode,
	)
	r, err := scanRedirect(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrRedirectTaken
		}
		return nil, err
	}
	return r, nil
}

// ListRedirects returns a website's redirects, org-scoped.
func (s *Store) ListRedirects(ctx context.Context, orgID, websiteID uuid.UUID) ([]Redirect, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+redirectCols+` FROM domain_redirects WHERE website_id = $1 AND organization_id = $2 ORDER BY created_at ASC`, websiteID, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Redirect
	for rows.Next() {
		r, err := scanRedirect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// ListForWebsiteUnscoped returns a website's redirects without org scoping
// (internal reconcile payload / fanout use only).
func (s *Store) ListForWebsiteUnscoped(ctx context.Context, websiteID uuid.UUID) ([]Redirect, error) {
	return s.listRedirectsAny(ctx, websiteID)
}

// RedirectsForWebsite returns the full redirect rows of a website without
// org scoping (exported for the reconcile payload builder).
func (s *Store) RedirectsForWebsite(ctx context.Context, websiteID uuid.UUID) ([]Redirect, error) {
	return s.listRedirectsAny(ctx, websiteID)
}

// RedirectTargetsForWebsite returns only ENABLED redirects in the agent wire
// shape [{from,to,status}] for the DesiredPayload "redirects" field.
func (s *Store) RedirectTargetsForWebsite(ctx context.Context, websiteID uuid.UUID) ([]RedirectTarget, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT from_domain, to_url, status_code FROM domain_redirects
		WHERE website_id = $1 AND enabled = TRUE
		ORDER BY created_at ASC
	`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RedirectTarget
	for rows.Next() {
		var t RedirectTarget
		if err := rows.Scan(&t.Domain, &t.To, &t.Status); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) listRedirectsAny(ctx context.Context, websiteID uuid.UUID) ([]Redirect, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+redirectCols+` FROM domain_redirects WHERE website_id = $1 ORDER BY created_at ASC`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Redirect
	for rows.Next() {
		r, err := scanRedirect(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// GetRedirect resolves a redirect, org-scoped.
func (s *Store) GetRedirect(ctx context.Context, orgID, redirectID uuid.UUID) (*Redirect, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+redirectCols+` FROM domain_redirects WHERE id = $1 AND organization_id = $2`, redirectID, orgID)
	r, err := scanRedirect(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRedirectNotFound
	}
	return r, err
}

// UpdateRedirectPatch carries the optional PATCH fields (nil = unchanged).
type UpdateRedirectPatch struct {
	ToURL      *string
	StatusCode *int
	Enabled    *bool
}

// UpdateRedirect applies to_url/status_code/enabled changes.
func (s *Store) UpdateRedirect(ctx context.Context, r *Redirect, patch UpdateRedirectPatch) (*Redirect, error) {
	toURL := r.ToURL
	if patch.ToURL != nil {
		if err := ValidateRedirectToURL(*patch.ToURL); err != nil {
			return nil, err
		}
		toURL = *patch.ToURL
	}
	statusCode := r.StatusCode
	if patch.StatusCode != nil {
		if *patch.StatusCode != 301 && *patch.StatusCode != 302 && *patch.StatusCode != 307 && *patch.StatusCode != 308 {
			return nil, errors.New("status_code must be one of 301, 302, 307, 308")
		}
		statusCode = *patch.StatusCode
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE domain_redirects SET
			to_url = $2,
			status_code = $3,
			enabled = COALESCE($4, enabled),
			updated_at = now()
		WHERE id = $1
		RETURNING `+redirectCols,
		r.ID, toURL, statusCode, patch.Enabled,
	)
	return scanRedirect(row)
}

// DeleteRedirect removes a redirect, org-scoped.
func (s *Store) DeleteRedirect(ctx context.Context, orgID, redirectID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM domain_redirects WHERE id = $1 AND organization_id = $2`, redirectID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrRedirectNotFound
	}
	return nil
}

// domainOwnedByWebsite reports whether the domain is attached to the website
// (domains table) or equals the website's primary domain.
func (s *Store) domainOwnedByWebsite(ctx context.Context, websiteID uuid.UUID, domain string) (bool, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*) FROM domains
		WHERE website_id = $1 AND (domain = $2 OR $2 = (SELECT primary_domain FROM websites WHERE id = $1))
	`, websiteID, domain).Scan(&n)
	return n > 0, err
}

// DomainBelongsToWebsite is the exported DomainChecker for the dns package:
// does this domain belong to the website (attached or primary)?
func (s *Store) DomainBelongsToWebsite(ctx context.Context, websiteID uuid.UUID, domain string) (bool, error) {
	return s.domainOwnedByWebsite(ctx, websiteID, domain)
}
