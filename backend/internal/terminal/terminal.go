package terminal

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
	osuser "os/user"

	"github.com/gorilla/websocket"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/isolation"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// Limits: hard boundaries for the web terminal.
const (
	// IdleTimeout closes sessions idle longer than this.
	IdleTimeout = 10 * time.Minute
	// MaxSessionsPerSite caps concurrent terminals per website.
	MaxSessionsPerSite = 2
	// MaxSessionsGlobal caps the whole panel.
	MaxSessionsGlobal = 32
	// MaxMessageBytes caps a single inbound write (paste flood guard).
	MaxMessageBytes = 16 * 1024
)

type session struct {
	id        string
	websiteID uuid.UUID
	orgID     uuid.UUID
	uid       int
	ptyFile   *os.File
	ws        *websocket.Conn
	lastWrite time.Time
	mu        sync.Mutex
}

type Hub struct {
	mu       sync.Mutex
	sessions map[string][]*session // by website
	total    int
}

func NewHub() *Hub { return &Hub{sessions: make(map[string][]*session)} }

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Same-origin is enforced by the session cookie; the panel may also be
	// reached via a different origin (dev server) — allow and rely on auth.
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Handler serves GET /v1/organizations/{org}/websites/{ws}/terminal
// (upgraded to WebSocket). Auth: session cookie via middleware (developer+).
type Handler struct {
	Hub        *Hub
	Orgs       *organizations.Store
	Websites   *websites.Store
	RequireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/organizations/{org_id}/websites/{website_id}/terminal", h.requireOrgDev(h.terminal))
}

// Admin+ (Phase 5): the web terminal is an admin-only tool. The customer
// cPanel never links it, and org members below admin rank are refused
// server-side too — the UI hiding is not the enforcement boundary.
func (h *Handler) requireOrgDev(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if _, apiErr := h.RequireOrg(r, r.PathValue("org_id"), organizations.RoleAdmin); apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r)
	}
}

func (h *Handler) terminal(w http.ResponseWriter, r *http.Request) {
	orgID, err := uuid.Parse(r.PathValue("org_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization id"))
		return
	}
	user, _ := httpapi.UserFrom(r.Context())
	if user == nil || httpapi.IsAPIToken(r.Context()) {
		httpapi.RespondError(w, httpapi.ErrForbidden("web terminal requires an interactive user session"))
		return
	}

	websiteID, err := uuid.Parse(r.PathValue("website_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid website id"))
		return
	}
	ws, err := h.Websites.GetByID(r.Context(), orgID, websiteID)
	if err != nil || ws.Status != "ready" {
		httpapi.RespondError(w, httpapi.ErrNotFound("website not found or not ready"))
		return
	}

	// Site user + docroot (identity we will run as).
	siteBase := "/srv/epicpanel/websites/" + ws.ID.String()
	info, err := os.Stat(siteBase)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrNotFound("site tree missing"))
		return
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid == 0 {
		httpapi.RespondError(w, httpapi.ErrInternal(errors.New("site has no assigned user")))
		return
	}
	uid := int(st.Uid)
	gid := int(st.Gid)

	// Upgrade first so errors can be sent over WS if needed.
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("terminal upgrade failed", "err", err)
		return
	}

	sess := h.Hub.admit(websiteID, orgID, conn)
	if sess == nil {
		conn.WriteMessage(websocket.TextMessage, []byte("terminal session limit reached\r\n"))
		conn.Close()
		return
	}
	defer h.Hub.release(websiteID)

	auditTerminal(user.Email, ws.ID, "terminal.opened")

	// Resolve username for setuid exec.
	username, err := usernameForUID(uid)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("failed to resolve site user\r\n"))
		conn.Close()
		return
	}

	// Kernel sandbox: bwrap mount+PID namespaces, no-new-privs, rlimits,
	// running as the site user with the restricted shell inside.
	spec := isolation.Load(ws.ID.String(), siteBase, username, uid, gid)
	spec.UID = uid
	spec.GID = gid
	cmd, err := isolation.Command(spec, "/usr/local/bin/epicpanel-shell")
	if err != nil {
		// fail closed: never start an unsandboxed shell
		auditTerminal(user.Email, ws.ID, "terminal.sandbox_denied")
		conn.WriteMessage(websocket.TextMessage, []byte("terminal sandbox unavailable - session denied\r\n"))
		conn.Close()
		return
	}
	cmd.Dir = siteBase
	ptyFile, err := pty.Start(cmd)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte("failed to start shell: "+err.Error()+"\r\n"))
		conn.Close()
		return
	}
	defer func() {
		_ = ptyFile.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	sess.ptyFile = ptyFile
	// Initial window size.
	_ = pty.Setsize(ptyFile, &pty.Winsize{Rows: 30, Cols: 110})

	// Idle watchdog.
	done := make(chan struct{})
	timer := time.AfterFunc(IdleTimeout, func() {
		auditTerminal(user.Email, ws.ID, "terminal.idle_timeout")
		conn.WriteMessage(websocket.TextMessage, []byte("\r\nsession closed (idle timeout)\r\n"))
		conn.Close()
		close(done)
	})
	defer timer.Stop()

	// pty -> websocket
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptyFile.Read(buf)
			if n > 0 {
				sess.mu.Lock()
				sess.lastWrite = time.Now()
				sess.mu.Unlock()
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		timer.Stop()
		conn.Close()
	}()

	// websocket -> pty
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if len(data) > MaxMessageBytes {
			continue
		}
		// JSON control messages: {"resize": {"rows": R, "cols": C}}
		if mt == websocket.TextMessage && len(data) > 0 && data[0] == '{' {
			var ctl struct {
				Resize struct {
					Rows int `json:"rows"`
					Cols int `json:"cols"`
				} `json:"resize"`
			}
			if err := jsonUnmarshalCtl(data, &ctl); err == nil && ctl.Resize.Cols > 0 && ctl.Resize.Rows > 0 {
				_ = pty.Setsize(ptyFile, &pty.Winsize{Rows: uint16(ctl.Resize.Rows), Cols: uint16(ctl.Resize.Cols)})
				continue
			}
		}
		sess.mu.Lock()
		sess.lastWrite = time.Now()
		sess.mu.Unlock()
		if _, err := ptyFile.Write(data); err != nil {
			break
		}
	}
	auditTerminal(user.Email, ws.ID, "terminal.closed")
	select {
	case <-done:
	default:
	}
}

func (hub *Hub) admit(websiteID, orgID uuid.UUID, conn *websocket.Conn) *session {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.total >= MaxSessionsGlobal {
		return nil
	}
	// Per-site cap actually enforced: sessions are registered here (the
	// audit found the map was never written, so the cap never triggered).
	if len(hub.sessions[websiteID.String()]) >= MaxSessionsPerSite {
		return nil
	}
	sess := &session{
		id:        newSessionID(),
		websiteID: websiteID,
		orgID:     orgID,
		uid:       -1,
		ws:        conn,
		lastWrite: time.Now(),
	}
	hub.sessions[websiteID.String()] = append(hub.sessions[websiteID.String()], sess)
	hub.total++
	return sess
}

func (hub *Hub) release(websiteID uuid.UUID) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.total > 0 {
		hub.total--
	}
	list := hub.sessions[websiteID.String()]
	if len(list) > 0 {
		hub.sessions[websiteID.String()] = list[:len(list)-1]
	}
	if len(hub.sessions[websiteID.String()]) == 0 {
		delete(hub.sessions, websiteID.String())
	}
}

func newSessionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func usernameForUID(uid int) (string, error) {
	u, err := osuser.LookupId(fmt.Sprint(uid))
	if err != nil {
		return "", err
	}
	return u.Username, nil
}

var _ = net.JoinHostPort

func auditTerminal(email string, websiteID uuid.UUID, action string) {
	slog.Info("terminal", "action", action, "user", email, "website", websiteID)
}

// jsonUnmarshalCtl decodes resize controls without importing encoding/json
// at multiple call sites.
func jsonUnmarshalCtl(data []byte, v any) error {
	return ctlDecode(data, v)
}

func ctlDecode(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
