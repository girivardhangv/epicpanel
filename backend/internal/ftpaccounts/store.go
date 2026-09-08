package ftpaccounts

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

var (
	ErrNotFound = errors.New("ftp account not found")
	ErrTaken    = errors.New("ftp account label already in use for this website and protocol")
	ErrBadProto = errors.New("protocol must be ftp or sftp")
	ErrBadDir   = errors.New("home_subdir must be a relative path inside the site (no '..' or leading '/')")
	ErrTooMany  = errors.New("ftp account limit reached (10 per protocol per site)")
)

const (
	ProtocolFTP  = "ftp"
	ProtocolSFTP = "sftp"

	maxPerProtocol   = 10
	maxHomeSubdirLen = 100
)

type Status string

const (
	StatusPending Status = "pending"
	StatusActive  Status = "active"
	StatusFailed  Status = "failed"
)

// Account is the API-facing record. The encrypted password is never exposed;
// plaintext is returned exactly once by the create/password endpoints.
type Account struct {
	ID           uuid.UUID `json:"id"`
	Organization uuid.UUID `json:"organization_id"`
	WebsiteID    uuid.UUID `json:"website_id"`
	Protocol     string    `json:"protocol"`
	Label        string    `json:"label"`
	UserName     string    `json:"user_name"`
	HomeSubdir   string    `json:"home_subdir"`
	Status       Status    `json:"status"`
	ErrorMessage string    `json:"error_message,omitempty"`
	Fingerprint  string    `json:"password_fingerprint"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, website_id, protocol, label, user_name, home_subdir, status, error_message, password_fingerprint, created_at, updated_at`

func scanRow(row pgx.Row) (*Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Organization, &a.WebsiteID, &a.Protocol, &a.Label, &a.UserName,
		&a.HomeSubdir, &a.Status, &a.ErrorMessage, &a.Fingerprint, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func ValidProtocol(p string) bool { return p == ProtocolFTP || p == ProtocolSFTP }

// CreatePassword generates a 24-char URL-safe random password.
func CreatePassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

var sanitizeRe = regexp.MustCompile(`[^a-z0-9]+`)

// DeriveUserName builds the system username for an account:
// ep-ftp-<website8>-<sanitized label>, truncated to 32 chars so it always
// satisfies the agent's validUnixUserName rule (ep- prefix, [a-z0-9_-], 2-32).
func DeriveUserName(websiteID uuid.UUID, label string) string {
	sanitized := sanitizeRe.ReplaceAllString(strings.ToLower(label), "-")
	sanitized = strings.Trim(sanitized, "-")
	if sanitized == "" {
		sanitized = "account"
	}
	ws := websiteID.String()
	if len(ws) > 8 {
		ws = ws[:8]
	}
	full := "ep-ftp-" + ws + "-" + sanitized
	if len(full) > 32 {
		full = full[:32]
	}
	return strings.TrimRight(full, "-")
}

// ValidateHomeSubdir normalizes a relative subdirectory under the site root.
// Empty is allowed (chroot = site root). Rejects absolute paths, '..' escape
// attempts and anything longer than 100 chars after cleaning.
func ValidateHomeSubdir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return "", nil
	}
	if strings.HasPrefix(dir, "/") || filepath.IsAbs(dir) {
		return "", ErrBadDir
	}
	for _, part := range strings.Split(dir, "/") {
		if part == ".." {
			return "", ErrBadDir
		}
	}
	clean := filepath.Clean(dir)
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasSuffix(clean, "/..") {
		return "", ErrBadDir
	}
	if len(clean) > maxHomeSubdirLen {
		return "", ErrBadDir
	}
	return clean, nil
}

// HomeDirFor is the canonical chroot/home path the agent must create for an
// account: /srv/epicpanel/websites/<websiteID>[/<home_subdir>].
func HomeDirFor(websiteID uuid.UUID, homeSubdir string) string {
	base := "/srv/epicpanel/websites/" + websiteID.String()
	if homeSubdir == "" {
		return base
	}
	return base + "/" + homeSubdir
}

// Create validates inputs, encrypts the password and inserts the account as
// pending; the sync job flips it to active/failed.
func (s *Store) Create(ctx context.Context, orgID, websiteID uuid.UUID, protocol, label, password, homeSubdir string) (*Account, error) {
	if !ValidProtocol(protocol) {
		return nil, ErrBadProto
	}
	subdir, err := ValidateHomeSubdir(homeSubdir)
	if err != nil {
		return nil, err
	}
	var count int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM ftp_accounts WHERE website_id = $1 AND protocol = $2`, websiteID, protocol,
	).Scan(&count); err != nil {
		return nil, err
	}
	if count >= maxPerProtocol {
		return nil, ErrTooMany
	}
	enc, err := secretbox.Encrypt(password)
	if err != nil {
		return nil, err
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO ftp_accounts (organization_id, website_id, protocol, label, user_name, password_enc, password_fingerprint, home_subdir)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+cols,
		orgID, websiteID, protocol, label, DeriveUserName(websiteID, label), enc, secretbox.Fingerprint(password), subdir,
	)
	a, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrTaken
		}
		return nil, err
	}
	return a, nil
}

// List returns the website's accounts (org-scoped) without credentials.
func (s *Store) List(ctx context.Context, orgID, websiteID uuid.UUID) ([]Account, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT `+cols+` FROM ftp_accounts
		WHERE organization_id = $1 AND website_id = $2
		ORDER BY created_at ASC
	`, orgID, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		a, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, orgID, accountID uuid.UUID) (*Account, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM ftp_accounts WHERE id = $1 AND organization_id = $2`, accountID, orgID)
	a, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) Delete(ctx context.Context, orgID, accountID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM ftp_accounts WHERE id = $1 AND organization_id = $2`, accountID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePassword re-encrypts the credential and refreshes the fingerprint.
func (s *Store) UpdatePassword(ctx context.Context, orgID, accountID uuid.UUID, password string) (*Account, error) {
	enc, err := secretbox.Encrypt(password)
	if err != nil {
		return nil, err
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE ftp_accounts SET password_enc = $3, password_fingerprint = $4, updated_at = now()
		WHERE id = $1 AND organization_id = $2
		RETURNING `+cols,
		accountID, orgID, enc, secretbox.Fingerprint(password),
	)
	a, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

// SetStatus flips the account lifecycle state after sync outcomes.
func (s *Store) SetStatus(ctx context.Context, accountID uuid.UUID, status Status, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE ftp_accounts SET status = $2, error_message = $3, updated_at = now() WHERE id = $1
	`, accountID, status, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkOutcome records a sync result: success activates the account, failure
// keeps it failed with the agent's message.
func (s *Store) MarkOutcome(ctx context.Context, accountID uuid.UUID, success bool, errMsg string) error {
	status := StatusActive
	if !success {
		status = StatusFailed
	}
	return s.SetStatus(ctx, accountID, status, errMsg)
}

// SyncAccount is one account's desired state for the agent. PasswordCrypt is
// a salted one-way SHA-512-crypt hash — never a plaintext or reversible
// secret — so the payload is safe to persist in the jobs feed.
type SyncAccount struct {
	ID            string `json:"id"`
	UserName      string `json:"user_name"`
	Protocol      string `json:"protocol"`
	PasswordCrypt string `json:"password_crypt"`
	HomeDir       string `json:"home_dir"`
}

// SyncPayload is the sync_ftp_accounts job payload (agent contract).
type SyncPayload struct {
	WebsiteID string        `json:"website_id"`
	Accounts  []SyncAccount `json:"accounts"`
}

// DesiredForWebsite builds the full desired account list for a website
// (reconciliation source for the agent-side sync op).
func (s *Store) DesiredForWebsite(ctx context.Context, websiteID uuid.UUID) ([]SyncAccount, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_name, protocol, password_enc, home_subdir
		FROM ftp_accounts WHERE website_id = $1
		ORDER BY created_at ASC
	`, websiteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncAccount
	for rows.Next() {
		var (
			id     uuid.UUID
			name   string
			proto  string
			enc    []byte
			subdir string
		)
		if err := rows.Scan(&id, &name, &proto, &enc, &subdir); err != nil {
			return nil, err
		}
		plain, err := secretbox.Decrypt(enc)
		if err != nil {
			return nil, err
		}
		crypt, err := HashPassword(plain)
		if err != nil {
			return nil, err
		}
		out = append(out, SyncAccount{
			ID:            id.String(),
			UserName:      name,
			Protocol:      proto,
			PasswordCrypt: crypt,
			HomeDir:       HomeDirFor(websiteID, subdir),
		})
	}
	return out, rows.Err()
}

// RevealPassword decrypts the stored credential (caller must audit).
func (s *Store) RevealPassword(ctx context.Context, accountID uuid.UUID) (string, error) {
	var enc []byte
	err := s.Pool.QueryRow(ctx, `SELECT password_enc FROM ftp_accounts WHERE id = $1`, accountID).Scan(&enc)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return secretbox.Decrypt(enc)
}

// WebsiteForAccount resolves the site an account belongs to (org-checked),
// used by account-scoped endpoints that need to re-sync the website.
func (s *Store) WebsiteForAccount(ctx context.Context, orgID, accountID uuid.UUID) (uuid.UUID, error) {
	var websiteID uuid.UUID
	err := s.Pool.QueryRow(ctx,
		`SELECT website_id FROM ftp_accounts WHERE id = $1 AND organization_id = $2`, accountID, orgID,
	).Scan(&websiteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNotFound
	}
	return websiteID, err
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// ApplySyncOutcome is the OnJobFinished fanout sink for sync_ftp_accounts
// jobs: on success every account in the payload becomes active; on failure
// they are marked failed with the job error. No-op for other job types.
func ApplySyncOutcome(ctx context.Context, s *Store, job *jobs.Job, result json.RawMessage) {
	if s == nil || job == nil || job.Type != TypeSyncFTPAccounts || job.WebsiteID == nil {
		return
	}
	var payload SyncPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return
	}
	success := job.Status == jobs.StatusSuccess
	for _, acc := range payload.Accounts {
		id, err := uuid.Parse(acc.ID)
		if err != nil {
			continue
		}
		_ = s.MarkOutcome(ctx, id, success, job.Error)
	}
}
