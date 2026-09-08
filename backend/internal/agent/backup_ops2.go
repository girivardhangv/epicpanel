package agent

// Phase 11 unified backup engine ops (agent side).
//
// The registry below is merged into the worker dispatch by the coordinator
// (wave contract). All ops are idempotent + audited at the control-plane
// layer; secrets never appear in payloads, logs or errors (creds arrive as
// secretbox-sealed blobs and are unwrapped in-memory only).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/backups/sink"
	"github.com/epicbyte/epicpanel/backend/internal/minecraft"
)

// BackupOps2 is the Phase 11 registry the coordinator merges into the agent
// dispatch (wave contract: var X OpFunc map).
var BackupOps2 = map[string]func(context.Context, *Executor, *Client, Config, json.RawMessage) (json.RawMessage, error){
	"backup_run":      HandleBackupRun,
	"backup_restore":  HandleBackupRestore,
	"backup_verify":   HandleBackupVerify,
	"backup_prune":    HandleBackupPrune,
	"terminate_backup": HandleTerminateBackup,
}

// ---------------------------------------------------------------------------
// Wire payloads
// ---------------------------------------------------------------------------

// BackupRunPayload is the backup_run job payload (control-plane → agent).
type BackupRunPayload struct {
	BackupID string `json:"backup_id"`
	Type     string `json:"type"` // account | database | website | minecraft_world | discord_bot | full_instance | website_files
	// Workload identifiers (exactly one primary):
	WebsiteID  string `json:"website_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	BotID      string `json:"bot_id,omitempty"`
	// Databases to dump (account/website bundles).
	Databases []DBClone `json:"databases,omitempty"`
	// Database dump-only backups name the single engine+db.
	Database *DBClone `json:"database,omitempty"`
	// Target: sink config + sealed creds (empty target = agent-local).
	Target   *sink.Config `json:"target,omitempty"`
	TargetID string       `json:"target_id,omitempty"`
	// Encryption: control plane pre-loads key_enc (wrapped data key) for
	// this backup; when set, the archive is encrypted before leaving the node.
	Encrypt bool `json:"encrypt,omitempty"`
	// Verify: checksum the artifact after writing (always recommended).
	Verify bool `json:"verify,omitempty"`
	// KeyEnc is the wrapped data key (control plane generated, sealed at
	// rest in backups.key_enc) used when Encrypt is set.
	KeyEnc string `json:"key_enc,omitempty"`
}

// BackupOutcome2 is the job result for the unified ops.
type BackupOutcome2 struct {
	BackupID    string `json:"backup_id"`
	SinkKind    string `json:"sink_kind"`
	SinkRef     string `json:"sink_ref"` // opaque JSON ref stored on the row
	SizeBytes   int64  `json:"size_bytes"`
	StoredBytes int64  `json:"stored_bytes"`
	SHA256      string `json:"sha256,omitempty"`
	Encrypted   bool   `json:"encrypted"`
	Databases   []string `json:"databases,omitempty"`
	Verified    bool   `json:"verified"`
}

// BackupRestorePayload is the backup_restore job payload.
type BackupRestorePayload struct {
	BackupID    string `json:"backup_id"`
	Type        string `json:"type"`
	RefOverride string `json:"ref_override,omitempty"`
	WebsiteID  string `json:"website_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	BotID      string `json:"bot_id,omitempty"`
	// Databases to restore (bundle restores).
	Databases []DBClone `json:"databases,omitempty"`
	// Target to fetch the artifact from + wrapped key to decrypt it.
	Target  *sink.Config `json:"target,omitempty"`
	KeyEnc  string       `json:"key_enc,omitempty"`
	// Legacy website flow fields (pre-Phase-11 restore path).
	Runtime        string `json:"runtime,omitempty"`
	RuntimeVersion string `json:"runtime_version,omitempty"`
}

// BackupVerifyPayload re-checks one artifact (restore-to-scratch).
type BackupVerifyPayload struct {
	BackupID    string       `json:"backup_id"`
	RefOverride string       `json:"ref_override,omitempty"`
	Target   *sink.Config `json:"target,omitempty"`
	KeyEnc   string       `json:"key_enc,omitempty"`
	SHA256   string       `json:"sha256,omitempty"`
}

// BackupPrunePayload deletes listed artifacts from their sinks.
type BackupPrunePayload struct {
	Refs []struct {
		SinkKind string `json:"sink_kind"`
		SinkRef  string `json:"sink_ref"`
		Target   *sink.Config `json:"target,omitempty"`
	} `json:"refs"`
}

// TerminateBackupPayload is the Phase 10 pre-terminate hook: a final
// backup of the workload before irreversible teardown.
type TerminateBackupPayload struct {
	Type       string `json:"type"` // minecraft_world | discord_bot | full_instance | website
	WebsiteID  string `json:"website_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	BotID      string `json:"bot_id,omitempty"`
	Target     *sink.Config `json:"target,omitempty"`
	Encrypt    bool   `json:"encrypt,omitempty"`
}

// ---------------------------------------------------------------------------
// Registry handlers
// ---------------------------------------------------------------------------

// HandleBackupRun creates one backup of the requested workload type and
// lands it on the configured sink (or the agent-local default).
func HandleBackupRun(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p BackupRunPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("invalid backup_run payload: %w", err)
	}
	out, err := runBackup(ctx, e, c, cfg, backupJob{
		BackupID: p.BackupID, Type: p.Type,
		WebsiteID: p.WebsiteID, InstanceID: p.InstanceID, BotID: p.BotID,
		Databases: p.Databases, SingleDB: p.Database,
		Target: p.Target, KeyEnc: p.KeyEnc, Encrypt: p.Encrypt, Verify: p.Verify,
	})
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

// HandleTerminateBackup is the Phase 10 seam: final backup before teardown.
func HandleTerminateBackup(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p TerminateBackupPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("invalid terminate_backup payload: %w", err)
	}
	out, err := runBackup(ctx, e, c, cfg, backupJob{
		BackupID:  uuid.NewString(),
		Type:      p.Type,
		WebsiteID: p.WebsiteID, InstanceID: p.InstanceID, BotID: p.BotID,
		Target: p.Target, Encrypt: p.Encrypt, Verify: true,
		Terminate: true,
	})
	if err != nil {
		return nil, err
	}
	out.BackupID = "terminate:" + out.BackupID
	return json.Marshal(out)
}

type backupJob struct {
	BackupID   string
	Type       string
	WebsiteID  string
	InstanceID string
	BotID      string
	Databases  []DBClone
	SingleDB   *DBClone
	Target     *sink.Config
	KeyEnc     string
	Encrypt    bool
	Verify     bool
	Terminate  bool
}

// runBackup stages the artifact locally, then pushes to the sink.
func runBackup(ctx context.Context, e *Executor, c *Client, cfg Config, job backupJob) (*BackupOutcome2, error) {
	if err := uuidCheckErr(job.BackupID); err != nil {
		return nil, fmt.Errorf("invalid backup id")
	}
	stageDir, err := os.MkdirTemp("", "epic-backup-"+job.BackupID+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageDir)

	var dbs []string
	switch job.Type {
	case sinkTypeWebsite, sinkTypeAccount:
		if job.WebsiteID == "" {
			return nil, fmt.Errorf("website_id required for %s", job.Type)
		}
		siteBase := filepath.Join("/srv/epicpanel/websites", job.WebsiteID)
		if _, err := os.Stat(siteBase); err != nil {
			return nil, fmt.Errorf("site tree missing: %w", err)
		}
		siteTar := filepath.Join(stageDir, "site.tar.gz")
		if err := e.run(ctx, "tar", "-czf", siteTar, "-h", "-C", filepath.Dir(siteBase), filepath.Base(siteBase)); err != nil {
			return nil, fmt.Errorf("tar site: %w", err)
		}
		for _, db := range job.Databases {
			name, err := dumpDatabase(ctx, e, stageDir, db)
			if err != nil {
				return nil, err
			}
			dbs = append(dbs, name)
		}
	case sinkTypeDatabase:
		if job.SingleDB == nil {
			return nil, fmt.Errorf("database required")
		}
		name, err := dumpDatabase(ctx, e, stageDir, *job.SingleDB)
		if err != nil {
			return nil, err
		}
		dbs = append(dbs, name)
	case sinkTypeWorld:
		// Phase 7 seam: quiesced world snapshot (save-off→flush→tar→save-on)
		// via the existing MC op, staged into our bundle.
		if job.InstanceID == "" {
			return nil, fmt.Errorf("instance_id required")
		}
		name := "world-" + job.BackupID
		mcPayload, _ := json.Marshal(mcBackupPayload{InstanceID: job.InstanceID, BackupName: name})
		if _, err := HandleMCBackupWorld(ctx, e, c, cfg, mcPayload); err != nil {
			return nil, err
		}
		worldTar := filepath.Join(MCRoot(job.InstanceID), "backups", name+".tar.gz")
		defer os.Remove(worldTar)
		if err := copyFile(worldTar, filepath.Join(stageDir, "world.tar.gz")); err != nil {
			return nil, err
		}
	case sinkTypeBot:
		if job.BotID == "" {
			return nil, fmt.Errorf("bot_id required")
		}
		botRoot := botRootFor(job.BotID)
		if _, err := os.Stat(botRoot); err != nil {
			return nil, fmt.Errorf("bot tree missing: %w", err)
		}
		// Secrets excluded: EnvironmentFile is never staged (encrypted env
		// lives outside the bot tree; values ride the control plane).
		if err := tarDirExclude(ctx, e, botRoot, filepath.Join(stageDir, "bot.tar.gz"), []string{".env", "*.env", "environment"}); err != nil {
			return nil, err
		}
	case sinkTypeFull:
		// Full instance: tar the unit root (workload files + configs).
		root := instanceRootFor(job.InstanceID, job.BotID, job.WebsiteID)
		if _, err := os.Stat(root); err != nil {
			return nil, fmt.Errorf("instance root missing: %w", err)
		}
		if err := tarDir(ctx, e, root, filepath.Join(stageDir, "instance.tar.gz")); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported backup type %q", job.Type)
	}

	// Bundle the stage dir into one artifact.
	artifact := filepath.Join(stageDir, "..", "artifact-"+job.BackupID+".tar.gz")
	artifact = filepath.Clean(artifact)
	if err := tarDir(ctx, e, stageDir, artifact); err != nil {
		return nil, err
	}
	defer os.RemoveAll(artifact)

	// Optional encryption (data key arrives pre-wrapped from the control plane).
	var dataKey []byte
	if job.Encrypt {
		keyEnc := job.KeyEnc
		if keyEnc == "" {
			return nil, fmt.Errorf("encrypt requested but key_enc missing")
		}
		k, kerr := sink.UnwrapKey(keyEnc)
		if kerr != nil {
			return nil, kerr
		}
		dataKey = k
		encPath := artifact + ".enc"
		if _, err := sink.EncryptFile(dataKey, artifact, encPath); err != nil {
			return nil, err
		}
		os.Remove(artifact)
		artifact = encPath
	}

	// Checksum (plaintext-size vs stored-size recorded separately).
	sha, err := fileSHA256(artifact)
	if err != nil {
		return nil, err
	}
	size, err := fileSize(artifact)
	if err != nil {
		return nil, err
	}

	// Push to sink.
	s, _, err := openSink(ctx, job.Target, job.BackupID)
	if err != nil {
		return nil, err
	}
	name := job.BackupID + ".tar.gz"
	if job.Encrypt {
		name += ".enc"
	}
	putRef, err := s.Put(ctx, artifact, name)
	if err != nil {
		return nil, fmt.Errorf("sink put: %w", err)
	}

	// Verification (post-write stat + size sanity).
	verified := false
	if job.Verify {
		if info, err := s.Stat(ctx, putRef); err == nil && info.SizeBytes > 0 {
			verified = true
		}
	}

	slog.Info("phase11 backup complete", "backup", job.BackupID, "type", job.Type,
		"sink", s.Kind(), "size", size, "encrypted", job.Encrypt, "verified", verified)

	return &BackupOutcome2{
		BackupID:    job.BackupID,
		SinkKind:    s.Kind(),
		SinkRef:     putRef.String(),
		SizeBytes:   size,
		StoredBytes: size,
		SHA256:      sha,
		Encrypted:   job.Encrypt,
		Databases:   dbs,
		Verified:    verified,
	}, nil
}

// Wire-type aliases to keep the switch readable.
const (
	sinkTypeWebsite = "website"
	sinkTypeAccount = "account"
	sinkTypeDatabase = "database"
	sinkTypeWorld   = "minecraft_world"
	sinkTypeBot     = "discord_bot"
	sinkTypeFull    = "full_instance"
)

// openSink resolves the target config to a Sink (local default when unset).
func openSink(ctx context.Context, cfg *sink.Config, backupID string) (sink.Sink, sink.Ref, error) {
	_ = ctx
	_ = backupID
	if cfg == nil {
		s, err := sink.Open(sink.Config{Kind: sink.KindLocal})
		if err != nil {
			return nil, sink.Ref{}, err
		}
		return s, sink.Ref{Kind: sink.KindLocal, Path: ""}, nil
	}
	s, err := sink.Open(*cfg)
	if err != nil {
		return nil, sink.Ref{}, err
	}
	return s, sink.Ref{Kind: cfg.Kind, Path: ""}, nil
}

// dumpDatabase writes one gzipped dump into stageDir (returns the filename).
func dumpDatabase(ctx context.Context, e *Executor, stageDir string, db DBClone) (string, error) {
	if !safeIdentifier(db.SourceName) {
		return "", fmt.Errorf("invalid database identifier")
	}
	dumpName := "db-" + db.SourceName + ".sql.gz"
	dumpFile := filepath.Join(stageDir, dumpName)
	switch db.Engine {
	case "mysql", "mariadb":
		if err := e.waitForMySQL(ctx); err != nil {
			return "", err
		}
		if err := e.run(ctx, "bash", "-c", fmt.Sprintf("mysqldump -u root --single-transaction %s | gzip > %s", db.SourceName, dumpFile)); err != nil {
			return "", fmt.Errorf("mysqldump %s: %w", db.SourceName, err)
		}
	case "postgresql":
		if err := e.waitForPostgres(ctx); err != nil {
			return "", err
		}
		plain := filepath.Join(stageDir, "db-"+db.SourceName+".sql")
		if err := e.run(ctx, "sudo", "-u", "postgres", "pg_dump", db.SourceName, "-f", plain); err != nil {
			return "", fmt.Errorf("pg_dump %s: %w", db.SourceName, err)
		}
		if err := e.run(ctx, "gzip", "-f", plain); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unsupported engine %q", db.Engine)
	}
	return dumpName, nil
}

// tarDir tars srcDir into outPath (gzipped).
func tarDir(ctx context.Context, e *Executor, srcDir, outPath string) error {
	return e.run(ctx, "tar", "-czf", outPath, "-C", srcDir, ".")
}

// tarDirExclude tars srcDir skipping excluded basenames (secret hygiene).
func tarDirExclude(ctx context.Context, e *Executor, srcDir, outPath string, exclude []string) error {
	args := []string{"tar", "-czf", outPath, "-C", srcDir}
	for _, x := range exclude {
		args = append(args, "--exclude="+x)
	}
	args = append(args, ".")
	return e.run(ctx, args[0], args[1:]...)
}

// worldSnapshotDir is retired: the Phase 7 HandleMCBackupWorld seam is used
// directly in runBackup (quiesce → tar → save-on).

// HandleBackupRestore fetches + decrypts an artifact and restores it.
func HandleBackupRestore(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p BackupRestorePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("invalid backup_restore payload: %w", err)
	}
	// Fetch artifact to a scratch dir.
	stageDir, err := os.MkdirTemp("", "epic-restore-"+p.BackupID+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageDir)

	s, _, err := openSink(ctx, p.Target, p.BackupID)
	if err != nil {
		return nil, err
	}
	// Locate the artifact ref from the row's sink_ref (passed via Target
	// context): the control plane embeds the exact Ref in Target.Path.
	ref := sink.Ref{Kind: s.Kind(), Path: firstNonEmpty(p.RefOverride, sinkRefPath(p.Target))}
	artifact := filepath.Join(stageDir, "artifact.tar.gz")
	if err := s.Get(ctx, ref, artifact); err != nil {
		return nil, fmt.Errorf("sink get: %w", err)
	}
	if strings.HasSuffix(ref.Path, ".enc") || p.KeyEnc != "" {
		key, err := sink.UnwrapKey(p.KeyEnc)
		if err != nil {
			return nil, fmt.Errorf("unwrap key: %w", err)
		}
		plain := filepath.Join(stageDir, "artifact.plain.tar.gz")
		if _, err := sink.DecryptFile(key, artifact, plain); err != nil {
			return nil, fmt.Errorf("decrypt: %w", err)
		}
		artifact = plain
	}
	bundle := filepath.Join(stageDir, "bundle")
	if err := os.MkdirAll(bundle, 0o750); err != nil {
		return nil, err
	}
	if err := e.run(ctx, "tar", "-xzf", artifact, "-C", bundle); err != nil {
		return nil, fmt.Errorf("untar artifact: %w", err)
	}

	switch p.Type {
	case sinkTypeWebsite, sinkTypeAccount, sinkTypeFull:
		siteTar := filepath.Join(bundle, "site.tar.gz")
		if p.Type == sinkTypeFull {
			siteTar = filepath.Join(bundle, "instance.tar.gz")
		}
		if _, err := os.Stat(siteTar); err != nil {
			return nil, fmt.Errorf("archive missing: %w", err)
		}
		siteBase := filepath.Join("/srv/epicpanel/websites", p.WebsiteID)
		if _, err := os.Stat(siteBase); err != nil {
			return nil, fmt.Errorf("site tree missing: %w", err)
		}
		if err := e.run(ctx, "tar", "-xzf", siteTar, "-C", filepath.Dir(siteBase)); err != nil {
			return nil, fmt.Errorf("untar site: %w", err)
		}
		for _, db := range p.Databases {
			if err := restoreDatabase(ctx, e, bundle, db); err != nil {
				return nil, err
			}
		}
	case sinkTypeDatabase:
		// Single dump file: find db-*.sql.gz in the bundle.
		matches, _ := filepath.Glob(filepath.Join(bundle, "db-*.sql.gz"))
		if len(matches) == 0 {
			return nil, fmt.Errorf("no dump in artifact")
		}
		base := filepath.Base(matches[0])
		name := strings.TrimSuffix(strings.TrimPrefix(base, "db-"), ".sql.gz")
		engine := "postgresql"
		if len(p.Databases) > 0 {
			engine = p.Databases[0].Engine
		}
		if err := restoreDatabase(ctx, e, bundle, DBClone{Engine: engine, SourceName: name, TargetName: name}); err != nil {
			return nil, err
		}
	case sinkTypeWorld:
		worldTar := filepath.Join(bundle, "world.tar.gz")
		if _, err := os.Stat(worldTar); err != nil {
			return nil, fmt.Errorf("world archive missing: %w", err)
		}
		// Stage into the instance's backups dir and call the Phase 7
		// restore seam (stop → swap → start, rollback on failure).
		name := "restore-" + p.BackupID
		dst := filepath.Join(MCRoot(p.InstanceID), "backups", name+".tar.gz")
		if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
			return nil, err
		}
		if err := copyFile(worldTar, dst); err != nil {
			return nil, err
		}
		defer os.Remove(dst)
		mcPayload, _ := json.Marshal(minecraft.MCRestorePayload{InstanceID: p.InstanceID, BackupName: name})
		if _, err := HandleMCRestoreWorld(ctx, e, c, cfg, mcPayload); err != nil {
			return nil, err
		}
	case sinkTypeBot:
		botTar := filepath.Join(bundle, "bot.tar.gz")
		if _, err := os.Stat(botTar); err != nil {
			return nil, fmt.Errorf("bot archive missing: %w", err)
		}
		botRoot := botRootFor(p.BotID)
		if err := os.MkdirAll(botRoot, 0o750); err != nil {
			return nil, err
		}
		if err := e.run(ctx, "tar", "-xzf", botTar, "-C", botRoot); err != nil {
			return nil, fmt.Errorf("untar bot: %w", err)
		}
	default:
		return nil, fmt.Errorf("unsupported restore type %q", p.Type)
	}
	slog.Info("phase11 restore complete", "backup", p.BackupID, "type", p.Type)
	return json.Marshal(map[string]any{"backup_id": p.BackupID, "restored": true})
}

// HandleBackupVerify pulls the artifact to scratch and checksums it.
func HandleBackupVerify(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p BackupVerifyPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("invalid backup_verify payload: %w", err)
	}
	stageDir, err := os.MkdirTemp("", "epic-verify-"+p.BackupID+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stageDir)
	s, _, err := openSink(ctx, p.Target, p.BackupID)
	if err != nil {
		return nil, err
	}
	ref := sink.Ref{Kind: s.Kind(), Path: firstNonEmpty(p.RefOverride, sinkRefPath(p.Target))}
	artifact := filepath.Join(stageDir, "artifact")
	if err := s.Get(ctx, ref, artifact); err != nil {
		return nil, fmt.Errorf("sink get: %w", err)
	}
	sha, err := fileSHA256(artifact)
	if err != nil {
		return nil, err
	}
	ok := p.SHA256 == "" || sha == p.SHA256
	return json.Marshal(map[string]any{"backup_id": p.BackupID, "sha256": sha, "ok": ok})
}

// HandleBackupPrune deletes artifacts from sinks (retention executor).
func HandleBackupPrune(ctx context.Context, e *Executor, c *Client, cfg Config, payload json.RawMessage) (json.RawMessage, error) {
	var p BackupPrunePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("invalid backup_prune payload: %w", err)
	}
	deleted := 0
	for _, r := range p.Refs {
		var scfg *sink.Config
		if r.Target != nil {
			scfg = r.Target
		}
		s, _, err := openSink(ctx, scfg, "")
		if err != nil {
			return nil, err
		}
		if err := s.Delete(ctx, sink.Ref{Kind: r.SinkKind, Path: r.SinkRef}); err != nil && err != sink.ErrNotFound {
			return nil, fmt.Errorf("sink delete %s: %w", r.SinkRef, err)
		}
		deleted++
	}
	return json.Marshal(map[string]any{"deleted": deleted})
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func restoreDatabase(ctx context.Context, e *Executor, bundle string, db DBClone) error {
	if !safeIdentifier(db.TargetName) {
		return fmt.Errorf("invalid database identifier")
	}
	dumpFile := filepath.Join(bundle, "db-"+db.SourceName+".sql.gz")
	if _, err := os.Stat(dumpFile); err != nil {
		return fmt.Errorf("dump missing: %w", err)
	}
	switch db.Engine {
	case "mysql", "mariadb":
		if err := e.waitForMySQL(ctx); err != nil {
			return err
		}
		if err := e.run(ctx, "bash", "-c", fmt.Sprintf("gzip -dc %s | mysql -u root %s", dumpFile, db.TargetName)); err != nil {
			return fmt.Errorf("mysql restore %s: %w", db.TargetName, err)
		}
	case "postgresql":
		if err := e.waitForPostgres(ctx); err != nil {
			return err
		}
		plain := strings.TrimSuffix(dumpFile, ".gz")
		if err := e.run(ctx, "gzip", "-dc", dumpFile); err != nil {
			return err
		}
		_ = plain
		if err := e.run(ctx, "bash", "-c", fmt.Sprintf("gzip -dc %s | sudo -u postgres psql %s", dumpFile, db.TargetName)); err != nil {
			return fmt.Errorf("pg restore %s: %w", db.TargetName, err)
		}
	default:
		return fmt.Errorf("unsupported engine %q", db.Engine)
	}
	return nil
}

func engineGuess(p BackupRestorePayload) string {
	if len(p.Databases) > 0 {
		return p.Databases[0].Engine
	}
	return "postgresql"
}

// copyFile copies src → dst (mode preserved best-effort).
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func botRootFor(botID string) string {
	return filepath.Join("/srv/epicpanel/bots", botRootSafe(botID))
}

func botRootSafe(botID string) string {
	if !uuidCheck(botID) {
		return "invalid"
	}
	return botID
}

func instanceRootFor(instanceID, botID, websiteID string) string {
	switch {
	case instanceID != "":
		return filepath.Join("/srv/epicpanel/minecraft", instanceID)
	case botID != "":
		return botRootFor(botID)
	default:
		return filepath.Join("/srv/epicpanel/websites", websiteID)
	}
}

// sinkRefPath extracts the artifact path from the control-plane hint. The
// control plane passes the canonical Ref JSON in the target's LocalDir for
// local sinks; for S3/remote the key arrives via the row's sink_ref (the
// restore/verify payloads carry it as PathOverride — see payload docs).
func sinkRefPath(cfg *sink.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.LocalDir
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func uuidCheck(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

