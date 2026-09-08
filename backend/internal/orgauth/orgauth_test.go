package orgauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/permissions"
)

func TestRequireOrgPermission(t *testing.T) {
	orgID := uuid.New()
	otherOrg := uuid.New()

	rs := Resolvers{RoleFor: func(r *http.Request, id uuid.UUID) (organizations.Role, *httpapi.APIError) {
		switch r.Header.Get("X-Test-Role") {
		case "owner":
			return organizations.RoleOwner, nil
		case "billing":
			return organizations.RoleBilling, nil
		case "reseller":
			return organizations.RoleReseller, nil
		default:
			return "", nil // non-member
		}
	}}

	called := false
	var handler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
		called = true
		if got, ok := OrgFrom(r.Context()); !ok || got != orgID {
			t.Errorf("org context missing/wrong: %v %v", got, ok)
		}
		w.WriteHeader(200)
	}
	// PathValue() only populates through a ServeMux, so test via a mux.
	inner := RequireOrgPermission(permissions.ServerManage, rs, handler)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/organizations/{org_id}/servers", inner)

	run := func(build func(*http.Request) *http.Request) *httptest.ResponseRecorder {
		called = false
		req := httptest.NewRequest("POST", "/v1/organizations/"+orgID.String()+"/servers", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, build(req))
		return rec
	}

	// Super Admin session passes.
	rec := run(func(r *http.Request) *http.Request {
		ctx := httpapi.WithUser(r.Context(), &httpapi.User{ID: uuid.NewString(), Role: "admin"})
		return r.WithContext(ctx)
	})
	if rec.Code != 200 || !called {
		t.Fatalf("platform admin should pass: %d", rec.Code)
	}

	// Admin-role TOKEN does NOT pass ServerManage (scope servers:write missing).
	rec = run(func(r *http.Request) *http.Request {
		ctx := httpapi.WithUser(r.Context(), &httpapi.User{ID: uuid.NewString(), Role: "admin"})
		ctx = httpapi.WithTokenAuth(ctx, map[string]bool{"websites:read": true}, orgID.String())
		return r.WithContext(ctx)
	})
	if rec.Code != 403 || called {
		t.Fatalf("token without servers:write should 403: %d", rec.Code)
	}

	// Token with the right scope but wrong org gets 404 (cloaking).
	rec = run(func(r *http.Request) *http.Request {
		ctx := httpapi.WithUser(r.Context(), &httpapi.User{ID: uuid.NewString(), Role: "admin"})
		ctx = httpapi.WithTokenAuth(ctx, map[string]bool{"servers:write": true}, otherOrg.String())
		return r.WithContext(ctx)
	})
	if rec.Code != 404 || called {
		t.Fatalf("cross-org token should 404: %d", rec.Code)
	}

	// Owner (Customer) does NOT hold server.manage (fleet = admins only).
	rec = run(func(r *http.Request) *http.Request {
		r.Header.Set("X-Test-Role", "owner")
		ctx := httpapi.WithUser(r.Context(), &httpapi.User{ID: uuid.NewString(), Role: "user"})
		return r.WithContext(ctx)
	})
	if rec.Code != 403 || called {
		t.Fatalf("owner must not hold server.manage: %d", rec.Code)
	}
	// ...but does hold account.manage.
	called = false
	acct := RequireOrgPermission(permissions.AccountManage, rs, func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/v1/organizations/"+orgID.String()+"/accounts/x", nil)
	mux2 := http.NewServeMux()
	mux2.HandleFunc("PATCH /v1/organizations/{org_id}/accounts/{id}", acct)
	req.Header.Set("X-Test-Role", "owner")
	req = req.WithContext(httpapi.WithUser(req.Context(), &httpapi.User{ID: uuid.NewString(), Role: "user"}))
	mux2.ServeHTTP(rec, req)
	if rec.Code != 200 || !called {
		t.Fatalf("owner should pass account.manage: %d", rec.Code)
	}

	// Billing cannot manage servers.
	rec = run(func(r *http.Request) *http.Request {
		r.Header.Set("X-Test-Role", "billing")
		ctx := httpapi.WithUser(r.Context(), &httpapi.User{ID: uuid.NewString(), Role: "user"})
		return r.WithContext(ctx)
	})
	if rec.Code != 403 || called {
		t.Fatalf("billing should fail server.manage: %d", rec.Code)
	}

	// Non-members get 404, not 403 (existence cloaking preserved).
	rec = run(func(r *http.Request) *http.Request {
		ctx := httpapi.WithUser(r.Context(), &httpapi.User{ID: uuid.NewString(), Role: "user"})
		return r.WithContext(ctx)
	})
	if rec.Code != 404 {
		t.Fatalf("non-member should 404: %d", rec.Code)
	}

	// Reseller has ServerRead but not ServerManage.
	ok := permissions.Can(organizations.RoleReseller, permissions.ServerRead)
	if !ok {
		t.Fatal("reseller should hold server.read")
	}
	if permissions.Can(organizations.RoleReseller, permissions.ServerManage) {
		t.Fatal("reseller must not hold server.manage")
	}
	_ = context.Background
}
