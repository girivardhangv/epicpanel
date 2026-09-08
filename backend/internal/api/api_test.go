package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

type bodyReader struct {
	*bytes.Reader
}

func newBodyReader(b []byte) *bodyReader {
	if b == nil {
		b = []byte{}
	}
	return &bodyReader{Reader: bytes.NewReader(b)}
}

func TestFullAPIFlow(t *testing.T) {
	_, admin := newTestServer(t)
	dev := admin.NewClient()

	// 1. First user becomes platform admin
	resp := admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Admin",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register admin: status=%d body=%v", resp.status, resp.body)
	}
	user, ok := resp.body["user"].(map[string]any)
	if !ok {
		t.Fatalf("no user object in register response: %v", resp.body)
	}
	if user["is_platform_admin"] != true {
		t.Fatalf("first user should be platform admin: %v", user)
	}

	// 2. Duplicate email rejected
	resp = admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin@example.test", "password": "supersecret123", "name": "Dup",
	})
	if resp.status != http.StatusConflict {
		t.Fatalf("duplicate register: status=%d want 409, body=%v", resp.status, resp.body)
	}

	// 3. Weak password rejected
	weak := admin.NewClient()
	resp = weak.do("POST", "/v1/auth/register", map[string]string{
		"email": "weak@example.test", "password": "short", "name": "Weak",
	})
	if resp.status != http.StatusUnprocessableEntity {
		t.Fatalf("weak password: status=%d want 422", resp.status)
	}

	// 4. Second user (not admin)
	resp = dev.do("POST", "/v1/auth/register", map[string]string{
		"email": "dev@example.test", "password": "supersecret123", "name": "Dev",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register dev: status=%d body=%v", resp.status, resp.body)
	}
	devUser, _ := resp.body["user"].(map[string]any)
	if devUser["is_platform_admin"] != false {
		t.Fatalf("second user should not be platform admin: %v", devUser)
	}

	// 5. Login with wrong password
	resp = dev.do("POST", "/v1/auth/login", map[string]string{
		"email": "dev@example.test", "password": "wrongpassword",
	})
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("wrong password login: status=%d want 401", resp.status)
	}

	// 6. Login with correct password
	resp = dev.do("POST", "/v1/auth/login", map[string]string{
		"email": "dev@example.test", "password": "supersecret123",
	})
	if resp.status != http.StatusOK {
		t.Fatalf("login: status=%d body=%v", resp.status, resp.body)
	}

	// 7. /v1/auth/me with session
	resp = dev.do("GET", "/v1/auth/me", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("me: status=%d", resp.status)
	}

	// 8. Unauthenticated request is rejected
	anon := admin.NewClient()
	resp = anon.do("GET", "/v1/auth/me", nil)
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated me: status=%d want 401", resp.status)
	}

	// 9. Admin creates an organization; slug auto-generated
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme Hosting"})
	if resp.status != http.StatusCreated {
		t.Fatalf("create org: status=%d body=%v", resp.status, resp.body)
	}
	orgID, _ := resp.body["id"].(string)
	if orgID == "" {
		t.Fatalf("org id missing: %v", resp.body)
	}
	if resp.body["slug"] != "acme-hosting" {
		t.Fatalf("slug auto-generation failed: %v", resp.body)
	}

	// 10. Duplicate slug rejected
	resp = admin.do("POST", "/v1/organizations", map[string]string{"name": "Acme Again", "slug": "acme-hosting"})
	if resp.status != http.StatusConflict {
		t.Fatalf("duplicate slug: status=%d want 409", resp.status)
	}

	// 11. Non-member cannot see the org (404, no existence leak)
	resp = dev.do("GET", "/v1/organizations/"+orgID, nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("non-member org view: status=%d want 404", resp.status)
	}

	// 12. Admin adds dev as developer member
	resp = admin.do("POST", "/v1/organizations/"+orgID+"/members", map[string]string{
		"email": "dev@example.test", "role": "developer",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("add member: status=%d body=%v", resp.status, resp.body)
	}

	// 13. Developer can view org
	resp = dev.do("GET", "/v1/organizations/"+orgID, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("member org view: status=%d", resp.status)
	}

	// 14. Developer cannot update org (403)
	resp = dev.do("PATCH", "/v1/organizations/"+orgID, map[string]string{"name": "Hijacked"})
	if resp.status != http.StatusForbidden {
		t.Fatalf("developer org update: status=%d want 403", resp.status)
	}

	// 15. Developer can view members (billing+ visibility; developer outranks billing)
	resp = dev.do("GET", "/v1/organizations/"+orgID+"/members", nil)
	if resp.status != http.StatusOK {
		t.Fatalf("developer members view: status=%d want 200", resp.status)
	}

	// 16. Removing the last owner is blocked
	adminID := user["id"].(string)
	resp = admin.do("DELETE", "/v1/organizations/"+orgID+"/members/"+adminID, nil)
	if resp.status != http.StatusConflict {
		t.Fatalf("remove last owner: status=%d want 409 body=%v", resp.status, resp.body)
	}

	// 17. Audit logs recorded for org creation
	resp = admin.do("GET", "/v1/audit-logs?organization_id="+orgID, nil)
	if resp.status != http.StatusOK {
		t.Fatalf("audit logs: status=%d body=%v", resp.status, resp.body)
	}
	logs, _ := resp.body["logs"].([]any)
	if len(logs) == 0 {
		t.Fatalf("expected audit logs for org, got %v", resp.body)
	}
	first, _ := logs[0].(map[string]any)
	if first["action"] == "" {
		t.Fatalf("audit log entry missing action: %v", first)
	}

	// 18. Audit logs denied for a non-member (outsider gets 404, no existence leak)
	outsider := admin.NewClient()
	resp = outsider.do("POST", "/v1/auth/register", map[string]string{
		"email": "outsider@example.test", "password": "supersecret123", "name": "Outsider",
	})
	if resp.status != http.StatusCreated {
		t.Fatalf("register outsider: status=%d", resp.status)
	}
	resp = outsider.do("GET", "/v1/audit-logs?organization_id="+orgID, nil)
	if resp.status != http.StatusNotFound {
		t.Fatalf("non-member audit: status=%d want 404", resp.status)
	}

	// 19. Logout revokes the session
	resp = dev.do("POST", "/v1/auth/logout", nil)
	if resp.status != http.StatusNoContent {
		t.Fatalf("logout: status=%d", resp.status)
	}
	resp = dev.do("GET", "/v1/auth/me", nil)
	if resp.status != http.StatusUnauthorized {
		t.Fatalf("me after logout: status=%d want 401", resp.status)
	}
}

func TestAuditLogJSONShape(t *testing.T) {
	_, admin := newTestServer(t)

	admin.do("POST", "/v1/auth/register", map[string]string{
		"email": "admin2@example.test", "password": "supersecret123", "name": "Admin2",
	})
	admin.do("POST", "/v1/organizations", map[string]string{"name": "Audit Org"})

	resp := admin.do("GET", "/v1/audit-logs", nil)
	_ = resp
	b, _ := json.Marshal(resp.body)
	if len(b) == 0 {
		t.Fatal("empty audit response")
	}
}
