package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Client talks to the EpicPanel control plane with the agent token.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL:    baseURL,
		token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

type Job struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"max_attempts"`
}

// Claim fetches the next pending job, or nil.
func (c *Client) Claim(ctx context.Context) (*Job, error) {
	var out struct {
		Job *Job `json:"job"`
	}
	if err := c.post(ctx, "/v1/agent/jobs/claim", nil, &out); err != nil {
		return nil, err
	}
	return out.Job, nil
}

type ResultRequest struct {
	Success bool            `json:"success"`
	Error   string          `json:"error,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
}

func (c *Client) ReportResult(ctx context.Context, jobID string, req ResultRequest) error {
	return c.post(ctx, "/v1/agent/jobs/"+jobID+"/result", req, nil)
}

// ReportProgress streams live install progress to the control plane. Best
// effort: a failed progress update must never fail the job.
func (c *Client) ReportProgress(ctx context.Context, jobID string, percent int, step string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := c.post(ctx, "/v1/agent/jobs/"+jobID+"/progress", map[string]any{"progress": percent, "step": step}, nil); err != nil {
		slog.Debug("progress report failed", "job", jobID, "err", err)
	}
}

// Heartbeat sends liveness to the control plane over HTTP. It is the legacy
// fallback path (used when the metrics stream is disabled); while the
// agentproto stream is connected it also keeps the server's last_seen_at
// fresh on the control plane.
func (c *Client) Heartbeat(ctx context.Context) error {
	return c.post(ctx, "/v1/agent/heartbeat", map[string]any{}, nil)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("control plane %s %s: %d %s", http.MethodPost, path, resp.StatusCode, string(b))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			return fmt.Errorf("decode response: %w", err)
		}
	}
	return nil
}

// --- tiny parsing helpers (kept dependency-free) ---

func splitLines(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, string(b[start:i]))
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func firstLine(b []byte) string {
	for i, c := range b {
		if c == '\n' {
			return string(b[:i])
		}
	}
	return string(b)
}

func HeartbeatOnce(ctx context.Context, c *Client, cfg Config) error {
	if err := c.Heartbeat(ctx); err != nil {
		slog.Debug("heartbeat error", "err", err)
		return err
	}
	return nil
}
