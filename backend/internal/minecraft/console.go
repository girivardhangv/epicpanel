// Instance console: an in-memory ring buffer of recent log lines per
// instance plus a WebSocket hub for live tailing. Lines arrive from agent
// mc_logs job results (agent-side scrubbed against the RCON password) and
// are scrubbed AGAIN here before they can ever reach a client.
//
// Command input is NOT part of the WS: commands go through the API
// allowlist (mc_command → RCON). The WS accepts no input at all — a
// customer can never send a shell command, only allowlisted console
// commands, and only through the validated REST path.
package minecraft

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// RingLines is the per-instance history size (~1000 lines).
const RingLines = 1000

// MaxLineBytes caps one console line (log-flood guard).
const MaxLineBytes = 4096

// consoleStore holds one ring per instance.
type consoleStore struct {
	mu    sync.Mutex
	rings map[uuid.UUID]*consoleRing
}

type consoleRing struct {
	lines []ConsoleLine
	next  int64
}

var consoles = &consoleStore{rings: map[uuid.UUID]*consoleRing{}}

// AppendConsole adds scrubbed lines to an instance's ring and fans out to
// subscribed websockets. Idempotent per seq (agent job retries can replay).
func AppendConsole(instanceID uuid.UUID, lines []ConsoleLine, secrets []string) {
	if len(lines) == 0 {
		return
	}
	consoles.mu.Lock()
	r := consoles.rings[instanceID]
	if r == nil {
		r = &consoleRing{}
		consoles.rings[instanceID] = r
	}
	latest := r.next
	for _, l := range lines {
		if l.Seq <= latest {
			continue // already seen (replayed job)
		}
		if l.Seq > latest {
			latest = l.Seq
		}
		r.lines = append(r.lines, ConsoleLine{Seq: l.Seq, Ts: l.Ts, Text: ScrubText(l.Text, secrets)})
	}
	r.next = latest
	if len(r.lines) > RingLines {
		r.lines = r.lines[len(r.lines)-RingLines:]
	}
	consoles.mu.Unlock()

	for _, l := range lines {
		b, err := json.Marshal(map[string]any{"type": "mc_console", "instance_id": instanceID.String(), "line": l})
		if err == nil {
			consoleHub.broadcast(instanceID, b)
		}
	}
}

// ConsoleTail returns the last n lines of the ring (oldest first).
func ConsoleTail(instanceID uuid.UUID, n int) []ConsoleLine {
	if n <= 0 || n > RingLines {
		n = RingLines
	}
	consoles.mu.Lock()
	defer consoles.mu.Unlock()
	r := consoles.rings[instanceID]
	if r == nil {
		return nil
	}
	start := 0
	if len(r.lines) > n {
		start = len(r.lines) - n
	}
	out := make([]ConsoleLine, len(r.lines)-start)
	copy(out, r.lines[start:])
	return out
}

// DropConsole removes a deleted instance's ring (no unbounded memory growth).
func DropConsole(instanceID uuid.UUID) {
	consoles.mu.Lock()
	delete(consoles.rings, instanceID)
	consoles.mu.Unlock()
}

// ---------------------------------------------------------------------------
// WebSocket hub (live tail)
// ---------------------------------------------------------------------------

type consoleClient struct {
	instanceID uuid.UUID
	send       chan []byte
}

// ConsoleHub fans console frames out to subscribed browsers.
type ConsoleHub struct {
	mu      sync.Mutex
	clients map[uuid.UUID]map[*consoleClient]struct{}
}

// consoleHub is the package-level hub (wired by the api registration).
var consoleHub = &ConsoleHub{clients: map[uuid.UUID]map[*consoleClient]struct{}{}}

func (h *ConsoleHub) broadcast(instanceID uuid.UUID, b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients[instanceID] {
		select {
		case c.send <- b:
		default: // slow client: drop rather than block the ring
		}
	}
}

func (h *ConsoleHub) subscribe(instanceID uuid.UUID) *consoleClient {
	c := &consoleClient{instanceID: instanceID, send: make(chan []byte, 128)}
	h.mu.Lock()
	if h.clients[instanceID] == nil {
		h.clients[instanceID] = map[*consoleClient]struct{}{}
	}
	h.clients[instanceID][c] = struct{}{}
	h.mu.Unlock()
	return c
}

func (h *ConsoleHub) unsubscribe(c *consoleClient) {
	h.mu.Lock()
	if set := h.clients[c.instanceID]; set != nil {
		delete(set, c)
		if len(set) == 0 {
			delete(h.clients, c.instanceID)
		}
	}
	h.mu.Unlock()
	close(c.send)
}

var consoleUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	// Origin is enforced by session auth + CSRF model; the panel may be
	// reached cross-origin in dev (same policy as the terminal hub).
	CheckOrigin: func(r *http.Request) bool { return true },
}

// ServeConsoleWS upgrades GET .../minecraft/{id}/console/ws and tails the
// ring. INBOUND MESSAGES ARE IGNORED except a ping/pong contract — a
// customer can never send input through this socket. Server commands go
// through the REST allowlist only.
func ServeConsoleWS(w http.ResponseWriter, r *http.Request, instanceID uuid.UUID) {
	conn, err := consoleUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := consoleHub.subscribe(instanceID)
	defer func() {
		consoleHub.unsubscribe(c)
		_ = conn.Close()
	}()

	// Seed: current tail.
	tail := ConsoleTail(instanceID, 200)
	if tail != nil {
		b, _ := json.Marshal(map[string]any{"type": "mc_console", "instance_id": instanceID.String(), "tail": tail})
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_ = conn.WriteMessage(websocket.TextMessage, b)
	}

	// Writer pump.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for msg := range c.send {
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// Reader pump: read and DISCARD (controlled input = none through this
	// socket). Pongs keep the read deadline refreshed.
	conn.SetReadLimit(512)
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// RunConsoleJanitor trims rings for deleted instances (called by a scheduler).
func RunConsoleJanitor(ctx context.Context, store *Store, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rows, err := store.Pool.Query(ctx, `SELECT id FROM minecraft_instances WHERE status = 'deleted'`)
				if err != nil {
					continue
				}
				for rows.Next() {
					var id uuid.UUID
					if rows.Scan(&id) == nil {
						DropConsole(id)
					}
				}
				rows.Close()
			}
		}
	}()
}
