package auth

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
	"github.com/epicbyte/epicpanel/backend/internal/users"
)

const mfaChallengeTTL = 5 * time.Minute
const totpIssuer = "EpicPanel"

// MFAHandler implements the 2FA foundation:
//
//	POST /v1/auth/mfa/setup    — generate TOTP secret (stored encrypted, inactive until verified)
//	POST /v1/auth/mfa/enable   — verify first code, activate, return recovery codes once
//	POST /v1/auth/mfa/disable  — deactivate (password required)
//	POST /v1/auth/mfa/verify   — complete an MFA-gated login (TOTP or recovery code)
type MFAHandler struct {
	Users    *users.Store
	MFA      *MFAStore
	Sessions *SessionStore
	Audit    *audit.Store
	Cfg      config.Config
}

func (h *MFAHandler) audit(r *http.Request, action string, userID uuid.UUID, meta map[string]any) {
	if h.Audit == nil {
		return
	}
	uid := userID
	h.Audit.RecordBestEffort(r.Context(), audit.Entry{
		ActorUserID:  &uid,
		ActorType:    audit.ActorUser,
		Action:       action,
		ResourceType: "user",
		ResourceID:   userID.String(),
		Metadata:     meta,
		IP:           httpapi.ClientIP(r),
	})
}

func (h *MFAHandler) currentUser(r *http.Request) (uuid.UUID, *httpapi.APIError) {
	usr, ok := httpapi.UserFrom(r.Context())
	if !ok {
		return uuid.Nil, httpapi.ErrUnauthorized("authentication required")
	}
	id, err := uuid.Parse(usr.ID)
	if err != nil {
		return uuid.Nil, httpapi.ErrInternal(err)
	}
	return id, nil
}

// Setup generates a fresh TOTP secret, stores it encrypted (inactive) and
// returns the secret + otpauth URI for the authenticator app.
func (h *MFAHandler) Setup(w http.ResponseWriter, r *http.Request) {
	userID, apiErr := h.currentUser(r)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	enc, err := secretbox.Encrypt(secret)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.MFA.SetSecret(r.Context(), userID, enc); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	u, err := h.Users.GetByID(r.Context(), userID)
	if err != nil || u == nil {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrNotFound(err)))
		return
	}
	h.audit(r, "mfa.setup_started", userID, nil)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"secret":      secret,
		"otpauth_uri": TOTPProvisioningURI(secret, u.Email, totpIssuer),
		"note":        "scan with an authenticator app, then POST /v1/auth/mfa/enable with the 6-digit code",
	})
}

// Enable verifies the first TOTP code, activates MFA and returns recovery
// codes exactly once (only hashes are stored).
func (h *MFAHandler) Enable(w http.ResponseWriter, r *http.Request) {
	userID, apiErr := h.currentUser(r)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	enc, err := h.MFA.SecretEncrypted(r.Context(), userID)
	if err != nil || len(enc) == 0 {
		httpapi.RespondError(w, httpapi.ErrConflict("start MFA setup first"))
		return
	}
	secret, err := secretbox.Decrypt(enc)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if !VerifyTOTP(secret, strings.TrimSpace(req.Code), time.Now()) {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("invalid verification code"))
		return
	}
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = hashToken(c)
	}
	if err := h.MFA.ReplaceRecoveryCodes(r.Context(), userID, hashes); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.MFA.Enable(r.Context(), userID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, "mfa.enabled", userID, nil)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"enabled":        true,
		"recovery_codes": codes,
		"note":           "store these recovery codes safely; they are shown only once",
	})
}

// Disable turns MFA off; requires the account password (defense against a
// hijacked session dropping 2FA).
func (h *MFAHandler) Disable(w http.ResponseWriter, r *http.Request) {
	userID, apiErr := h.currentUser(r)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	u, err := h.Users.GetByID(r.Context(), userID)
	if err != nil || u == nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if !CheckPassword(u.PasswordHash, req.Password) {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("password is incorrect"))
		return
	}
	if err := h.MFA.Disable(r.Context(), userID); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, "mfa.disabled", userID, nil)
	w.WriteHeader(http.StatusNoContent)
}

// Verify completes an MFA-gated login: consumes the pending challenge, checks
// the TOTP (or a recovery code) and issues the real session.
func (h *MFAHandler) Verify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MFAToken string `json:"mfa_token"`
		Code     string `json:"code"`
	}
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	userID, err := h.MFA.ConsumeChallenge(r.Context(), strings.TrimSpace(req.MFAToken))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("mfa challenge is invalid or expired"))
		return
	}
	enc, err := h.MFA.SecretEncrypted(r.Context(), userID)
	if err != nil || len(enc) == 0 {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	secret, err := secretbox.Decrypt(enc)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	code := strings.TrimSpace(req.Code)
	if !VerifyTOTP(secret, code, time.Now()) {
		// Fall back to a single-use recovery code.
		ok, err := h.MFA.ConsumeRecoveryCode(r.Context(), userID, code)
		if err != nil || !ok {
			h.audit(r, "mfa.verify_failed", userID, nil)
			httpapi.RespondError(w, httpapi.ErrUnauthorized("invalid verification code"))
			return
		}
		h.audit(r, "mfa.recovery_code_used", userID, nil)
	}
	u, err := h.Users.GetByID(r.Context(), userID)
	if err != nil || u == nil || u.Status != "active" {
		httpapi.RespondError(w, httpapi.ErrForbidden("account is disabled"))
		return
	}
	token, sess, err := h.Sessions.Create(r.Context(), userID, h.Cfg.SessionTTL, r.UserAgent(), httpapi.ClientIP(r))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	writeSessionCookie(w, h.Cfg, token)
	h.audit(r, "auth.login", userID, map[string]any{"mfa": true})
	httpapi.WriteJSON(w, http.StatusOK, authResponse{Token: token, ExpiresAt: sess.ExpiresAt, User: u})
}

func errOrNotFound(err error) error {
	if err != nil {
		return err
	}
	return errNoUser
}

type simpleErr string

func (e simpleErr) Error() string { return string(e) }

const errNoUser = simpleErr("user not found")
