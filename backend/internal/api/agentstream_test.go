package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/epicbyte/epicpanel/backend/internal/metrics"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// waitMetric polls fn until it returns true or the timeout elapses.
func waitMetric(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

// TestAgentStreamEndToEnd registers a server, enrolls an agent, streams
// samples over /v1/agent/stream, and asserts: (1) live-store ingest with
// sequence continuity, (2) REST /metrics serves the LIVE frame from memory,
// (3) an authenticated browser WS client receives pushed metrics frames.
func TestAgentStreamEndToEnd(t *testing.T) {
	srv, admin := newTestServer(t)
	base := admin.base
	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "stream-admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register admin: %d", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "StreamOrg"})
	if resp.status != http.StatusCreated {
		t.Fatalf("create org: %d", resp.status)
	}
	orgID, _ := resp.body["id"].(string)

	// Register + enroll the agent (same flow as TestServerLifecycle).
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "stream-node"})
	if resp.status != http.StatusCreated {
		t.Fatalf("create server: %d %v", resp.status, resp.body)
	}
	serverID, _ := resp.body["server"].(map[string]any)["id"].(string)
	regToken, _ := resp.body["registration_token"].(string)

	resp = admin.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "stream-host", "os_info": "test-os", "agent_version": "test",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("enroll: %d %v", resp.status, resp.body)
	}
	agentToken, _ := resp.body["agent_token"].(string)

	// Connect the agent stream (agent token auth).
	agentConn := dialAgentStream(t, base, agentToken)
	defer agentConn.Close()
	mustWriteFrame(t, agentConn, agentproto.Frame{
		Type: agentproto.TypeHello, SessionID: "sess-e2e",
		Data: mustMarshal(agentproto.Hello{Protocol: agentproto.ProtocolVersion, AgentVersion: "test", Hostname: "stream-host"}),
	})

	sample := agentproto.Sample{Node: agentproto.NodeSample{
		CPUPercent: 82, CPUCores: 8, Load1: 4.2, Load5: 3.1, Load15: 2.0,
		MemoryTotal: 32 << 30, MemoryUsed: 16 << 30, MemoryAvailable: 16 << 30,
		SwapTotal: 8 << 30, SwapUsed: 1 << 30,
		Net:            agentproto.NetSample{RxBPS: 1_000_000, TxBPS: 500_000, RxBytes: 1000, TxBytes: 1000},
		Disks:          []agentproto.DiskSample{{Fs: "ext4", Mount: "/", TotalBytes: 500 << 30, UsedBytes: 250 << 30, InodesTotal: 30_000_000, InodesUsed: 1_000_000}},
		TCPEstablished: 42, TCPTotal: 100, Processes: 321, UptimeS: 987654,
	}}
	sid := mustUUID(t, serverID)

	// Browser WS client (session cookie auth) — connected before frames so
	// it receives the live broadcasts.
	browserConn := dialBrowserWS(t, base, admin.http.Jar)
	defer browserConn.Close()

	// Wait for the control-plane welcome before streaming.
	waitForFrame(t, agentConn, 3*time.Second, func(f agentproto.Frame) bool { return f.Type == agentproto.TypeWelcome })

	// Send two samples; both must be acked (sequence continuity).
	mustWriteFrame(t, agentConn, agentproto.Frame{Type: agentproto.TypeMetrics, SessionID: "sess-e2e", Seq: 1, Ts: time.Now().UTC(), Data: mustMarshal(sample)})
	mustWriteFrame(t, agentConn, agentproto.Frame{Type: agentproto.TypeMetrics, SessionID: "sess-e2e", Seq: 2, Ts: time.Now().UTC(), Data: mustMarshal(sample)})
	waitForFrame(t, agentConn, 3*time.Second, func(f agentproto.Frame) bool {
		if f.Type != agentproto.TypeAck {
			return false
		}
		var a agentproto.Ack
		_ = json.Unmarshal(f.Data, &a)
		return a.Seq == 2
	})

	// Live store: seq 2, ONLINE, LIVE.
	waitMetric(t, 3*time.Second, func() bool {
		f := srv.LiveStore.Frame(sid)
		return f != nil && f.Seq == 2
	})
	frame := srv.LiveStore.Frame(sid)
	if frame.NodeState != metrics.NodeOnline || frame.Freshness["state"] != "LIVE" {
		t.Fatalf("live frame: state=%s fresh=%v", frame.NodeState, frame.Freshness)
	}
	if frame.Sample.Node.CPUPercent != 82 {
		t.Fatalf("cpu = %v, want 82", frame.Sample.Node.CPUPercent)
	}
	if frame.Sample.Node.Net.RxBPS != 1_000_000 {
		t.Fatalf("network rx missing: %v", frame.Sample.Node.Net.RxBPS)
	}

	// REST: live dashboard reads memory (LIVE), not the historical DB.
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/servers/"+serverID+"/metrics", nil)
	if resp.status != 200 {
		t.Fatalf("GET metrics: %d", resp.status)
	}
	fresh := resp.body["freshness"].(map[string]any)
	if fresh["state"] != "LIVE" {
		t.Fatalf("REST freshness = %v, want LIVE", fresh)
	}

	// Duplicate seq 2 must be rejected (no double-ingest).
	mustWriteFrame(t, agentConn, agentproto.Frame{Type: agentproto.TypeMetrics, SessionID: "sess-e2e", Seq: 2, Ts: time.Now().UTC(), Data: mustMarshal(sample)})
	time.Sleep(150 * time.Millisecond)
	if got := srv.LiveStore.Snapshot(sid).RecvCount; got != 2 {
		t.Fatalf("duplicate frame ingested: RecvCount=%d, want 2", got)
	}

	// Seq 3: the browser WS must receive the pushed metrics broadcast.
	mustWriteFrame(t, agentConn, agentproto.Frame{Type: agentproto.TypeMetrics, SessionID: "sess-e2e", Seq: 3, Ts: time.Now().UTC(), Data: mustMarshal(sample)})
	waitForWSMessage(t, browserConn, 3*time.Second, func(msg map[string]any) bool {
		if msg["type"] != "metrics" {
			return false
		}
		data, _ := msg["data"].(map[string]any)
		return data != nil && data["server_id"] == serverID && int64(toF(data["seq"])) == 3
	})
}

func toF(v any) float64 {
	f, _ := v.(float64)
	return f
}

func mustMarshal(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func mustUUID(t *testing.T, s string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(s)
	if err != nil {
		t.Fatalf("bad uuid %q: %v", s, err)
	}
	return id
}

func dialAgentStream(t *testing.T, base, token string) *websocket.Conn {
	t.Helper()
	wsBase := strings.Replace(base, "http", "ws", 1) + "/v1/agent/stream"
	conn, _, err := websocket.DefaultDialer.Dial(wsBase, http.Header{"Authorization": []string{"Bearer " + token}})
	if err != nil {
		t.Fatalf("agent stream dial: %v", err)
	}
	return conn
}

func dialBrowserWS(t *testing.T, base string, jar http.CookieJar) *websocket.Conn {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse base: %v", err)
	}
	wsBase := strings.Replace(base, "http", "ws", 1) + "/v1/ws"
	hdr := http.Header{}
	for _, c := range jar.Cookies(u) {
		hdr.Add("Cookie", c.Name+"="+c.Value)
	}
	conn, _, err := websocket.DefaultDialer.Dial(wsBase, hdr)
	if err != nil {
		t.Fatalf("browser ws dial: %v", err)
	}
	return conn
}

func mustWriteFrame(t *testing.T, conn *websocket.Conn, f agentproto.Frame) {
	t.Helper()
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(f); err != nil {
		t.Fatalf("write frame %s seq %d: %v", f.Type, f.Seq, err)
	}
}

func waitForFrame(t *testing.T, conn *websocket.Conn, timeout time.Duration, pred func(agentproto.Frame) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(timeout))
		var f agentproto.Frame
		if err := conn.ReadJSON(&f); err != nil {
			t.Fatalf("read frame: %v", err)
		}
		if pred(f) {
			return
		}
	}
	t.Fatal("timed out waiting for frame")
}

func waitForWSMessage(t *testing.T, conn *websocket.Conn, timeout time.Duration, pred func(map[string]any) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(timeout))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read ws message: %v", err)
		}
		var msg map[string]any
		if json.Unmarshal(raw, &msg) == nil && pred(msg) {
			return
		}
	}
	t.Fatal("timed out waiting for ws message")
}

// TestAgentStreamRequiresAuth verifies the stream endpoint rejects requests
// without a valid agent token.
func TestAgentStreamRequiresAuth(t *testing.T) {
	_, admin := newTestServer(t)
	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "auth-admin@example.test", "password": "supersecret123", "name": "A",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register: %d", resp.status)
	}
	wsBase := strings.Replace(testBaseURL, "http", "ws", 1) + "/v1/agent/stream"
	_, resp2, err := websocket.DefaultDialer.Dial(wsBase, http.Header{"Authorization": []string{"Bearer agt_invalid"}})
	if err == nil {
		t.Fatal("dial with invalid token must fail")
	}
	if resp2 != nil && resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp2.StatusCode)
	}
}
