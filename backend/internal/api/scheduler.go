package api

import (
	"context"
	"log/slog"
	"time"

	"github.com/epicbyte/epicpanel/backend/internal/backups"
	"github.com/epicbyte/epicpanel/backend/internal/domains"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
)

const (
	renewalHorizon    = 30 * 24 * time.Hour
	schedulerInterval = time.Hour
	renewalBatch      = 100
	fastInterval      = 30 * time.Second // leases, statuses, event reaping
	retentionJobs     = 30 * 24 * time.Hour
	vhostResyncEvery  = 24 * time.Hour // slow anti-drift sweep; boot-time run covers upgrades
)

// StartBackground launches the control-plane maintenance loops:
// hourly — certificate renewals, scheduled backups, cron re-sync, site usage;
// fast — job lease reaping, server online/offline refresh, retention pruning;
// metrics — legacy-heartbeat snapshots into the async history writer.
func (s *Server) StartBackground(ctx context.Context) {
	// Phase 3: legacy heartbeats (agents without the stream) must also land
	// in the historical store — drained from the live store, never in a
	// request path.
	if s.LiveStore != nil && s.History != nil {
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					s.History.DrainLiveSnapshot(ctx)
				}
			}
		}()
	}
	go func() {
		ticker := time.NewTicker(schedulerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.enqueueRenewals(ctx)
				s.enqueueScheduledBackups(ctx)
				s.resyncCrontabs(ctx)
				s.enqueueSiteUsage(ctx)
				s.enqueueEnforceLimits(ctx)
				s.pruneOldJobs(ctx)
			}
		}
	}()
	// Fast loop: the audit showed hourly status derivation let dead agents
	// appear online for up to an hour and stuck 'running' jobs never converge.
	go func() {
		ticker := time.NewTicker(fastInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refreshServerStatuses(ctx)
				s.reapExpiredLeases(ctx)
			}
		}
	}()
	// Dynamic resources allocator: traffic-adaptive allocation + bot defense
	// decision loop (internal/traffic scoring → enforce/suspend jobs).
	go s.StartDynamicAllocator(ctx)
	// Run once shortly after boot so restarts converge immediately.
	go func() {
		time.Sleep(10 * time.Second)
		s.refreshServerStatuses(ctx)
		s.reapExpiredLeases(ctx)
		s.enqueueRenewals(ctx)
		s.enqueueScheduledBackups(ctx)
		s.resyncCrontabs(ctx)
		s.enqueueEnforceLimits(ctx)
		s.resyncVhosts(ctx)
	}()
	// Slow vhost resync: re-render every ready site's serving config from
	// desired state (idempotent provision jobs) so platform-wide serving
	// changes (e.g. the bandwidth accounting stamps) converge even on sites
	// nothing else ever touches.
	go func() {
		ticker := time.NewTicker(vhostResyncEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.resyncVhosts(ctx)
			}
		}
	}()
}

// resyncVhosts re-enqueues the idempotent provision/converge job for every
// ready site (bounded per server like the other sweeps via job idempotency:
// one pending provision per site).
func (s *Server) resyncVhosts(ctx context.Context) {
	sites, err := s.Websites.ReadyForLimits(ctx, 1000)
	if err != nil {
		slog.Warn("vhost resync scan failed", "err", err)
		return
	}
	for i := range sites {
		ws := &sites[i]
		s.reconcileWebsiteServing(ctx, ws.ID, ws.Organization, ws.ServerID)
	}
}

// reapExpiredLeases requeues jobs whose agent died mid-run and emits events.
func (s *Server) reapExpiredLeases(ctx context.Context) {
	reaped, err := s.Jobs.ReapExpiredLeases(ctx)
	if err != nil {
		slog.Warn("lease reap failed", "err", err)
		return
	}
	for _, j := range reaped {
		if s.Events == nil {
			continue
		}
		evType := "job.requeued"
		if j.Status == jobs.StatusFailed {
			evType = "job.failed"
		}
		s.Events.Publish(ctx, events.Event{
			Type:         evType,
			ActorType:    "system",
			ResourceType: "job",
			ResourceID:   j.ID.String(),
			Payload:      map[string]any{"job_type": string(j.Type), "reason": "lease_expired"},
		})
	}
}

// pruneOldJobs bounds jobs table growth (audit: unbounded retention).
func (s *Server) pruneOldJobs(ctx context.Context) {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM jobs WHERE created_at < now() - $1::interval AND status IN ('success','failed')`, retentionJobs)
	if err != nil {
		slog.Warn("jobs retention prune failed", "err", err)
		return
	}
	if tag.RowsAffected() > 0 {
		slog.Info("old jobs pruned", "deleted", tag.RowsAffected())
	}
}

func (s *Server) refreshServerStatuses(ctx context.Context) {
	n, err := s.Servers.RefreshStatuses(ctx)
	if err != nil {
		slog.Warn("server status refresh failed", "err", err)
		return
	}
	if n > 0 {
		slog.Info("server statuses refreshed", "updated", n)
	}
}

func (s *Server) enqueueRenewals(ctx context.Context) {
	list, err := s.Domains.ExpiringForRenewal(ctx, renewalHorizon, renewalBatch)
	if err != nil {
		slog.Warn("renewal scan failed", "err", err)
		return
	}
	for _, d := range list {
		payload := domains.CertPayload{DomainID: d.ID, Domain: d.Domain, Mode: string(d.SSLMode)}
		if _, err := s.Jobs.EnqueueForWebsite(ctx, d.WebsiteID, jobs.TypeIssueCertificate, payload); err != nil {
			slog.Warn("renewal enqueue failed", "domain", d.Domain, "err", err)
			continue
		}
		// Flip to issuing so the domain is not re-enqueued every hour.
		if _, err := s.Domains.SetSSLMode(ctx, d.ID, d.SSLMode); err != nil {
			slog.Warn("renewal state flip failed", "domain", d.Domain, "err", err)
		}
		slog.Info("certificate renewal scheduled", "domain", d.Domain, "expires", d.SSLExpiresAt)
	}
}

// enqueueScheduledBackups creates backup jobs for websites whose schedule is
// due, then applies retention pruning via the fanout.
// Audit fix: DueBackup now carries the real server_id, and scheduled backups
// pass NULL created_by (system actor) instead of uuid.Nil which violated the
// users FK — scheduled backups previously never ran.
func (s *Server) enqueueScheduledBackups(ctx context.Context) {
	due, err := s.Backups.DueForSchedule(ctx, 50)
	if err != nil {
		slog.Warn("backup schedule scan failed", "err", err)
		return
	}
	for _, d := range due {
		b, err := s.Backups.CreateSystem(ctx, d.OrganizationID, d.WebsiteID, d.ServerID, "scheduled")
		if err != nil {
			slog.Warn("scheduled backup create failed", "website", d.WebsiteID, "err", err)
			continue
		}
		refs, err := s.websiteDatabases(ctx, d.WebsiteID)
		if err != nil {
			slog.Warn("scheduled backup db lookup failed", "website", d.WebsiteID, "err", err)
			continue
		}
		payload := backups.BackupJobPayload{BackupID: b.ID.String(), WebsiteID: d.WebsiteID.String()}
		for _, ref := range refs {
			payload.Databases = append(payload.Databases, backups.DBClonePayload{Engine: ref.Engine, SourceName: ref.Name, TargetName: ref.Name, User: ref.User})
		}
		if _, err := s.Jobs.Enqueue(ctx, d.ServerID, &d.WebsiteID, jobs.TypeCreateBackup, payload); err != nil {
			_ = s.Backups.MarkFailed(ctx, b.ID, "enqueue failed")
			slog.Warn("scheduled backup enqueue failed", "website", d.WebsiteID, "err", err)
			continue
		}
		if s.Events != nil {
			org := d.OrganizationID
			s.Events.Publish(ctx, events.Event{
				Type:         "backup.scheduled",
				Organization: &org,
				ResourceType: "backup",
				ResourceID:   b.ID.String(),
				Payload:      map[string]any{"website_id": d.WebsiteID.String()},
			})
		}
	}
}

// resyncCrontabs re-enqueues crontab syncs for every site with cron entries
// (desired-state safety net; catches missed events or manual crontab edits).
func (s *Server) resyncCrontabs(ctx context.Context) {
	sites, err := s.Crons.WebsitesWithCrons(ctx)
	if err != nil {
		slog.Warn("cron re-sync scan failed", "err", err)
		return
	}
	for _, sc := range sites {
		entries, err := s.Crons.EntriesForWebsite(ctx, sc.WebsiteID)
		if err != nil {
			continue
		}
		payload := map[string]any{
			"website_id": sc.WebsiteID.String(),
			"entries":    entries,
		}
		if _, err := s.Jobs.Enqueue(ctx, sc.ServerID, &sc.WebsiteID, jobs.TypeSyncCrontab, payload); err != nil {
			slog.Warn("crontab re-sync enqueue failed", "website", sc.WebsiteID, "err", err)
		}
	}
}

// enqueueSiteUsage samples resource usage for ready sites whose last
// snapshot is older than two minutes (cPanel-style per-site usage graphs
// without hammering the agent).
func (s *Server) enqueueSiteUsage(ctx context.Context) {
	sites, err := s.Websites.StaleUsage(ctx, 2*time.Minute, 50)
	if err != nil {
		slog.Warn("usage scan failed", "err", err)
		return
	}
	for _, ws := range sites {
		payload := map[string]string{"website_id": ws.ID.String()}
		if _, err := s.Jobs.Enqueue(ctx, ws.ServerID, &ws.ID, jobs.TypeSiteUsage, payload); err != nil {
			continue
		}
	}
}
