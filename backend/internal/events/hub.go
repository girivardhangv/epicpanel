// Package events — WebSocket hub. GET /v1/ws upgrades an authenticated
// request and streams JSON event frames scoped to the caller's organizations
// (platform admins receive everything). Phase 3 layers the live-metrics
// protocol on top of this skeleton.
package events

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/epicbyte/epicpanel/backend/internal/organizations"
)

// Upgrader is created by the api wiring with an origin check that mirrors
// the CORS configuration (cross-origin browser clients must be allowed
// explicitly; non-browser clients don't send Origin).
type WSOptions struct {
	AllowedOrigins []string
	Orgs           *organizations.Store
	// UserOrgs resolves the organizations a user may see events for.
	UserOrgs func(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}

type client struct {
	conn   *websocket.Conn
	orgs   map[uuid.UUID]bool // nil = all (platform admin)
	send   chan []byte
	closed chan struct{}
}

// Hub fans driver events out to subscribed WebSocket clients.
type Hub struct {
	Opts    WSOptions
	mu      sync.Mutex
	clients map[*client]struct{}
}

func NewHub(opts WSOptions) *Hub {
	return &Hub{Opts: opts, clients: make(map[*client]struct{})}
}

// Run consumes bus events (local subscription) and dispatches to clients.
// Blocks until ctx is done; started by the api wiring.
func (h *Hub) Run(ctx context.Context, bus *Bus) {
	SubscribeLocal(func(ev Event) { h.dispatch(ev) })
	if bus.Driver != nil {
		// Cross-process events arrive via the driver; local Publish already
		// delivered them in-process, so nothing extra to subscribe here.
		_ = bus.Driver
	}
	<-ctx.Done()
}

// Dispatch routes one event to all matching WebSocket clients. Called by the
// driver subscriber for cross-process events and by the bus for local ones.
func (h *Hub) Dispatch(ev Event) { h.dispatch(ev) }

// BroadcastMetrics pushes a pre-built metrics frame to every connected
// client WITHOUT persisting to the events table (high-frequency frames must
// not hit the DB — protocol-metrics.md). All clients receive the fleet:
// servers are platform-wide infrastructure, matching REST policy.
func (h *Hub) BroadcastMetrics(frame any) {
	b, err := json.Marshal(map[string]any{"type": "metrics", "data": frame})
	if err != nil {
		return
	}
	h.broadcast(b)
}

// BroadcastServerState pushes a node connection-state transition
// (agent stream connected / dropped) to every client.
func (h *Hub) BroadcastServerState(serverID string, online bool) {
	b, err := json.Marshal(map[string]any{
		"type": "server_state", "data": map[string]any{"server_id": serverID, "connected": online},
	})
	if err != nil {
		return
	}
	h.broadcast(b)
}

// BroadcastJSON pushes one typed frame (server lifecycle, node sync) to every
// connected browser WITHOUT persisting to the events table.
func (h *Hub) BroadcastJSON(msgType string, data any) {
	b, err := json.Marshal(map[string]any{"type": msgType, "data": data})
	if err != nil {
		return
	}
	h.broadcast(b)
}

func (h *Hub) broadcast(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- b:
		default: // slow client: drop rather than block ingest
		}
	}
}

func (h *Hub) dispatch(ev Event) {
	b, err := json.Marshal(map[string]any{
		"type":            ev.Type,
		"organization_id": ev.Organization,
		"resource_type":   ev.ResourceType,
		"resource_id":     ev.ResourceID,
		"payload":         ev.Payload,
		"created_at":      ev.CreatedAt,
	})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		if c.orgs != nil && (ev.Organization == nil || !c.orgs[*ev.Organization]) {
			continue
		}
		select {
		case c.send <- b:
		default: // slow client: drop the frame rather than block the bus
		}
	}
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return false }, // replaced per-handler
}

// HandleWS serves GET /v1/ws. Auth is enforced by the wrapper that mounts it
// (session or API token); this handler resolves org scope and upgrades.
func (h *Hub) HandleWS(w http.ResponseWriter, r *http.Request, userID string, isPlatformAdmin, isToken bool, tokenOrg string) {
	allowed := originAllowed(r, h.Opts.AllowedOrigins)
	if !allowed {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	c := &client{send: make(chan []byte, 64), closed: make(chan struct{})}

	if isPlatformAdmin && !isToken {
		c.orgs = nil // sees all
	} else if isToken {
		orgID, err := uuid.Parse(tokenOrg)
		if err != nil {
			http.Error(w, "invalid token org", http.StatusForbidden)
			return
		}
		c.orgs = map[uuid.UUID]bool{orgID: true}
	} else {
		uid, err := uuid.Parse(userID)
		if err != nil {
			http.Error(w, "invalid user", http.StatusForbidden)
			return
		}
		orgs, err := h.Opts.UserOrgs(r.Context(), uid)
		if err != nil {
			http.Error(w, "org resolution failed", http.StatusInternalServerError)
			return
		}
		c.orgs = make(map[uuid.UUID]bool, len(orgs))
		for _, o := range orgs {
			c.orgs[o] = true
		}
	}

	up := upgrader
	up.CheckOrigin = func(_ *http.Request) bool { return true } // already validated above
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c.conn = conn

	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	hello, _ := json.Marshal(map[string]any{"type": "connected", "created_at": time.Now().UTC()})
	select {
	case c.send <- hello:
	default:
	}

	go h.writeLoop(c)
	h.readLoop(c)
}

func originAllowed(r *http.Request, allowed []string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser clients
	}
	// Same-origin deployments: the API binary itself serves the panel, so the
	// page origin is exactly this request's scheme+host. Browsers ALWAYS attach
	// Origin to WebSocket handshakes (unlike same-origin fetches), and the
	// default EPICPANEL_CORS_ORIGINS only lists dev vite ports — without this
	// check every production install 403s its own panel's live feed.
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	if origin == scheme+"://"+r.Host {
		return true
	}
	for _, a := range allowed {
		if a == origin {
			return true
		}
	}
	return false
}

func (h *Hub) readLoop(c *client) {
	defer h.remove(c)
	c.conn.SetReadLimit(4096)
	for {
		if _, _, err := c.conn.ReadMessage(); err != nil {
			return
		}
	}
}

func (h *Hub) writeLoop(c *client) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case msg := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				h.remove(c)
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				h.remove(c)
				return
			}
		case <-c.closed:
			return
		}
	}
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.closed)
	}
	h.mu.Unlock()
	_ = c.conn.Close()
}
