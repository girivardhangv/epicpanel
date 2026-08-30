// Package servers implements agent registration and server inventory.
// Agents authenticate with a shared registration key on first enrollment and
// receive an opaque per-server token (stored hashed) for subsequent calls.
package servers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Service struct {
	pool *pgxpool.Pool
}

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool} }

type RegisterInput struct {
	Label        string         `json:"label"`
	Hostname     string         `json:"hostname"`
	OS           string         `json:"os"`
	OSVersion    string         `json:"os_version"`
	Arch         string         `json:"arch"`
	AgentVersion string         `json:"agent_version"`
	Specs        map[string]any `json:"specs"`
	OpsAddr      string         `json:"ops_addr"` // advertised agent management URL
}

func hashAgentToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func mustJSONOrDefault(m map[string]any) string {
	if len(m) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func unmarshalSpecs(s string) map[string]any {
	out := map[string]any{}
	if s == "" {
		return out
	}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func validateSpec(in RegisterInput) error {
	switch in.OS {
	case "linux", "windows":
	default:
		return errors.New("os must be 'linux' or 'windows'")
	}
	switch in.Arch {
	case "amd64", "arm64":
	default:
		return errors.New("arch must be 'amd64' or 'arm64'")
	}
	if in.Hostname != "" && len(in.Hostname) > 255 {
		return errors.New("hostname too long")
	}
	if len(in.Label) > 128 {
		return errors.New("label too long")
	}
	if len(in.OpsAddr) > 512 {
		return errors.New("ops_addr too long")
	}
	if in.OpsAddr != "" && !strings.HasPrefix(in.OpsAddr, "http://") && !strings.HasPrefix(in.OpsAddr, "https://") {
		return errors.New("ops_addr must be an http(s) URL")
	}
	return nil
}

// Enroll registers a machine and returns its permanent agent token and the
// ops channel secret exactly once.
func (s *Service) Enroll(ctx context.Context, in RegisterInput, ip string) (*Server, string, string, error) {
	if in.Hostname == "" || len(in.Hostname) > 255 {
		return nil, "", "", apierror.BadRequest("hostname is required")
	}
	if err := validateSpec(in); err != nil {
		return nil, "", "", apierror.BadRequest(err.Error())
	}

	tokenB := make([]byte, 48)
	if _, err := rand.Read(tokenB); err != nil {
		return nil, "", "", apierror.From(err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenB)

	opsB := make([]byte, 48)
	if _, err := rand.Read(opsB); err != nil {
		return nil, "", "", apierror.From(err)
	}
	opsToken := base64.RawURLEncoding.EncodeToString(opsB)

	var srv Server
	var specStr string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO servers (label, hostname, os, os_version, arch, agent_version, specs, agent_token_hash,
		                     status, registered_ip, registered_at, last_seen_at, agent_url, ops_token)
		VALUES (NULLIF($1,''), $2, $3, $4, $5, $6, $7::jsonb, $8, 'online', $9, now(), now(), NULLIF($10,''), $11)
		RETURNING id, COALESCE(label,''), hostname, os, COALESCE(os_version,''), arch,
		          COALESCE(agent_version,''), specs::text, status, COALESCE(registered_ip,''),
		          registered_at, last_seen_at, COALESCE(agent_url,''), capabilities::text`,
		in.Label, in.Hostname, in.OS, in.OSVersion, in.Arch, in.AgentVersion,
		mustJSONOrDefault(in.Specs), hashAgentToken(token), ip, in.OpsAddr, opsToken).
		Scan(&srv.ID, &srv.Label, &srv.Hostname, &srv.OS, &srv.OSVersion, &srv.Arch,
			&srv.AgentVersion, &specStr, &srv.Status, &srv.RegisteredIP, &srv.RegisteredAt, &srv.LastSeenAt,
			&srv.AgentURL, &srv.CapabilitiesRaw)
	if err != nil {
		return nil, "", "", apierror.From(err)
	}
	srv.Specs = unmarshalSpecs(specStr)
	srv.Online = true
	return &srv, token, opsToken, nil
}

type Server struct {
	ID           string     `json:"id"`
	Label        string     `json:"label"`
	Hostname     string     `json:"hostname"`
	OS           string     `json:"os"`
	OSVersion    string     `json:"os_version"`
	Arch         string     `json:"arch"`
	AgentVersion string     `json:"agent_version"`
	Status       string     `json:"status"`
	RegisteredIP string     `json:"registered_ip"`
	RegisteredAt time.Time  `json:"registered_at"`
	LastSeenAt   *time.Time `json:"last_seen_at"`

	AgentURL        string         `json:"agent_url"`
	Manageable      bool           `json:"manageable"` // ops channel configured
	CapabilitiesRaw string         `json:"-"`
	Capabilities    map[string]any `json:"capabilities,omitempty"`

	Specs  map[string]any `json:"specs,omitempty"`
	Online bool           `json:"online"`
}

const serverCols = `
	id, COALESCE(label,''), hostname, os, COALESCE(os_version,''), arch, COALESCE(agent_version,''),
	specs::text, status, COALESCE(registered_ip,''), registered_at, last_seen_at,
	COALESCE(agent_url,''), COALESCE(ops_token,'') <> '', capabilities::text`

// List returns every non-revoked server, computing liveness from last_seen_at.
func (s *Service) List(ctx context.Context, offlineAfterMin int) ([]Server, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+serverCols+`
		FROM servers WHERE status <> 'revoked' ORDER BY registered_at DESC`)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()

	out := []Server{}
	cutoff := time.Now().Add(-time.Duration(offlineAfterMin) * time.Minute)
	for rows.Next() {
		var v Server
		var specStr string
		if err := rows.Scan(&v.ID, &v.Label, &v.Hostname, &v.OS, &v.OSVersion, &v.Arch,
			&v.AgentVersion, &specStr, &v.Status, &v.RegisteredIP, &v.RegisteredAt, &v.LastSeenAt,
			&v.AgentURL, &v.Manageable, &v.CapabilitiesRaw); err != nil {
			return nil, apierror.From(err)
		}
		v.Specs = unmarshalSpecs(specStr)
		v.Capabilities = unmarshalSpecs(v.CapabilitiesRaw)
		v.Online = v.Status == "online" && v.LastSeenAt != nil && v.LastSeenAt.After(cutoff)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Service) Get(ctx context.Context, id string, offlineAfterMin int) (*Server, error) {
	var v Server
	var specStr string
	err := s.pool.QueryRow(ctx, `SELECT `+serverCols+` FROM servers WHERE id = $1`, id).
		Scan(&v.ID, &v.Label, &v.Hostname, &v.OS, &v.OSVersion, &v.Arch, &v.AgentVersion,
			&specStr, &v.Status, &v.RegisteredIP, &v.RegisteredAt, &v.LastSeenAt,
			&v.AgentURL, &v.Manageable, &v.CapabilitiesRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("server")
		}
		return nil, apierror.From(err)
	}
	v.Specs = unmarshalSpecs(specStr)
	v.Capabilities = unmarshalSpecs(v.CapabilitiesRaw)
	cutoff := time.Now().Add(-time.Duration(offlineAfterMin) * time.Minute)
	v.Online = v.Status == "online" && v.LastSeenAt != nil && v.LastSeenAt.After(cutoff)
	return &v, nil
}

// Heartbeat updates live inventory data keyed by the agent token itself and
// adopts the agent's advertised management URL when present.
func (s *Service) Heartbeat(ctx context.Context, token, os, osVersion, agentVersion, agentURL string, specs map[string]any) error {
	if token == "" {
		return apierror.Unauthorized
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE servers SET
			os            = COALESCE(NULLIF($2,''), os),
			os_version    = COALESCE(NULLIF($3,''), os_version),
			agent_version = CASE WHEN $4 <> '' THEN $4 ELSE agent_version END,
			agent_url     = COALESCE(NULLIF($5,''), agent_url),
			specs         = CASE WHEN $6::text = '{}' THEN specs ELSE $6::jsonb END,
			status        = 'online', last_seen_at = now()
		WHERE agent_token_hash = $1 AND status <> 'revoked'`,
		hashAgentToken(token), os, osVersion, agentVersion, agentURL, mustJSONOrDefault(specs))
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.Unauthorized
	}
	return nil
}

// Revoke disables future agent access; history is preserved.
func (s *Service) Revoke(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE servers SET status = 'revoked' WHERE id = $1`, id)
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.NotFound("server")
	}
	return nil
}

// Summary provides real panel-side aggregates for the dashboard.
type Summary struct {
	ServersTotal   int64 `json:"servers_total"`
	ServersOnline  int64 `json:"servers_online"`
	WebsitesTotal  int64 `json:"websites_total"`
	WebsitesActive int64 `json:"websites_active"`
	UsersCount     int64 `json:"users_count"`
	SessionsActive int64 `json:"sessions_active"`
}

func (s *Service) DashboardSummary(ctx context.Context, offlineAfterMin int) (*Summary, error) {
	sum := &Summary{}
	cutoff := time.Now().Add(-time.Duration(offlineAfterMin) * time.Minute)

	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status <> 'revoked'),
		       count(*) FILTER (WHERE status = 'online' AND last_seen_at IS NOT NULL AND last_seen_at > $1)
		FROM servers`, cutoff).Scan(&sum.ServersTotal, &sum.ServersOnline)
	if err != nil {
		return nil, apierror.From(err)
	}
	err = s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE status = 'active') FROM websites`).Scan(&sum.WebsitesTotal, &sum.WebsitesActive)
	if err != nil {
		return nil, apierror.From(err)
	}
	err = s.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE is_active`).Scan(&sum.UsersCount)
	if err != nil {
		return nil, apierror.From(err)
	}
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE revoked_at IS NULL AND expires_at > now()`).Scan(&sum.SessionsActive)
	if err != nil {
		return nil, apierror.From(err)
	}
	return sum, nil
}
