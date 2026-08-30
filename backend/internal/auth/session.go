// Session management for the panel API.
//
// Design:
//   - The browser holds an opaque 256-bit token in an httpOnly cookie.
//   - Only a SHA-256 digest of the token is stored server-side.
//   - Every session carries its own CSRF token delivered in a second
//     non-httpOnly cookie; mutating requests must echo it via X-CSRF-Token.
//   - Expiry is enforced from the database so logout/revocation is immediate.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	SessionCookieName = "epicpanel_session"
	CSRFCookieName    = "epicpanel_csrf"
	CSRFHeaderName    = "X-CSRF-Token"
)

type Identity struct {
	UserID      string   `json:"user_id"`
	Username    string   `json:"username"`
	Email       *string  `json:"email"`
	DisplayName string   `json:"display_name"`
	SessionID   string   `json:"-"`
	CSRFToken   string   `json:"-"`
	Permissions []string `json:"-"`
}

type Store struct {
	pool    *pgxpool.Pool
	lifetime time.Duration
}

func NewStore(pool *pgxpool.Pool, lifetime time.Duration) *Store {
	if lifetime <= 0 {
		lifetime = 24 * time.Hour
	}
	return &Store{pool: pool, lifetime: lifetime}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create opens a new session using the store's default lifetime.
func (s *Store) Create(ctx context.Context, userID, ip, userAgent string) (*Identity, string, error) {
	return s.CreateTTL(ctx, userID, ip, userAgent, s.lifetime)
}

// CreateTTL opens a new session whose lifetime is decided at call time
// (e.g. operator-configurable session length).
func (s *Store) CreateTTL(ctx context.Context, userID, ip, userAgent string, ttl time.Duration) (*Identity, string, error) {
	if ttl <= 0 {
		ttl = s.lifetime
	}
	token, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	expires := time.Now().Add(ttl)

	var id Identity
	var email any
	err = s.pool.QueryRow(ctx, `
		WITH ins AS (
			INSERT INTO sessions (user_id, token_hash, csrf_token, ip_address, user_agent, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id, csrf_token
		), u AS (
			SELECT username, email FROM users WHERE id = $1
		)
		SELECT ins.id, ins.csrf_token, u.username, u.email FROM ins CROSS JOIN u`,
		userID, hashToken(token), csrf, ip, userAgent, expires,
	).Scan(&id.SessionID, &id.CSRFToken, &id.Username, &email)
	if err != nil {
		return nil, "", err
	}
	id.UserID = userID
	if str, ok := email.(string); ok && str != "" {
		id.Email = &str
	}
	return &id, token, nil
}

// Resolve validates a raw session token and returns the live identity with
// fresh permissions. Expired or revoked sessions are treated uniformly.
func (s *Store) Resolve(ctx context.Context, token string, permissions []string) (*Identity, error) {
	if token == "" {
		return nil, ErrNoSession
	}
	var id Identity
	var email any
	var expiresAt time.Time
	var revokedAt *time.Time
	var isActive bool
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.csrf_token, s.expires_at, s.revoked_at, u.id, u.username, u.email, u.is_active
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, hashToken(token),
	).Scan(&id.SessionID, &id.CSRFToken, &expiresAt, &revokedAt, &id.UserID, &id.Username, &email, &isActive)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		return nil, ErrNoSession // row-not-found maps to the same unauthenticated path
	}
	if revokedAt != nil || time.Now().After(expiresAt) || !isActive {
		return nil, ErrSessionExpired
	}
	if strings.Compare(id.UserID, "") == 0 {
		return nil, ErrNoSession
	}
	if str, ok := email.(string); ok && str != "" {
		id.Email = &str
	}
	id.Permissions = permissions
	return &id, nil
}

// Touch updates last_seen_at; best-effort.
func (s *Store) Touch(ctx context.Context, sessionID string) {
	_, _ = s.pool.Exec(ctx, `UPDATE sessions SET last_seen_at = now() WHERE id = $1`, sessionID)
}

// Refresh rotates the token of an active session (sliding sessions).
// The old token stops working immediately. Returns the new raw token.
func (s *Store) Refresh(ctx context.Context, rawToken string, extendTo time.Time) (*Identity, string, error) {
	newToken, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, "", err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET token_hash = $2, csrf_token = $3, expires_at = $4, last_seen_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()`,
		hashToken(rawToken), hashToken(newToken), csrf, extendTo)
	if err != nil {
		return nil, "", err
	}
	if tag.RowsAffected() == 0 {
		return nil, "", ErrSessionExpired
	}
	var id Identity
	var email any
	err = s.pool.QueryRow(ctx, `
		SELECT s.id, s.csrf_token, u.id, u.username, u.email FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1`, hashToken(newToken)).Scan(&id.SessionID, &id.CSRFToken, &id.UserID, &id.Username, &email)
	if err != nil {
		return nil, "", err
	}
	if str, ok := email.(string); ok && str != "" {
		id.Email = &str
	}
	id.Permissions = []string{}
	return &id, newToken, nil
}

// Revoke kills the session holding rawToken.
func (s *Store) Revoke(ctx context.Context, rawToken, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = NULLIF($2,'')
		WHERE token_hash = $1 AND revoked_at IS NULL`, hashToken(rawToken), reason)
	return err
}

// RevokeAllForUser implements "log out everywhere" semantics; optionally the
// current session can be preserved by passing its ID.
func (s *Store) RevokeAllForUser(ctx context.Context, userID uuid.UUID, exceptSessionID, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now(), revoked_reason = NULLIF($3,'')
		WHERE user_id = $1 AND revoked_at IS NULL AND ($2::uuid IS NULL OR id <> $2::uuid)`,
		userID, nullableID(exceptSessionID), reason)
	return err
}

func nullableID(s string) any {
	if s == "" {
		return nil
	}
	return s
}

var (
	ErrNoSession     = errors.New("auth: no valid session")
	ErrSessionExpired = errors.New("auth: session expired")
)

// ---------------------------------------------------------------------------
// HTTP glue

type CookieOpts struct {
	Secure bool
	Path   string
}

func setCookie(w http.ResponseWriter, name, value string, maxAge int, opts CookieOpts) http.Cookie {
	path := opts.Path
	if path == "" {
		path = "/" // site-wide, otherwise browsers scope it to the request directory
	}
	c := http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   maxAge,
		HttpOnly: name == SessionCookieName,
		Secure:   opts.Secure,
		SameSite: http.SameSiteLaxMode,
	}
	http.SetCookie(w, &c)
	return c
}

// WriteAuthCookies sets both session and CSRF cookies on the response.
func WriteAuthCookies(w http.ResponseWriter, token, csrf string, lifetime int, opts CookieOpts) {
	setCookie(w, SessionCookieName, token, lifetime, opts)
	setCookie(w, CSRFCookieName, csrf, lifetime, opts)
}

// ClearAuthCookies invalidates cookies client-side.
func ClearAuthCookies(w http.ResponseWriter, opts CookieOpts) {
	setCookie(w, SessionCookieName, "", -1, opts)
	setCookie(w, CSRFCookieName, "", -1, opts)
}

// TokenFromRequest extracts the session token from the cookie jar.
func TokenFromRequest(r *http.Request) string {
	c, err := r.Cookie(SessionCookieName)
	if err != nil || c.Value == "" {
		return ""
	}
	return c.Value
}

// CheckCSRF validates that the X-CSRF-Token header matches the session's
// stored CSRF token. Compare runs in constant time.
func CheckCSRF(r *http.Request, sess *Identity) bool {
	got := r.Header.Get(CSRFHeaderName)
	if got == "" {
		cookie, err := r.Cookie(CSRFCookieName)
		if err != nil {
			return false
		}
		got = cookie.Value
	}
	if got == "" || len(got) > 512 {
		return false
	}
	return hmac.Equal([]byte(got), []byte(sess.CSRFToken))
}

// RequireCSRF wraps unsafe methods with CSRF validation.
func RequireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			v, _ := r.Context().Value(ctxIdentity).(*Identity)
			if v == nil || !CheckCSRF(r, v) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"CSRF_TOKEN_INVALID","message":"Missing or invalid CSRF token"}}`))
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
