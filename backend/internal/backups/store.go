package backups

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("backup not found")

type Status string

const (
	StatusPending    Status = "pending"
	StatusRunning    Status = "running"
	StatusSuccessful Status = "successful"
	StatusFailed     Status = "failed"
)

// Verification status values (column verification: pending|ok|failed).
const (
	VerifyPending string = "pending"
	VerifyOK      string = "ok"
	VerifyFailed  string = "failed"
)

// Backup is one backup row. SinkRef/Creds stay opaque: ciphertext-at-rest
// columns and sealed blobs only.
type Backup struct {
	ID             uuid.UUID  `json:"id"`
	Organization   uuid.UUID  `json:"organization_id"`
	WebsiteID      *uuid.UUID `json:"website_id,omitempty"`
	TargetID       *uuid.UUID `json:"target_id,omitempty"`
	Type           string     `json:"type"`
	Status         Status     `json:"status"`
	TriggerType    string     `json:"trigger_type"`
	SizeBytes      int64      `json:"size_bytes"`
	SHA256         string     `json:"sha256,omitempty"`
	Encrypted      bool       `json:"encrypted"`
	Verification   string     `json:"verification"`
	SinkKind       string     `json:"sink_kind"`
	SinkRef        string     `json:"sink_ref,omitempty"`
	StoredBytes    int64      `json:"stored_bytes,omitempty"`
	TargetServerID *uuid.UUID `json:"target_server_id,omitempty"`
	Databases      []string   `json:"databases"`
	Error          string     `json:"error,omitempty"`
	CreatedBy      *uuid.UUID `json:"created_by,omitempty"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	VerifiedAt     *time.Time `json:"verified_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

// Cols is the shared SELECT projection.
const cols = `id, organization_id, website_id, target_id, type, status,
	trigger_type, size_bytes, sha256, encrypted, verification, sink_kind, sink_ref,
	stored_bytes, target_server_id, databases, error, created_by, started_at,
	finished_at, verified_at, created_at`

func scanRow(row pgx.Row) (*Backup, error) {
	var b Backup
	var dbs, sinkRef []byte
	var createdBy []byte
	var websiteID, targetID, targetServer []byte
	err := row.Scan(&b.ID, &b.Organization, &websiteID, &targetID,
		&b.Type, &b.Status, &b.TriggerType, &b.SizeBytes, &b.SHA256, &b.Encrypted,
		&b.Verification, &b.SinkKind, &sinkRef, &b.StoredBytes, &targetServer,
		&dbs, &b.Error, &createdBy, &b.StartedAt, &b.FinishedAt, &b.VerifiedAt, &b.CreatedAt)
	if err != nil {
		return nil, err
	}
	b.WebsiteID = nullUUID(websiteID)
	b.TargetID = nullUUID(targetID)
	b.TargetServerID = nullUUID(targetServer)
	if createdBy != nil {
		id, err := uuid.Parse(string(createdBy))
		if err == nil {
			b.CreatedBy = &id
		}
	}
	_ = json.Unmarshal(dbs, &b.Databases)
	b.SinkRef = string(sinkRef)
	return &b, nil
}

// sinkRefJSON is the internal raw sink ref (opaque JSON stored in the row).
// Parsed by SinkRefOf when needed; not exposed over the API.
type sinkRefJSON = json.RawMessage

func nullUUID(b []byte) *uuid.UUID {
	if len(b) != 16 {
		return nil
	}
	id, err := uuid.FromBytes(b)
	if err != nil {
		return nil
	}
	return &id
}

// ---------------------------------------------------------------------------
// Target — a backup destination (sink driver config, credentials sealed).
// ---------------------------------------------------------------------------

type Target struct {
	ID           uuid.UUID `json:"id"`
	Organization uuid.UUID `json:"organization_id"`
	Name         string    `json:"name"`
	Kind         string    `json:"kind"`
	// Config is the public config (no secrets: endpoint/bucket/host/...).
	Config json.RawMessage `json:"config"`
	// CredsEnc is the sealed credentials blob — NEVER returned by the API
	// (write-only). It stays in the struct for the agent payload builder.
	CredsEnc  string    `json:"-"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
}

type targetRow struct {
	Target
	CredsEnc string
}

const targetCols = `id, organization_id, name, kind, config, creds_enc, is_default, created_at`

func scanTarget(row pgx.Row) (*targetRow, error) {
	var t targetRow
	var cfg []byte
	err := row.Scan(&t.ID, &t.Organization, &t.Name, &t.Kind, &cfg, &t.CredsEnc, &t.IsDefault, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	t.Config = cfg
	return &t, nil
}

// ---------------------------------------------------------------------------
// Schedule — cron-driven backups per workload.
// ---------------------------------------------------------------------------

type Schedule struct {
	ID        uuid.UUID  `json:"id"`
	OrgID     uuid.UUID  `json:"organization_id"`
	WebsiteID *uuid.UUID `json:"website_id,omitempty"`
	Type      string     `json:"type"`
	Cron      string     `json:"cron"`
	Enabled   bool       `json:"enabled"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
	NextRunAt time.Time  `json:"next_run_at"`
	CreatedAt time.Time  `json:"created_at"`
}

const scheduleCols = `id, organization_id, website_id, type, cron, enabled, last_run_at, next_run_at, created_at`

func scanSchedule(row pgx.Row) (*Schedule, error) {
	var sc Schedule
	var websiteID []byte
	err := row.Scan(&sc.ID, &sc.OrgID, &websiteID, &sc.Type, &sc.Cron,
		&sc.Enabled, &sc.LastRunAt, &sc.NextRunAt, &sc.CreatedAt)
	if err != nil {
		return nil, err
	}
	sc.WebsiteID = nullUUID(websiteID)
	return &sc, nil
}

// ---------------------------------------------------------------------------
// Store
// ---------------------------------------------------------------------------

type Store struct {
	Pool *pgxpool.Pool
}

// ---------------------------------------------------------------------------
// Backups CRUD (legacy website rows keep working: nullable refs)
// ---------------------------------------------------------------------------

// Create inserts a pending backup row.
func (s *Store) Create(ctx context.Context, b *Backup) (*Backup, error) {
	dbs, _ := json.Marshal(b.Databases)
	sinkRef := b.SinkRef
	if sinkRef == "" {
		sinkRef = "{}" // JSONB-safe default (agent overwrites via outcome)
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO backups (organization_id, website_id, target_id,
			type, status, trigger_type, size_bytes, sha256, encrypted, verification,
			sink_kind, sink_ref, stored_bytes, target_server_id, databases, error, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING `+cols,
		b.Organization, b.WebsiteID, b.TargetID,
		b.Type, b.Status, b.TriggerType, b.SizeBytes, b.SHA256, b.Encrypted,
		b.Verification, b.SinkKind, []byte(sinkRef), b.StoredBytes, b.TargetServerID,
		dbs, b.Error, b.CreatedBy,
	)
	return scanRow(row)
}

// CreateWebsiteLegacy preserves the pre-Phase-11 shape (website-scoped row).
func (s *Store) CreateWebsiteLegacy(ctx context.Context, orgID, websiteID, createdBy uuid.UUID, triggerType string) (*Backup, error) {
	var by *uuid.UUID
	if createdBy != uuid.Nil {
		by = &createdBy
	}
	return s.Create(ctx, &Backup{
		Organization: orgID, WebsiteID: &websiteID, CreatedBy: by,
		Type: TypeWebsite, Status: StatusPending, TriggerType: triggerType,
		Encrypted: false, Verification: VerifyPending, Databases: []string{},
	})
}

func (s *Store) MarkRunning(ctx context.Context, backupID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE backups SET status = 'running', started_at = now() WHERE id = $1`, backupID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkSuccessful records a finished backup (bytes = plaintext size;
// storedBytes = post-encryption size when encrypted).
func (s *Store) MarkSuccessful(ctx context.Context, backupID uuid.UUID, bytes, storedBytes int64, sha string, databases []string) error {
	dbs, _ := json.Marshal(databases)
	tag, err := s.Pool.Exec(ctx, `
		UPDATE backups SET status = 'successful', size_bytes = $2, stored_bytes = $3,
			sha256 = $4, databases = $5, finished_at = now(), verification = CASE WHEN $6::text = '' THEN verification ELSE 'pending' END
		WHERE id = $1
	`, backupID, bytes, storedBytes, sha, dbs, "")
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MarkFailed(ctx context.Context, backupID uuid.UUID, errMsg string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE backups SET status = 'failed', error = $2, finished_at = now() WHERE id = $1`, backupID, errMsg)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSink records where the artifact landed (kind + opaque ref + sealed key).
func (s *Store) SetSink(ctx context.Context, backupID uuid.UUID, kind, ref string, keyEnc string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE backups SET sink_kind = $2, sink_ref = $3, key_enc = $4 WHERE id = $1
	`, backupID, kind, ref, keyEnc)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetVerification records a checksum verification result.
func (s *Store) SetVerification(ctx context.Context, backupID uuid.UUID, status string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE backups SET verification = $2, verified_at = CASE WHEN $2 = 'ok' THEN now() ELSE verified_at END
		WHERE id = $1
	`, backupID, status)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkEncrypted flips the encrypted flag once the sealed data key is stored.
func (s *Store) MarkEncrypted(ctx context.Context, backupID uuid.UUID) error {
	_, err := s.Pool.Exec(ctx, `UPDATE backups SET encrypted = TRUE WHERE id = $1`, backupID)
	return err
}

func (s *Store) GetByID(ctx context.Context, orgID, backupID uuid.UUID) (*Backup, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM backups WHERE id = $1 AND organization_id = $2`, backupID, orgID)
	b, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

func (s *Store) GetAny(ctx context.Context, backupID uuid.UUID) (*Backup, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+cols+` FROM backups WHERE id = $1`, backupID)
	b, err := scanRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

func (s *Store) ListForWebsite(ctx context.Context, websiteID uuid.UUID, limit int) ([]Backup, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM backups WHERE website_id = $1 ORDER BY created_at DESC LIMIT $2`, websiteID, limit)
	return scanBackups(rows, err)
}

// ListForOrg lists org backups across all workload types.
func (s *Store) ListForOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]Backup, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+cols+` FROM backups WHERE organization_id = $1 ORDER BY created_at DESC LIMIT $2`, orgID, limit)
	return scanBackups(rows, err)
}

func scanBackups(rows pgx.Rows, err error) ([]Backup, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Backup
	for rows.Next() {
		b, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *b)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Retention: plan count + time-based pruning (idempotent).
// ---------------------------------------------------------------------------

// Pruned is one removed backup (used for the prune event + sink delete).
type Pruned struct {
	ID       uuid.UUID `json:"id"`
	SinkKind string    `json:"sink_kind"`
	SinkRef  string    `json:"sink_ref"`
	KeyEnc   string    `json:"key_enc"`
	Type     string    `json:"type"`
}

// scanPrunedRows is the raw scan with internal columns (incl. key_enc).
func scanPrunedRows(rows pgx.Rows, err error) ([]Pruned, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pruned
	for rows.Next() {
		var p Pruned
		var ref []byte
		if err := rows.Scan(&p.ID, &p.SinkKind, &ref, &p.KeyEnc, &p.Type); err != nil {
			return nil, err
		}
		p.SinkRef = string(ref)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PruneExcessForOrg enforces the plan's Backups count across the org's
// successful backups, oldest first (Phase 9 Backups: N).
func (s *Store) PruneExcessForOrg(ctx context.Context, orgID uuid.UUID, keep int) ([]Pruned, error) {
	if keep <= 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		DELETE FROM backups
		WHERE organization_id = $1 AND status = 'successful'
		  AND id NOT IN (
			SELECT id FROM backups WHERE organization_id = $1 AND status = 'successful'
			ORDER BY created_at DESC LIMIT $2
		  )
		RETURNING id, COALESCE(sink_kind,''), COALESCE(sink_ref,''), COALESCE(key_enc,''), type
	`, orgID, keep)
	return scanPrunedRows(rows, err)
}

// PruneExcessForWebsite keeps the legacy per-website retention.
func (s *Store) PruneExcessForWebsite(ctx context.Context, websiteID uuid.UUID, keep int) ([]Pruned, error) {
	if keep <= 0 {
		return nil, nil
	}
	rows, err := s.Pool.Query(ctx, `
		DELETE FROM backups
		WHERE website_id = $1 AND status = 'successful'
		  AND id NOT IN (
			SELECT id FROM backups WHERE website_id = $1 AND status = 'successful'
			ORDER BY created_at DESC LIMIT $2
		  )
		RETURNING id, COALESCE(sink_kind,''), COALESCE(sink_ref,''), COALESCE(key_enc,''), type
	`, websiteID, keep)
	return scanPrunedRows(rows, err)
}

// PruneOlderThan deletes successful backups older than the given age (time
// prune). Returns what was removed (idempotent: re-run deletes nothing).
func (s *Store) PruneOlderThan(ctx context.Context, orgID uuid.UUID, olderThan time.Duration) ([]Pruned, error) {
	rows, err := s.Pool.Query(ctx, `
		DELETE FROM backups
		WHERE organization_id = $1 AND status = 'successful' AND created_at < now() - $2::interval
		RETURNING id, COALESCE(sink_kind,''), COALESCE(sink_ref,''), COALESCE(key_enc,''), type
	`, orgID, formatInterval(olderThan))
	return scanPrunedRows(rows, err)
}

func formatInterval(d time.Duration) string {
	return "interval '" + d.Truncate(time.Minute).String() + "'"
}

// PruneFailedOlderThan drops failed backup rows after a day (row hygiene).
func (s *Store) PruneFailedOlderThan(ctx context.Context, age time.Duration) (int64, error) {
	tag, err := s.Pool.Exec(ctx,
		`DELETE FROM backups WHERE status = 'failed' AND created_at < now() - $1::interval`,
		formatInterval(age))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---------------------------------------------------------------------------
// Health (WHM backup health: success/failure rates)
// ---------------------------------------------------------------------------

// Health aggregates backup outcomes for an org over a window.
type Health struct {
	Total      int64 `json:"total"`
	Successful int64 `json:"successful"`
	Failed     int64 `json:"failed"`
	Pending    int64 `json:"pending"`
}

func (h Health) SuccessRate() float64 {
	done := h.Successful + h.Failed
	if done == 0 {
		return 0
	}
	return float64(h.Successful) / float64(done) * 100
}

func (s *Store) HealthForOrg(ctx context.Context, orgID uuid.UUID, window time.Duration) (Health, error) {
	var h Health
	err := s.Pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'successful'),
		       count(*) FILTER (WHERE status = 'failed'),
		       count(*) FILTER (WHERE status IN ('pending','running'))
		FROM backups WHERE organization_id = $1 AND created_at > now() - $2::interval
	`, orgID, formatInterval(window)).Scan(&h.Total, &h.Successful, &h.Failed, &h.Pending)
	return h, err
}

// ---------------------------------------------------------------------------
// Targets CRUD (credentials sealed; never selected into API responses)
// ---------------------------------------------------------------------------

// TargetInput is the exported create shape (API layer cannot build the
// cred-carrying row type directly).
type TargetInput struct {
	OrgID     uuid.UUID
	Name      string
	Kind      string
	Config    json.RawMessage
	CredsEnc  string
	IsDefault bool
}

// CreateTargetInput creates one target row from the exported input shape.
func (s *Store) CreateTargetInput(ctx context.Context, in TargetInput) (*Target, error) {
	return s.CreateTarget(ctx, &targetRow{
		Target: Target{Organization: in.OrgID, Name: in.Name, Kind: in.Kind,
			Config: in.Config, IsDefault: in.IsDefault},
		CredsEnc: in.CredsEnc,
	})
}

func (s *Store) CreateTarget(ctx context.Context, t *targetRow) (*Target, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO backup_targets (organization_id, name, kind, config, creds_enc, is_default)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING `+targetCols,
		t.Organization, t.Name, t.Kind, []byte(t.Config), t.CredsEnc, t.IsDefault,
	)
	r, err := scanTarget(row)
	if err != nil {
		return nil, err
	}
	return &r.Target, nil
}

func (s *Store) ListTargets(ctx context.Context, orgID uuid.UUID) ([]Target, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+targetCols+` FROM backup_targets WHERE organization_id = $1 ORDER BY created_at ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t.Target)
	}
	return out, rows.Err()
}

func (s *Store) GetTarget(ctx context.Context, orgID, targetID uuid.UUID) (*targetRow, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+targetCols+` FROM backup_targets WHERE id = $1 AND organization_id = $2`, targetID, orgID)
	t, err := scanTarget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// GetTargetAny resolves a target without org scoping (agent payload path).
func (s *Store) GetTargetAny(ctx context.Context, targetID uuid.UUID) (*targetRow, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+targetCols+` FROM backup_targets WHERE id = $1`, targetID)
	t, err := scanTarget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

func (s *Store) UpdateTarget(ctx context.Context, t *targetRow) (*Target, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE backup_targets SET name = $2, kind = $3, config = $4,
			creds_enc = CASE WHEN $5 = '' THEN creds_enc ELSE $5 END, is_default = $6
		WHERE id = $1 AND organization_id = $7
		RETURNING `+targetCols,
		t.ID, t.Name, t.Kind, []byte(t.Config), t.CredsEnc, t.IsDefault, t.Organization,
	)
	r, err := scanTarget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r.Target, nil
}

func (s *Store) DeleteTarget(ctx context.Context, orgID, targetID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM backup_targets WHERE id = $1 AND organization_id = $2`, targetID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DefaultTarget returns the org's default target (or nil when local-only).
func (s *Store) DefaultTarget(ctx context.Context, orgID uuid.UUID) (*targetRow, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+targetCols+` FROM backup_targets WHERE organization_id = $1 AND is_default ORDER BY created_at ASC LIMIT 1`, orgID)
	t, err := scanTarget(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return t, err
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

func (s *Store) CreateSchedule(ctx context.Context, sc *Schedule) (*Schedule, error) {
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO backup_schedules (organization_id, website_id, type, cron, enabled, next_run_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING `+scheduleCols,
		sc.OrgID, sc.WebsiteID, sc.Type, sc.Cron, sc.Enabled, sc.NextRunAt,
	)
	return scanSchedule(row)
}

func (s *Store) ListSchedules(ctx context.Context, orgID uuid.UUID) ([]Schedule, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+scheduleCols+` FROM backup_schedules WHERE organization_id = $1 ORDER BY created_at ASC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sc)
	}
	return out, rows.Err()
}

func (s *Store) GetSchedule(ctx context.Context, orgID, scheduleID uuid.UUID) (*Schedule, error) {
	row := s.Pool.QueryRow(ctx, `SELECT `+scheduleCols+` FROM backup_schedules WHERE id = $1 AND organization_id = $2`, scheduleID, orgID)
	sc, err := scanSchedule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return sc, err
}

func (s *Store) DeleteSchedule(ctx context.Context, orgID, scheduleID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM backup_schedules WHERE id = $1 AND organization_id = $2`, scheduleID, orgID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DueSchedule is one schedule whose next_run_at has passed (sweeper input).
type DueSchedule struct {
	Schedule Schedule
	ServerID uuid.UUID
}

// DueForScheduleRow is scheduler.go's legacy shape: a due website schedule.
type DueForScheduleRow struct {
	OrganizationID uuid.UUID
	WebsiteID      uuid.UUID
	ServerID       uuid.UUID
	ScheduleID     uuid.UUID
	Cron           string
}

// DueForSchedule returns enabled, due website schedules for the scheduler.
func (s *Store) DueForSchedule(ctx context.Context, limit int) ([]DueForScheduleRow, error) {
	due, err := s.DueSchedules(ctx, limit)
	if err != nil {
		return nil, err
	}
	var out []DueForScheduleRow
	for _, d := range due {
		if d.Schedule.WebsiteID == nil {
			continue
		}
		out = append(out, DueForScheduleRow{
			OrganizationID: d.Schedule.OrgID,
			WebsiteID:      *d.Schedule.WebsiteID,
			ServerID:       d.ServerID,
			ScheduleID:     d.Schedule.ID,
			Cron:           d.Schedule.Cron,
		})
	}
	return out, nil
}

// CreateSystem enqueues a system-triggered (scheduled) website backup row.
func (s *Store) CreateSystem(ctx context.Context, orgID, websiteID, serverID uuid.UUID, triggerType string) (*Backup, error) {
	return s.Create(ctx, &Backup{
		Organization: orgID, WebsiteID: &websiteID,
		Type: TypeWebsite, Status: StatusPending, TriggerType: triggerType,
		Encrypted: false, Verification: VerifyPending, Databases: []string{},
		TargetServerID: &serverID,
	})
}

func (s *Store) DueSchedules(ctx context.Context, limit int) ([]DueSchedule, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT bsc.id, bsc.organization_id, bsc.website_id, bsc.type, bsc.cron,
		       bsc.enabled, bsc.last_run_at, bsc.next_run_at, bsc.created_at,
		       w.server_id AS server_id
		FROM backup_schedules bsc
		JOIN websites w ON w.id = bsc.website_id
		WHERE bsc.enabled AND bsc.next_run_at <= now()
		  AND w.server_id IS NOT NULL
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DueSchedule
	for rows.Next() {
		var d DueSchedule
		var websiteID []byte
		err := rows.Scan(&d.Schedule.ID, &d.Schedule.OrgID, &websiteID,
			&d.Schedule.Type, &d.Schedule.Cron, &d.Schedule.Enabled,
			&d.Schedule.LastRunAt, &d.Schedule.NextRunAt, &d.Schedule.CreatedAt,
			&d.ServerID)
		if err != nil {
			return nil, err
		}
		d.Schedule.WebsiteID = nullUUID(websiteID)
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkScheduleFired bumps last_run_at and seeds the next run from now.
func (s *Store) MarkScheduleFired(ctx context.Context, scheduleID uuid.UUID, next time.Time) error {
	_, err := s.Pool.Exec(ctx,
		`UPDATE backup_schedules SET last_run_at = now(), next_run_at = $2 WHERE id = $1`, scheduleID, next)
	return err
}

// ---------------------------------------------------------------------------
// Key material (per-backup data keys, sealed at rest)
// ---------------------------------------------------------------------------

// SetKeyEnc stores the wrapped data key (called by the agent outcome path).
func (s *Store) SetKeyEnc(ctx context.Context, backupID uuid.UUID, keyEnc string) error {
	return s.SetSinkKeyOnly(ctx, backupID, keyEnc)
}

func (s *Store) SetSinkKeyOnly(ctx context.Context, backupID uuid.UUID, keyEnc string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE backups SET key_enc = $2 WHERE id = $1`, backupID, keyEnc)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// KeyEncFor reads the wrapped data key for one backup (agent payload path).
func (s *Store) KeyEncFor(ctx context.Context, backupID uuid.UUID) (string, error) {
	var keyEnc string
	err := s.Pool.QueryRow(ctx, `SELECT COALESCE(key_enc,'') FROM backups WHERE id = $1`, backupID).Scan(&keyEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return keyEnc, err
}
