// Package licensing implements the panel's commercial license foundation.
//
// The panel talks to an independently hosted licensing server
// (config.LicensingConfig.APIBaseURL). The license binds to this
// installation's persistent instance_id fingerprint so keys cannot float
// between servers.
//
// Resilience charter: after a successful activation/validation, short-lived
// outages of the licensing server must not render the panel unusable while
// the configured grace window lasts. All of that logic lives in Policy.go,
// which is unit tested without network access.
package licensing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Status values mirrored by the licenses.status CHECK constraint.
const (
	StatusInactive   = "inactive"
	StatusActive     = "active"
	StatusGrace      = "grace"
	StatusExpired    = "expired"
	StatusSuspended  = "suspended"
	StatusInvalid    = "invalid"
)

// Info is the canonical license snapshot used by both API responses and UI.
type Info struct {
	Status        string     `json:"status"`
	Plan          string     `json:"plan,omitempty"`
	Seats         *int       `json:"seats,omitempty"`
	KeyHint       string     `json:"key_hint"`
	ExternalID    string     `json:"external_id,omitempty"`
	Features      []string   `json:"features,omitempty"`
	ActivatedAt   *time.Time `json:"activated_at,omitempty"`
	LastValidatedAt *time.Time `json:"last_validated_at,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Fingerprint   string     `json:"fingerprint"`
}

// Operation marks which licensing operation failed.
type Operation string

const (
	OpActivate   Operation = "activate"
	OpValidate   Operation = "validate"
	OpDeactivate Operation = "deactivate"
)

// Service is the licensing contract consumed by the installer and settings API.
type Service interface {
	Activate(ctx context.Context, rawKey string) (*Info, error)
	Validate(ctx context.Context) (*Info, error)
	Deactivate(ctx context.Context) error
	Refresh(ctx context.Context) (*Info, error)
	Status(ctx context.Context) (*Info, error)
	// Usable reports whether licensed operation is currently permitted
	// without contacting the licensing server (drives installer gating).
	Usable(ctx context.Context) bool
}

var (
	ErrNoLicense           = errors.New("licensing: no license stored")
	ErrUnreachable         = errors.New("licensing server unreachable")
	ErrInvalidKey          = errors.New("licensing: invalid license key")
	ErrExpired             = errors.New("licensing: license expired")
	ErrSuspended           = errors.New("licensing: license suspended")
	ErrAlreadyActive       = errors.New("licensing: license already activated elsewhere")
	ErrFingerprintMismatch = errors.New("licensing: license bound to another installation")
)

func isActiveState(s string) bool {
	return s == StatusActive || s == StatusGrace
}

func nullUUID(id uuid.UUID) any { return id }
