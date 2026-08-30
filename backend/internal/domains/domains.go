// Package domains implements domain name management for websites.
//
// Validation is whitelist-based: a normalized domain may only contain
// ASCII letters, digits, hyphens and dots (plus a single leftmost wildcard
// where explicitly allowed). This inherently rejects path traversal
// ("../"), shell metacharacters (";", "|", "$(...)"), whitespace and any
// character that could poison an Nginx server_name directive.
package domains

import (
	"context"
	"errors"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Domain types.
const (
	TypePrimary   = "primary"
	TypeAlias     = "alias"
	TypeSubdomain = "subdomain"
)

// Normalize lowercases, trims and removes a single trailing dot. It does not
// judge whether the result is a valid domain — call Validate for that.
func Normalize(input string) string {
	d := strings.ToLower(strings.TrimSpace(input))
	d = strings.TrimSuffix(d, ".")
	return d
}

// Validate checks a normalized domain against the panel's hosting rules.
// allowWildcard permits exactly one leftmost wildcard label ("*.example.com").
func Validate(domain string, allowWildcard bool) error {
	if domain == "" {
		return errors.New("domain is required")
	}
	if len(domain) > 253 {
		return errors.New("domain exceeds the maximum length of 253 characters")
	}
	for _, r := range domain {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
		case r == '*':
			if !allowWildcard {
				return errors.New("wildcard domains are only allowed as aliases")
			}
		default:
			// Explicitly name the common dangerous classes in the error even
			// though the whitelist already rejects them.
			return errors.New("domain contains characters that are not allowed (letters, digits, dots and hyphens only)")
		}
	}

	wildcard := strings.HasPrefix(domain, "*.")
	body := strings.TrimPrefix(domain, "*.")
	if strings.Contains(domain, "*") && !wildcard {
		return errors.New("a wildcard is only allowed as the leftmost label (e.g. *.example.com)")
	}
	if strings.Contains(body, "*") {
		return errors.New("domain contains a wildcard in an invalid position")
	}

	labels := strings.Split(body, ".")
	if len(labels) < 2 {
		return errors.New("domain must include a top-level domain (e.g. example.com)")
	}
	for _, label := range labels {
		if label == "" {
			return errors.New("domain contains an empty label (check for consecutive dots)")
		}
		if len(label) > 63 {
			return errors.New("domain label exceeds 63 characters")
		}
		if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("domain labels may not start or end with a hyphen")
		}
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 {
		return errors.New("domain top-level domain is too short")
	}
	for _, r := range tld {
		if (r < 'a' || r > 'z') && r != '-' && (r < '0' || r > '9') {
			return errors.New("domain top-level domain contains invalid characters")
		}
	}
	return nil
}

// ValidateAndNormalize normalizes then validates user input.
func ValidateAndNormalize(input string, allowWildcard bool) (string, error) {
	d := Normalize(input)
	if err := Validate(d, allowWildcard); err != nil {
		return "", apierror.BadRequest(err.Error())
	}
	return d, nil
}

// Service provides domain persistence on top of PostgreSQL.
type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool} }

// Domain is the API view of a stored domain.
type Domain struct {
	ID        string     `json:"id"`
	ServerID  string     `json:"server_id"`
	Domain    string     `json:"domain"`
	Type      string     `json:"type"`
	Status    string     `json:"status"`
	WebsiteID *string    `json:"website_id"` // set when the domain is attached to a website (alias)
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
	Website   *string    `json:"website_name,omitempty"` // joined for convenience in listings
}

const domainCols = `d.id, d.server_id, d.domain, d.type, d.status, d.website_id,
	d.created_at::text, d.updated_at::text, w.name`

func (s *Service) scanDomain(row pgx.Row) (*Domain, error) {
	var d Domain
	err := row.Scan(&d.ID, &d.ServerID, &d.Domain, &d.Type, &d.Status, &d.WebsiteID,
		&d.CreatedAt, &d.UpdatedAt, &d.Website)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// Create inserts a validated domain for an existing, non-revoked server.
func (s *Service) Create(ctx context.Context, serverID, rawDomain, domainType string) (*Domain, error) {
	allowWildcard := domainType == TypeAlias
	normalized, err := ValidateAndNormalize(rawDomain, allowWildcard)
	if err != nil {
		return nil, err
	}
	switch domainType {
	case TypePrimary, TypeAlias, TypeSubdomain:
	default:
		return nil, apierror.BadRequest("domain type must be primary, alias or subdomain")
	}

	d, err := s.scanDomain(s.pool.QueryRow(ctx, `
		INSERT INTO domains (server_id, domain, type)
		VALUES ($1, $2, $3)
		RETURNING id, server_id, domain, type, status, website_id,
			created_at::text, updated_at::text, NULL::text`, serverID, normalized, domainType))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, apierror.Conflict("this domain is already registered on the server")
		}
		if isForeignKeyViolation(err) {
			return nil, apierror.NotFound("server")
		}
		return nil, apierror.From(err)
	}
	return d, nil
}

// List returns domains, optionally filtered by server, newest first.
func (s *Service) List(ctx context.Context, serverID string) ([]Domain, error) {
	q := `SELECT ` + domainCols + ` FROM domains d
	      LEFT JOIN websites w ON w.id = d.website_id`
	args := []any{}
	if serverID != "" {
		q += ` WHERE d.server_id = $1`
		args = append(args, serverID)
	}
	q += ` ORDER BY d.created_at DESC`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.ServerID, &d.Domain, &d.Type, &d.Status, &d.WebsiteID,
			&d.CreatedAt, &d.UpdatedAt, &d.Website); err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Get fetches a single domain.
func (s *Service) Get(ctx context.Context, id string) (*Domain, error) {
	d, err := s.scanDomain(s.pool.QueryRow(ctx,
		`SELECT `+domainCols+` FROM domains d
		 LEFT JOIN websites w ON w.id = d.website_id WHERE d.id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("domain")
		}
		return nil, apierror.From(err)
	}
	return d, nil
}

// Delete removes a domain. Domains still attached to a website are refused.
func (s *Service) Delete(ctx context.Context, id string) error {
	d, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if d.WebsiteID != nil {
		name := d.Domain
		if d.Website != nil && *d.Website != "" {
			name = *d.Website
		}
		return apierror.Conflict("domain is attached to website " + name + "; detach it first")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM domains WHERE id = $1`, id)
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.NotFound("domain")
	}
	return nil
}

// AttachToWebsite / Detach are used by the websites service when aliases are
// assigned. They assume ownership checks already happened upstream.
func (s *Service) AttachToWebsite(ctx context.Context, domainID, websiteID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE domains SET website_id = $2, updated_at = now() WHERE id = $1`, domainID, websiteID)
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.NotFound("domain")
	}
	return nil
}

func (s *Service) DetachAllFromWebsite(ctx context.Context, websiteID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE domains SET website_id = NULL, updated_at = now() WHERE website_id = $1`, websiteID)
	if err != nil {
		return apierror.From(err)
	}
	return nil
}
