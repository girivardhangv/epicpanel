package websites

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

// Job types for the account lifecycle (suspend/resume). Declared here rather
// than in the jobs package so the jobs store stays lifecycle-agnostic; the
// values match the job_type enum added by migration 0026.
const (
	TypeSuspendWebsite jobs.Type = "suspend_website"
	TypeResumeWebsite  jobs.Type = "resume_website"
)

// SuspensionReason is WHY a website is suspended — data next to the
// 'suspended' status, not a new status (ADR-047 state machine unchanged).
// Mirrors the websites_suspension_reason_check constraint from migration 0050.
type SuspensionReason string

const (
	ReasonManual             SuspensionReason = "manual"
	ReasonBandwidthExhausted SuspensionReason = "bandwidth_exhausted"
	ReasonAbuse              SuspensionReason = "abuse"
	ReasonPayment            SuspensionReason = "payment"
	ReasonAdmin              SuspensionReason = "admin"
	ReasonSystem             SuspensionReason = "system"
	ReasonAttack             SuspensionReason = "attack"
)

// UserSuspensionReasons is the set a human caller may request through the
// suspend API. bandwidth_exhausted / attack / system are system-reserved:
// they are produced by the quota engine, the attack defense and the platform
// itself — a dashboard must never be able to forge them (the bandwidth
// reason renders quota numbers on the public stub page).
var UserSuspensionReasons = map[SuspensionReason]bool{
	ReasonManual: true,
	ReasonAbuse:  true,
	ReasonAdmin:  true,
	ReasonSystem: true,
	ReasonPayment: true,
}

// TerminationReason documents why a website was terminated (free-form,
// operator-supplied through the terminate API and recorded for audit).
type TerminationReason string

// SuspendPayload is the wire form of a suspend_website job. Reason is
// optional for backward compatibility: legacy payloads (and every pre-0050
// producer) carry no reason and mean "manual".
type SuspendPayload struct {
	WebsiteID string         `json:"website_id"`
	Reason    string         `json:"reason,omitempty"`
	Message   string         `json:"message,omitempty"`  // optional operator note (audit only)
	Metadata  map[string]any `json:"metadata,omitempty"` // reason-specific values (e.g. bandwidth used/limit/period)
}

// EffectiveReason normalizes the payload reason: absent = manual.
func (p SuspendPayload) EffectiveReason() SuspensionReason {
	if p.Reason == "" {
		return ReasonManual
	}
	return SuspensionReason(p.Reason)
}

// ResumePayload is the wire form of a resume_website job.
type ResumePayload struct {
	WebsiteID string `json:"website_id"`
}

// ErrMarkReadyBlocked is returned by MarkReady when the current status
// (deleting/deleted) must not transition to ready.
var ErrMarkReadyBlocked = errors.New("website status prevents the ready transition")

// MarkSuspended flips a website to suspended and records WHY (reason,
// suspended_at, metadata). Guarded: allowed from ready/failed (or when
// already suspended, as an idempotent no-op that refreshes the reason) and
// never overrides deleting/deleted/terminated. Called only on suspend job
// success — a failed suspend leaves the previous status untouched (error
// lives on the job). An empty reason persists as 'manual' (legacy producer
// compatibility).
func (s *Store) MarkSuspended(ctx context.Context, websiteID uuid.UUID, reason SuspensionReason, metadata map[string]any) error {
	if reason == "" {
		reason = ReasonManual
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET status = 'suspended', suspension_reason = $2,
			suspended_at = now(), suspension_metadata = $3, updated_at = now()
		WHERE id = $1 AND status IN ('ready', 'failed', 'suspended')
	`, websiteID, string(reason), metadata)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkResumed flips a suspended website back to ready and clears the
// suspension reason + timestamp + metadata. Guarded: only from suspended.
// Called only on resume job success — a failed resume leaves the site
// suspended (error lives on the job).
func (s *Store) MarkResumed(ctx context.Context, websiteID uuid.UUID) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET status = 'ready', error_message = '',
			suspension_reason = NULL, suspended_at = NULL, suspension_metadata = NULL,
			updated_at = now()
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

// MarkTerminated records a successful terminate_website job: status ->
// terminated with the timestamp + reason. Guarded: allowed from ready,
// failed and suspended (an operator may terminate a suspended site) and
// never overrides deleting/deleted (deletion owns its own destructive
// lifecycle).
func (s *Store) MarkTerminated(ctx context.Context, websiteID uuid.UUID, reason string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE websites SET status = 'terminated', terminated_at = now(),
			termination_reason = $2, suspension_reason = NULL, suspension_metadata = NULL,
			updated_at = now()
		WHERE id = $1 AND status IN ('ready', 'failed', 'suspended')
	`, websiteID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// publishLifecycleEvent emits a website lifecycle event (website.suspended /
// website.resumed) on the platform event bus so WS clients see manual and
// system-driven transitions in real time. Nil-safe (tests without a bus).
func (h *Handler) publishLifecycleEvent(ctx context.Context, eventType string, job *jobs.Job, extra map[string]any) {
	if h.Events == nil || job.WebsiteID == nil {
		return
	}
	ws, err := h.Websites.GetByIDAny(ctx, *job.WebsiteID)
	if err != nil || ws == nil {
		return
	}
	payload := map[string]any{
		"website_id": ws.ID.String(),
		"name":       ws.Name,
		"status":     string(ws.Status),
	}
	for k, v := range extra {
		payload[k] = v
	}
	org := ws.Organization
	h.Events.Publish(ctx, events.Event{
		Type:         eventType,
		Organization: &org,
		ActorType:    "system",
		ResourceType: "website",
		ResourceID:   ws.ID.String(),
		Payload:      payload,
	})
}
