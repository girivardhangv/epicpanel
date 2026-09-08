package api

import (
	"context"
	"net/http"
	"testing"
)

// ============================================================================
// Phase 9 integration coverage: migration 0027 seeds the 7 verbatim plans;
// the resource engine gates counts at create time; the enforce_limits job
// payload carries the SAME numbers the usage endpoint displays (drift rule).
// ============================================================================

// seedWebsite enrolls a server and provisions one site; returns ids plus
// the agent-authenticated client (for job claims).
func seedWebsite(t *testing.T, admin *testClient) (orgID, serverID, websiteID string, agent *testClient) {
	t.Helper()
	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	org := admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	orgID, _ = org.body["id"].(string)

	reg := admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	regToken, _ := reg.body["registration_token"].(string)
	agent = admin.NewClient()
	enroll := agent.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "web-01.local",
	})
	setBearer(agent, enroll.body["agent_token"].(string))
	serverID, _ = enroll.body["server"].(map[string]any)["id"].(string)

	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites", map[string]any{
		"name": "app", "server_id": serverID, "primary_domain": "app.example.test",
	})
	website, _ := resp.body["website"].(map[string]any)
	websiteID, _ = website["id"].(string)
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result":  map[string]string{"unix_user": website["unix_user"].(string), "document_root": "/srv/epicpanel/websites/" + websiteID + "/public"},
	})
	return orgID, serverID, websiteID, agent
}

// TestPhase9PlanSeedMatrix — the 7 verbatim plans exist with their matrix.
func TestPhase9PlanSeedMatrix(t *testing.T) {
	_, admin := newTestServer(t)
	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	resp := admin.do("GET", "/v1/admin/packages", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("list packages: %d %v", resp.status, resp.body)
	}
	pkgs, _ := resp.body["packages"].([]any)
	byName := map[string]map[string]any{}
	for _, p := range pkgs {
		m, _ := p.(map[string]any)
		byName[m["name"].(string)] = m
	}
	for _, name := range []string{
		"Starter", "Pro", "Business", "Minecraft 2GB", "Minecraft 4GB",
		"Discord Basic", "Discord Pro",
	} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("verbatim plan %q not seeded", name)
		}
	}
	mc := byName["Minecraft 4GB"]
	// Verbatim matrix mirrored into the legacy columns.
	if got := int(mc["memory_limit_mb"].(float64)); got != 4096 {
		t.Fatalf("Minecraft 4GB memory_limit_mb = %d, want 4096", got)
	}
	if got := int(mc["max_disk_mb"].(float64)); got != 20480 {
		t.Fatalf("Minecraft 4GB max_disk_mb = %d, want 20480 (20 GB)", got)
	}
	if got := mc["cpu_cores"].(float64); got != 2.0 {
		t.Fatalf("Minecraft 4GB cpu_cores = %v, want 2.0 (200%%)", got)
	}
	// The unified columns come through the engine adapter, not the legacy
	// list shape — read them via the DB-backed resolver through a website.
	// ( covered by TestPhase9EnforcePayloadMatchesDisplay below. )
}

// TestPhase9EnforcePayloadMatchesDisplay — after assigning Minecraft 4GB,
// the enforce_limits job payload must carry exactly the limits the usage
// endpoint displays: RAM 4096 MB, CPU 200%, Disk 20480 MB, Bandwidth 2 TB,
// PIDs 256 (the drift rule, end-to-end).
func TestPhase9EnforcePayloadMatchesDisplay(t *testing.T) {
	srv, admin := newTestServer(t)
	orgID, serverID, websiteID, agent := seedWebsite(t, admin)
	_ = serverID

	// Find the seeded Minecraft 4GB package and assign it.
	resp := admin.do("GET", "/v1/admin/packages", nil)
	pkgs, _ := resp.body["packages"].([]any)
	var mcID string
	for _, p := range pkgs {
		m, _ := p.(map[string]any)
		if m["name"] == "Minecraft 4GB" {
			mcID, _ = m["id"].(string)
		}
	}
	if mcID == "" {
		t.Fatal("Minecraft 4GB not seeded")
	}
	if st := admin.do("POST", "/v1/admin/organizations/"+orgID+"/package", map[string]string{
		"package_id": mcID,
	}).status; st != http.StatusOK {
		t.Fatalf("assign package: %d", st)
	}

	// The assignment hook enforces the new plan on existing sites.
	claim := agent.do("POST", "/v1/agent/jobs/claim", nil)
	job, _ := claim.body["job"].(map[string]any)
	if job == nil || job["type"] != "enforce_limits" {
		t.Fatalf("expected enforce_limits after plan assignment, got %v", claim.body)
	}
	payload, _ := job["payload"].(map[string]any)
	want := map[string]float64{
		"memory_mb":    4096,
		"cpu_percent":  200,
		"disk_mb":      20480,
		"bandwidth_mb": 2 * 1024 * 1024,
		"pids_max":     256,
	}
	for k, v := range want {
		got, _ := payload[k].(float64)
		if got != v {
			t.Errorf("enforce payload %s = %v, want %v (verbatim)", k, got, v)
		}
	}
	agent.do("POST", "/v1/agent/jobs/"+job["id"].(string)+"/result", map[string]any{
		"success": true,
		"result": map[string]any{
			"website_id": websiteID, "plan": "Minecraft 4GB", "applied_at": "2026-01-01T00:00:00Z",
			"mechanisms": []map[string]string{
				{"resource": "ram", "mode": "enforced", "mechanism": "cgroup-v2"},
				{"resource": "bandwidth", "mode": "accounted", "mechanism": "nftables",
					"detail": "chain counters rx=0 tx=0 (period), budget=2097152MB"},
			},
			"usage": map[string]float64{"ram": 1024, "disk": 2048, "bandwidth": 100},
		},
	})

	// Bandwidth accounting row persisted from the SAME agent measurement.
	var count int
	if err := srv.Pool.QueryRow(context.Background(), `SELECT count(*) FROM workload_resource_usage WHERE resource = 'bandwidth'`).Scan(&count); err != nil {
		t.Fatalf("accounting table: %v", err)
	}
	if count != 1 {
		t.Fatalf("bandwidth accounting rows = %d, want 1", count)
	}

	// The display path (usage endpoint) reports the same memory cap bytes.
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/usage", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("usage: %d %v", resp.status, resp.body)
	}
	if got := resp.body["memory_limit"].(float64); got != float64(4096*1024*1024) {
		t.Fatalf("displayed memory_limit = %v, want %d (== memory.max) — DRIFT", got, 4096*1024*1024)
	}
	if got := resp.body["cpu_limit_cores"].(float64); got != 2.0 {
		t.Fatalf("displayed cpu_limit_cores = %v, want 2 — DRIFT", got)
	}
	if got := resp.body["disk_limit_mb"].(float64); got != 20480 {
		t.Fatalf("displayed disk_limit_mb = %v, want 20480 — DRIFT", got)
	}
}

// TestPhase9BackupCountGate — Minecraft 4GB allows exactly 3 backups
// (verbatim Backups: 3); the 4th is denied by the unified count gate.
func TestPhase9BackupCountGate(t *testing.T) {
	_, admin := newTestServer(t)
	orgID, _, websiteID, _ := seedWebsite(t, admin)

	resp := admin.do("GET", "/v1/admin/packages", nil)
	pkgs, _ := resp.body["packages"].([]any)
	var mcID string
	for _, p := range pkgs {
		m, _ := p.(map[string]any)
		if m["name"] == "Minecraft 4GB" {
			mcID, _ = m["id"].(string)
		}
	}
	admin.do("POST", "/v1/admin/organizations/"+orgID+"/package", map[string]string{"package_id": mcID})

	for i := 0; i < 3; i++ {
		if st := admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/backups", nil).status; st != http.StatusAccepted {
			t.Fatalf("backup %d: %d (plan allows 3)", i+1, st)
		}
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/websites/"+websiteID+"/backups", nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("4th backup: %d %v, want 403", resp.status, resp.body)
	}
	if msg, _ := resp.body["error"].(map[string]any)["message"].(string); msg != "package limit reached: Minecraft 4GB allows 3 backups" {
		t.Fatalf("gate message = %q", msg)
	}
}
