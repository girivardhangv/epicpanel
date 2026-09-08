package ftpaccounts

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/db"
)

const testDBURLDefault = "postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test?sslmode=disable"

func testPool(t *testing.T) *pgxpool.Pool {
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

	// Reset this module's table, then re-apply only its migration so the
	// rest of the shared test schema is left untouched.
	if _, err := pool.Exec(ctx, `DROP TABLE IF EXISTS ftp_accounts CASCADE`); err != nil {
		t.Fatalf("drop ftp_accounts: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("ensure schema_migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, "0023_ftp_accounts.sql"); err != nil {
		t.Fatalf("reset migration marker: %v", err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

type fixture struct {
	orgID    uuid.UUID
	otherOrg uuid.UUID
	siteID   uuid.UUID
}

func seedFixture(t *testing.T, pool *pgxpool.Pool) fixture {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(uuid.NewString()[:10], "-", "")

	var userID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (email, password_hash, name) VALUES ($1, 'x', 'FTP Store Test') RETURNING id
	`, "ftp-store-"+suffix+"@example.invalid").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var orgID, otherOrg uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO organizations (name, slug, created_by) VALUES ($1, $2, $3) RETURNING id
	`, "FTP Org "+suffix, "ftporg-"+suffix, userID).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO organizations (name, slug, created_by) VALUES ($1, $2, $3) RETURNING id
	`, "FTP Other Org "+suffix, "ftpoth-"+suffix, userID).Scan(&otherOrg); err != nil {
		t.Fatalf("seed other org: %v", err)
	}
	var serverID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO servers (organization_id, name, hostname, registered_by) VALUES ($1, $2, $3, $4) RETURNING id
	`, orgID, "srv-"+suffix, "srv-"+suffix+".example.invalid", userID).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	var siteID uuid.UUID
	if err := pool.QueryRow(ctx, `
		INSERT INTO websites (organization_id, server_id, name, primary_domain, created_by)
		VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, orgID, serverID, "ftpstore-"+suffix, "ftpstore-"+suffix+".example.invalid", userID).Scan(&siteID); err != nil {
		t.Fatalf("seed website: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id IN ($1, $2)`, orgID, otherOrg)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	})
	return fixture{orgID: orgID, otherOrg: otherOrg, siteID: siteID}
}

func TestStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	fx := seedFixture(t, pool)
	s := &Store{Pool: pool}

	acct, err := s.Create(ctx, fx.orgID, fx.siteID, ProtocolSFTP, "Backups", "pw-secret-1", "ftp/backups")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if acct.Status != StatusPending {
		t.Errorf("new account status = %q, want pending", acct.Status)
	}
	if acct.UserName != "ep-ftp-"+fx.siteID.String()[:8]+"-backups" {
		t.Errorf("user_name = %q", acct.UserName)
	}
	if acct.Fingerprint == "" {
		t.Error("password fingerprint missing")
	}

	list, err := s.List(ctx, fx.orgID, fx.siteID)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}

	updated, err := s.UpdatePassword(ctx, fx.orgID, acct.ID, "new-password-2")
	if err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}
	if updated.Fingerprint == acct.Fingerprint {
		t.Error("fingerprint did not change after password update")
	}
	plain, err := s.RevealPassword(ctx, acct.ID)
	if err != nil || plain != "new-password-2" {
		t.Fatalf("RevealPassword = %q, %v", plain, err)
	}

	if err := s.MarkOutcome(ctx, acct.ID, true, ""); err != nil {
		t.Fatalf("MarkOutcome success: %v", err)
	}
	got, err := s.Get(ctx, fx.orgID, acct.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusActive || got.ErrorMessage != "" {
		t.Errorf("status after success = %q / %q", got.Status, got.ErrorMessage)
	}
	if err := s.MarkOutcome(ctx, acct.ID, false, "useradd failed"); err != nil {
		t.Fatalf("MarkOutcome failure: %v", err)
	}
	got, err = s.Get(ctx, fx.orgID, acct.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusFailed || got.ErrorMessage != "useradd failed" {
		t.Errorf("status after failure = %q / %q", got.Status, got.ErrorMessage)
	}

	if err := s.Delete(ctx, fx.orgID, acct.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := s.Delete(ctx, fx.orgID, acct.ID); err != ErrNotFound {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

func TestStoreOrgScoping(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	fx := seedFixture(t, pool)
	s := &Store{Pool: pool}

	acct, err := s.Create(ctx, fx.orgID, fx.siteID, ProtocolFTP, "shared", "pw-secret-1", "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Get(ctx, fx.otherOrg, acct.ID); err != ErrNotFound {
		t.Errorf("Get from other org = %v, want ErrNotFound", err)
	}
	list, err := s.List(ctx, fx.otherOrg, fx.siteID)
	if err != nil {
		t.Fatalf("List from other org: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List from other org returned %d accounts, want 0", len(list))
	}
	if err := s.Delete(ctx, fx.otherOrg, acct.ID); err != ErrNotFound {
		t.Errorf("Delete from other org = %v, want ErrNotFound", err)
	}
	if _, err := s.UpdatePassword(ctx, fx.otherOrg, acct.ID, "hijack-pw"); err != ErrNotFound {
		t.Errorf("UpdatePassword from other org = %v, want ErrNotFound", err)
	}
	// The cross-org attempts must not have touched the credential.
	if plain, err := s.RevealPassword(ctx, acct.ID); err != nil || plain != "pw-secret-1" {
		t.Errorf("credential changed after cross-org attempts: %q, %v", plain, err)
	}
}

func TestStoreCapsAndDuplicates(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	fx := seedFixture(t, pool)
	s := &Store{Pool: pool}

	for i := 0; i < 10; i++ {
		if _, err := s.Create(ctx, fx.orgID, fx.siteID, ProtocolFTP, "acct"+string(rune('a'+i)), "pw-secret-1", ""); err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
	}
	if _, err := s.Create(ctx, fx.orgID, fx.siteID, ProtocolFTP, "overflow", "pw-secret-1", ""); err != ErrTooMany {
		t.Errorf("11th ftp account = %v, want ErrTooMany", err)
	}
	// The cap is per protocol: sftp still allowed.
	if _, err := s.Create(ctx, fx.orgID, fx.siteID, ProtocolSFTP, "only-sftp", "pw-secret-1", ""); err != nil {
		t.Fatalf("sftp create under full ftp cap: %v", err)
	}
	// Same label+protocol on the same site collides (UNIQUE website/protocol/label).
	if _, err := s.Create(ctx, fx.orgID, fx.siteID, ProtocolSFTP, "only-sftp", "pw-secret-1", ""); err != ErrTaken {
		t.Errorf("duplicate label = %v, want ErrTaken", err)
	}
}

func TestDesiredForWebsite(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	fx := seedFixture(t, pool)
	s := &Store{Pool: pool}

	if _, err := s.Create(ctx, fx.orgID, fx.siteID, ProtocolSFTP, "deploy", "pw-secret-1", "ftp/deploy"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create(ctx, fx.orgID, fx.siteID, ProtocolFTP, "root", "pw-secret-2", ""); err != nil {
		t.Fatalf("Create: %v", err)
	}
	desired, err := s.DesiredForWebsite(ctx, fx.siteID)
	if err != nil {
		t.Fatalf("DesiredForWebsite: %v", err)
	}
	if len(desired) != 2 {
		t.Fatalf("DesiredForWebsite returned %d accounts, want 2", len(desired))
	}
	byName := map[string]SyncAccount{}
	for _, d := range desired {
		byName[d.UserName] = d
		if !strings.HasPrefix(d.PasswordCrypt, "$6$") {
			t.Errorf("account %s: PasswordCrypt %q lacks $6$ prefix", d.ID, d.PasswordCrypt)
		}
		if d.ID == "" || d.Protocol == "" {
			t.Errorf("account payload incomplete: %+v", d)
		}
	}
	d := byName["ep-ftp-"+fx.siteID.String()[:8]+"-deploy"]
	if d.HomeDir != "/srv/epicpanel/websites/"+fx.siteID.String()+"/ftp/deploy" {
		t.Errorf("subdir account HomeDir = %q", d.HomeDir)
	}
	if !VerifyPassword(d.PasswordCrypt, "pw-secret-1") {
		t.Error("PasswordCrypt does not verify against the account password")
	}
	root := byName["ep-ftp-"+fx.siteID.String()[:8]+"-root"]
	if root.HomeDir != "/srv/epicpanel/websites/"+fx.siteID.String() {
		t.Errorf("root account HomeDir = %q", root.HomeDir)
	}
}

func TestDeriveUserNameMatchesUnixRules(t *testing.T) {
	siteID := uuid.MustParse("abcdefab-1111-2222-3333-444455556666")
	// Long label truncation still yields a valid unix user name.
	labels := []string{
		"a", "AVeryLongLabelThatWouldOverflowTheThirtyTwoCharLimit", "!!!", "!!trailing--spaces!!",
	}
	for _, label := range labels {
		name := DeriveUserName(siteID, label)
		if len(name) < 2 || len(name) > 32 {
			t.Errorf("DeriveUserName(%q) = %q violates 2-32", label, name)
		}
		if !strings.HasPrefix(name, "ep-") {
			t.Errorf("DeriveUserName(%q) = %q lacks ep- prefix", label, name)
		}
		for _, c := range name[3:] {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
				t.Errorf("DeriveUserName(%q) = %q has invalid char %q", label, name, c)
			}
		}
	}
}
