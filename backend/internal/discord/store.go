// BotInstance store + lifecycle state machine (Phase 8).
//
// Verbatim state machine: installing → stopped → starting → running →
// stopping → crashed, plus failed/deleting/deleted. One Transition function
// guards every edge; reconciliation applies AGENT truth (the systemd unit
// state observed on the node), never assuming an API acceptance succeeded.
package discord

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound      = errors.New("bot not found")
	ErrNameTaken     = errors.New("bot name already in use")
	ErrIllegalState  = errors.New("illegal state transition")
)

// Status is the verbatim lifecycle.
type Status string

const (
	StatusInstalling Status = "installing"
	StatusStopped    Status = "stopped"
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusStopping   Status = "stopping"
	StatusCrashed    Status = "crashed"
	StatusFailed     Status = "failed"
	StatusDeleting   Status = "deleting"
	StatusDeleted    Status = "deleted"
)

// DesiredState is what the customer asked for; the agent reconciles.
const (
	DesiredRunning = "running"
	DesiredStopped = "stopped"
)

// transitions is the complete legal-edge set (guarded by Transition).
var transitions = map[Status][]Status{
	StatusInstalling: {StatusStopped, StatusStarting, StatusRunning, StatusFailed, StatusDeleting},
	StatusStopped:   {StatusStarting, StatusDeleting, StatusFailed},
	StatusStarting:  {StatusRunning, StatusCrashed, StatusFailed, StatusStopped, StatusDeleting},
	StatusRunning:   {StatusStopping, StatusCrashed, StatusStarting, StatusDeleting, StatusFailed},
	StatusStopping:  {StatusStopped, StatusCrashed, StatusFailed, StatusDeleting},
	StatusCrashed:   {StatusStarting, StatusStopped, StatusDeleting, StatusFailed},
	StatusFailed:    {StatusInstalling, StatusStopped, StatusDeleting, StatusDeleted},
	StatusDeleting:  {StatusDeleted},
	StatusDeleted:   {},
}

// CanTransition reports whether from→to is a legal edge.
func CanTransition(from, to Status) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// TransitionError carries the illegal edge for audit + API mapping.
type TransitionError struct{ From, To Status }

func (e *TransitionError) Error() string {
	return "illegal bot state transition " + string(e.From) + " -> " + string(e.To)
}

// AgentStatus maps a systemd unit state (agent truth) to a bot Status.
func AgentStatus(unitState string, desired string) Status {
	switch unitState {
	case "active":
		if desired == DesiredRunning {
			return StatusRunning
		}
		return StatusStopping // running but not wanted: converge by stopping
	case "activating":
		return StatusStarting
	case "deactivating":
		return StatusStopping
	case "failed":
		return StatusCrashed
	case "inactive", "":
		return StatusStopped
	default:
		return StatusStopped
	}
}

// Bot is one bot instance row (secrets NEVER included in plaintext).
type Bot struct {
	ID           uuid.UUID  `json:"id"`
	OrgID        uuid.UUID  `json:"organization_id"`
	ServerID     uuid.UUID  `json:"server_id"`
	CreatedBy    uuid.UUID  `json:"created_by"`
	Name         string     `json:"name"`
	Runtime      string     `json:"runtime"`
	RuntimeVer   string     `json:"runtime_version"`
	Status       Status     `json:"status"`
	DesiredState string     `json:"desired_state"`
	StartupFile  string     `json:"startup_file"`
	StartupCmd   string     `json:"startup_command"`
	BuildCmd     string     `json:"build_command"`
	RestartPolicy string    `json:"restart_policy"`
	MaxRestarts  int        `json:"max_restarts"`
	EpisodeRestarts int     `json:"episode_restarts"`
	EpisodeStart *time.Time `json:"episode_started_at,omitempty"`
	RestartCount int        `json:"restart_count"`
	GitRepo      string     `json:"git_repo_url"`
	GitBranch    string     `json:"git_branch"`
	EnvKeys      []string   `json:"env_keys"`
	NetAllow     []string   `json:"net_allow,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	UnitState    string     `json:"unit_state,omitempty"`
	AgentSeenAt  *time.Time `json:"agent_seen_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// EnvKeysJSON projects the env keys as the masked API shape.
// BotRow is the full row (ciphertext fields included) for internal callers
// (job payload assembly, env re-encryption). NEVER serialized to a response.
type BotRow struct {
	Bot
	EnvEnc      string
	GitTokenEnc string
	// Episode state used by the crash-recovery loop.
	EpisodeRestarts int
	EpisodeStart    *time.Time
}

// Public returns the API-safe projection (no ciphertext, no secret values).
func (r *BotRow) Public() *Bot { b := r.Bot; return &b }

const botCols = `id, organization_id, server_id, created_by, name, runtime, runtime_version,
	status, desired_state, startup_file, startup_command, build_command,
	restart_policy, max_restarts, episode_restarts, episode_started_at, restart_count,
	git_repo_url, git_branch, git_token_enc, env_enc, net_allow,
	last_error, unit_state, agent_seen_at, created_at, updated_at`

func scanBot(row pgx.Row) (*BotRow, error) {
	var r BotRow
	err := row.Scan(&r.Bot.ID, &r.Bot.OrgID, &r.Bot.ServerID, &r.Bot.CreatedBy, &r.Bot.Name,
		&r.Bot.Runtime, &r.Bot.RuntimeVer, &r.Bot.Status, &r.Bot.DesiredState,
		&r.Bot.StartupFile, &r.Bot.StartupCmd, &r.Bot.BuildCmd,
		&r.Bot.RestartPolicy, &r.Bot.MaxRestarts, &r.EpisodeRestarts, &r.EpisodeStart, &r.Bot.RestartCount,
		&r.Bot.GitRepo, &r.Bot.GitBranch, &r.GitTokenEnc, &r.EnvEnc, &r.Bot.NetAllow,
		&r.Bot.LastError, &r.Bot.UnitState, &r.Bot.AgentSeenAt, &r.Bot.CreatedAt, &r.Bot.UpdatedAt)
	if err != nil {
		return nil, err
	}
	// Env keys are derived from the encrypted blob (values stay sealed).
	if vars, derr := DecryptEnv(r.EnvEnc); derr == nil {
		r.Bot.EnvKeys = MaskEnv(vars).Keys
	}
	return &r, nil
}

// Store persists bot instances.
type Store struct{ Pool *pgxpool.Pool }

// Create inserts a bot in installing state with an encrypted env blob.
func (s *Store) Create(ctx context.Context, orgID, serverID, createdBy uuid.UUID,
	name, runtime, runtimeVersion, startupFile, startupCmd, buildCmd string,
	restartPolicy string, maxRestarts int, envEnc, gitRepo, gitBranch, gitTokenEnc string,
	netAllow []string) (*Bot, error) {

	if restartPolicy == "" {
		restartPolicy = "on-failure"
	}
	if maxRestarts <= 0 {
		maxRestarts = 5
	}
	if netAllow == nil {
		netAllow = []string{}
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO bot_instances
			(organization_id, server_id, created_by, name, runtime, runtime_version,
			 status, desired_state, startup_file, startup_command, build_command,
			 restart_policy, max_restarts, env_enc, git_repo_url, git_branch, git_token_enc, net_allow)
		VALUES ($1,$2,$3,$4,$5,$6,'installing','stopped',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		RETURNING `+botCols,
		orgID, serverID, createdBy, name, runtime, runtimeVersion,
		startupFile, startupCmd, buildCmd, restartPolicy, maxRestarts,
		envEnc, gitRepo, gitBranch, gitTokenEnc, netAllow)
	r, err := scanBot(row)
	if err != nil {
		if isUnique(err) {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	return r.Public(), nil
}

func isUnique(err error) bool {
	var pgErr interface{ SQLState() string }
	if errors.As(err, &pgErr) {
		return pgErr.SQLState() == "23505"
	}
	return false
}

// GetByID resolves a bot org-scoped. Cross-tenant access = not found.
func (s *Store) GetByID(ctx context.Context, orgID, botID uuid.UUID) (*Bot, error) {
	r, err := s.getBotRow(ctx, orgID, botID)
	if err != nil {
		return nil, err
	}
	return r.Public(), nil
}

// GetBotRow returns the full row (ciphertext included) for internal use.
func (s *Store) GetBotRow(ctx context.Context, orgID, botID uuid.UUID) (*BotRow, error) {
	return s.getBotRow(ctx, orgID, botID)
}

func (s *Store) getBotRow(ctx context.Context, orgID, botID uuid.UUID) (*BotRow, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+botCols+` FROM bot_instances WHERE id = $1 AND organization_id = $2`, botID, orgID)
	r, err := scanBot(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// GetByIDAny resolves without org scoping (internal job fanout only).
func (s *Store) GetByIDAny(ctx context.Context, botID uuid.UUID) (*Bot, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+botCols+` FROM bot_instances WHERE id = $1`, botID)
	r, err := scanBot(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r.Public(), err
}

// ListForOrg lists the org's bots (deleted excluded).
func (s *Store) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]Bot, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+botCols+` FROM bot_instances WHERE organization_id = $1 AND status <> 'deleted' ORDER BY created_at ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bot
	for rows.Next() {
		r, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r.Public())
	}
	return out, rows.Err()
}

// CountForOrg counts live bots (create-time plan gate).
func (s *Store) CountForOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM bot_instances WHERE organization_id = $1 AND status <> 'deleted'`, orgID).Scan(&n)
	return n, err
}

// UpdateConfig rewrites the mutable desired-state fields.
func (s *Store) UpdateConfig(ctx context.Context, botID uuid.UUID,
	startupFile, startupCmd, buildCmd, runtimeVersion string, envEnc string,
	gitRepo, gitBranch, gitTokenEnc string) error {

	ct, err := s.Pool.Exec(ctx, `
		UPDATE bot_instances SET
			startup_file = $2, startup_command = $3, build_command = $4,
			runtime_version = $5, env_enc = $6,
			git_repo_url = $7, git_branch = $8, git_token_enc = $9, updated_at = now()
		WHERE id = $1 AND status <> 'deleted'`,
		botID, startupFile, startupCmd, buildCmd, runtimeVersion, envEnc,
		gitRepo, gitBranch, gitTokenEnc)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetStatus applies a state transition guarded by the machine. When the row
// is already in the target state the update is a no-op success (idempotent
// convergence from racing agent reports).
func (s *Store) SetStatus(ctx context.Context, botID uuid.UUID, to Status, detail string) error {
	var cur Status
	err := s.Pool.QueryRow(ctx, `SELECT status FROM bot_instances WHERE id = $1`, botID).Scan(&cur)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cur == to {
		return nil
	}
	if !CanTransition(cur, to) {
		return &TransitionError{From: cur, To: to}
	}
	ct, err := s.Pool.Exec(ctx, `
		UPDATE bot_instances SET status = $2, last_error = CASE WHEN $3 = '' THEN last_error ELSE $3 END,
			updated_at = now()
		WHERE id = $1`, botID, to, detail)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReconcileAgentTruth applies the observed unit state. Unlike SetStatus it
// is allowed to move crashed→starting etc. without a customer action, and it
// maintains the crash episode counters (restart policy + restart count).
// Returns the applied status.
func (s *Store) ReconcileAgentTruth(ctx context.Context, botID uuid.UUID, unitState string) (Status, error) {
	r, err := s.getBotRowAny(ctx, botID)
	if err != nil {
		return "", err
	}
	observed := AgentStatus(unitState, r.Bot.DesiredState)
	now := time.Now().UTC()

	// Crash episode bookkeeping: a crash inside an open episode increments
	// the episode counter (the metric), and restart_count is lifetime.
	if observed == StatusCrashed && r.Bot.Status != StatusCrashed {
		episodeStart := r.EpisodeStart
		if episodeStart == nil || now.Sub(*episodeStart) > EpisodeWindow {
			episodeStart = &now
		}
		_, err = s.Pool.Exec(ctx, `
			UPDATE bot_instances SET
				status = 'crashed', unit_state = $2, agent_seen_at = $3,
				restart_count = restart_count + 1,
				episode_restarts = CASE
					WHEN episode_started_at IS NULL OR episode_started_at < $4 THEN 1
					ELSE episode_restarts + 1 END,
				episode_started_at = $5,
				updated_at = now()
			WHERE id = $1`,
			botID, unitState, now, now.Add(-EpisodeWindow), episodeStart)
		return StatusCrashed, err
	}

	if observed == r.Bot.Status {
		// Still refresh unit_state + agent_seen_at (truth age for the UI).
		_, err = s.Pool.Exec(ctx, `UPDATE bot_instances SET unit_state = $2, agent_seen_at = $3, updated_at = now() WHERE id = $1`,
			botID, unitState, now)
		return observed, err
	}

	// Desired-state convergence: a unit observed running while desired is
	// stopped (and vice versa) keeps the DB on the OBSERVED truth; the next
	// reconcile job enforces the desired state.
	if !CanTransition(r.Bot.Status, observed) {
		_, uerr := s.Pool.Exec(ctx, `
			UPDATE bot_instances SET status = $2, unit_state = $3, agent_seen_at = $4, updated_at = now()
			WHERE id = $1`, botID, observed, unitState, now)
		return observed, uerr
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE bot_instances SET status = $2, unit_state = $3, agent_seen_at = $4, updated_at = now()
		WHERE id = $1`, botID, observed, unitState, now)
	return observed, err
}

// MarkStarting stamps the transition into starting (customer-initiated).
func (s *Store) MarkStarting(ctx context.Context, botID uuid.UUID) error {
	r, err := s.getBotRowAny(ctx, botID)
	if err != nil {
		return err
	}
	if r.Bot.Status == StatusStarting || r.Bot.Status == StatusRunning {
		return nil // idempotent double-start
	}
	if !CanTransition(r.Bot.Status, StatusStarting) {
		return &TransitionError{From: r.Bot.Status, To: StatusStarting}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE bot_instances SET status = 'starting', updated_at = now() WHERE id = $1`, botID)
	return err
}

// MarkStopping stamps the transition into stopping (customer-initiated).
func (s *Store) MarkStopping(ctx context.Context, botID uuid.UUID) error {
	r, err := s.getBotRowAny(ctx, botID)
	if err != nil {
		return err
	}
	if r.Bot.Status == StatusStopping || r.Bot.Status == StatusStopped {
		return nil // idempotent double-stop
	}
	if !CanTransition(r.Bot.Status, StatusStopping) {
		return &TransitionError{From: r.Bot.Status, To: StatusStopping}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE bot_instances SET status = 'stopping', updated_at = now() WHERE id = $1`, botID)
	return err
}

// SetDesiredState records what the customer wants (reconciliation target).
func (s *Store) SetDesiredState(ctx context.Context, botID uuid.UUID, desired string) error {
	ct, err := s.Pool.Exec(ctx, `UPDATE bot_instances SET desired_state = $2, updated_at = now() WHERE id = $1 AND status <> 'deleted'`, botID, desired)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetError records the last job failure detail (never secrets — payloads are
// scrubbed before enqueue).
func (s *Store) SetError(ctx context.Context, botID uuid.UUID, msg string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE bot_instances SET last_error = $2, updated_at = now() WHERE id = $1`, botID, msg)
	return err
}

// MarkDeleting moves a live bot into deleting (delete job follows).
func (s *Store) MarkDeleting(ctx context.Context, botID uuid.UUID) error {
	r, err := s.getBotRowAny(ctx, botID)
	if err != nil {
		return err
	}
	if r.Bot.Status == StatusDeleting {
		return nil
	}
	if !CanTransition(r.Bot.Status, StatusDeleting) {
		return &TransitionError{From: r.Bot.Status, To: StatusDeleting}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE bot_instances SET status = 'deleting', updated_at = now() WHERE id = $1`, botID)
	return err
}

// MarkDeleted finalizes deletion.
func (s *Store) MarkDeleted(ctx context.Context, botID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE bot_instances SET status = 'deleted', updated_at = now() WHERE id = $1`, botID)
	return err
}

// MarkFailed records a provisioning/deploy failure (idempotent).
func (s *Store) MarkFailed(ctx context.Context, botID uuid.UUID, msg string) error {
	r, err := s.getBotRowAny(ctx, botID)
	if err != nil {
		return err
	}
	if r.Bot.Status == StatusFailed {
		_, uerr := s.Pool.Exec(ctx, `UPDATE bot_instances SET last_error = $2, updated_at = now() WHERE id = $1`, botID, msg)
		return uerr
	}
	if !CanTransition(r.Bot.Status, StatusFailed) {
		return &TransitionError{From: r.Bot.Status, To: StatusFailed}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE bot_instances SET status = 'failed', last_error = $2, updated_at = now() WHERE id = $1`, botID, msg)
	return err
}

// ListDueSchedules returns enabled schedules whose next run is due.
func (s *Store) ListDueSchedules(ctx context.Context, limit int) ([]BotSchedule, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, bot_id, kind, cron, enabled, last_run_at, next_run_at, created_at
		FROM bot_schedules WHERE enabled AND next_run_at <= now()
		ORDER BY next_run_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotSchedule
	for rows.Next() {
		var sc BotSchedule
		if err := rows.Scan(&sc.ID, &sc.BotID, &sc.Kind, &sc.Cron, &sc.Enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// BotSchedule is one scheduled lifecycle task.
type BotSchedule struct {
	ID        uuid.UUID  `json:"id"`
	BotID     uuid.UUID  `json:"bot_id"`
	Kind      string     `json:"kind"`
	Cron      string     `json:"cron"`
	Enabled   bool       `json:"enabled"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	NextRunAt time.Time  `json:"next_run_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// CreateSchedule adds a schedule; the interval is stored directly (the
// control-plane scheduler sweeps due rows).
func (s *Store) CreateSchedule(ctx context.Context, botID uuid.UUID, kind, cron string, interval time.Duration) (*BotSchedule, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO bot_schedules (bot_id, kind, cron, next_run_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4))
		RETURNING id, bot_id, kind, cron, enabled, last_run_at, next_run_at, created_at`,
		botID, kind, cron, interval.Seconds())
	var sc BotSchedule
	err := row.Scan(&sc.ID, &sc.BotID, &sc.Kind, &sc.Cron, &sc.Enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

// ListSchedules lists a bot's schedules.
func (s *Store) ListSchedules(ctx context.Context, botID uuid.UUID) ([]BotSchedule, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, bot_id, kind, cron, enabled, last_run_at, next_run_at, created_at
		FROM bot_schedules WHERE bot_id = $1 ORDER BY created_at ASC`, botID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BotSchedule
	for rows.Next() {
		var sc BotSchedule
		if err := rows.Scan(&sc.ID, &sc.BotID, &sc.Kind, &sc.Cron, &sc.Enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// DeleteSchedule removes one schedule (org-checked via join).
func (s *Store) DeleteSchedule(ctx context.Context, orgID, scheduleID uuid.UUID) error {
	ct, err := s.Pool.Exec(ctx, `
		DELETE FROM bot_schedules bs USING bot_instances b
		WHERE bs.id = $1 AND bs.bot_id = b.id AND b.organization_id = $2`, scheduleID, orgID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ScheduleFired stamps the run and pushes next_run.
func (s *Store) ScheduleFired(ctx context.Context, scheduleID uuid.UUID, interval time.Duration) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE bot_schedules SET last_run_at = now(), next_run_at = now() + make_interval(secs => $2) WHERE id = $1`,
		scheduleID, interval.Seconds())
	return err
}

// GetBotRowAny resolves the full row without org scoping (internal loops).
func (s *Store) GetBotRowAny(ctx context.Context, botID uuid.UUID) (*BotRow, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+botCols+` FROM bot_instances WHERE id = $1`, botID)
	r, err := scanBot(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// getBotRowAny is the internal alias.
func (s *Store) getBotRowAny(ctx context.Context, botID uuid.UUID) (*BotRow, error) {
	return s.GetBotRowAny(ctx, botID)
}

// SetEnvEnc replaces the encrypted env blob.
func (s *Store) SetEnvEnc(ctx context.Context, botID uuid.UUID, envEnc string) error {
	ct, err := s.Pool.Exec(ctx, `UPDATE bot_instances SET env_enc = $2, updated_at = now() WHERE id = $1 AND status <> 'deleted'`, botID, envEnc)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListActiveBots returns bots the reconciliation loop cares about (anything
// live or transitioning; deleted excluded).
func (s *Store) ListActiveBots(ctx context.Context) ([]Bot, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+botCols+` FROM bot_instances
		WHERE status NOT IN ('deleted', 'deleting') ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bot
	for rows.Next() {
		r, err := scanBot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r.Public())
	}
	return out, rows.Err()
}
