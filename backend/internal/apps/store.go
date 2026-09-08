package apps

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"encoding/base64"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
)

var ErrNotFound = errors.New("application not found")

type Health string

const (
	HealthUnknown  Health = "unknown"
	HealthStarting Health = "starting"
	HealthRunning  Health = "running"
	HealthStopped  Health = "stopped"
	HealthCrashed  Health = "crashed"
)

type App struct {
	ID          uuid.UUID       `json:"id"`
	WebsiteID   uuid.UUID       `json:"website_id"`
	StartupCmd  string          `json:"startup_command"`
	BuildCmd    string          `json:"build_command"`
	StartupFile string          `json:"startup_file"`
	Port        int             `json:"internal_port"`
	Env         json.RawMessage `json:"env_vars"`
	ProcessName string          `json:"process_name"`
	Health      Health          `json:"health"`
	CreatedAt   time.Time       `json:"created_at"`
}

type Store struct{ Pool *pgxpool.Pool }

const cols = `id, website_id, startup_command, build_command, startup_file, internal_port, env_vars, process_name, health, created_at`

func scanRow(row pgx.Row) (*App, error) {
	var a App
	err := row.Scan(&a.ID, &a.WebsiteID, &a.StartupCmd, &a.BuildCmd, &a.StartupFile,
		&a.Port, &a.Env, &a.ProcessName, &a.Health, &a.CreatedAt)
	if err != nil {
		return nil, err
	}
	a.Env = decryptEnv(a.Env)
	return &a, nil
}

// env_vars is encrypted at rest (audit finding: env vars often carry secrets
// and were stored plaintext). The column keeps its JSONB type: a JSON string
// holding base64 AES-GCM ciphertext. Legacy plaintext objects decrypt to
// themselves, so old rows keep working until the next update.
func encryptEnv(env json.RawMessage) json.RawMessage {
	enc, err := secretbox.Encrypt(string(env))
	if err != nil {
		return env
	}
	b64 := base64.StdEncoding.EncodeToString(enc)
	out, err := json.Marshal(b64)
	if err != nil {
		return env
	}
	return out
}

func decryptEnv(stored json.RawMessage) json.RawMessage {
	var b64 string
	if err := json.Unmarshal(stored, &b64); err != nil {
		return stored // legacy plaintext JSON object
	}
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return stored
	}
	plain, err := secretbox.Decrypt(raw)
	if err != nil {
		return stored
	}
	return json.RawMessage(plain)
}

func (s *Store) Create(ctx context.Context, websiteID uuid.UUID, startupCmd, buildCmd, startupFile string, port int, env json.RawMessage) (*App, error) {
	if env == nil {
		env = json.RawMessage("{}")
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO applications (website_id, startup_command, build_command, startup_file, internal_port, env_vars)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+cols,
		websiteID, startupCmd, buildCmd, startupFile, port, encryptEnv(env))
	a, err := scanRow(row)
	if err != nil {
		var pgErr interface{ SQLState() string }
		if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
			return nil, errors.New("an application already exists for this website")
		}
		return nil, err
	}
	return a, nil
}

func (s *Store) GetByWebsite(ctx context.Context, websiteID uuid.UUID) (*App, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM applications WHERE website_id = $1`, websiteID)
	a, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) GetByWebsiteAndOrg(ctx context.Context, orgID, websiteID uuid.UUID) (*App, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT a.id, a.website_id, a.startup_command, a.build_command, a.startup_file,
		       a.internal_port, a.env_vars, a.process_name, a.health, a.created_at
		FROM applications a
		JOIN websites w ON w.id = a.website_id
		WHERE a.website_id = $2 AND w.organization_id = $1`, orgID, websiteID)
	a, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) SetHealth(ctx context.Context, websiteID uuid.UUID, h Health) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE applications SET health = $2, last_health_check = now(), updated_at = now()
		WHERE website_id = $1`, websiteID, h)
	return err
}

func (s *Store) Update(ctx context.Context, app *App, startupCmd, buildCmd, startupFile string, port int, env json.RawMessage) (*App, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE applications SET startup_command = $2, build_command = $3, startup_file = $4,
		       internal_port = $5, env_vars = $6, updated_at = now()
		WHERE website_id = $1
		RETURNING `+cols,
		app.WebsiteID, startupCmd, buildCmd, startupFile, port, encryptEnv(env))
	a, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return a, err
}

func (s *Store) Delete(ctx context.Context, websiteID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM applications WHERE website_id = $1`, websiteID)
	return err
}
