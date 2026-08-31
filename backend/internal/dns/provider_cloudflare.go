package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// cloudflareProvider syncs zones + records via the Cloudflare API v4.
type cloudflareProvider struct {
	token string
	client *http.Client
	log   *slog.Logger
}

const cloudflareBase = "https://api.cloudflare.com/client/v4"

// NewCloudflare returns a Cloudflare-backed Provider.
func NewCloudflare(token string, log *slog.Logger) Provider {
	if log == nil {
		log = slog.Default()
	}
	return &cloudflareProvider{
		token:  token,
		client: &http.Client{Timeout: 30 * time.Second},
		log:    log,
	}
}

type cfResponse struct {
	Success bool           `json:"success"`
	Errors  []cfAPIError   `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfAPIError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (p *cloudflareProvider) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, cloudflareBase+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set("Content-Type", "application/json")
	if body == nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))

	var cf cfResponse
	if err := json.Unmarshal(raw, &cf); err != nil {
		return fmt.Errorf("malformed cloudflare response: %w", err)
	}
	if !cf.Success {
		msgs := make([]string, 0, len(cf.Errors))
		for _, e := range cf.Errors {
			msgs = append(msgs, fmt.Sprintf("[%d] %s", e.Code, e.Message))
		}
		return fmt.Errorf("cloudflare: %s", strings.Join(msgs, "; "))
	}
	if out != nil && len(cf.Result) > 0 {
		if err := json.Unmarshal(cf.Result, out); err != nil {
			return fmt.Errorf("cloudflare result parse: %w", err)
		}
	}
	return nil
}

// EnsureZone creates the zone if it doesn't already exist.
func (p *cloudflareProvider) EnsureZone(ctx context.Context, zone Zone) (string, error) {
	// Existing?
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = p.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(zone.Domain), nil, &zones)
	for _, z := range zones {
		if strings.EqualFold(z.Name, zone.Domain) {
			return z.ID, nil
		}
	}
	// Create.
	var created struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	body := map[string]any{"name": zone.Domain, "type": "full"}
	if err := p.do(ctx, http.MethodPost, "/zones", body, &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

// recordPayload maps our record to the Cloudflare API shape.
func (p *cloudflareProvider) recordPayload(rec Record) map[string]any {
	name := rec.Name
	if name == "" {
		name = "@"
	}
	payload := map[string]any{
		"type":    rec.Type,
		"name":    name,
		"content": rec.Value,
		"ttl":     rec.TTL,
	}
	switch rec.Type {
	case "MX", "SRV":
		payload["priority"] = rec.Priority
	}
	if rec.Proxied && (rec.Type == "A" || rec.Type == "AAAA" || rec.Type == "CNAME") {
		payload["proxied"] = true
	}
	return payload
}

// UpsertRecord creates or updates one record (match by name+type).
func (p *cloudflareProvider) UpsertRecord(ctx context.Context, zone Zone, rec Record) (string, error) {
	zoneID, err := p.EnsureZone(ctx, zone)
	if err != nil {
		return "", err
	}
	// Full DNS name = record name joined to the zone apex (root = apex).
	fullName := zone.Domain
	if rec.Name != "" {
		fullName = rec.Name + "." + zone.Domain
	}
	// Find an existing record with the same name+type to update in place.
	var records []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	}
	_ = p.do(ctx, http.MethodGet,
		"/zones/"+zoneID+"/dns_records?name="+url.QueryEscape(fullName), nil, &records)
	for _, r := range records {
		if strings.EqualFold(r.Name, fullName) && strings.EqualFold(r.Type, rec.Type) {
			var updated struct{ ID string `json:"id"` }
			if err := p.do(ctx, http.MethodPut,
				"/zones/"+zoneID+"/dns_records/"+r.ID, p.recordPayload(rec), &updated); err != nil {
				return "", err
			}
			return updated.ID, nil
		}
	}
	// Create new.
	var created struct{ ID string `json:"id"` }
	if err := p.do(ctx, http.MethodPost,
		"/zones/"+zoneID+"/dns_records", p.recordPayload(rec), &created); err != nil {
		return "", err
	}
	return created.ID, nil
}

// DeleteRecord removes a record by its provider ID.
func (p *cloudflareProvider) DeleteRecord(ctx context.Context, zone Zone, providerRecordID string) error {
	if providerRecordID == "" {
		return nil // nothing to delete remotely
	}
	zoneID, err := p.EnsureZone(ctx, zone)
	if err != nil {
		return err
	}
	return p.do(ctx, http.MethodDelete,
		"/zones/"+zoneID+"/dns_records/"+providerRecordID, nil, nil)
}
