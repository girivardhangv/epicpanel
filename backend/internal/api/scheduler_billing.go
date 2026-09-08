package api

// ============================================================================
// Phase 10 — scheduler_billing.go: the renewal + failure-walk + convergence
// loops, following the existing scheduler.go pattern (background tickers,
// never in request paths).
//
//   renewal loop (hourly + boot):  invoice due subscriptions -> payment
//     attempt -> Extend. Failure -> grace (configurable days, default 3).
//   grace walk (hourly + boot):    grace elapsed -> SUSPENDING (existing
//     workload suspend jobs) -> SUSPENDED. Suspended beyond the terminate
//     window -> TERMINATING (backup-first) -> TERMINATED.
//   convergence loop (30s):        finished state-changing jobs advance
//     PROVISIONING -> ACTIVE, SUSPENDING -> SUSPENDED, TERMINATING ->
//     TERMINATED. Agent/job truth is the ONLY authority.
//
// Idempotency: every scan is guarded by state + timestamps (a replayed scan
// enqueues nothing new); every job carries an idempotency key. Disable knob
// for deterministic tests: EPICPANEL_DISABLE_BILLING_LOOPS=1.
// ============================================================================

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/billing"
	"github.com/epicbyte/epicpanel/backend/internal/discord"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/minecraft"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

var billingLoopsOnce struct{ done bool }

// startBillingLoops runs the billing maintenance loops (once per process).
func (s *Server) startBillingLoops() {
	if billingLoopsOnce.done {
		return
	}
	billingLoopsOnce.done = true
	if os.Getenv("EPICPANEL_DISABLE_BILLING_LOOPS") == "1" {
		return
	}
	svc := s.newBillingService()

	// Boot pass: converge immediately after restart.
	go func() {
		time.Sleep(15 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		svc.RunRenewals(ctx)
		s.billingConverge(ctx, svc)
	}()

	// Renewal + failure walk: hourly (the scheduler.go cadence).
	go func() {
		ticker := time.NewTicker(schedulerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
				svc.RunRenewals(ctx)
				cancel()
			}
		}
	}()

	// Convergence: 30s (matches the other control-plane fast loops).
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
				s.billingConverge(ctx, svc)
				cancel()
			}
		}
	}()
}

// billingConverge advances subscriptions in transitional states from
// finished jobs. The subscription's last_job_id is the tracked job.
func (s *Server) billingConverge(ctx context.Context, svc *billing.Service) {
	for _, state := range []billing.State{billing.StateProvisioning, billing.StateSuspending, billing.StateTerminating} {
		subs, err := svc.Store.ListInState(ctx, state, 200)
		if err != nil {
			slog.Warn("billing: converge scan failed", "state", string(state), "err", err)
			continue
		}
		for i := range subs {
			sub := &subs[i]
			if sub.LastJobID == nil {
				// Nothing pending: surface the stall via last_error only
				// (dispatch already audited the failure cause).
				continue
			}
			job, err := s.Jobs.GetByID(ctx, *sub.LastJobID)
			if err != nil {
				continue
			}
			if job.Status != jobs.StatusSuccess && job.Status != jobs.StatusFailed {
				continue // still in flight
			}
			svc.ApplyJobOutcome(ctx, billing.JobOutcome{
				SubscriptionID: sub.ID,
				JobType:        job.Type,
				Success:        job.Status == jobs.StatusSuccess,
				Error:          job.Error,
			})
		}
	}
}

// billingStalledRecovery re-enqueues dispatch jobs for subscriptions stuck
// PROVISIONING whose tracked job is gone (e.g. a control-plane restart
// between order payment and enqueue). Idempotent: the enqueue keys collide
// with any live duplicate.
func (s *Server) billingStalledRecovery(ctx context.Context, svc *billing.Service) {
	subs, err := svc.Store.ListInState(ctx, billing.StateProvisioning, 100)
	if err != nil {
		return
	}
	for i := range subs {
		sub := &subs[i]
		if sub.LastJobID != nil {
			if _, err := s.Jobs.GetByID(ctx, *sub.LastJobID); err == nil {
				continue // job tracked and known
			}
		}
		if sub.ProductID == nil {
			continue
		}
		product, err := svc.Store.GetProduct(ctx, uuidPtrValue(sub.ProductID))
		if err != nil {
			continue
		}
		if err := s.billingProvision(ctx, svc, sub, product); err != nil {
			slog.Warn("billing: stalled recovery failed", "sub", sub.ID, "err", err)
		}
	}
}

// billingReconcileWorkloadTruth applies AGENT truth for subscriptions whose
// workload died outside billing (crash while ACTIVE keeps the subscription
// ACTIVE — workload crash recovery is the workload engine's job — but a
// manually deleted workload must not leave billing believing it exists).
func (s *Server) billingReconcileWorkloadTruth(ctx context.Context, svc *billing.Service) {
	subs, err := svc.Store.ListInState(ctx, billing.StateActive, 200)
	if err != nil {
		return
	}
	for i := range subs {
		sub := &subs[i]
		switch {
		case sub.BotID != nil:
			if _, err := (&discord.Store{Pool: s.Pool}).GetByIDAny(ctx, *sub.BotID); err != nil {
				continue
			}
		case sub.InstanceID != nil:
			if _, err := (&minecraft.Store{Pool: s.Pool}).GetByIDAny(ctx, *sub.InstanceID); err != nil {
				continue
			}
		case sub.WebsiteID != nil:
			ws, err := (&websites.Store{Pool: s.Pool}).GetByIDAny(ctx, *sub.WebsiteID)
			if err != nil || ws == nil {
				continue
			}
			if ws.Status == websites.StatusDeleted {
				// Workload deleted out-of-band: terminate the billing side
				// so the machine never lies about a live service.
				_ = svc.BeginTerminate(ctx, sub, "workload deleted out-of-band")
			}
		}
	}
}

// uuidPtrValue dereferences an optional uuid pointer (nil -> uuid.Nil).
func uuidPtrValue(p *uuid.UUID) uuid.UUID {
	if p == nil {
		return uuid.Nil
	}
	return *p
}
