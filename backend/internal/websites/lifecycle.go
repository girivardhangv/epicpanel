package websites

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// Job types for the account lifecycle (suspend/resume). Declared here rather
// than in the jobs package so the jobs store stays lifecycle-agnostic; the
// values match the job_type enum added by migration 0026.
const (
	TypeSuspendWebsite jobs.Type = "suspend_website"
	TypeResumeWebsite  jobs.Type = "resume_website"
)

// SuspendPayload is the wire form of a suspend_website job.
type SuspendPayload struct {
	WebsiteID string `json:"website_id"`
}

// ResumePayload is the wire form of a resume_website job.
type ResumePayload struct {
	WebsiteID string `json:"website_id"`
}

// ErrMarkReadyBlocked is returned by MarkReady when the current status
// (deleting/deleted) must not transition to ready.
var ErrMarkReadyBlocked = errors.New("website status prevents the ready transition")

// MarkSuspended flips a website to suspended. Guarded: allowed from
// ready/failed (or when already suspended, as an idempotent no-op) and never
// overrides deleting/deleted. Called only on suspend job success — a failed
// suspend leaves the previous status untouched (error lives on the job).
func (s *Store) MarkSuspended(ctx context.Context, websiteID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET status = 'suspended', updated_at = now()
		WHERE id = $1 AND status IN ('ready', 'failed', 'suspended')
	`, websiteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkResumed flips a suspended website back to ready. Guarded: only from
// suspended. Called only on resume job success — a failed resume leaves the
// site suspended (error lives on the job).
func (s *Store) MarkResumed(ctx context.Context, websiteID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET status = 'ready', error_message = '', updated_at = now()
		WHERE id = $1 AND status = 'suspended'
	`, websiteID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
