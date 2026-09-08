package sshkeys

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound = errors.New("ssh key not found")
	ErrBadKey   = errors.New("invalid SSH public key format")
	ErrTooMany  = errors.New("ssh key limit reached (10 per site)")
)

const maxKeysPerSite = 10

type SSHKey struct {
	ID           uuid.UUID `json:"id"`
	UserID       uuid.UUID `json:"user_id"`
	Organization uuid.UUID `json:"organization_id"`
	WebsiteID    uuid.UUID `json:"website_id"`
	Name         string    `json:"name"`
	PublicKey    string    `json:"public_key"`
	Fingerprint  string    `json:"fingerprint"`
	AddedAt      time.Time `json:"added_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, user_id, organization_id, website_id, name, public_key, fingerprint, added_at`

func scanRow(row pgx.Row) (*SSHKey, error) {
	var k SSHKey
	err := row.Scan(&k.ID, &k.UserID, &k.Organization, &k.WebsiteID, &k.Name, &k.PublicKey, &k.Fingerprint, &k.AddedAt)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// ParseAndFingerprint validates an OpenSSH public key line
// ("ssh-ed25519 AAAA... comment") and returns (normalized, fingerprint).
// It accepts ssh-ed25519, rsa, ecdsa types; the actual cryptographic
// validation happens when sshd uses the key — here we enforce format sanity.
func ParseAndFingerprint(raw string) (string, string, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, "\n", " "))
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return "", "", ErrBadKey
	}
	typ := fields[0]
	validTypes := map[string]bool{
		"ssh-ed25519": true, "ssh-rsa": true,
		"ecdsa-sha2-nistp256": true, "ecdsa-sha2-nistp384": true, "ecdsa-sha2-nistp521": true,
		"sk-ssh-ed25519@openssh.com": true, "sk-ecdsa-sha2-nistp256@openssh.com": true,
	}
	if !validTypes[typ] {
		return "", "", ErrBadKey
	}
	b64 := fields[1]
	if len(b64) < 40 {
		return "", "", ErrBadKey
	}
	keyBlob, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", "", ErrBadKey
	}
	// Fingerprint: SHA256 like ssh-keygen -lf
	sum := sha256.Sum256(keyBlob)
	fp := "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
	// Normalize: type + key (+ optional comment)
	normalized := typ + " " + b64
	if len(fields) >= 3 {
		normalized += " " + strings.Join(fields[2:], " ")
	}
	if len(normalized) > 4000 {
		return "", "", fmt.Errorf("key too long")
	}
	return normalized, fp, nil
}

func (s *Store) Create(ctx context.Context, userID, orgID, websiteID uuid.UUID, name, publicKey, fingerprint string) (*SSHKey, error) {
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM ssh_keys WHERE website_id = $1`, websiteID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxKeysPerSite {
		return nil, ErrTooMany
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO ssh_keys (user_id, organization_id, website_id, name, public_key, fingerprint)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+cols,
		userID, orgID, websiteID, name, publicKey, fingerprint,
	)
	k, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, errors.New("this key is already added to the site")
		}
		return nil, err
	}
	return k, nil
}

func (s *Store) ListForWebsite(ctx context.Context, websiteID uuid.UUID) ([]SSHKey, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM ssh_keys WHERE website_id = $1 ORDER BY added_at ASC`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SSHKey
	for rows.Next() {
		k, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *k)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, orgID, keyID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM ssh_keys WHERE id = $1 AND organization_id = $2`, keyID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// KeysForWebsite returns authorized_keys lines for a site.
func (s *Store) KeysForWebsite(ctx context.Context, websiteID uuid.UUID) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT public_key FROM ssh_keys WHERE website_id = $1`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
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

// WebsiteForKey resolves the site a key belongs to (org-checked).
func (s *Store) WebsiteForKey(ctx context.Context, orgID, keyID uuid.UUID) (uuid.UUID, error) {
	var websiteID uuid.UUID
	err := s.Pool.QueryRow(ctx, `SELECT website_id FROM ssh_keys WHERE id = $1 AND organization_id = $2`, keyID, orgID).Scan(&websiteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return websiteID, err
}
