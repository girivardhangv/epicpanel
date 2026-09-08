package databases

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

// SSOPayload is what travels (encrypted) inside the one-time SSO token.
// Exported: the api package's public /v1/pma-gate decrypts and renders it.
type SSOPayload struct {
	Engine   string `json:"engine"`
	Database string `json:"database"`
	User     string `json:"user"`
	Password string `json:"password"`
	Exp      int64  `json:"exp"`
}

const ssoTTL = 60 * time.Second

// SettingsGetter lets the SSO endpoint read the dbadmin signing key without a
// hard dependency on the settings package.
type SettingsGetter interface {
	GetSSOKey(ctx context.Context) (string, error)
}

// ssoEncrypt seals the payload with the dbadmin SSO key (AES-256-GCM) in the
// layout the PHP shim on the server expects: nonce(12) | ct | tag(16),
// base64url-encoded. The shim holds the same key in
// /etc/epicpanel/dbadmin-sso.key, provisioned by the agent.
func ssoEncrypt(keyHex string, p SSOPayload) (string, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil || len(key) != 32 {
		return "", errors.New("invalid sso key")
	}
	plain, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(gcm.Seal(nonce, nonce, plain, nil)), nil
}

// GET .../databases/{db_id}/pma-sso → {"url": "/v1/pma-gate?t=..."}
// Returns a one-time link that auto-logs the user into phpMyAdmin (MySQL/
// MariaDB) or Adminer (PostgreSQL) WITHOUT showing the password. The token
// encrypts the credentials with the dbadmin SSO key and expires in 60s.
func (h *Handler) PmaSSO(w http.ResponseWriter, r *http.Request) {
	orgID, ok := OrgIDFromRequest(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrInternal(errOrgContext))
		return
	}
	db, apiErr := h.dbFromPath(r, orgID)
	if apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if db.Status != StatusReady {
		httpapi.RespondError(w, httpapi.ErrConflict("database is not ready yet"))
		return
	}
	keyHex := ""
	if h.SSOKey != nil {
		keyHex, _ = h.SSOKey(r.Context())
	}
	if keyHex == "" {
		httpapi.RespondError(w, httpapi.ErrConflict("database tools are not installed yet — install phpMyAdmin from the Software page first"))
		return
	}
	password, err := h.Databases.RevealPassword(r.Context(), db.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
		return
	}
	h.audit(r, &orgID, "database.pma_sso_opened", "database", db.ID.String(), map[string]any{"engine": string(db.Engine)})

	token, err := ssoEncrypt(keyHex, SSOPayload{
		Engine: string(db.Engine), Database: db.Name, User: db.DBUser, Password: password,
		Exp: time.Now().Add(ssoTTL).Unix(),
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"url": "/v1/pma-gate?t=" + token})
}
