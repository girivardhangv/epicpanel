package servers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
)

// AgentStream serves GET /v1/agent/stream: the persistent node-agent metrics
// WebSocket (agentproto v1). Auth is the server agent token; browsers never
// connect here (no origin concerns — non-browser clients send no Origin).
// Frames are ingested into the LiveStore and fanned out via OnSample; the
// historical writer consumes the store asynchronously (metrics/history.go).
func (h *Handler) AgentStream(w http.ResponseWriter, r *http.Request) {
	srv, ok := ServerFromAgentContext(r)
	if !ok {
		httpapi.RespondError(w, httpapi.ErrUnauthorized("agent authentication required"))
		return
	}
	if h.Live == nil {
		httpapi.RespondError(w, httpapi.ErrInternal(errStr("metrics live store not wired")))
		return
	}

	up := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     func(_ *http.Request) bool { return true },
	}
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	live := h.Live
	live.Connected(srv.ID, "", true)
	if h.OnStreamEvent != nil {
		h.OnStreamEvent(srv.ID, true)
	}
	// A connected agent is a seen agent: refresh liveness in the DB too.
	_, _ = h.Store.Pool.Exec(r.Context(), `UPDATE servers SET last_seen_at = now(), updated_at = now() WHERE id = $1`, srv.ID)
	defer func() {
		live.Connected(srv.ID, "", false)
		if h.OnStreamEvent != nil {
			h.OnStreamEvent(srv.ID, false)
		}
	}()

	slog.Info("agent stream connected", "server", srv.ID.String())

	// Writer loop: welcome + periodic acks (last ingested seq).
	ackCh := make(chan int64, 8)
	go func() {
		defer func() {
			// Close the read loop on writer failure via a write deadline miss.
			_ = conn.Close()
		}()
		writeJSON := func(v any) bool {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			return conn.WriteJSON(v) == nil
		}
		if !writeJSON(agentproto.Frame{Type: agentproto.TypeWelcome, Ts: time.Now().UTC(),
			Data: mustJSONRaw(agentproto.Welcome{Protocol: agentproto.ProtocolVersion})}) {
			return
		}
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		lastAcked := int64(0)
		for {
			select {
			case seq, ok := <-ackCh:
				if !ok {
					return
				}
				if seq > lastAcked {
					lastAcked = seq
					if !writeJSON(agentproto.Frame{Type: agentproto.TypeAck, Ts: time.Now().UTC(),
						Data: mustJSONRaw(agentproto.Ack{Seq: lastAcked})}) {
						return
					}
				}
			case <-ticker.C:
				if !writeJSON(agentproto.Frame{Type: agentproto.TypePing, Ts: time.Now().UTC()}) {
					return
				}
			}
		}
	}()

	// Read loop: ingest frames until the agent goes away.
	conn.SetReadLimit(1024 * 1024)
	conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	for {
		var frame agentproto.Frame
		if err := conn.ReadJSON(&frame); err != nil {
			slog.Info("agent stream closed", "server", srv.ID.String(), "err", err)
			return
		}
		conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		frame.SessionID = coalesceSession(frame.SessionID, srv.ID.String())
		switch frame.Type {
		case agentproto.TypeMetrics, agentproto.TypeHeartbeat, agentproto.TypeHello, agentproto.TypeResume, agentproto.TypePong:
			if ok := live.Ingest(srv.ID, frame); ok {
				// Keep the DB liveness fresh without a write storm: the
				// scheduler fast-loop derives ONLINE/STALE/OFFLINE from the
				// live store; last_seen_at is updated at most every 15s.
				touchAgentSeen(h, srv.ID, frame)
				select {
				case ackCh <- frame.Seq:
				default:
				}
			}
		default:
			// Unknown frame type: tolerate (protocol forward compatibility).
		}
	}
}

// lastSeenWrites rate-limits last_seen_at updates per agent connection.
var lastSeenMu = struct {
	sync.Mutex
	m map[string]time.Time
}{m: map[string]time.Time{}}

func lastSeenAllow(key string, every time.Duration) bool {
	lastSeenMu.Lock()
	defer lastSeenMu.Unlock()
	if now := time.Now(); now.Sub(lastSeenMu.m[key]) < every {
		return false
	}
	lastSeenMu.m[key] = time.Now()
	return true
}

func touchAgentSeen(h *Handler, serverID uuid.UUID, _ agentproto.Frame) {
	if lastSeenAllow(serverID.String(), 15*time.Second) {
		_, _ = h.Store.Pool.Exec(context.Background(),
			`UPDATE servers SET last_seen_at = now(), updated_at = now() WHERE id = $1`, serverID)
	}
}

func coalesceSession(sid, fallback string) string {
	if sid == "" {
		return fallback
	}
	return sid
}

func mustJSONRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
}
