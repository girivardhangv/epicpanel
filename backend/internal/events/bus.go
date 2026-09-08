// Package events is the platform event bus: domain events are persisted to
// the events table and fanned out to live subscribers (WebSocket clients).
// Delivery drivers: Postgres LISTEN/NOTIFY by default (boring, no extra
// moving parts) or Redis pub/sub when EPICPANEL_REDIS_URL is set — both
// implement the same Driver interface so later phases don't care which is
// active.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Channel is the pub/sub channel name shared by every driver.
const Channel = "epicpanel_events"

// Event is one platform domain event (jobs, servers, websites, alerts...).
type Event struct {
	ID           int64      `json:"id,omitempty"`
	Type         string     `json:"type"` // job.claimed, job.succeeded, server.offline, website.ready, alert.raised, ...
	Organization *uuid.UUID `json:"organization_id,omitempty"`
	ActorType    string     `json:"actor_type"` // user | system | api_token
	ResourceType string     `json:"resource_type,omitempty"`
	ResourceID   string     `json:"resource_id,omitempty"`
	Payload      any        `json:"payload,omitempty"`
	CreatedAt    time.Time  `json:"created_at,omitempty"`
}

// Handler receives decoded events from the subscribed driver.
type Handler func(Event)

// Driver publishes and delivers raw event JSON.
type Driver interface {
	Publish(ctx context.Context, payload []byte) error
	Subscribe(ctx context.Context, onEvent Handler) // blocking; returns when ctx is done
	Close(ctx context.Context)
}

// Bus persists events and fans them out locally + via the driver.
type Bus struct {
	Pool   *pgxpool.Pool
	Driver Driver // nil = local-only (no cross-process fanout)
}

// Publish records the event durably, then notifies subscribers. The events
// table is the source of truth; live delivery is best-effort.
func (b *Bus) Publish(ctx context.Context, ev Event) {
	if ev.ActorType == "" {
		ev.ActorType = "system"
	}
	ev.CreatedAt = time.Now().UTC()

	var orgID *uuid.UUID
	row := b.Pool.QueryRow(ctx, `
		INSERT INTO events (type, organization_id, actor_type, resource_type, resource_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, organization_id
	`, ev.Type, ev.Organization, ev.ActorType, ev.ResourceType, ev.ResourceID, mustJSON(ev.Payload))
	if err := row.Scan(&ev.ID, &orgID); err != nil {
		slog.Warn("event persist failed", "type", ev.Type, "err", err)
		return
	}
	ev.Organization = orgID

	// Local in-process delivery.
	for _, h := range localHandlers {
		safeHandle(h, ev)
	}

	if b.Driver == nil {
		return
	}
	// Cross-process: cap NOTIFY payloads at ~7KB (8KB protocol limit).
	bj, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if len(bj) > 7*1024 {
		ev.Payload = nil
		bj, _ = json.Marshal(ev)
	}
	if err := b.Driver.Publish(ctx, bj); err != nil {
		slog.Warn("event publish failed", "type", ev.Type, "err", err)
	}
}

var localHandlers []Handler

// SubscribeLocal registers an in-process listener (WS hub registers itself).
func SubscribeLocal(h Handler) {
	localHandlers = append(localHandlers, h)
}

func safeHandle(h Handler, ev Event) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("event handler panicked", "type", ev.Type, "panic", rec)
		}
	}()
	h(ev)
}

func mustJSON(v any) []byte {
	if v == nil {
		return []byte("{}")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("{}")
	}
	return b
}
