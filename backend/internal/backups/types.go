// Package backups — Phase 11: first-class backup/restore for EVERY workload
// type (account, database, website, full instance) with sink drivers
// (local / remote / object storage), encryption
// at rest (per-backup data key wrapped via secretbox), verification
// (checksum + restore-to-scratch), retention (plan count + time prune) and
// schedules. The pre-Phase-11 website flow keeps working on the same rows
// (kind "website") via the same job types (create_backup / restore_backup).
package backups

// Types (verbatim from the master doc "Support:" list).
const (
	TypeAccount      = "account"
	TypeDatabase     = "database"
	TypeWebsite      = "website"
	TypeFull         = "full_instance"
	TypeWebsiteFiles = "website_files"
)

// AllTypes is the verbatim set + the website-files split (files-only vs
// account bundle both exist as user-visible choices; "website" remains the
// row type for the pre-Phase-11 flow).
var AllTypes = []string{TypeAccount, TypeDatabase, TypeWebsite, TypeFull, TypeWebsiteFiles}

// ValidType reports whether t is a supported backup type.
func ValidType(t string) bool {
	switch t {
	case TypeAccount, TypeDatabase, TypeWebsite, TypeFull, TypeWebsiteFiles:
		return true
	}
	return false
}

// Job types (enqueued; consumed by the agent ops in backup_ops2.go).
const (
	JobBackup  = "backup_run"
	JobRestore = "backup_restore"
	JobVerify  = "backup_verify"
	JobPrune   = "backup_prune"
	// JobTerminateBackup is the Phase 10 seam: the pre-terminate automatic
	// backup ("terminate flow always leaves a final backup").
	JobTerminateBackup = "terminate_backup"
)

// Events published on the bus (Phase 13 subscribes to backup.* defensively).
const (
	EventCreated   = "backup.created"
	EventSucceeded = "backup.succeeded"
	EventFailed    = "backup.failed"
	EventVerifyOK  = "backup.verified"
	// EventVerifyFailed marks an unverified backup (Phase 13 alert input).
	EventVerifyFailed  = "backup.verify_failed"
	EventRestored      = "backup.restored"
	EventRestoreFailed = "backup.restore_failed"
	EventPruned        = "backup.pruned"
)

// AesGcmCiphertext is a base64 blob constant marker for docs/tests.
const AesGcmCiphertext = "base64(nonce|ciphertext|tag)"
