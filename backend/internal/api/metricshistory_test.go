package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/agentproto"
	"github.com/epicbyte/epicpanel/backend/internal/metrics"
)

// TestMetricsHistoryPersisted verifies the asynchronous historical path:
// frames ingested through the live store land in server_metrics with the new
// network/swap columns, and the rollup table can be built from raw rows.
// This is the graph/billing path — deliberately decoupled from the live one.
func TestMetricsHistoryPersisted(t *testing.T) {
	srv, admin := newTestServer(t)
	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "hist-admin@example.test", "password": "supersecret123", "name": "A",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register: %d", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "HistOrg"})
	if resp.status != http.StatusCreated {
		t.Fatalf("org: %d", resp.status)
	}
	orgID, _ := resp.body["id"].(string)
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "hist-node"})
	if resp.status != http.StatusCreated {
		t.Fatalf("server: %d %v", resp.status, resp.body)
	}
	serverID, _ := resp.body["server"].(map[string]any)["id"].(string)

	sample := agentproto.Sample{Node: agentproto.NodeSample{
		CPUPercent: 66.5, CPUCores: 4,
		MemoryTotal: 16 << 30, MemoryUsed: 8 << 30, MemoryAvailable: 8 << 30,
		SwapTotal: 4 << 30, SwapUsed: 512 << 20,
		Net:            agentproto.NetSample{RxBPS: 2_500_000, TxBPS: 1_250_000, RxBytes: 1000, TxBytes: 1000},
		Disks:          []agentproto.DiskSample{{Fs: "ext4", Mount: "/", TotalBytes: 100 << 30, UsedBytes: 40 << 30, InodesTotal: 6_000_000, InodesUsed: 500_000}},
		IO:             agentproto.DiskIOSample{ReadBPS: 1024, WriteBPS: 2048, TotalRead: 4096, TotalWrite: 8192},
		TCPEstablished: 7, TCPTotal: 20, Processes: 180, UptimeS: 123456,
	}}
	if !srv.LiveStore.Ingest(mustUUID(t, serverID), agentproto.Frame{
		Type: agentproto.TypeMetrics, SessionID: "hist", Seq: 1, Ts: time.Now().UTC(),
		Data: mustMarshal(sample),
	}) {
		t.Fatal("ingest failed")
	}

	// Async writer: drain + flush (never in a request path).
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.History.DrainLiveSnapshot(ctx)
	srv.History.Flush(ctx)

	var count int
	if err := srv.Pool.QueryRow(ctx, `SELECT count(*) FROM server_metrics WHERE server_id = $1`, mustUUID(t, serverID)).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("server_metrics rows = %d, want 1", count)
	}
	var cpu float64
	var rxBPS float64
	var swapUsed int64
	var degraded bool
	if err := srv.Pool.QueryRow(ctx, `
		SELECT cpu_percent, network_rx_bps, swap_used_bytes, degraded
		FROM server_metrics WHERE server_id = $1`, mustUUID(t, serverID),
	).Scan(&cpu, &rxBPS, &swapUsed, &degraded); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if cpu < 66.4 || cpu > 66.6 || rxBPS != 2_500_000 || swapUsed != 512<<20 || degraded {
		t.Fatalf("persisted row wrong: cpu=%v rxBPS=%v swap=%v degraded=%v", cpu, rxBPS, swapUsed, degraded)
	}

	// History endpoint (raw range) serves the persisted row.
	resp = admin.do("GET", fmt.Sprintf("/v1/organizations/%s/servers/%s/metrics/history", orgID, serverID), nil)
	if resp.status != 200 {
		t.Fatalf("history: %d", resp.status)
	}
	points, _ := resp.body["metrics"].([]any)
	if len(points) != 1 {
		t.Fatalf("history points = %d, want 1", len(points))
	}
	// Rollup job builds 5-minute buckets from raw rows.
	if err := metrics.Rollup(ctx, srv.Pool); err != nil {
		t.Fatalf("rollup: %v", err)
	}
	var buckets int
	if err := srv.Pool.QueryRow(ctx, `SELECT count(*) FROM server_metrics_rollup_5m WHERE server_id = $1`, mustUUID(t, serverID)).Scan(&buckets); err != nil {
		t.Fatalf("rollup count: %v", err)
	}
	if buckets != 1 {
		t.Fatalf("rollup buckets = %d, want 1", buckets)
	}
}
