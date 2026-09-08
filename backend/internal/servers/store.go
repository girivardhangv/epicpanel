package servers

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

var (
	ErrNotFound       = errors.New("server not found")
	ErrNameTaken      = errors.New("a server with that name already exists in this organization")
	ErrTokenInvalid   = errors.New("registration token is invalid, already used, or expired")
	ErrTokenExpired   = errors.New("registration token has expired")
	ErrAgentForbidden = errors.New("agent token is invalid or revoked")
)

const (
	StatusPending  = "pending"
	StatusOnline   = "online"
	StatusOffline  = "offline"
	StatusDisabled = "disabled"
)

// OfflineAfter is how long a server may go without a heartbeat before it is
// considered offline instead of online.
const OfflineAfter = 2 * time.Minute

type Server struct {
	ID           uuid.UUID  `json:"id"`
	Organization uuid.UUID  `json:"organization_id"`
	Name         string     `json:"name"`
	Hostname     string     `json:"hostname"`
	OSInfo       string     `json:"os_info"`
	AgentVersion string     `json:"agent_version"`
	Status       string     `json:"status"`
	EnrolledAt   *time.Time `json:"enrolled_at,omitempty"`
	LastSeenAt   *time.Time `json:"last_seen_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Store struct {
	Pool *pgxpool.Pool
}

const cols = `id, organization_id, name, hostname, os_info, agent_version, status, enrolled_at, last_seen_at, created_at`

func scanRow(row pgx.Row) (*Server, error) {
	var s Server
	err := row.Scan(&s.ID, &s.Organization, &s.Name, &s.Hostname, &s.OSInfo, &s.AgentVersion,
		&s.Status, &s.EnrolledAt, &s.LastSeenAt, &s.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// FirstOnline returns the most recently enrolled online server, regardless of
// org (setup wizard targets the local single-server install).
func (s *Store) FirstOnline(ctx context.Context) (uuid.UUID, string, error) {
	var id uuid.UUID
	var name string
	err := s.Pool.QueryRow(ctx, `
		SELECT id, name FROM servers WHERE status = 'online' ORDER BY enrolled_at ASC LIMIT 1
	`).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, "", ErrNotFound
	}
	return id, name, err
}

// Create registers a new server in pending state and returns it together with
// a one-time registration token (raw token is returned once, only its hash is
// stored).
func (s *Store) Create(ctx context.Context, orgID, registeredBy uuid.UUID, name string, tokenTTL time.Duration) (*Server, string, error) {
	token, hash, err := newToken("reg")
	if err != nil {
		return nil, "", err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	row := tx.QueryRow(ctx, `
		INSERT INTO servers (organization_id, name, registered_by)
		VALUES ($1, $2, $3)
		RETURNING `+cols,
		orgID, name, registeredBy,
	)
	srv, err := scanRow(row)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, "", ErrNameTaken
		}
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO server_registration_tokens (server_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, srv.ID, hash, time.Now().Add(tokenTTL)); err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return srv, token, nil
}

// Enroll exchanges a valid one-time registration token for a persistent agent
// token. The server transitions from pending to enrolled.
func (s *Store) Enroll(ctx context.Context, regToken, hostname, osInfo, agentVersion string) (*Server, string, error) {
	hash := hashToken(regToken)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback(ctx)

	var serverID uuid.UUID
	var expiresAt time.Time
	var usedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT server_id, expires_at, used_at FROM server_registration_tokens
		WHERE token_hash = $1
		FOR UPDATE
	`, hash).Scan(&serverID, &expiresAt, &usedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrTokenInvalid
	}
	if err != nil {
		return nil, "", err
	}
	if usedAt != nil {
		return nil, "", ErrTokenInvalid
	}
	if time.Now().After(expiresAt) {
		return nil, "", ErrTokenExpired
	}

	agentToken, agentHash, err := newToken("agt")
	if err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO server_agent_tokens (server_id, token_hash) VALUES ($1, $2)
	`, serverID, agentHash); err != nil {
		return nil, "", err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE server_registration_tokens
		SET used_at = now(), used_by = $2
		WHERE token_hash = $1
	`, hash, hostname); err != nil {
		return nil, "", err
	}

	row := tx.QueryRow(ctx, `
		UPDATE servers
		SET status = 'online', hostname = $2, os_info = $3, agent_version = $4,
		    enrolled_at = now(), last_seen_at = now(), updated_at = now()
		WHERE id = $1
		RETURNING `+cols,
		serverID, hostname, osInfo, agentVersion,
	)
	srv, err := scanRow(row)
	if err != nil {
		return nil, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, "", err
	}
	return srv, agentToken, nil
}

// ServerForAgentToken resolves a server from a raw agent token.
func (s *Store) ServerForAgentToken(ctx context.Context, token string) (*Server, error) {
	var serverID uuid.UUID
	err := s.Pool.QueryRow(ctx, `
		SELECT server_id FROM server_agent_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL
	`, hashToken(token)).Scan(&serverID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAgentForbidden
	}
	if err != nil {
		return nil, err
	}
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM servers WHERE id = $1`, serverID)
	srv, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return srv, err
}

// Heartbeat records liveness (and optional metrics) for an agent.
func (s *Store) Heartbeat(ctx context.Context, serverID uuid.UUID, m *Metrics) error {
	if m == nil {
		_, err := s.Pool.Exec(ctx, `UPDATE servers SET last_seen_at = now(), updated_at = now() WHERE id = $1`, serverID)
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE servers SET last_seen_at = now(), updated_at = now() WHERE id = $1`, serverID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO server_metrics
			(server_id, cpu_percent, memory_total_bytes, memory_used_bytes, disk_total_bytes, disk_used_bytes, load1, load5, load15)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, serverID, m.CPUPercent, m.MemoryTotal, m.MemoryUsed, m.DiskTotal, m.DiskUsed, m.Load1, m.Load5, m.Load15); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RefreshStatus recomputes status from heartbeat freshness for servers that
// were enrolled (not disabled, not pending). Returns affected rows.
func (s *Store) RefreshStatuses(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE servers
		SET status = CASE
			WHEN last_seen_at IS NOT NULL AND last_seen_at > now() - $1::interval THEN 'online'
			ELSE 'offline'
		END,
		updated_at = now()
		WHERE status IN ('online', 'offline')
	`, OfflineAfter)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// GetByID resolves a server by id. Servers are platform-wide infrastructure:
// every organization shares the same fleet, so no org filter is applied.
func (s *Store) GetByID(ctx context.Context, serverID uuid.UUID) (*Server, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM servers WHERE id = $1`, serverID)
	srv, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return srv, err
}

// ListAll returns the platform-wide fleet. All organizations see the same
// servers — this is an operator control panel, not per-tenant hardware.
func (s *Store) ListAll(ctx context.Context) ([]Server, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM servers ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		srv, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *srv)
	}
	return out, rows.Err()
}

func (s *Store) Delete(ctx context.Context, serverID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM servers WHERE id = $1`, serverID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateRegistrationToken invalidates previous unused tokens for the server
// and issues a fresh one. Returns the raw token (shown once).
func (s *Store) RotateRegistrationToken(ctx context.Context, serverID uuid.UUID, tokenTTL time.Duration) (string, time.Time, error) {
	raw, newHash, err := newToken("reg")
	if err != nil {
		return "", time.Time{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", time.Time{}, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `DELETE FROM server_registration_tokens WHERE server_id = $1 AND used_at IS NULL`, serverID); err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(tokenTTL)
	if _, err := tx.Exec(ctx, `
		INSERT INTO server_registration_tokens (server_id, token_hash, expires_at)
		VALUES ($1, $2, $3)
	`, serverID, newHash, expiresAt); err != nil {
		return "", time.Time{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", time.Time{}, err
	}
	return raw, expiresAt, nil
}

func (s *Store) LatestMetrics(ctx context.Context, serverID uuid.UUID) (*Metrics, error) {
	var m Metrics
	err := s.Pool.QueryRow(ctx, `
		SELECT cpu_percent, memory_total_bytes, memory_used_bytes, disk_total_bytes, disk_used_bytes, load1, load5, load15, collected_at
		FROM server_metrics WHERE server_id = $1
		ORDER BY collected_at DESC LIMIT 1
	`, serverID).Scan(&m.CPUPercent, &m.MemoryTotal, &m.MemoryUsed, &m.DiskTotal, &m.DiskUsed, &m.Load1, &m.Load5, &m.Load15, &m.CollectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// LatestMetricsForAll returns the newest metric row per server in one query
// (replaces the frontend's per-server N+1 loop).
func (s *Store) LatestMetricsForAll(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (m.server_id)
			m.server_id, m.cpu_percent, m.memory_total_bytes, m.memory_used_bytes,
			m.disk_total_bytes, m.disk_used_bytes, m.load1, m.load5, m.load15, m.collected_at
		FROM server_metrics m
		ORDER BY m.server_id, m.collected_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var serverID uuid.UUID
		var m Metrics
		if err := rows.Scan(&serverID, &m.CPUPercent, &m.MemoryTotal, &m.MemoryUsed, &m.DiskTotal, &m.DiskUsed, &m.Load1, &m.Load5, &m.Load15, &m.CollectedAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"server_id": serverID, "metrics": m,
			"freshness": classifyFreshness(&m, nil),
		})
	}
	return out, rows.Err()
}

// --- token helpers ---

func newToken(prefix string) (raw string, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = prefix + "_" + hex.EncodeToString(b)
	return raw, hashToken(raw), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// AutoPickServer selects the best online server on the platform for a new
// website: least combined CPU+memory utilization, optionally requiring a
// specific runtime version to be available. Empty runtime = any server.
// The fleet is shared across organizations, so no org filter is applied.
func (s *Store) AutoPickServer(ctx context.Context, runtimeType, runtimeVersion string) (uuid.UUID, error) {
	var id uuid.UUID
	err := s.Pool.QueryRow(ctx, `
		SELECT s.id FROM servers s
		LEFT JOIN LATERAL (
			SELECT cpu_percent, memory_used_bytes, memory_total_bytes
			FROM server_metrics m WHERE m.server_id = s.id
			ORDER BY collected_at DESC LIMIT 1
		) lm ON TRUE
		WHERE s.status = 'online' AND s.maintenance_mode = FALSE
		  AND s.last_seen_at > now() - interval '2 minutes'
		  AND ($1::text = '' OR EXISTS (
			SELECT 1 FROM runtimes r
			WHERE r.server_id = s.id AND r.type = $1::text AND r.version = $2::text AND r.status = 'available'
		  ))
		ORDER BY
			COALESCE(lm.cpu_percent, 50) * 0.6
			+ COALESCE(100.0 * lm.memory_used_bytes / NULLIF(lm.memory_total_bytes, 0), 50) * 0.4,
			s.created_at ASC
		LIMIT 1
	`, runtimeType, runtimeVersion).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrNoCandidate
	}
	return id, err
}

var ErrNoCandidate = errors.New("no suitable server available for placement")

// SetMaintenance toggles maintenance mode; agents stop receiving new jobs in
// maintenance mode (claim filters it) and placement skips the server.
func (s *Store) SetMaintenance(ctx context.Context, serverID uuid.UUID, on bool) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE servers SET maintenance_mode = $2, updated_at = now()
		WHERE id = $1
	`, serverID, on)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Capacity summarizes a server for placement decisions.
type Capacity struct {
	ServerID     uuid.UUID `json:"server_id"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	Maintenance  bool      `json:"maintenance_mode"`
	CPUPercent   float64   `json:"cpu_percent"`
	MemPercent   float64   `json:"memory_percent"`
	WebsiteCount int       `json:"website_count"`
	Score        float64   `json:"placement_score"`
	Eligible     bool      `json:"eligible"`
}

func (s *Store) CapacityAll(ctx context.Context) ([]Capacity, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT s.id, s.name, s.status, s.maintenance_mode,
		       COALESCE(lm.cpu_percent, 50),
		       COALESCE(100.0 * lm.memory_used_bytes / NULLIF(lm.memory_total_bytes, 0), 50),
		       (SELECT count(*) FROM websites w WHERE w.server_id = s.id AND w.status != 'deleted')
		FROM servers s
		LEFT JOIN LATERAL (
			SELECT cpu_percent, memory_used_bytes, memory_total_bytes
			FROM server_metrics m WHERE m.server_id = s.id
			ORDER BY collected_at DESC LIMIT 1
		) lm ON TRUE
		ORDER BY s.created_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Capacity
	for rows.Next() {
		var c Capacity
		if err := rows.Scan(&c.ServerID, &c.Name, &c.Status, &c.Maintenance, &c.CPUPercent, &c.MemPercent, &c.WebsiteCount); err != nil {
			return nil, err
		}
		c.Score = c.CPUPercent*0.6 + c.MemPercent*0.4
		c.Eligible = c.Status == "online" && !c.Maintenance
		out = append(out, c)
	}
	return out, rows.Err()
}
