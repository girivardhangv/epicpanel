package licensing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RemoteClient talks to the independently hosted licensing API.
//
// Contract (implemented by the licensing server, hosted separately):
//   POST /v1/activate    {license_key, fingerprint}           -> LicenseResponse
//   POST /v1/validate    {fingerprint}                        -> LicenseResponse
//   POST /v1/deactivate  {fingerprint}                        -> {"ok":true}
//
// A minimal development reference implementation of this contract ships in
// scripts/licensing-server (development only).
type RemoteClient struct {
	BaseURL string
	Harness *http.Client
}

// remoteResponse is the canonical payload returned by the licensing server.
type remoteResponse struct {
	Status       string     `json:"status"`        // valid | expired | suspended | invalid
	Message      string     `json:"message,omitempty"`
	LicenseID    string     `json:"license_id"`
	Plan         string     `json:"plan"`
	Seats        *int       `json:"seats"`
	Features     []string   `json:"features"`
	IssuedToName string     `json:"issued_to_name,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at"`
}

func (c *RemoteClient) client() *http.Client {
	if c.Harness != nil {
		return c.Harness
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (c *RemoteClient) call(ctx context.Context, path string, body any) (*remoteResponse, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return nil, fmt.Errorf("%w: no licensing server configured", ErrUnreachable)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Panel-Product", "epicpanel")

	resp, err := c.client().Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, errors.Join(ErrUnreachable, ctxErr)
		}
		return nil, fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusPaymentRequired:
		var rr remoteResponse
		if json.Unmarshal(raw, &rr) == nil {
			return mapRemote(&rr), nil
		}
		return &remoteResponse{Status: "invalid", Message: "License not accepted by licensing server"}, nil
	default:
		return nil, fmt.Errorf("licensing: unexpected status %d from server", resp.StatusCode)
	}

	var rr remoteResponse
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, fmt.Errorf("licensing: malformed response: %w", err)
	}
	return mapRemote(&rr), nil
}

func mapRemote(rr *remoteResponse) *remoteResponse {
	// Normalise status vocabulary to local constants.
	switch rr.Status {
	case "", "ok", "active", "valid":
		rr.Status = StatusActive
	case "grace":
		rr.Status = StatusActive // panel-level grace policy owns the final state
	case "expired":
		rr.Status = StatusExpired
	case "suspended":
		rr.Status = StatusSuspended
	case "invalid", "revoked", "not_found":
		rr.Status = StatusInvalid
	default:
		rr.Status = StatusInvalid
	}
	return rr
}

func (c *RemoteClient) Activate(ctx context.Context, key, fingerprint string) (*remoteResponse, error) {
	return c.call(ctx, "/v1/activate", map[string]string{
		"license_key": key,
		"fingerprint": fingerprint,
	})
}

func (c *RemoteClient) Validate(ctx context.Context, fingerprint string) (*remoteResponse, error) {
	return c.call(ctx, "/v1/validate", map[string]string{"fingerprint": fingerprint})
}

func (c *RemoteClient) Deactivate(ctx context.Context, licenseID, fingerprint string) error {
	_, err := c.call(ctx, "/v1/deactivate", map[string]string{
		"license_id":  licenseID,
		"fingerprint": fingerprint,
	})
	return err
}
