package licensing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConcreteService implements Service backed by PostgreSQL + RemoteClient.
type ConcreteService struct {
	pool        *pgxpool.Pool
	client      *RemoteClient
	policy      Policy
	revalidate  time.Duration
	fingerprint func() string // installation fingerprint provider
	log         *slog.Logger
}

func New(pool *pgxpool.Pool, client *RemoteClient, policy Policy, revalidate time.Duration,
	fingerprint func() string, log *slog.Logger) *ConcreteService {
	if revalidate <= 0 {
		revalidate = 6 * time.Hour
	}
	if log == nil {
		log = discardLogger()
	}
	return &ConcreteService{pool: pool, client: client, policy: policy, revalidate: revalidate, fingerprint: fingerprint, log: log}
}

var _ Service = (*ConcreteService)(nil)

func discardLogger() *slog.Logger { return slog.Default() }

func hintOf(key string) string {
	k := strings.TrimSpace(key)
	runes := []rune(k)
	n := len(runes)
	switch {
	case n == 0:
		return ""
	case n <= 4:
		return "••••"
	case n <= 8:
		return "••••" + string(runes[n-4:])
	default:
		return string(runes[:2]) + "…••••" + string(runes[n-4:])
	}
}

func marshalSlice(items []string) []byte {
	raw, _ := json.Marshal(items)
	return raw
}

func marshalRemote(rr *remoteResponse) []byte {
	raw, _ := json.Marshal(rr)
	return raw
}

func seatsAny(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// stored holds the locally persisted license row.
type stored struct {
	status          string
	keyHint         string
	externalID      string
	plan            string
	seats           *int
	featuresRaw     []byte
	fingerprint     string
	activatedAt     *time.Time
	lastValidatedAt *time.Time
	expiresAt       *time.Time
	rawPayload      []byte
}

func (st *stored) info() *Info {
	feats := []string{}
	if len(st.featuresRaw) > 0 {
		_ = json.Unmarshal(st.featuresRaw, &feats)
	}
	return &Info{
		Status:          st.status,
		Plan:            st.plan,
		Seats:           st.seats,
		KeyHint:         st.keyHint,
		ExternalID:      st.externalID,
		Features:        feats,
		ActivatedAt:     st.activatedAt,
		LastValidatedAt: st.lastValidatedAt,
		ExpiresAt:       st.expiresAt,
		Fingerprint:     st.fingerprint,
	}
}

const licenseSelect = `
	SELECT status, COALESCE(license_key_hint,''), COALESCE(external_license_id,''), COALESCE(plan,''),
	       seats, features::text, COALESCE(activation_fingerprint,''),
	       activated_at, last_validated_at, expires_at, raw_payload::text
	FROM licenses WHERE installation_id = 1`

func (s *ConcreteService) load(ctx context.Context) (*stored, error) {
	var st stored
	var featuresJSON, payloadJSON *string
	err := s.pool.QueryRow(ctx, licenseSelect).Scan(
		&st.status, &st.keyHint, &st.externalID, &st.plan, &st.seats, &featuresJSON,
		&st.fingerprint, &st.activatedAt, &st.lastValidatedAt, &st.expiresAt, &payloadJSON)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, apierror.From(err)
	}
	st.featuresRaw = []byte(orEmptyObj(featuresJSON))
	st.rawPayload = []byte(orEmptyObj(payloadJSON))
	if st.status == "" {
		st.status = StatusInactive
	}
	return &st, nil
}

func orEmptyObj(p *string) string {
	if p == nil || *p == "" {
		return "{}"
	}
	return *p
}

// upsert writes canonical state; the full key is never persisted locally.
// Deliberately uses UPDATE-then-INSERT instead of INSERT..SELECT..ON CONFLICT:
// plain statements have full target-column typing and zero inference surprises.
func (s *ConcreteService) upsert(ctx context.Context, st *stored) error {
	features := bytesOrNilJSON(st.featuresRaw)
	payload := bytesOrNilJSON(st.rawPayload)
	args := []any{
		st.keyHint, st.externalID, st.plan, seatsAny(st.seats), st.status,
		string(features), st.fingerprint, st.activatedAt, st.lastValidatedAt,
		st.expiresAt, string(payload),
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE licenses SET
			license_key_hint      = $1::text,
			external_license_id   = $2::text,
			plan                  = $3::text,
			seats                 = $4::int,
			status                = $5::text,
			features              = $6::jsonb,
			activation_fingerprint = $7::text,
			activated_at          = COALESCE($8::timestamptz, activated_at),
			last_validated_at     = $9::timestamptz,
			expires_at            = $10::timestamptz,
			raw_payload           = $11::jsonb,
			updated_at            = now()
		WHERE installation_id = 1`, args...)
	if err != nil {
		return fmt.Errorf("update license state: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO licenses (installation_id, license_key_hint, external_license_id, plan, seats, status,
			features, activation_fingerprint, activated_at, last_validated_at, expires_at, raw_payload)
		VALUES (1, $1::text, $2::text, $3::text, $4::int, $5::text, $6::jsonb, $7::text,
		        $8::timestamptz, $9::timestamptz, $10::timestamptz, $11::jsonb)`, args...)
	if err != nil {
		return fmt.Errorf("insert license state: %w", err)
	}
	return nil
}

func bytesOrNilJSON(b []byte) string {
	if len(b) == 0 {
		return "{}"
	}
	return string(b)
}

// Activate binds a license key to this installation via the remote server.
func (s *ConcreteService) Activate(ctx context.Context, rawKey string) (*Info, error) {
	key := strings.TrimSpace(rawKey)
	if key == "" || len(key) < 8 || len(key) > 128 {
		return nil, apierror.BadRequest("Enter a valid license key")
	}
	fp := s.fingerprint()

	resp, err := s.client.Activate(ctx, key, fp)
	if err != nil {
		return nil, mapRemoteError(OpActivate, err)
	}
	if resp.Status != StatusActive {
		s.applyDenialState(ctx, resp.Status, resp.LicenseID)
		return nil, denialError(resp.Status, resp.Message)
	}

	now := time.Now().UTC()
	st := &stored{
		status:          StatusActive,
		keyHint:         hintOf(key),
		externalID:      resp.LicenseID,
		plan:            resp.Plan,
		seats:           resp.Seats,
		featuresRaw:     marshalSlice(resp.Features),
		fingerprint:     fp,
		activatedAt:     &now,
		lastValidatedAt: &now,
		expiresAt:       resp.ExpiresAt,
		rawPayload:      marshalRemote(resp),
	}
	if err := s.upsert(ctx, st); err != nil {
		return nil, apierror.From(err)
	}
	return st.info(), nil
}

// Validate performs a live check against the licensing server and reconciles
// local state. A temporary outage engages the grace policy instead of locking
// operators out of an otherwise validated installation.
func (s *ConcreteService) Validate(ctx context.Context) (*Info, error) {
	st, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if st == nil || st.fingerprint == "" {
		return nil, ErrNoLicense
	}
	resp, cerr := s.client.Validate(ctx, st.fingerprint)
	if cerr != nil {
		if errors.Is(cerr, ErrUnreachable) {
			info, gerr := s.outageGrace(ctx, st)
			if gerr != nil {
				return info, gerr
			}
			return info, apierror.New(503, "LICENSE_SERVER_UNREACHABLE",
				"Licensing server unreachable; operating on previously validated license")
		}
		return nil, apierror.From(cerr)
	}
	if resp.Status == StatusActive {
		now := time.Now().UTC()
		st.status = StatusActive
		st.lastValidatedAt = &now
		if resp.Plan != "" {
			st.plan = resp.Plan
		}
		if resp.ExpiresAt != nil {
			st.expiresAt = resp.ExpiresAt
		}
		st.rawPayload = marshalRemote(resp)
		if uperr := s.upsert(ctx, st); uperr != nil {
			return nil, apierror.From(uperr)
		}
		return st.info(), nil
	}
	s.applyDenialState(ctx, resp.Status, resp.LicenseID)
	return nil, denialError(resp.Status, resp.Message)
}

func (s *ConcreteService) applyDenialState(ctx context.Context, status, externalID string) {
	st, lerr := s.load(ctx)
	if lerr != nil || st == nil {
		return
	}
	st.status = normalizeDenial(status)
	if externalID != "" {
		st.externalID = externalID
	}
	if uerr := s.upsert(ctx, st); uerr != nil {
		s.log.Warn("licensing: could not persist denial state", "status", status, "error", uerr.Error())
	}
}

func normalizeDenial(status string) string {
	switch status {
	case StatusExpired, StatusSuspended, StatusInvalid:
		return status
	default:
		return StatusInvalid
	}
}

func denialError(status, remoteMsg string) error {
	code := "LICENSE_REJECTED"
	message := remoteMsg
	switch status {
	case StatusExpired:
		code = "LICENSE_EXPIRED"
	case StatusSuspended:
		code = "LICENSE_SUSPENDED"
	case StatusInvalid:
		code = "LICENSE_INVALID"
	}
	if message == "" {
		message = denialFallbackMessage(code)
	}
	return apierror.New(402, code, message)
}

func denialFallbackMessage(code string) string {
	switch code {
	case "LICENSE_EXPIRED":
		return "License has expired"
	case "LICENSE_SUSPENDED":
		return "License is suspended"
	case "LICENSE_INVALID":
		return "License was not accepted by the licensing server"
	default:
		return "License was not accepted by the licensing server"
	}
}

// outageGrace evaluates whether a previously validated installation may keep
// operating during a licensing-server outage.
func (s *ConcreteService) outageGrace(ctx context.Context, st *stored) (*Info, error) {
	d := s.policy.Evaluate(true, st.status, st.lastValidatedAt, st.expiresAt, time.Now().UTC())
	if d.Usable {
		st.status = StatusGrace
		if uerr := s.upsert(ctx, st); uerr != nil {
			s.log.Warn("licensing: grace persistence failed", "error", uerr.Error())
		}
		return st.info(), nil
	}
	return st.info(), apierror.New(503, "LICENSE_SERVER_UNREACHABLE",
		"Licensing server unreachable and the license grace period has elapsed")
}

// Refresh forces a remote validation pass and reports current local state even
// when the remote call fails (so the UI can show last-known-good information).
func (s *ConcreteService) Refresh(ctx context.Context) (*Info, error) {
	if _, verr := s.load(ctx); verr != nil {
		return nil, verr
	}
	info, err := s.Validate(ctx)
	if err != nil {
		st, _ := s.load(ctx)
		if st != nil {
			return st.info(), err
		}
		return info, err
	}
	return info, nil
}

// Deactivate unbinds the license from this installation. Removal is local so
// operators can recover from a dead licensing server by hand if ever needed.
func (s *ConcreteService) Deactivate(ctx context.Context) error {
	st, err := s.load(ctx)
	if err != nil {
		return err
	}
	if st == nil {
		return ErrNoLicense
	}
	cerr := s.client.Deactivate(ctx, st.externalID, st.fingerprint)
	if cerr != nil && !errors.Is(cerr, ErrUnreachable) {
		return mapRemoteError(OpDeactivate, cerr)
	}
	if _, derr := s.pool.Exec(ctx, `DELETE FROM licenses WHERE installation_id = 1`); derr != nil {
		return apierror.From(derr)
	}
	return nil
}

// Status returns the locally known snapshot without network calls.
func (s *ConcreteService) Status(ctx context.Context) (*Info, error) {
	st, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return &Info{Status: StatusInactive}, nil
	}
	d := s.policy.Evaluate(true, st.status, st.lastValidatedAt, st.expiresAt, time.Now().UTC())
	info := st.info()
	info.Status = d.State
	return info, nil
}

// Usable informs gates (installer, completion) whether licensed operation is permitted.
func (s *ConcreteService) Usable(ctx context.Context) bool {
	st, err := s.load(ctx)
	if err != nil || st == nil {
		return false
	}
	d := s.policy.Evaluate(true, st.status, st.lastValidatedAt, st.expiresAt, time.Now().UTC())
	return d.Usable
}

func mapRemoteError(op Operation, err error) error {
	if errors.Is(err, ErrUnreachable) {
		return apierror.New(503, "LICENSE_SERVER_UNREACHABLE", "Licensing server is unreachable; try again later")
	}
	return apierror.New(502, "LICENSE_SERVER_ERROR",
		"Licensing server returned an unexpected response ("+string(op)+")")
}
