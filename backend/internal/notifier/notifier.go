// Package notifier delivers alert notifications to configured channels
// (webhook, Slack, Discord, SMTP email). Delivery runs as a background job
// (notify_alert) with the runner's standard retry/backoff; channel config is
// stored in PostgreSQL so the only state is durable.
package notifier

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel types.
const (
	TypeWebhook = "webhook"
	TypeSlack   = "slack"
	TypeDiscord = "discord"
	TypeEmail   = "email"
)

// Channel is one configured delivery target.
type Channel struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Config   map[string]any `json:"config"` // webhook_url, smtp_*, from/to
	Severity string         `json:"severity"` // minimum severity to deliver (warning | critical)
	Enabled  bool           `json:"enabled"`
	Created  string         `json:"created_at"`

	ConfigRaw string `json:"-"` // scan target for updates
}

type Service struct {
	pool  *pgxpool.Pool
	http  *http.Client
	audit *audit.Service
	log   *slog.Logger
}

func NewService(pool *pgxpool.Pool, auditSvc *audit.Service, log *slog.Logger) *Service {
	return &Service{
		pool:  pool,
		http:  &http.Client{Timeout: 20 * time.Second},
		audit: auditSvc,
		log:   log,
	}
}

// ListChannels returns all channels, most recent first.
func (s *Service) ListChannels(ctx context.Context) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, name, type, config::text, severity, enabled, created_at::text
		FROM notification_channels ORDER BY created_at DESC`)
	if err != nil {
		return nil, apierror.From(err)
	}
	defer rows.Close()
	out := []Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, apierror.From(err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func scanChannel(row pgx.Row) (Channel, error) {
	var c Channel
	var configRaw, created string
	if err := row.Scan(&c.ID, &c.Name, &c.Type, &configRaw, &c.Severity, &c.Enabled, &created); err != nil {
		return c, err
	}
	c.Config = map[string]any{}
	if configRaw != "" {
		_ = json.Unmarshal([]byte(configRaw), &c.Config)
	}
	c.Created = created
	return c, nil
}

// CreateChannel validates and stores a channel.
func (s *Service) CreateChannel(ctx context.Context, name, chType string, config map[string]any, severity string, enabled bool) (*Channel, error) {
	if strings.TrimSpace(name) == "" || len(name) > 128 {
		return nil, apierror.BadRequest("channel name is required")
	}
	switch chType {
	case TypeWebhook, TypeSlack, TypeDiscord, TypeEmail:
	default:
		return nil, apierror.BadRequest("channel type must be webhook, slack, discord or email")
	}
	if severity != "critical" {
		severity = "warning"
	}
	if err := validateConfig(chType, config); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return nil, apierror.From(err)
	}
		row := s.pool.QueryRow(ctx, `
		INSERT INTO notification_channels (name, type, config, severity, enabled)
		VALUES ($1, $2, $3::jsonb, $4, $5)
		RETURNING id, name, type, config::text, severity, enabled, created_at::text`,
		name, chType, string(raw), severity, enabled)
	c, err := scanChannel(row)
	if err != nil {
		return nil, apierror.From(err)
	}
	s.audit.Log(ctx, audit.Entry{
		ActorType: "system", Label: "system",
		Action: "notifications.channel_created", Resource: "notification_channel", ResourceID: c.ID,
		Metadata: map[string]any{"name": c.Name, "type": c.Type},
	})
	return &c, nil
}

// UpdateChannel edits mutable fields.
func (s *Service) UpdateChannel(ctx context.Context, id string, name *string, config map[string]any, severity *string, enabled *bool) (*Channel, error) {
	if config != nil {
		if err := validateConfig("", config); err != nil {
			return nil, err
		}
	}
	var c Channel
	var configRaw, created string
	err := s.pool.QueryRow(ctx, `
		UPDATE notification_channels SET
			name   = COALESCE($2, name),
			config = CASE WHEN $3 IS NULL THEN config ELSE $3::jsonb END,
			severity = COALESCE($4, severity),
			enabled  = COALESCE($5, enabled),
			updated_at = now()
		WHERE id = $1
		RETURNING id, name, type, config::text, severity, enabled, created_at::text`,
		id, name, nullJSON(config), severity, enabled).
		Scan(&c.ID, &c.Name, &c.Type, &configRaw, &c.Severity, &c.Enabled, &created)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("channel")
		}
		return nil, apierror.From(err)
	}
	return s.GetChannel(ctx, id)
}

// GetChannel fetches one channel.
func (s *Service) GetChannel(ctx context.Context, id string) (*Channel, error) {
	c, err := scanChannel(s.pool.QueryRow(ctx, `
		SELECT id, name, type, config::text, severity, enabled, created_at::text
		FROM notification_channels WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierror.NotFound("channel")
		}
		return nil, apierror.From(err)
	}
	return &c, nil
}

// DeleteChannel removes a channel.
func (s *Service) DeleteChannel(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification_channels WHERE id = $1`, id)
	if err != nil {
		return apierror.From(err)
	}
	if tag.RowsAffected() == 0 {
		return apierror.NotFound("channel")
	}
	s.audit.Log(ctx, audit.Entry{
		ActorType: "system", Label: "system",
		Action: "notifications.channel_deleted", Resource: "notification_channel", ResourceID: id,
	})
	return nil
}

func nullJSON(v map[string]any) any {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	return string(raw)
}

// validateConfig checks required fields per type (secrets are never echoed).
func validateConfig(chType string, config map[string]any) error {
	if chType == TypeEmail || chType == "" && config["smtp_host"] != nil {
		if s, _ := config["smtp_host"].(string); s == "" {
			return apierror.BadRequest("smtp_host is required for email channels")
		}
		if s, _ := config["from"].(string); s == "" {
			return apierror.BadRequest("from address is required for email channels")
		}
		if to, _ := config["to"].(string); to == "" {
			return apierror.BadRequest("to address is required for email channels")
		}
		return nil
	}
	webhookURL, _ := config["webhook_url"].(string)
	if webhookURL == "" {
		return apierror.BadRequest("webhook_url is required")
	}
	if !strings.HasPrefix(webhookURL, "http://") && !strings.HasPrefix(webhookURL, "https://") {
		return apierror.BadRequest("webhook_url must be http(s)")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Delivery (invoked by the notify_alert job handler)
// ---------------------------------------------------------------------------

// AlertPayload is the normalized notification body shared by all channels.
type AlertPayload struct {
	Event       string  `json:"event"` // triggered | acknowledged | resolved
	Severity    string  `json:"severity"`
	ServerName  string  `json:"server_name"`
	ServerID    string  `json:"server_id"`
	Rule        string  `json:"rule"`
	RuleType    string  `json:"rule_type"`
	Message     string  `json:"message"`
	MetricValue *float64 `json:"metric_value"`
	Threshold   *float64 `json:"threshold"`
	TriggeredAt string  `json:"triggered_at"`
	PanelURL    string  `json:"panel_url"`
}

// Deliver sends one payload through a channel.
func (s *Service) Deliver(ctx context.Context, ch Channel, p AlertPayload) error {
	switch ch.Type {
	case TypeWebhook, TypeSlack, TypeDiscord:
		return s.deliverWebhook(ctx, ch, p)
	case TypeEmail:
		return s.deliverEmail(ch, p)
	default:
		return fmt.Errorf("unsupported channel type %q", ch.Type)
	}
}

func (s *Service) deliverWebhook(ctx context.Context, ch Channel, p AlertPayload) error {
	url, _ := ch.Config["webhook_url"].(string)
	var body any
	switch ch.Type {
	case TypeSlack:
		body = map[string]any{"text": p.Message}
	case TypeDiscord:
		body = map[string]any{"content": p.Message}
	default:
		body = p
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func (s *Service) deliverEmail(ch Channel, p AlertPayload) error {
	host, _ := ch.Config["smtp_host"].(string)
	port := 587
	if pv, ok := ch.Config["smtp_port"].(float64); ok && pv > 0 {
		port = int(pv)
	}
	username, _ := ch.Config["smtp_username"].(string)
	password, _ := ch.Config["smtp_password"].(string)
	from, _ := ch.Config["from"].(string)
	to, _ := ch.Config["to"].(string)

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	subject := fmt.Sprintf("[EpicPanel %s] %s — %s", strings.ToUpper(p.Severity), p.Rule, p.ServerName)
	text := fmt.Sprintf("%s\n\nServer: %s\nRule: %s (%s)\nMessage: %s\nTriggered: %s\n\nPanel: %s",
		p.Event, p.ServerName, p.Rule, p.RuleType, p.Message, p.TriggeredAt, p.PanelURL)
	msg := "From: " + from + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + mailHeader(subject) + "\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n" +
		text

	client, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer client.Close()
	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(mailAddress(from)); err != nil {
		return err
	}
	if err := client.Rcpt(mailAddress(to)); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	return w.Close()
}

func mailHeader(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", "")
}

func mailAddress(s string) string {
	if a, err := mail.ParseAddress(s); err == nil {
		return a.Address
	}
	return s
}

// ---------------------------------------------------------------------------
// notify_alert job handler
// ---------------------------------------------------------------------------

// HandleNotifyAlert delivers one alert to one channel; returns an error so
// the runner retries with backoff. The payload carries channel_id + alert
// fields to keep the job self-contained.
func (s *Service) HandleNotifyAlert(ctx context.Context, job *jobs.Job, _ jobs.ProgressFunc) error {
	chID, _ := job.Payload["channel_id"].(string)
	if chID == "" {
		return errors.New("notify job missing channel_id")
	}
	ch, err := s.GetChannel(ctx, chID)
	if err != nil {
		return err
	}
	if !ch.Enabled {
		return nil // channel was disabled since enqueue; nothing to do
	}
	p := AlertPayload{
		Event:       stringOf(job.Payload, "event"),
		Severity:    stringOf(job.Payload, "severity"),
		ServerName:  stringOf(job.Payload, "server_name"),
		ServerID:    stringOf(job.Payload, "server_id"),
		Rule:        stringOf(job.Payload, "rule"),
		RuleType:    stringOf(job.Payload, "rule_type"),
		Message:     stringOf(job.Payload, "message"),
		TriggeredAt: stringOf(job.Payload, "triggered_at"),
		PanelURL:    stringOf(job.Payload, "panel_url"),
	}
	if v, ok := job.Payload["metric_value"].(float64); ok {
		p.MetricValue = &v
	}
	if v, ok := job.Payload["threshold"].(float64); ok {
		p.Threshold = &v
	}
	if err := s.Deliver(ctx, *ch, p); err != nil {
		s.log.Warn("notification delivery failed", "channel", ch.Name, "err", err)
		return err
	}
	s.audit.Log(ctx, audit.Entry{
		ActorType: "system", Label: "monitoring",
		Action: "notifications.delivered", Resource: "notification_channel", ResourceID: ch.ID,
		Metadata: map[string]any{"rule": p.Rule, "event": p.Event},
	})
	return nil
}

func stringOf(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}
