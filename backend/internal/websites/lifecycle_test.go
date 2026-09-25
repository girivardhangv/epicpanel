package websites

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/epicbyte/epicpanel/backend/internal/db"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/limits"
	"github.com/epicbyte/epicpanel/backend/internal/packages"
)

const testDBURLDefault = "postgres://epicpanel:epicpanel_dev@localhost:5432/epicpanel_test?sslmode=disable"

// lifecycleFixture holds the minimal row set for lifecycle tests.
type lifecycleFixture struct {
	pool     *dbPool
	store    *Store
	jobStore *jobs.Store
	handler  *Handler
	orgID    uuid.UUID
	serverID uuid.UUID
	userID   uuid.UUID
	cleanup  func()
}

// dbPool aliases the concrete pool type so the fixture stays terse.
type dbPool = pgxpool.Pool

func newLifecycleFixture(t *testing.T) *lifecycleFixture {
	t.Helper()
	dbURL := os.Getenv("EPICPANEL_TEST_DATABASE_URL")
	if dbURL == "" {
		dbURL = testDBURLDefault
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Skipf("skipping lifecycle test: no test database (%v)", err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	suffix := uuid.NewString()[:8]
	userID, orgID, serverID := seedRow(t, pool, suffix)
	store := &Store{Pool: pool}
	jobStore := &jobs.Store{Pool: pool}
	jobStore.SetBackoffUnit(time.Nanosecond)
	handler := &Handler{Websites: store, Jobs: jobStore}

	suffixUser := suffix
	f := &lifecycleFixture{
		pool:     pool,
		store:    store,
		jobStore: jobStore,
		handler:  handler,
		orgID:    orgID,
		serverID: serverID,
		userID:   userID,
	}
	f.cleanup = func() { teardownRow(context.Background(), t, pool, orgID, serverID, suffixUser) }
	return f
}

// createWebsite inserts a minimal website row and marks it ready.
func (f *lifecycleFixture) createWebsite(t *testing.T, name string) *Website {
	t.Helper()
	ws, _, err := f.store.Create(context.Background(), f.orgID, f.serverID, f.userID, name, name+".example.test", RuntimePHP, "8.3", "nginx")
	if err != nil {
		t.Fatalf("create website: %v", err)
	}
	if err := f.store.MarkReady(context.Background(), ws.ID, ws.UnixUser, DocumentRootFor(ws.ID)); err != nil {
		t.Fatalf("mark ready: %v", err)
	}
	got, err := f.store.GetByIDAny(context.Background(), ws.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	return got
}

// runJob claims and finishes a job, mimicking the agent round-trip.
func (f *lifecycleFixture) runJob(t *testing.T, jobID uuid.UUID, success bool, errMsg string) *jobs.Job {
	t.Helper()
	claimed, err := f.jobStore.ClaimNext(context.Background(), f.serverID)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if claimed == nil || claimed.ID != jobID {
		t.Fatalf("expected to claim job %s, got %+v", jobID, claimed)
	}
	updated, err := f.jobStore.ReportResult(context.Background(), jobID, success, nil, errMsg)
	if err != nil {
		t.Fatalf("report result: %v", err)
	}
	return updated
}

func seedRow(t *testing.T, pool *dbPool, suffix string) (userID, orgID, serverID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	email := "lifecycle-" + suffix + "@test.local"
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password_hash, name) VALUES ($1, 'x', 'lifecycle-test') RETURNING id`,
		email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug, created_by) VALUES ('lifecycle-test', $1, $2) RETURNING id`,
		"lc-"+suffix, userID).Scan(&orgID); err != nil {
		t.Fatalf("seed org: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO servers (organization_id, name, registered_by, status) VALUES ($1, $2, $3, 'online') RETURNING id`,
		orgID, "srv-"+suffix, userID).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}
	return userID, orgID, serverID
}

func teardownRow(ctx context.Context, t *testing.T, pool *dbPool, orgID, serverID uuid.UUID, suffix string) {
	t.Helper()
	_, _ = pool.Exec(ctx, `DELETE FROM websites WHERE organization_id = $1`, orgID)
	_, _ = pool.Exec(ctx, `DELETE FROM jobs WHERE server_id = $1`, serverID)
	_, _ = pool.Exec(ctx, `DELETE FROM servers WHERE organization_id = $1`, orgID)
	_, _ = pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1`, orgID)
	_, _ = pool.Exec(ctx, `DELETE FROM users WHERE email = $1`, "lifecycle-"+suffix+"@test.local")
}

func TestWebsiteSuspendResumeLifecycle(t *testing.T) {
	f := newLifecycleFixture(t)
	defer f.cleanup()
	ctx := context.Background()

	ws := f.createWebsite(t, "lc-main")
	if ws.Status != StatusReady {
		t.Fatalf("expected ready after provision, got %s", ws.Status)
	}

	// Suspend enqueue: status stays ready until the agent succeeds.
	job, err := f.jobStore.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, TypeSuspendWebsite, SuspendPayload{WebsiteID: ws.ID.String()}, "suspend_"+ws.ID.String())
	if err != nil {
		t.Fatalf("enqueue suspend: %v", err)
	}
	got, err := f.store.GetByIDAny(ctx, ws.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	if got.Status != StatusReady {
		t.Fatalf("status changed at enqueue time: %s", got.Status)
	}

	// Idempotent re-enqueue returns the same pending job.
	dup, err := f.jobStore.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, TypeSuspendWebsite, SuspendPayload{WebsiteID: ws.ID.String()}, "suspend_"+ws.ID.String())
	if err != nil {
		t.Fatalf("re-enqueue suspend: %v", err)
	}
	if dup.ID != job.ID {
		t.Fatalf("idempotency key not honored: %s vs %s", dup.ID, job.ID)
	}

	// Suspend failure keeps ready (job goes back to pending for retry).
	updated := f.runJob(t, job.ID, false, "fpm stop failed")
	if got, _ = f.store.GetByIDAny(ctx, ws.ID); got.Status != StatusReady {
		t.Fatalf("failed suspend changed status: %s", got.Status)
	}

	// Retry path: claim + success -> suspended.
	updated = f.runJob(t, job.ID, true, "")
	f.handler.ApplyWebsiteTransition(updated, nil)
	if got, _ = f.store.GetByIDAny(ctx, ws.ID); got.Status != StatusSuspended {
		t.Fatalf("expected suspended after success, got %s", got.Status)
	}

	// MarkReady must not resurrect a suspended site.
	if err := f.store.MarkReady(ctx, ws.ID, ws.UnixUser, DocumentRootFor(ws.ID)); err != nil {
		t.Fatalf("MarkReady on suspended should be a no-op success, got %v", err)
	}
	if got, _ = f.store.GetByIDAny(ctx, ws.ID); got.Status != StatusSuspended {
		t.Fatalf("MarkReady resurrected suspended site: %s", got.Status)
	}

	// Resume enqueue -> success -> ready.
	rjob, err := f.jobStore.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, TypeResumeWebsite, ResumePayload{WebsiteID: ws.ID.String()}, "resume_"+ws.ID.String())
	if err != nil {
		t.Fatalf("enqueue resume: %v", err)
	}
	updated = f.runJob(t, rjob.ID, true, "")
	f.handler.ApplyWebsiteTransition(updated, nil)
	if got, _ = f.store.GetByIDAny(ctx, ws.ID); got.Status != StatusReady {
		t.Fatalf("expected ready after resume, got %s", got.Status)
	}
}

// TestWebsiteSuspensionReasonPersistence covers migration 0050 semantics:
// the fanout persists WHY a site was suspended (reason + timestamp +
// metadata) and resume clears all of it. Legacy payloads (no reason) mean
// manual.
func TestWebsiteSuspensionReasonPersistence(t *testing.T) {
	f := newLifecycleFixture(t)
	defer f.cleanup()
	ctx := context.Background()

	ws := f.createWebsite(t, "lc-reason")
	if ws.Status != StatusReady {
		t.Fatalf("expected ready, got %s", ws.Status)
	}

	// Fanout path: a suspend job carrying a reason + metadata persists them.
	meta := map[string]any{"used_bytes": 9223231298, "limit_bytes": 10737418240}
	job, err := f.jobStore.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, TypeSuspendWebsite,
		SuspendPayload{WebsiteID: ws.ID.String(), Reason: string(ReasonBandwidthExhausted), Metadata: meta},
		"overlimit_suspend_bandwidth_"+ws.ID.String())
	if err != nil {
		t.Fatalf("enqueue suspend: %v", err)
	}
	updated := f.runJob(t, job.ID, true, "")
	f.handler.ApplyWebsiteTransition(updated, nil)

	got, err := f.store.GetByIDAny(ctx, ws.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	if got.Status != StatusSuspended {
		t.Fatalf("status: %s want suspended", got.Status)
	}
	if got.SuspensionReason == nil || *got.SuspensionReason != string(ReasonBandwidthExhausted) {
		t.Fatalf("suspension reason: %v want bandwidth_exhausted", got.SuspensionReason)
	}
	if got.SuspendedAt == nil {
		t.Fatalf("suspended_at not recorded")
	}
	if got.SuspensionMetadata == nil || got.SuspensionMetadata["limit_bytes"] != float64(10737418240) {
		t.Fatalf("suspension metadata: %+v", got.SuspensionMetadata)
	}

	// Legacy payload (no reason) refreshes an existing suspension as manual
	// (backward compatible producer behavior).
	job2, err := f.jobStore.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, TypeSuspendWebsite,
		SuspendPayload{WebsiteID: ws.ID.String()}, "manual_suspend_"+ws.ID.String())
	if err != nil {
		t.Fatalf("enqueue manual suspend: %v", err)
	}
	updated = f.runJob(t, job2.ID, true, "")
	f.handler.ApplyWebsiteTransition(updated, nil)
	if got, _ = f.store.GetByIDAny(ctx, ws.ID); got.SuspensionReason == nil || *got.SuspensionReason != string(ReasonManual) {
		t.Fatalf("legacy payload must persist manual reason, got %v", got.SuspensionReason)
	}

	// Resume clears reason + timestamp + metadata.
	rjob, err := f.jobStore.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, TypeResumeWebsite,
		ResumePayload{WebsiteID: ws.ID.String()}, "resume_"+ws.ID.String())
	if err != nil {
		t.Fatalf("enqueue resume: %v", err)
	}
	updated = f.runJob(t, rjob.ID, true, "")
	f.handler.ApplyWebsiteTransition(updated, nil)
	got, err = f.store.GetByIDAny(ctx, ws.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	if got.Status != StatusReady {
		t.Fatalf("status after resume: %s want ready", got.Status)
	}
	if got.SuspensionReason != nil || got.SuspendedAt != nil || got.SuspensionMetadata != nil {
		t.Fatalf("resume must clear suspension data: reason=%v at=%v meta=%v",
			got.SuspensionReason, got.SuspendedAt, got.SuspensionMetadata)
	}
}

func TestWebsiteDeleteFromSuspended(t *testing.T) {
	f := newLifecycleFixture(t)
	defer f.cleanup()
	ctx := context.Background()

	ws := f.createWebsite(t, "lc-del")
	if err := f.store.MarkSuspended(ctx, ws.ID, ReasonManual, nil); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	// Delete of a suspended site is allowed (mirrors the handler path).
	if err := f.store.SetStatus(ctx, ws.ID, StatusDeleting, ""); err != nil {
		t.Fatalf("delete-from-suspended blocked: %v", err)
	}
	job, err := f.jobStore.Enqueue(ctx, ws.ServerID, &ws.ID, jobs.TypeDeleteWebsite, DesiredPayload{WebsiteID: ws.ID})
	if err != nil {
		t.Fatalf("enqueue delete: %v", err)
	}
	updated := f.runJob(t, job.ID, true, "")
	f.handler.ApplyWebsiteTransition(updated, nil)
	if _, err := f.store.GetByIDAny(ctx, ws.ID); err != ErrNotFound {
		t.Fatalf("expected row deleted, got %v", err)
	}
}

func TestWebsiteMarkReadyGuard(t *testing.T) {
	f := newLifecycleFixture(t)
	defer f.cleanup()
	ctx := context.Background()

	ws := f.createWebsite(t, "lc-guard")
	if err := f.store.SetStatus(ctx, ws.ID, StatusDeleting, ""); err != nil {
		t.Fatalf("set deleting: %v", err)
	}
	if err := f.store.MarkReady(ctx, ws.ID, ws.UnixUser, DocumentRootFor(ws.ID)); err != ErrMarkReadyBlocked {
		t.Fatalf("expected ErrMarkReadyBlocked for deleting site, got %v", err)
	}
	if err := f.store.MarkSuspended(ctx, ws.ID, ReasonManual, nil); err != ErrNotFound {
		t.Fatalf("MarkSuspended must not resurrect deleting site, got %v", err)
	}
}

func TestForPackageMapping(t *testing.T) {
	// Phase 9: the mapping is engine-owned (resources.PoolLimitsFor).
	// MemoryLimitMB = per-request php ceiling (clamped to 256M);
	// MaxChildren = clamp(ram/32, 1, 100) — the plan's PHP-worker bound.
	cases := []struct {
		memMB        int
		wantMem      int
		wantChildren int
	}{
		{0, 0, 0},
		{16, 16, 1},
		{512, 256, 16},
		{1024, 256, 32},
		{100000, 256, 100},
	}
	for _, c := range cases {
		got := limits.ForPackage(&packages.Package{MemoryLimitMB: c.memMB})
		if got.MemoryLimitMB != c.wantMem || got.MaxChildren != c.wantChildren {
			t.Errorf("ForPackage(mem=%d) = %+v, want {%d, %d}", c.memMB, got, c.wantMem, c.wantChildren)
		}
	}
	if got := limits.ForPackage(nil); got != (limits.PoolLimits{}) {
		t.Errorf("ForPackage(nil) = %+v, want zero", got)
	}
	if got := limits.ForWebsite(nil); got != (limits.EngineLimits{}) {
		t.Errorf("ForWebsite(nil) = %+v, want zero", got)
	}
	// ForWebsite now derives real engine limits from the package columns.
	got := limits.ForWebsite(&packages.Package{MemoryLimitMB: 4096, CPUCores: 2})
	if got.CPUPercent != 200 || got.MemoryBytes != int64(4096)*1024*1024 {
		t.Errorf("ForWebsite(pkg) = %+v, want cpu 200%% / mem 4096MB", got)
	}
}
