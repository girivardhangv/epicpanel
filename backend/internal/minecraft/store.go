// MinecraftInstance store + lifecycle state machine (Phase 7).
//
// Verbatim state machine: installing → stopped → starting → running →
// stopping → crashed, plus failed/deleting/deleted. One Transition guard
// protects every edge; reconciliation applies AGENT truth (the systemd unit
// state observed on the node), never assuming an API acceptance succeeded.
package minecraft

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound     = errors.New("minecraft instance not found")
	ErrNameTaken    = errors.New("minecraft instance name already in use")
	ErrIllegalState = errors.New("illegal state transition")
)

// Instance is one Minecraft instance row (secrets NEVER in plaintext).
type Instance struct {
	ID            uuid.UUID  `json:"id"`
	OrgID         uuid.UUID  `json:"organization_id"`
	ServerID      uuid.UUID  `json:"server_id"`
	CreatedBy     uuid.UUID  `json:"created_by"`
	Name          string     `json:"name"`
	Provider      string     `json:"provider"`
	Version       string     `json:"version"`
	JavaMajor     int        `json:"java_major"`
	Status        Status     `json:"status"`
	DesiredState  string     `json:"desired_state"`
	Port          int        `json:"port"`
	RCONPort      int        `json:"rcon_port"`
	XmxMB         int64      `json:"xmx_mb"`
	ExtraArgs     []string   `json:"extra_args,omitempty"`
	RestartPolicy string     `json:"restart_policy"`
	MaxRestarts   int        `json:"max_restarts"`
	RestartCount  int        `json:"restart_count"`
	PropertyKeys  []string   `json:"property_keys"`
	LastError     string     `json:"last_error,omitempty"`
	UnitState     string     `json:"unit_state,omitempty"`
	AgentSeenAt   *time.Time `json:"agent_seen_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Row is the full row (ciphertext included) for internal callers (job
// payload assembly). NEVER serialized to a response.
type Row struct {
	Instance
	RCONPassEnc     string
	EpisodeRestarts int
	EpisodeStart    *time.Time
}

// Public returns the API-safe projection (no ciphertext).
func (r *Row) Public() *Instance { i := r.Instance; return &i }

const instanceCols = `id, organization_id, server_id, created_by, name, provider, version, java_major,
	status, desired_state, port, rcon_port, xmx_mb, extra_args,
	restart_policy, max_restarts, episode_restarts, episode_started_at, restart_count,
	properties, rcon_pass_enc, last_error, unit_state, agent_seen_at, created_at, updated_at`

func scanInstance(row pgx.Row) (*Row, error) {
	var r Row
	var propsJSON []byte
	err := row.Scan(&r.ID, &r.OrgID, &r.ServerID, &r.CreatedBy, &r.Name,
		&r.Provider, &r.Version, &r.JavaMajor,
		&r.Status, &r.DesiredState, &r.Port, &r.RCONPort, &r.XmxMB, &r.ExtraArgs,
		&r.RestartPolicy, &r.MaxRestarts, &r.EpisodeRestarts, &r.EpisodeStart, &r.RestartCount,
		&propsJSON, &r.RCONPassEnc, &r.LastError, &r.UnitState, &r.AgentSeenAt,
		&r.CreatedAt, &r.UpdatedAt)
	if err != nil {
		return nil, err
	}
	r.PropertyKeys = propertyKeys(propsJSON)
	return &r, nil
}

// propertyKeys derives the visible keys from the stored properties JSON
// (values of server.properties are customer config, not secrets, but the
// panel-managed ones never echo back; the RCON password is NOT in
// properties as far as the API is concerned — it is separate ciphertext).
func propertyKeys(propsJSON []byte) []string {
	props := map[string]string{}
	if len(propsJSON) > 0 {
		_ = jsonUnmarshalProps(propsJSON, &props)
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

// Store persists Minecraft instances.
type Store struct{ Pool *pgxpool.Pool }

// Create inserts an instance in installing state.
func (s *Store) Create(ctx context.Context, orgID, serverID, createdBy uuid.UUID,
	name, provider, version string, javaMajor int, port, rconPort int, xmxMB int64,
	restartPolicy string, maxRestarts int, propsJSON []byte, rconPassEnc string) (*Instance, error) {

	if restartPolicy == "" {
		restartPolicy = "on-failure"
	}
	if maxRestarts <= 0 {
		maxRestarts = 5
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO minecraft_instances
			(organization_id, server_id, created_by, name, provider, version, java_major,
			 status, desired_state, port, rcon_port, xmx_mb, restart_policy, max_restarts,
			 properties, rcon_pass_enc)
		VALUES ($1,$2,$3,$4,$5,$6,$7,'installing','stopped',$8,$9,$10,$11,$12,$13,$14)
		RETURNING `+instanceCols,
		orgID, serverID, createdBy, name, provider, version, javaMajor,
		port, rconPort, xmxMB, restartPolicy, maxRestarts, propsJSON, rconPassEnc)
	r, err := scanInstance(row)
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

// GetByID resolves an instance org-scoped. Cross-tenant access = not found.
func (s *Store) GetByID(ctx context.Context, orgID, id uuid.UUID) (*Instance, error) {
	r, err := s.getRow(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return r.Public(), nil
}

// GetRow returns the full row (ciphertext included) for internal use.
func (s *Store) GetRow(ctx context.Context, orgID, id uuid.UUID) (*Row, error) {
	return s.getRow(ctx, orgID, id)
}

func (s *Store) getRow(ctx context.Context, orgID, id uuid.UUID) (*Row, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+instanceCols+` FROM minecraft_instances WHERE id = $1 AND organization_id = $2`, id, orgID)
	r, err := scanInstance(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// GetByIDAny resolves without org scoping (internal job fanout only).
func (s *Store) GetByIDAny(ctx context.Context, id uuid.UUID) (*Instance, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+instanceCols+` FROM minecraft_instances WHERE id = $1`, id)
	r, err := scanInstance(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r.Public(), err
}

// GetRowAny resolves the full row without org scoping (internal loops).
func (s *Store) GetRowAny(ctx context.Context, id uuid.UUID) (*Row, error) {
	return s.getRowAny(ctx, id)
}

// ListForOrg lists the org's instances (deleted excluded).
func (s *Store) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]Instance, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+instanceCols+` FROM minecraft_instances WHERE organization_id = $1 AND status <> 'deleted' ORDER BY created_at ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		r, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r.Public())
	}
	return out, rows.Err()
}

// CountForOrg counts live instances (create-time plan gate).
func (s *Store) CountForOrg(ctx context.Context, orgID uuid.UUID) (int, error) {
	var n int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM minecraft_instances WHERE organization_id = $1 AND status <> 'deleted'`, orgID).Scan(&n)
	return n, err
}

// UpdateConfig rewrites the mutable desired-state fields (startup config,
// version, JVM heap, extra args, properties).
func (s *Store) UpdateConfig(ctx context.Context, id uuid.UUID,
	version string, javaMajor int, xmxMB int64, extraArgs []string, propsJSON []byte) error {
	ct, err := s.Pool.Exec(ctx, `
		UPDATE minecraft_instances SET
			version = $2, java_major = $3, xmx_mb = $4, extra_args = $5, properties = $6, updated_at = now()
		WHERE id = $1 AND status <> 'deleted'`,
		id, version, javaMajor, xmxMB, extraArgs, propsJSON)
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
func (s *Store) SetStatus(ctx context.Context, id uuid.UUID, to Status, detail string) error {
	var cur Status
	err := s.Pool.QueryRow(ctx, `SELECT status FROM minecraft_instances WHERE id = $1`, id).Scan(&cur)
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
		UPDATE minecraft_instances SET status = $2,
			last_error = CASE WHEN $3 = '' THEN last_error ELSE $3 END, updated_at = now()
		WHERE id = $1`, id, to, detail)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReconcileAgentTruth applies the observed unit state. Unlike SetStatus it
// may move crashed→starting etc. without a customer action, and it
// maintains the crash episode counters (restart policy + restart count).
// Returns the applied status.
func (s *Store) ReconcileAgentTruth(ctx context.Context, id uuid.UUID, unitState string) (Status, error) {
	r, err := s.getRowAny(ctx, id)
	if err != nil {
		return "", err
	}
	observed := AgentStatus(unitState, r.DesiredState)
	now := time.Now().UTC()

	// Crash episode bookkeeping: a crash inside an open episode increments
	// the episode counter (the metric), restart_count is lifetime.
	if observed == StatusCrashed && r.Status != StatusCrashed {
		episodeStart := r.EpisodeStart
		if episodeStart == nil || now.Sub(*episodeStart) > EpisodeWindow {
			episodeStart = &now
		}
		_, err = s.Pool.Exec(ctx, `
			UPDATE minecraft_instances SET
				status = 'crashed', unit_state = $2, agent_seen_at = $3,
				restart_count = restart_count + 1,
				episode_restarts = CASE
					WHEN episode_started_at IS NULL OR episode_started_at < $4 THEN 1
					ELSE episode_restarts + 1 END,
				episode_started_at = $5,
				updated_at = now()
			WHERE id = $1`,
			id, unitState, now, now.Add(-EpisodeWindow), episodeStart)
		return StatusCrashed, err
	}

	if observed == r.Status {
		_, err = s.Pool.Exec(ctx, `UPDATE minecraft_instances SET unit_state = $2, agent_seen_at = $3, updated_at = now() WHERE id = $1`,
			id, unitState, now)
		return observed, err
	}

	if !CanTransition(r.Status, observed) {
		_, uerr := s.Pool.Exec(ctx, `
			UPDATE minecraft_instances SET status = $2, unit_state = $3, agent_seen_at = $4, updated_at = now()
			WHERE id = $1`, id, observed, unitState, now)
		return observed, uerr
	}
	_, err = s.Pool.Exec(ctx, `
		UPDATE minecraft_instances SET status = $2, unit_state = $3, agent_seen_at = $4, updated_at = now()
		WHERE id = $1`, id, observed, unitState, now)
	return observed, err
}

// MarkStarting stamps the transition into starting (customer-initiated).
func (s *Store) MarkStarting(ctx context.Context, id uuid.UUID) error {
	r, err := s.getRowAny(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == StatusStarting || r.Status == StatusRunning {
		return nil // idempotent double-start
	}
	if !CanTransition(r.Status, StatusStarting) {
		return &TransitionError{From: r.Status, To: StatusStarting}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE minecraft_instances SET status = 'starting', updated_at = now() WHERE id = $1`, id)
	return err
}

// MarkStopping stamps the transition into stopping (customer-initiated).
func (s *Store) MarkStopping(ctx context.Context, id uuid.UUID) error {
	r, err := s.getRowAny(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == StatusStopping || r.Status == StatusStopped {
		return nil // idempotent double-stop
	}
	if !CanTransition(r.Status, StatusStopping) {
		return &TransitionError{From: r.Status, To: StatusStopping}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE minecraft_instances SET status = 'stopping', updated_at = now() WHERE id = $1`, id)
	return err
}

// SetDesiredState records what the customer wants (reconciliation target).
func (s *Store) SetDesiredState(ctx context.Context, id uuid.UUID, desired string) error {
	ct, err := s.Pool.Exec(ctx, `UPDATE minecraft_instances SET desired_state = $2, updated_at = now() WHERE id = $1 AND status <> 'deleted'`, id, desired)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetError records the last job failure detail.
func (s *Store) SetError(ctx context.Context, id uuid.UUID, msg string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE minecraft_instances SET last_error = $2, updated_at = now() WHERE id = $1`, id, msg)
	return err
}

// MarkDeleting moves a live instance into deleting (delete job follows).
func (s *Store) MarkDeleting(ctx context.Context, id uuid.UUID) error {
	r, err := s.getRowAny(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == StatusDeleting {
		return nil
	}
	if !CanTransition(r.Status, StatusDeleting) {
		return &TransitionError{From: r.Status, To: StatusDeleting}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE minecraft_instances SET status = 'deleting', updated_at = now() WHERE id = $1`, id)
	return err
}

// MarkDeleted finalizes deletion.
func (s *Store) MarkDeleted(ctx context.Context, id uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE minecraft_instances SET status = 'deleted', updated_at = now() WHERE id = $1`, id)
	return err
}

// MarkFailed records a provisioning failure (idempotent).
func (s *Store) MarkFailed(ctx context.Context, id uuid.UUID, msg string) error {
	r, err := s.getRowAny(ctx, id)
	if err != nil {
		return err
	}
	if r.Status == StatusFailed {
		_, uerr := s.Pool.Exec(ctx, `UPDATE minecraft_instances SET last_error = $2, updated_at = now() WHERE id = $1`, id, msg)
		return uerr
	}
	if !CanTransition(r.Status, StatusFailed) {
		return &TransitionError{From: r.Status, To: StatusFailed}
	}
	_, err = s.Pool.Exec(ctx, `UPDATE minecraft_instances SET status = 'failed', last_error = $2, updated_at = now() WHERE id = $1`, id, msg)
	return err
}

// ListActiveInstances returns instances the reconciliation loop cares about.
func (s *Store) ListActiveInstances(ctx context.Context) ([]Instance, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+instanceCols+` FROM minecraft_instances
		WHERE status NOT IN ('deleted', 'deleting') ORDER BY updated_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		r, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r.Public())
	}
	return out, rows.Err()
}

func (s *Store) getRowAny(ctx context.Context, id uuid.UUID) (*Row, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+instanceCols+` FROM minecraft_instances WHERE id = $1`, id)
	r, err := scanInstance(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ---------------------------------------------------------------------------
// Schedules (restart/start/stop only — scheduled CUSTOM commands would be
// arbitrary console abuse and are rejected by design; command schedules go
// through the same allowlist at fire time).
// ---------------------------------------------------------------------------

// Schedule is one scheduled lifecycle task.
type Schedule struct {
	ID         uuid.UUID  `json:"id"`
	InstanceID uuid.UUID  `json:"instance_id"`
	Kind       string     `json:"kind"`
	Cron       string     `json:"cron"`
	Command    string     `json:"command,omitempty"`
	Enabled    bool       `json:"enabled"`
	LastRunAt  *time.Time `json:"last_run_at,omitempty"`
	NextRunAt  time.Time  `json:"next_run_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ListDueSchedules returns enabled schedules whose next run is due.
func (s *Store) ListDueSchedules(ctx context.Context, limit int) ([]Schedule, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, instance_id, kind, cron, command, enabled, last_run_at, next_run_at, created_at
		FROM minecraft_schedules WHERE enabled AND next_run_at <= now()
		ORDER BY next_run_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		if err := rows.Scan(&sc.ID, &sc.InstanceID, &sc.Kind, &sc.Cron, &sc.Command, &sc.Enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// CreateSchedule adds a schedule (kind restart/start/stop/command; command
// must already pass the allowlist).
func (s *Store) CreateSchedule(ctx context.Context, instanceID uuid.UUID, kind, cron, command string, interval time.Duration) (*Schedule, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO minecraft_schedules (instance_id, kind, cron, command, next_run_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))
		RETURNING id, instance_id, kind, cron, command, enabled, last_run_at, next_run_at, created_at`,
		instanceID, kind, cron, command, interval.Seconds())
	var sc Schedule
	err := row.Scan(&sc.ID, &sc.InstanceID, &sc.Kind, &sc.Cron, &sc.Command, &sc.Enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &sc, nil
}

// ListSchedules lists an instance's schedules.
func (s *Store) ListSchedules(ctx context.Context, instanceID uuid.UUID) ([]Schedule, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, instance_id, kind, cron, command, enabled, last_run_at, next_run_at, created_at
		FROM minecraft_schedules WHERE instance_id = $1 ORDER BY created_at ASC`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		if err := rows.Scan(&sc.ID, &sc.InstanceID, &sc.Kind, &sc.Cron, &sc.Command, &sc.Enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// DeleteSchedule removes one schedule (org-checked via join).
func (s *Store) DeleteSchedule(ctx context.Context, orgID, scheduleID uuid.UUID) error {
	ct, err := s.Pool.Exec(ctx, `
		DELETE FROM minecraft_schedules ms USING minecraft_instances mi
		WHERE ms.id = $1 AND ms.instance_id = mi.id AND mi.organization_id = $2`, scheduleID, orgID)
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
		UPDATE minecraft_schedules SET last_run_at = now(), next_run_at = now() + make_interval(secs => $2) WHERE id = $1`,
		scheduleID, interval.Seconds())
	return err
}

// ---------------------------------------------------------------------------
// World backups (agent-side world-scoped ops; the full backup engine is
// Phase 11 — these rows are the seams).
// ---------------------------------------------------------------------------

// WorldBackup is one completed world snapshot.
type WorldBackup struct {
	ID         uuid.UUID `json:"id"`
	InstanceID uuid.UUID `json:"instance_id"`
	Name       string    `json:"name"`
	SizeBytes  int64     `json:"size_bytes"`
	SHA256     string    `json:"sha256"`
	CreatedAt  time.Time `json:"created_at"`
}

// ListWorldBackups lists an instance's world backups.
func (s *Store) ListWorldBackups(ctx context.Context, instanceID uuid.UUID) ([]WorldBackup, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, instance_id, name, size_bytes, sha256, created_at
		FROM minecraft_world_backups WHERE instance_id = $1 ORDER BY created_at DESC LIMIT 100`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WorldBackup
	for rows.Next() {
		var b WorldBackup
		if err := rows.Scan(&b.ID, &b.InstanceID, &b.Name, &b.SizeBytes, &b.SHA256, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// CreateWorldBackup records a completed snapshot (idempotent per name).
func (s *Store) CreateWorldBackup(ctx context.Context, instanceID uuid.UUID, name string, sizeBytes int64, sha256 string) (*WorldBackup, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO minecraft_world_backups (instance_id, name, size_bytes, sha256)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (instance_id, name) DO UPDATE SET size_bytes = $3, sha256 = $4, created_at = now()
		RETURNING id, instance_id, name, size_bytes, sha256, created_at`,
		instanceID, name, sizeBytes, sha256)
	var b WorldBackup
	err := row.Scan(&b.ID, &b.InstanceID, &b.Name, &b.SizeBytes, &b.SHA256, &b.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// GetWorldBackup resolves one snapshot org-scoped.
func (s *Store) GetWorldBackup(ctx context.Context, orgID, backupID uuid.UUID) (*WorldBackup, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT b.id, b.instance_id, b.name, b.size_bytes, b.sha256, b.created_at
		FROM minecraft_world_backups b
		JOIN minecraft_instances mi ON mi.id = b.instance_id
		WHERE b.id = $1 AND mi.organization_id = $2`, backupID, orgID)
	var b WorldBackup
	err := row.Scan(&b.ID, &b.InstanceID, &b.Name, &b.SizeBytes, &b.SHA256, &b.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ---------------------------------------------------------------------------
// Port allocation via internal/ports (Range) + DB-driven used set. Minecraft
// gets its own range so instance ports never collide with web backend ports.
// ---------------------------------------------------------------------------

// PortRange returns the Minecraft server port range (env-configurable).
func PortRange() (min, max int) {
	min, max = 25565, 25665 // conventional MC range, 100 ports per node
	return
}

// AllocPort picks the lowest free port in the Minecraft range for a server
// (usedSet comes from the DB — control-plane authoritative).
func AllocPort(used map[int]bool) (int, bool) {
	for p := 25565; p <= 25665; p++ {
		if !used[p] {
			return p, true
		}
	}
	return 0, false
}

// AllocRCONPort picks the lowest free RCON port (separate range).
func AllocRCONPort(used map[int]bool) (int, bool) {
	for p := 25765; p <= 25865; p++ {
		if !used[p] {
			return p, true
		}
	}
	return 0, false
}

// sortStrings sorts a small string slice in place (insertion).
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
