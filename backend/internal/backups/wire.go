package backups

// Phase 11 wire payloads (control-plane ↔ agent). Shared by the API layer
// (enqueue), the scheduler and the agent ops (internal/agent/backup_ops2.go)
// so the job contract has exactly one definition.

// BackupRunPayload is the backup_run job payload (control plane → agent).
type BackupRunPayload struct {
	BackupID string `json:"backup_id"`
	Type     string `json:"type"` // account | database | website | minecraft_world | discord_bot | full_instance | website_files
	// Workload identifiers (exactly one primary).
	WebsiteID  string `json:"website_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	BotID      string `json:"bot_id,omitempty"`
	// Databases to dump (account/website bundles).
	Databases []DBClonePayload `json:"databases,omitempty"`
	// Target: sink config + sealed creds (nil = agent-local default).
	Target   *SinkConfigWire `json:"target,omitempty"`
	TargetID string          `json:"target_id,omitempty"`
	// Encryption: control plane pre-loads key_enc (wrapped data key) for
	// this backup; when set, the archive is encrypted before leaving the node.
	Encrypt bool   `json:"encrypt,omitempty"`
	KeyEnc  string `json:"key_enc,omitempty"`
	// Verify: checksum the artifact after writing (recommended).
	Verify bool `json:"verify,omitempty"`
}

// BackupRestorePayload is the backup_restore job payload.
type BackupRestorePayload struct {
	BackupID string `json:"backup_id"`
	Type     string `json:"type"`
	// RefOverride is the canonical artifact ref JSON (from the row).
	RefOverride string `json:"ref_override,omitempty"`
	WebsiteID   string `json:"website_id,omitempty"`
	InstanceID  string `json:"instance_id,omitempty"`
	BotID       string `json:"bot_id,omitempty"`
	// Databases to restore (bundle restores).
	Databases []DBClonePayload `json:"databases,omitempty"`
	// Target to fetch the artifact from + wrapped key to decrypt it.
	Target *SinkConfigWire `json:"target,omitempty"`
	KeyEnc string          `json:"key_enc,omitempty"`
}

// BackupVerifyPayload re-checks one artifact (restore-to-scratch).
type BackupVerifyPayload struct {
	BackupID    string          `json:"backup_id"`
	RefOverride string          `json:"ref_override,omitempty"`
	Target      *SinkConfigWire `json:"target,omitempty"`
	KeyEnc      string          `json:"key_enc,omitempty"`
	SHA256      string          `json:"sha256,omitempty"`
}

// BackupPrunePayload deletes listed artifacts from their sinks.
type BackupPrunePayload struct {
	Refs []PruneRef `json:"refs"`
}

// PruneRef names one artifact to delete (sink + ref + target config).
type PruneRef struct {
	SinkKind string          `json:"sink_kind"`
	SinkRef  string          `json:"sink_ref"`
	Target   *SinkConfigWire `json:"target,omitempty"`
}

// TerminateBackupPayload is the Phase 10 pre-terminate hook: a final backup
// of the workload before irreversible teardown.
type TerminateBackupPayload struct {
	Type       string          `json:"type"`
	WebsiteID  string          `json:"website_id,omitempty"`
	InstanceID string          `json:"instance_id,omitempty"`
	BotID      string          `json:"bot_id,omitempty"`
	Target     *SinkConfigWire `json:"target,omitempty"`
	Encrypt    bool            `json:"encrypt,omitempty"`
}

// BackupOutcome2 is the job result for the unified ops.
type BackupOutcome2 struct {
	BackupID    string   `json:"backup_id"`
	SinkKind    string   `json:"sink_kind"`
	SinkRef     string   `json:"sink_ref"`
	SizeBytes   int64    `json:"size_bytes"`
	StoredBytes int64    `json:"stored_bytes"`
	SHA256      string   `json:"sha256,omitempty"`
	Encrypted   bool     `json:"encrypted"`
	Databases   []string `json:"databases,omitempty"`
	Verified    bool     `json:"verified"`
}

// SinkConfigWire is the transport form of a sink config for job payloads
// (mirrors sink.Config so the agent decodes without importing the API layer).
type SinkConfigWire struct {
	Kind     string          `json:"kind"`
	LocalDir string          `json:"local_dir,omitempty"`
	S3       *SinkS3Wire     `json:"s3,omitempty"`
	Remote   *SinkRemoteWire `json:"remote,omitempty"`
	CredsEnc string          `json:"creds_enc,omitempty"`
}

// SinkS3Wire mirrors sink.S3Config.
type SinkS3Wire struct {
	Endpoint  string `json:"endpoint"`
	Region    string `json:"region"`
	Bucket    string `json:"bucket"`
	Prefix    string `json:"prefix,omitempty"`
	PathStyle bool   `json:"path_style,omitempty"`
}

// SinkRemoteWire mirrors sink.RemoteConfig.
type SinkRemoteWire struct {
	Host string `json:"host"`
	Port int    `json:"port,omitempty"`
	User string `json:"user"`
	Path string `json:"path"`
}
