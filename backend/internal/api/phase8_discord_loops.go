package api

// Phase 8 control-plane loops: reconciliation of bot rows against agent
// truth (live Phase 3 metrics + finished bot jobs) and the scheduled-restart
// sweeper. Started by registerPhase8 (once per process).

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/epicbyte/epicpanel/backend/internal/discord"
	"github.com/epicbyte/epicpanel/backend/internal/events"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
)

var botLoopsOnce syncOnce

type syncOnce struct{ done bool }

func (o *syncOnce) Do(f func()) {
	if o.done {
		return
	}
	o.done = true
	f()
}

// startBotLoops runs the reconciliation (30s) + schedule sweeper (30s).
// The loops are skipped when EPICPANEL_DISABLE_BOT_LOOPS=1 (deterministic
// unit-test harnesses drive reconcileBots directly).
func (s *Server) startBotLoops() {
	botLoopsOnce.Do(func() {
		if os.Getenv("EPICPANEL_DISABLE_BOT_LOOPS") == "1" {
			return
		}
		go func() {
			time.Sleep(10 * time.Second) // settle after boot; converge immediately
			ctx := context.Background()
			s.reconcileBots(ctx)
			s.fireDueBotSchedules(ctx)
		}()
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
					s.reconcileBots(ctx)
					s.fireDueBotSchedules(ctx)
					cancel()
				}
			}
		}()
	})
}

// reconcileBots converges bot rows to agent truth and applies crash
// recovery (restart policy + episode counter). Truth sources, in order:
//  1. live metrics envelope (LIVE samples only — STALE is not truth);
//  2. finished bot jobs since the row's last update.
func (s *Server) reconcileBots(ctx context.Context) {
	store := &discord.Store{Pool: s.Pool}
	bots, err := store.ListActiveBots(ctx)
	if err != nil {
		slog.Debug("bot reconcile list failed", "err", err)
		return
	}
	for i := range bots {
		bot := &bots[i]

		// --- live metrics truth ---
		if sample, fresh := s.botSample(bot); fresh {
			switch {
			case sample.Status == "active" && bot.Status == discord.StatusStarting && bot.DesiredState == discord.DesiredRunning:
				if _, err := store.ReconcileAgentTruth(ctx, bot.ID, "active"); err == nil {
					s.publishBotEvent(ctx, bot.OrgID, discord.EventBotStarted, bot.ID)
				}
			case (sample.Status == "failed" || sample.Status == "inactive") &&
				bot.DesiredState == discord.DesiredRunning && bot.Status == discord.StatusRunning:
				// Crash (or node-level stop): crash transition + policy.
				if _, err := store.ReconcileAgentTruth(ctx, bot.ID, sample.Status); err == nil {
					s.publishBotEvent(ctx, bot.OrgID, discord.EventBotCrashed, bot.ID)
					s.attemptCrashRecovery(ctx, store, bot)
				}
			case bot.DesiredState == discord.DesiredStopped && sample.Status == "active" &&
				(bot.Status == discord.StatusRunning || bot.Status == discord.StatusStopping):
				// Converge: wanted stopped, unit still active.
				_, _ = botEnqueueIdempotent(ctx, s.Jobs, bot.ServerID, bot.ID, jobs.Type(discord.JobBotStop),
					discord.BotIDPayload{BotID: bot.ID.String()}, "botstop-"+bot.ID.String())
			case sample.Status == "inactive" && bot.DesiredState == discord.DesiredStopped &&
				(bot.Status == discord.StatusStopping || bot.Status == discord.StatusRunning || bot.Status == discord.StatusStarting):
				// Agent truth: the unit is down and that is what the customer
				// wanted — confirm stopped (the stop job itself may still be
				// queued; the agent converge is idempotent).
				if err := store.SetStatus(ctx, bot.ID, discord.StatusStopped, ""); err == nil {
					s.publishBotEvent(ctx, bot.OrgID, discord.EventBotStopped, bot.ID)
				}
			}
		}

		// --- finished-job truth ---
		if err := s.applyBotJobOutcomes(ctx, store, bot); err != nil {
			slog.Debug("bot job outcome apply failed", "bot", bot.ID, "err", err)
		}

		// --- truth refresh when no live data ---
		if bot.Status == discord.StatusRunning || bot.Status == discord.StatusStarting {
			if _, fresh := s.botSample(bot); !fresh {
				_, _ = botEnqueueIdempotent(ctx, s.Jobs, bot.ServerID, bot.ID, jobs.Type(discord.JobBotStatus),
					discord.BotIDPayload{BotID: bot.ID.String()}, "botstatus-"+bot.ID.String())
			}
		}
	}
}

// AppSampleView mirrors the fields of agentproto.AppSample the loop uses
// (local to avoid importing the protocol package in the api wiring).
type AppSampleView = struct {
	Status       string
	RestartCount int
	CPUPercent   float64
	MemoryBytes  int64
}

// botSample returns the live app envelope for a bot + whether it is fresh.
func (s *Server) botSample(bot *discord.Bot) (*AppSampleView, bool) {
	if s.LiveStore == nil {
		return nil, false
	}
	frame := s.LiveStore.Frame(bot.ServerID)
	if frame == nil || frame.Sample == nil {
		return nil, false
	}
	if state, _ := frame.Freshness["state"].(string); state != "LIVE" {
		return nil, false
	}
	for i := range frame.Apps {
		if frame.Apps[i].WebsiteID == bot.ID.String() {
			return &AppSampleView{
				Status:       frame.Apps[i].Status,
				RestartCount: frame.Apps[i].RestartCount,
				CPUPercent:   frame.Apps[i].CPUPercent,
				MemoryBytes:  frame.Apps[i].MemoryBytes,
			}, true
		}
	}
	return nil, false
}

// applyBotJobOutcomes reads the bot's recent finished jobs and converges the
// row (self-contained; no coordinator wiring needed).
func (s *Server) applyBotJobOutcomes(ctx context.Context, store *discord.Store, bot *discord.Bot) error {
	list, err := botListRecent(ctx, s.Jobs, bot.ID, 10, false)
	if err != nil {
		return err
	}
	for _, job := range list {
		if job.CreatedAt.Before(bot.UpdatedAt.Add(-time.Minute)) {
			continue // stale relative to the row's last update
		}
		if job.Status != jobs.StatusSuccess && job.Status != jobs.StatusFailed {
			continue
		}
		switch job.Type {
		case jobs.Type(discord.JobBotStart), jobs.Type(discord.JobBotRestart):
			if job.Status == jobs.StatusSuccess {
				if bot.Status == discord.StatusStarting || bot.Status == discord.StatusCrashed {
					var out discord.BotStatusOutcome
					_ = json.Unmarshal(job.Result, &out)
					state := out.UnitState
					if state == "" {
						state = "active"
					}
					if _, aerr := store.ReconcileAgentTruth(ctx, bot.ID, state); aerr == nil {
						s.publishBotEvent(ctx, bot.OrgID, discord.EventBotStarted, bot.ID)
					}
				}
			} else if job.Attempts >= job.MaxAttempts && bot.Status == discord.StatusStarting {
				_ = store.MarkFailed(ctx, bot.ID, "start job failed: "+job.Error)
			}
		case jobs.Type(discord.JobBotStop), jobs.Type(discord.JobBotKill):
			if job.Status == jobs.StatusSuccess {
				if err := store.SetStatus(ctx, bot.ID, discord.StatusStopped, ""); err == nil {
					s.publishBotEvent(ctx, bot.OrgID, discord.EventBotStopped, bot.ID)
				}
			}
		case jobs.Type(discord.JobBotInstall):
			if job.Status == jobs.StatusSuccess && bot.Status == discord.StatusInstalling {
				if err := store.SetStatus(ctx, bot.ID, discord.StatusStopped, ""); err != nil {
					slog.Debug("bot install settle failed", "bot", bot.ID, "err", err)
				}
			} else if job.Status == jobs.StatusFailed && job.Attempts >= job.MaxAttempts && bot.Status == discord.StatusInstalling {
				_ = store.MarkFailed(ctx, bot.ID, "install failed: "+job.Error)
			}
		case jobs.Type(discord.JobBotDeployGit):
			if job.Status == jobs.StatusSuccess {
				s.publishBotEvent(ctx, bot.OrgID, discord.EventBotDeployed, bot.ID)
			} else if job.Status == jobs.StatusFailed && job.Attempts >= job.MaxAttempts {
				_ = store.SetError(ctx, bot.ID, "git deploy failed: "+job.Error)
			}
		case jobs.Type(discord.JobBotDelete):
			if job.Status == jobs.StatusSuccess {
				_ = store.MarkDeleted(ctx, bot.ID)
				discord.DropConsole(bot.ID)
			}
		}
	}
	return nil
}

// attemptCrashRecovery applies the restart policy: auto-restart crashes
// while the episode counter allows it (policy "no" never auto-restarts).
func (s *Server) attemptCrashRecovery(ctx context.Context, store *discord.Store, bot *discord.Bot) {
	if bot.RestartPolicy == "no" {
		return
	}
	row, err := store.GetBotRowAny(ctx, bot.ID)
	if err != nil {
		return
	}
	if row.EpisodeRestarts > row.Bot.MaxRestarts {
		slog.Warn("bot crash recovery exhausted", "bot", bot.ID, "episode_restarts", row.EpisodeRestarts)
		return
	}
	if err := store.MarkStarting(ctx, bot.ID); err != nil {
		return
	}
	payload := s.botStartPayloadFor(ctx, store, bot)
	if payload == nil {
		return
	}
	if _, err := botEnqueueIdempotent(ctx, s.Jobs, bot.ServerID, bot.ID, jobs.Type(discord.JobBotStart), *payload,
		"botcrash-"+bot.ID.String()+"-"+strconv.Itoa(row.EpisodeRestarts)); err != nil {
		slog.Error("bot crash recovery enqueue failed", "bot", bot.ID, "err", err)
		return
	}
	s.publishBotEvent(ctx, bot.OrgID, "bot.recovery_started", bot.ID)
	slog.Info("bot crash recovery enqueued", "bot", bot.ID, "episode_restarts", row.EpisodeRestarts)
}

// botStartPayloadFor rebuilds the start payload from the stored row.
func (s *Server) botStartPayloadFor(ctx context.Context, store *discord.Store, bot *discord.Bot) *discord.BotLifecyclePayload {
	row, err := store.GetBotRowAny(ctx, bot.ID)
	if err != nil {
		return nil
	}
	return &discord.BotLifecyclePayload{
		BotID:          row.Bot.ID.String(),
		Runtime:        row.Bot.Runtime,
		RuntimeVersion: row.Bot.RuntimeVer,
		StartupFile:    row.Bot.StartupFile,
		StartupCommand: row.Bot.StartupCmd,
		EnvEnc:         row.EnvEnc,
		RestartPolicy:  row.Bot.RestartPolicy,
		NetAllow:       row.Bot.NetAllow,
		Limits:         s.botLimitsForOrg(bot.OrgID),
	}
}

// botLimitsForOrg resolves the Phase 9 engine numbers for a bot unit.
func (s *Server) botLimitsForOrg(orgID uuid.UUID) discord.BotLimitsPayload {
	var out discord.BotLimitsPayload
	if s.ResourceLimits == nil {
		return out
	}
	limits, err := s.ResourceLimits.LimitsForOrg(context.Background(), orgID)
	if err != nil {
		return out
	}
	if r, ok := limits.Get(resources.ResCPU); ok && r.Limit > 0 {
		out.CPUPercent = r.Limit
	}
	if r, ok := limits.Get(resources.ResRAM); ok && r.Limit > 0 {
		out.MemoryMB = int64(r.Limit)
	}
	if r, ok := limits.Get(resources.ResDisk); ok && r.Limit > 0 {
		out.DiskMB = int64(r.Limit)
	}
	if r, ok := limits.Get(resources.ResProcesses); ok && r.Limit > 0 {
		out.PidsMax = int64(r.Limit)
	}
	return out
}

// fireDueBotSchedules enqueues due scheduled lifecycle actions and advances
// next_run (idempotent claim).
func (s *Server) fireDueBotSchedules(ctx context.Context) {
	store := &discord.Store{Pool: s.Pool}
	due, err := store.ListDueSchedules(ctx, 50)
	if err != nil {
		return
	}
	for _, sc := range due {
		bot, err := store.GetByIDAny(ctx, sc.BotID)
		if err != nil || bot == nil || bot.Status == discord.StatusDeleted {
			continue
		}
		interval := discord.NextCronAfter(sc.Cron, time.Now().UTC())
		if interval <= 0 {
			continue
		}
		jobType := discord.JobBotRestart
		var payload any = discord.BotIDPayload{BotID: bot.ID.String()}
		switch sc.Kind {
		case "restart", "start":
			_ = store.SetDesiredState(ctx, bot.ID, discord.DesiredRunning)
			if p := s.botStartPayloadFor(ctx, store, bot); p != nil {
				payload = *p
			}
			if sc.Kind == "start" {
				jobType = discord.JobBotStart
				if err := store.MarkStarting(ctx, bot.ID); err != nil {
					_ = store.ScheduleFired(ctx, sc.ID, interval)
					continue
				}
			} else if err := store.MarkStopping(ctx, bot.ID); err != nil {
				_ = store.ScheduleFired(ctx, sc.ID, interval)
				continue
			}
		case "stop":
			_ = store.SetDesiredState(ctx, bot.ID, discord.DesiredStopped)
			if err := store.MarkStopping(ctx, bot.ID); err != nil {
				_ = store.ScheduleFired(ctx, sc.ID, interval)
				continue
			}
		}
		if _, err := botEnqueueIdempotent(ctx, s.Jobs, bot.ServerID, bot.ID, jobs.Type(jobType), payload,
			"botsched-"+sc.ID.String()+"-"+strconv.FormatInt(time.Now().Unix()/60, 10)); err == nil {
			_ = store.ScheduleFired(ctx, sc.ID, interval)
			s.publishBotEvent(ctx, bot.OrgID, discord.EventBotRestartScheduled, bot.ID, map[string]any{"kind": sc.Kind, "cron": sc.Cron})
		}
	}
}

func (s *Server) publishBotEvent(ctx context.Context, orgID uuid.UUID, eventType string, botID uuid.UUID, payload ...map[string]any) {
	if s.Events == nil {
		return
	}
	org := orgID
	var p any
	if len(payload) > 0 {
		p = payload[0]
	}
	s.Events.Publish(ctx, events.Event{
		Type:         eventType,
		Organization: &org,
		ResourceType: "bot",
		ResourceID:   botID.String(),
		Payload:      p,
	})
}
