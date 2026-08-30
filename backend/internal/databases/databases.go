// Package databases implements managed MySQL/PostgreSQL databases. The panel
// records metadata and orchestrates jobs; all DDL runs on the managed server
// through the agent's typed db operations (no shell, validated identifiers).
package databases

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/agentclient"
	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Engine identifiers (mirror the agent contract).
const (
	EngineMySQL    = "mysql"
	EnginePostgres = "postgres"
)

// Statuses.
const (
	StatusProvisioning = "provisioning"
	StatusActive       = "active"
	StatusError        = "error"
	StatusDeleting     = "deleting"
)

var (
	dbNameRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	userNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// ValidateDBName / ValidateUserName are exported for handler reuse.
func ValidateDBName(name string) error {
	if !dbNameRe.MatchString(name) {
		return apierror.BadRequest("database name must be lowercase letters, digits or underscore, start with a letter, max 63 chars")
	}
	return nil
}

func ValidateUserName(name string) error {
	if !userNameRe.MatchString(name) {
		return apierror.BadRequest("database user must be lowercase letters, digits or underscore, start with a letter, max 32 chars")
	}
	return nil
}

type Deps struct {
	Pool    *pgxpool.Pool
	Log     *slog.Logger
	Agent   *agentclient.Client
	Servers *servers.Service
	Jobs    *jobs.Store
	Audit   *audit.Service
}

type Service struct{ deps Deps }

func New(deps Deps) *Service { return &Service{deps} }

// Database is the API view.
type Database struct {
	ID         string   `json:"id"`
	ServerID   string   `json:"server_id"`
	ServerName string   `json:"server_name"`
	WebsiteID  *string  `json:"website_id"`
	Engine     string   `json:"engine"`
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Error      string   `json:"error,omitempty"`
	Users      []User   `json:"users"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

// User is a database user (password never stored or returned).
type User struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
}

const dbCols = `d.id, d.server_id, d.website_id, d.engine, d.name, d.status,
	COALESCE(d.error,''), d.created_at::text, d.updated_at::text,
	COALESCE(s.label,''), COALESCE(s.hostname,'')`

func (s *Service) scanDatabase(row pgx.Row) (*Database, error) {
	var d Database
	var websiteID *string
	var serverName, hostname string
	if err := row.Scan(&d.ID, &d.ServerID, &websiteID, &d.Engine, &d.Name, &d.Status,
		&d.Error, &d.CreatedAt, &d.UpdatedAt, &serverName, &hostname); err != nil {
		return nil, err
	}
	d.WebsiteID = websiteID
	d.ServerName = serverName
	if d.ServerName == "" {
		d.ServerName = hostname
	}
	return &d, nil
}

const dbFrom = ` FROM databases d JOIN servers s ON s.id = d.server_id`

// List returns databases, optionally filtered by server and/or website.
func (s *Service) List(ctx context.Context, serverID, websiteID string) ([]Database, error) {
	q := `SELECT ` + dbCols + dbFrom
	args := []any{}
	conds := []string{}
	if serverID != "" {
		args = append(args, serverID)
		conds = append(conds, fmt.Sprintf("d.server_id = $%d", len(args)))
	}
	if websiteID != "" {
		args = append(args, websiteID)
		conds = append(conds, fmt.Sprintf("d.website_id = $%d", len(args)))
	}
	if len(conds) > 0 {
		q += ` WHERE ` + strings.Join(conds, " AND ")
	}
	q += ` ORDER BY d.created_at DESC`
	rows, err := s.deps.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []Database{}
	for rows.Next() {
		d, err := s.scanDatabase(rows)
		if err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, apierror.From(err)
	}
	for i := range out {
		users, err := s.listUsers(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Users = users
	}
	return out, nil
}

// Get fetches one database with its users.
func (s *Service) Get(ctx context.Context, id string) (*Database, error) {
	d, err := s.scanDatabase(s.deps.Pool.QueryRow(ctx,
		`SELECT `+dbCols+dbFrom+` WHERE d.id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("database")
		}
		return nil, apierror.From(err)
	}
	users, err := s.listUsers(ctx, id)
	if err != nil {
		return nil, err
	}
	d.Users = users
	return d, nil
}

func (s *Service) listUsers(ctx context.Context, databaseID string) ([]User, error) {
	rows, err := s.deps.Pool.Query(ctx, `
		SELECT id, username, status, created_at::text
		FROM database_users WHERE database_id = $1 ORDER BY username`, databaseID)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt); err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

type CreateInput struct {
	ServerID  string
	WebsiteID *string
	Engine    string
	Name      string
}

type CreateResult struct {
	Database *Database
	Job      *jobs.Job
}

func (s *Service) Create(ctx context.Context, actor Actor, in CreateInput) (*CreateResult, error) {
	if in.ServerID == "" || in.Name == "" {
		return nil, apierror.BadRequest("server_id and name are required")
	}
	if in.Engine != EngineMySQL && in.Engine != EnginePostgres {
		return nil, apierror.BadRequest("engine must be mysql or postgres")
	}
	if err := ValidateDBName(in.Name); err != nil {
		return nil, err
	}

	target, err := s.deps.Servers.OpsTarget(ctx, in.ServerID)
	if err != nil {
		return nil, err
	}
	if !target.Manageable {
		return nil, apierror.New(409, "SERVER_NOT_MANAGEABLE",
			"server has no management channel; re-enroll the agent")
	}

	// The requested engine must be configured + reachable on the agent.
	engines, err := s.deps.Agent.DBEngines(ctx, target.AgentURL, target.OpsToken)
	if err != nil {
		return nil, err
	}
	if !engineAvailable(engines, in.Engine) {
		return nil, apierror.BadRequest(fmt.Sprintf(
			"%s is not available on this server (configure the agent's %s admin credentials)",
			in.Engine, in.Engine))
	}

	// Optional website link must belong to the same server.
	if in.WebsiteID != nil && *in.WebsiteID != "" {
		var ws string
		err := s.deps.Pool.QueryRow(ctx,
			`SELECT server_id FROM websites WHERE id = $1`, *in.WebsiteID).Scan(&ws)
		if err != nil {
			return nil, apierror.BadRequest("website not found")
		}
		if ws != in.ServerID {
			return nil, apierror.BadRequest("website belongs to a different server")
		}
	} else {
		in.WebsiteID = nil
	}

	tx, err := s.deps.Pool.Begin(ctx)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer tx.Rollback(ctx)

	var id string
	err = tx.QueryRow(ctx, `
		INSERT INTO databases (server_id, website_id, engine, name, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`,
		in.ServerID, in.WebsiteID, in.Engine, in.Name, StatusProvisioning).Scan(&id)
	if err != nil {
		if isUnique(err) {
			return nil, apierror.Conflict("a database with that name already exists on this server/engine")
		}
		return nil, apierror.From(err)
	}
	payload := map[string]any{"engine": in.Engine, "name": in.Name, "database_id": id}
	job, err := s.deps.Jobs.CreateTx(ctx, tx, jobs.TypeProvisionDatabase, &in.ServerID, nil, payload)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, apierror.From(err)
	}

	s.audit(ctx, actor, "databases.created", id, in.Name, map[string]any{
		"server_id": in.ServerID, "engine": in.Engine, "job_id": job.ID,
	})
	d, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return &CreateResult{Database: d, Job: job}, nil
}

// ---------------------------------------------------------------------------
// Users (synchronous — password returned once)
// ---------------------------------------------------------------------------

func (s *Service) CreateUser(ctx context.Context, actor Actor, databaseID, username string) (*User, string, error) {
	d, err := s.Get(ctx, databaseID)
	if err != nil {
		return nil, "", err
	}
	if d.Status != StatusActive {
		return nil, "", apierror.Conflict("database must be active before adding users")
	}
	if err := ValidateUserName(username); err != nil {
		return nil, "", err
	}
	target, err := s.deps.Servers.OpsTarget(ctx, d.ServerID)
	if err != nil {
		return nil, "", err
	}
	if !target.Manageable {
		return nil, "", apierror.New(409, "SERVER_NOT_MANAGEABLE", "server has no management channel")
	}

	res, err := s.deps.Agent.DBUserCreate(ctx, target.AgentURL, target.OpsToken, d.Engine, d.Name, username)
	if err != nil {
		return nil, "", err
	}
	var u User
	err = s.deps.Pool.QueryRow(ctx, `
		INSERT INTO database_users (database_id, username, status)
		VALUES ($1, $2, 'active')
		RETURNING id, username, status, created_at::text`,
		databaseID, username).Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt)
	if err != nil {
		return nil, "", apierror.From(err)
	}
	s.audit(ctx, actor, "databases.user_created", databaseID, d.Name, map[string]any{"username": username})
	return &u, res.Password, nil
}

func (s *Service) RotatePassword(ctx context.Context, actor Actor, databaseID, userID string) (string, error) {
	d, err := s.Get(ctx, databaseID)
	if err != nil {
		return "", err
	}
	u, err := s.userByID(ctx, userID)
	if err != nil {
		return "", err
	}
	target, err := s.deps.Servers.OpsTarget(ctx, d.ServerID)
	if err != nil {
		return "", err
	}
	if !target.Manageable {
		return "", apierror.New(409, "SERVER_NOT_MANAGEABLE", "server has no management channel")
	}
	res, err := s.deps.Agent.DBUserPassword(ctx, target.AgentURL, target.OpsToken, d.Engine, u.Username)
	if err != nil {
		return "", err
	}
	s.audit(ctx, actor, "databases.user_password_rotated", databaseID, d.Name, map[string]any{"username": u.Username})
	return res.Password, nil
}

func (s *Service) DropUser(ctx context.Context, actor Actor, databaseID, userID string) error {
	d, err := s.Get(ctx, databaseID)
	if err != nil {
		return err
	}
	u, err := s.userByID(ctx, userID)
	if err != nil {
		return err
	}
	target, err := s.deps.Servers.OpsTarget(ctx, d.ServerID)
	if err != nil {
		return err
	}
	if target.Manageable {
		if err := s.deps.Agent.DBUserDrop(ctx, target.AgentURL, target.OpsToken, d.Engine, u.Username); err != nil {
			return err
		}
	}
	if _, err := s.deps.Pool.Exec(ctx,
		`DELETE FROM database_users WHERE id = $1`, userID); err != nil {
		return apierror.From(err)
	}
	s.audit(ctx, actor, "databases.user_deleted", databaseID, d.Name, map[string]any{"username": u.Username})
	return nil
}

func (s *Service) userByID(ctx context.Context, id string) (*User, error) {
	var u User
	err := s.deps.Pool.QueryRow(ctx, `
		SELECT id, username, status, created_at::text FROM database_users WHERE id = $1`, id).
		Scan(&u.ID, &u.Username, &u.Status, &u.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("database user")
		}
		return nil, apierror.From(err)
	}
	return &u, nil
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func (s *Service) RequestDelete(ctx context.Context, actor Actor, id string) (*jobs.Job, error) {
	d, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.Status == StatusDeleting {
		return nil, apierror.Conflict("database is already being deleted")
	}
	payload := map[string]any{"engine": d.Engine, "name": d.Name, "database_id": id}
	job, err := s.deps.Jobs.Create(ctx, jobs.TypeDeleteDatabase, &d.ServerID, nil, payload)
	if err != nil {
		return nil, err
	}
	if _, err := s.deps.Pool.Exec(ctx,
		`UPDATE databases SET status = $2, updated_at = now() WHERE id = $1`, id, StatusDeleting); err != nil {
		return nil, apierror.From(err)
	}
	s.audit(ctx, actor, "databases.delete_requested", id, d.Name, map[string]any{"job_id": job.ID})
	return job, nil
}

// ---------------------------------------------------------------------------
// Job handlers
// ---------------------------------------------------------------------------

func (s *Service) RegisterHandlers(runner *jobs.Runner) {
	runner.Register(jobs.TypeProvisionDatabase, s.handleProvision)
	runner.Register(jobs.TypeDeleteDatabase, s.handleDelete)
}

func (s *Service) handleProvision(ctx context.Context, job *jobs.Job, progress jobs.ProgressFunc) error {
	dbID := derefStr(job.Payload["database_id"])
	if dbID == "" {
		// Fallback: find by name+server (payload from Create lacks id; resolve).
		return errors.New("provision job missing database id")
	}
	d, err := s.Get(ctx, dbID)
	if err != nil {
		return err
	}
	target, err := s.deps.Servers.OpsTarget(ctx, d.ServerID)
	if err != nil {
		return err
	}
	progress(40, "Creating database "+d.Name)
	if err := s.deps.Agent.DBCreate(ctx, target.AgentURL, target.OpsToken, d.Engine, d.Name); err != nil {
		return err
	}
	progress(90, "Activating")
	if _, err := s.deps.Pool.Exec(ctx,
		`UPDATE databases SET status = 'active', error = '', updated_at = now() WHERE id = $1`, dbID); err != nil {
		return apierror.From(err)
	}
	return nil
}

func (s *Service) handleDelete(ctx context.Context, job *jobs.Job, progress jobs.ProgressFunc) error {
	dbID := derefStr(job.Payload["database_id"])
	engine := derefStr(job.Payload["engine"])
	name := derefStr(job.Payload["name"])
	if dbID == "" {
		return nil // already removed
	}
	d, err := s.Get(ctx, dbID)
	if err != nil {
		return nil // row gone
	}
	_ = engine
	_ = name
	target, err := s.deps.Servers.OpsTarget(ctx, d.ServerID)
	if err != nil {
		return err
	}
	progress(40, "Dropping database "+d.Name)
	if target.Manageable {
		if err := s.deps.Agent.DBDrop(ctx, target.AgentURL, target.OpsToken, d.Engine, d.Name); err != nil {
			s.deps.Log.Warn("db drop failed; removing metadata anyway", "err", err)
		}
	}
	progress(80, "Finalizing")
	if _, err := s.deps.Pool.Exec(ctx, `DELETE FROM databases WHERE id = $1`, dbID); err != nil {
		return apierror.From(err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// Actor identifies who triggered an operation.
type Actor struct {
	ID    *string
	Label string
	IP    string
}

func engineAvailable(e *agentclient.DBEnginesResult, engine string) bool {
	if e == nil {
		return false
	}
	if engine == EngineMySQL {
		return e.MySQL.Available
	}
	return e.Postgres.Available
}

func (s *Service) audit(ctx context.Context, actor Actor, action, resourceID, name string, meta map[string]any) {
	entry := audit.Entry{
		ActorType: "user", Label: actor.Label,
		Action: action, Resource: "database", ResourceID: resourceID,
		Metadata: meta,
	}
	if actor.Label == "" {
		entry.ActorType = "system"
		entry.Label = "databases engine"
	}
	s.deps.Audit.Log(ctx, entry)
}

func derefStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func isUnique(err error) bool {
	return strings.Contains(err.Error(), "databases_server_engine_name_key")
}
