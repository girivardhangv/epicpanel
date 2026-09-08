package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalidSession = errors.New("invalid or expired session")

const tokenBytes = 32

type Session struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	CreatedAt time.Time
	ExpiresAt time.Time
}

type SessionStore struct {
	Pool *pgxpool.Pool
}

// Create generates a new opaque session token and stores only its hash.
// Returns the raw token (shown once to the client) and the session.
func (s *SessionStore) Create(ctx context.Context, userID uuid.UUID, ttl time.Duration, userAgent, ip string) (string, *Session, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(raw)
	hash := hashToken(token)

	row := s.Pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, token_hash, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, created_at, expires_at
	`, userID, hash, userAgent, ip, time.Now().Add(ttl))

	var sess Session
	if err := row.Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt); err != nil {
		return "", nil, err
	}
	return token, &sess, nil
}

func (s *SessionStore) Get(ctx context.Context, token string) (*Session, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE sessions
		SET last_seen_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING id, user_id, created_at, expires_at
	`, hashToken(token))

	var sess Session
	err := row.Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidSession
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *SessionStore) Revoke(ctx context.Context, token string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE sessions SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, hashToken(token))
	return err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
