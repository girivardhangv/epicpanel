package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/apitokens"
	"github.com/epicbyte/epicpanel/backend/internal/apps"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/auth"
	"github.com/epicbyte/epicpanel/backend/internal/backups"
	"github.com/epicbyte/epicpanel/backend/internal/config"
	"github.com/epicbyte/epicpanel/backend/internal/databases"
	"github.com/epicbyte/epicpanel/backend/internal/db"
	"github.com/epicbyte/epicpanel/backend/internal/deployments"
	"github.com/epicbyte/epicpanel/backend/internal/dns"
	"github.com/epicbyte/epicpanel/backend/internal/domains"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/ftpaccounts"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/metrics"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/resourcelimits"
	"github.com/epicbyte/epicpanel/backend/internal/runtimes"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/epicbyte/epicpanel/backend/internal/users"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
	"github.com/google/uuid"
)

const testDBURLDefault = "postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test?sslmode=disable"

// testBaseURL is the httptest URL of the most recently created test server
// (used by WS-dialing tests).
var testBaseURL string

type testClient struct {
	t    *testing.T
	base string
	http *http.Client
}

func newTestServer(t *testing.T) (*Server, *testClient) {
	t.Helper()

	dbURL := os.Getenv("EPICPANEL_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skipf("skipping integration test: set EPICPANEL_TEST_DATABASE_URL to enable (default: %s)", testDBURLDefault)
	}

	ctx := context.Background()
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, table := range []string{
		"schedule_tasks", "server_variables", "mount_servers", "mount_eggs", "egg_variables",
		"eggs", "nests", "mounts", "subusers", "server_databases", "server_activities",
		"events", "resources", "invoices", "subscriptions", "services", "products", "customers",
		"recovery_codes", "mfa_challenges", "service_accounts", "api_tokens",
		"applications", "ssh_keys", "system_settings", "cron_jobs", "hosting_packages", "http_checks", "alerts", "backups", "deployments", "domains", "dns_records", "dns_zones", "domain_redirects", "ftp_accounts", "databases", "runtimes", "jobs", "websites",
		"workload_resource_usage", "allocations",
		"server_metrics", "server_agent_tokens", "server_registration_tokens", "servers",
		"audit_logs", "sessions", "organization_members", "organizations", "users", "schema_migrations",
	} {
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS "+table+" CASCADE"); err != nil {
			t.Fatalf("drop table %s: %v", table, err)
		}
	}
	if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS org_role"); err != nil {
		t.Fatalf("drop type: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS job_type"); err != nil {
		t.Fatalf("drop type: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS job_status"); err != nil {
		t.Fatalf("drop type: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS db_engine"); err != nil {
		t.Fatalf("drop type: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS db_status"); err != nil {
		t.Fatalf("drop type: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS domain_kind"); err != nil {
		t.Fatalf("drop type: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS ssl_mode"); err != nil {
		t.Fatalf("drop type: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TYPE IF EXISTS ssl_state"); err != nil {
		t.Fatalf("drop type: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	srv := &Server{
		Cfg: config.Config{
			CookieName: "epicpanel_session",
			SessionTTL: time.Hour,
		},
		Pool:           pool,
		Sessions:       &auth.SessionStore{Pool: pool},
		Users:          &users.Store{Pool: pool},
		Orgs:           &organizations.Store{Pool: pool},
		Servers:        &servers.Store{Pool: pool},
		Websites:       &websites.Store{Pool: pool},
		Jobs:           &jobs.Store{Pool: pool},
		Runtimes:       &runtimes.Store{Pool: pool},
		Databases:      &databases.Store{Pool: pool},
		Domains:        &domains.Store{Pool: pool},
		Apps:           &apps.Store{Pool: pool},
		Settings:       &settings.Store{Pool: pool},
		Packages:       &packages.Store{Pool: pool},
		Deployments:    &deployments.Store{Pool: pool},
		Backups:        &backups.Store{Pool: pool},
		Tokens:         &apitokens.Store{Pool: pool},
		Limiter:        httpapi.NewTokenBucket(),
		Audit:          &audit.Store{Pool: pool},
		WPPending:      &websites.WPPendingStore{Pool: pool},
		MFA:            &auth.MFAStore{Pool: pool},
		Accounts:       &apitokens.ServiceAccountStore{Pool: pool},
		FTPAccounts:    &ftpaccounts.Store{Pool: pool},
		DNS:            &dns.Store{Pool: pool},
		ResourceLimits: resourcelimits.New(&packages.Store{Pool: pool}, pool),
	}
	// Integration tests expect failed jobs to be reclaimable immediately;
	// production uses 30s exponential backoff.
	srv.Jobs.SetBackoffUnit(time.Nanosecond)

	// Events bus (local delivery; no cross-process driver in tests) + WS hub.
	orgStore := &organizations.Store{Pool: pool}
	srv.Events = &events.Bus{Pool: pool}
	srv.WSHub = events.NewHub(events.WSOptions{
		AllowedOrigins: []string{},
		UserOrgs:       orgStore.OrganizationsForUser,
	})
	// Phase 3 live metrics store (writer NOT running: history tests flush
	// explicitly against the test DB). Fan-out mirrors production wiring.
	srv.LiveStore = metrics.NewLiveStore()
	srv.LiveStore.OnSample = func(serverID uuid.UUID, frame map[string]any) {
		if frame != nil {
			srv.WSHub.BroadcastMetrics(frame)
		}
	}
	srv.History = metrics.NewWriter(pool, srv.LiveStore)

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	testBaseURL = ts.URL

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	client := &testClient{t: t, base: ts.URL, http: &http.Client{Jar: jar}}
	return srv, client
}

type response struct {
	status int
	body   map[string]any
}

func (c *testClient) do(method, path string, payload any) response {
	c.t.Helper()
	var reqBody *bodyReader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			c.t.Fatalf("marshal payload: %v", err)
		}
		reqBody = newBodyReader(b)
	} else {
		reqBody = newBodyReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, reqBody)
	if err != nil {
		c.t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// Match the real frontend, which sends the CSRF double-check header on
	// every request.
	req.Header.Set("X-EpicPanel", "1")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		// 204 No Content and empty bodies are fine
		decoded = map[string]any{}
	}
	return response{status: resp.StatusCode, body: decoded}
}

// NewClient creates an additional independent client (own cookie jar) against
// the same running test server.
func (c *testClient) NewClient() *testClient {
	jar, err := cookiejar.New(nil)
	if err != nil {
		c.t.Fatalf("cookie jar: %v", err)
	}
	return &testClient{t: c.t, base: c.base, http: &http.Client{Jar: jar}}
}
