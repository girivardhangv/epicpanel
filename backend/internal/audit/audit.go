package audit

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	ActorUser     = "user"
	ActorSystem   = "system"
	ActorAPIToken = "api_token"

	ResultSuccess = "success"
	ResultFailure = "failure"
)

type Entry struct {
	OrganizationID *uuid.UUID
	ActorUserID    *uuid.UUID
	ActorType      string
	Action         string
	ResourceType   string
	ResourceID     string
	Result         string
	Metadata       map[string]any
	IP             string
}

type Store struct {
	Pool *pgxpool.Pool
}

func (s *Store) Record(ctx context.Context, e Entry) error {
	if e.ActorType == "" {
		e.ActorType = ActorUser
	}
	if e.Result == "" {
		e.Result = ResultSuccess
	}
	meta := []byte("{}")
	if e.Metadata != nil {
		b, err := json.Marshal(e.Metadata)
		if err != nil {
			return err
		}
		meta = b
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO audit_logs
			(organization_id, actor_user_id, actor_type, action, resource_type, resource_id, result, metadata, ip)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, e.OrganizationID, e.ActorUserID, e.ActorType, e.Action, e.ResourceType, e.ResourceID, e.Result, meta, e.IP)
	return err
}

func (s *Store) RecordBestEffort(ctx context.Context, e Entry) {
	if err := s.Record(ctx, e); err != nil {
		slog.Error("audit record failed", "action", e.Action, "err", err)
	}
}

type LogRow struct {
	ID           int64           `json:"id"`
	Organization *uuid.UUID      `json:"organization_id"`
	ActorUser    *uuid.UUID      `json:"actor_user_id"`
	ActorEmail   *string         `json:"actor_email"`
	ActorType    string          `json:"actor_type"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Result       string          `json:"result"`
	Metadata     json.RawMessage `json:"metadata"`
	IP           string          `json:"ip"`
	CreatedAt    string          `json:"created_at"`
}

func (s *Store) List(ctx context.Context, orgID *uuid.UUID, limit, offset int) ([]LogRow, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id, a.organization_id, a.actor_user_id, u.email, a.actor_type,
		       a.action, a.resource_type, a.resource_id, a.result, a.metadata, a.ip, a.created_at
		FROM audit_logs a
		LEFT JOIN users u ON u.id = a.actor_user_id
		WHERE ($1::uuid IS NULL OR a.organization_id = $1)
		ORDER BY a.created_at DESC
		LIMIT $2 OFFSET $3
	`, orgID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LogRow
	for rows.Next() {
		var r LogRow
		var createdAt time.Time
		if err := rows.Scan(&r.ID, &r.Organization, &r.ActorUser, &r.ActorEmail, &r.ActorType,
			&r.Action, &r.ResourceType, &r.ResourceID, &r.Result, &r.Metadata, &r.IP, &createdAt); err != nil {
			return nil, err
		}
		r.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		out = append(out, r)
	}
	return out, rows.Err()
}
