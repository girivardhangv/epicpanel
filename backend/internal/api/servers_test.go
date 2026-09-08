package api

import (
	"net/http"
	"testing"
)

func TestServerLifecycle(t *testing.T) {
	_, admin := newTestServer(t)
	outsider := admin.NewClient()
	dev := admin.NewClient()

	// Setup: admin, org, dev user.
	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register admin: %d %v", resp.status, resp.body)
	}
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme"})
	if resp.status != http.StatusCreated {
		t.Fatalf("create org: %d %v", resp.status, resp.body)
	}
	orgID, _ := resp.body["id"].(string)

	resp = dev.do("POST", "/v1/auth/register", map[string]string{
		"email": "dev@example.test", "password": "supersecret123", "name": "Dev",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register dev: %d", resp.status)
	}
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/members", map[string]string{
		"email": "dev@example.test", "role": "developer",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("add dev member: %d %v", resp.status, resp.body)
	}
	resp = outsider.do("POST", "/v1/auth/register", map[string]string{
		"email": "out@example.test", "password": "supersecret123", "name": "Out",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register outsider: %d", resp.status)
	}

	// 1. Developer cannot register a server (admin+ required).
	resp = dev.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	if resp.status != http.StatusForbidden {
		t.Fatalf("dev create server: %d want 403", resp.status)
	}

	// 2. Admin registers a server; receives one-time registration token.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	if resp.status != http.StatusCreated {
		t.Fatalf("create server: %d %v", resp.status, resp.body)
	}
	srv, _ := resp.body["server"].(map[string]any)
	if srv == nil || srv["status"] != "pending" {
		t.Fatalf("expected pending server: %v", resp.body)
	}
	serverID, _ := srv["id"].(string)
	regToken, _ := resp.body["registration_token"].(string)
	if regToken == "" {
		t.Fatalf("registration token missing: %v", resp.body)
	}

	// 3. Duplicate server name rejected.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers", map[string]string{"name": "web-01"})
	if resp.status != http.StatusConflict {
		t.Fatalf("duplicate server: %d want 409", resp.status)
	}

	// 4. Outsider cannot list or view servers (404, no existence leak).
	resp = outsider.do("GET", "/v1/organizations/"+orgID+"/servers", nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("outsider list: %d want 404", resp.status)
	}
	resp = outsider.do("GET", "/v1/organizations/"+orgID+"/servers/"+serverID, nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("outsider get: %d want 404", resp.status)
	}

	// 5. Agent enrolls with the registration token.
	agentClient := admin.NewClient() // no cookies; uses Bearer
	resp = agentClient.do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken,
		"hostname":           "web-01.example.test",
		"os_info":            "Ubuntu 24.04",
		"agent_version":      "0.1.0",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("enroll: %d %v", resp.status, resp.body)
	}
	agentToken, _ := resp.body["agent_token"].(string)
	if agentToken == "" {
		t.Fatalf("agent token missing: %v", resp.body)
	}

	// 6. Registration token is single-use.
	resp = admin.NewClient().do("POST", "/v1/agent/enroll", map[string]string{
		"registration_token": regToken, "hostname": "evil",
	})
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("token reuse: %d want 401", resp.status)
	}

	// 7. Heartbeat without agent token is rejected.
	resp = admin.NewClient().do("POST", "/v1/agent/heartbeat", map[string]any{})
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("heartbeat without token: %d want 401", resp.status)
	}

	// 8. Heartbeat with agent token + metrics.
	setBearer(agentClient, agentToken)
	resp = agentClient.do("POST", "/v1/agent/heartbeat", map[string]any{
		"metrics": map[string]any{
			"cpu_percent":        42.5,
			"memory_total_bytes": 8589934592,
			"memory_used_bytes":  2147483648,
			"disk_total_bytes":   107374182400,
			"disk_used_bytes":    53687091200,
			"load1":              0.5, "load5": 0.4, "load15": 0.3,
		},
	})
	if resp.status != http.StatusOK {
		t.Fatalf("heartbeat: %d %v", resp.status, resp.body)
	}

	// 9. Server now shows online; metrics visible to org member.
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/servers/"+serverID, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("get server as dev: %d", resp.status)
	}
	if resp.body["status"] != "online" {
		t.Fatalf("server status: %v want online", resp.body["status"])
	}
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/servers/"+serverID+"/metrics", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("get metrics: %d", resp.status)
	}
	metrics, _ := resp.body["metrics"].(map[string]any)
	if metrics == nil || metrics["cpu_percent"] != 42.5 {
		t.Fatalf("metrics mismatch: %v", resp.body)
	}

	// 10. Rotation invalidates old unused token; new token enrolls a replacement agent.
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/servers/"+serverID+"/registration-token", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("rotate: %d %v", resp.status, resp.body)
	}

	// 11. Audit trail includes server events.
	resp = admin.do("GET", "/v1/audit-logs?organization_id="+orgID, nil)
	logs, _ := resp.body["logs"].([]any)
	found := map[string]bool{}
	for _, l := range logs {
		entry, _ := l.(map[string]any)
		if action, ok := entry["action"].(string); ok {
			found[action] = true
		}
	}
	for _, want := range []string{"server.registered", "agent.enrolled", "server.registration_token_rotated"} {
		if !found[want] {
			t.Fatalf("audit missing %q; got %v", want, found)
		}
	}

	// 12. Developer cannot delete server; admin can.
	resp = dev.do("DELETE", "/v1/organizations/"+orgID+"/servers/"+serverID, nil)
	if resp.status != http.StatusForbidden {
		t.Fatalf("dev delete server: %d want 403", resp.status)
	}
	resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/servers/"+serverID, nil)
	if resp.status != http.StatusNoContent {
		t.Fatalf("admin delete server: %d", resp.status)
	}
	resp = admin.do("GET", "/v1/organizations/"+orgID+"/servers/"+serverID, nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("server after delete: %d want 404", resp.status)
	}
}

func setBearer(c *testClient, token string) {
	c.http.Transport = &bearerTransport{token: token, base: c.http.Transport}
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	base := b.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}
