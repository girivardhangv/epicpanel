package events

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PGDriver delivers events via Postgres LISTEN/NOTIFY. Works across API
// instances sharing the database with zero extra infrastructure.
type PGDriver struct {
	Pool *pgxpool.Pool
	conn *pgxpool.Conn
}

// NewPGDriver acquires a dedicated connection and starts LISTENing.
func NewPGDriver(ctx context.Context, pool *pgxpool.Pool) (*PGDriver, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		conn.Release()
		return nil, err
	}
	return &PGDriver{Pool: pool, conn: conn}, nil
}

func (d *PGDriver) Publish(ctx context.Context, payload []byte) error {
	_, err := d.Pool.Exec(ctx, "SELECT pg_notify($1, $2)", Channel, string(payload))
	return err
}

// Subscribe blocks until ctx is done, dispatching every received notification.
func (d *PGDriver) Subscribe(ctx context.Context, onEvent Handler) {
	for ctx.Err() == nil {
		n, err := d.conn.Conn().WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("events: notification wait failed", "err", err)
			return
		}
		decodeAndDispatch(onEvent, []byte(n.Payload))
	}
}

func (d *PGDriver) Close(_ context.Context) {
	if d.conn != nil {
		d.conn.Release()
		d.conn = nil
	}
}

func decodeAndDispatch(onEvent Handler, payload []byte) {
	var ev Event
	if err := json.Unmarshal(payload, &ev); err != nil {
		slog.Warn("events: undecodable notification dropped", "err", err)
		return
	}
	onEvent(ev)
}
