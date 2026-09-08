package api

// ============================================================================
// Phase 10 — Billing & Provisioning API (registered via registerPhase10).
//
// Customer (org-scoped): subscriptions, invoices, orders/checkout, payment
// method, renewal status. Mutations require org admin+ (purchases move
// money); reads require billing+; cross-tenant access is 404.
// Admin (platform-admin session only): plans<->products, invoice
// issue/void, billing settings (grace period etc.), fleet subscriptions,
// manual retry of FAILED subscriptions.
// Webhook (public): signature-verified gateway callback — verify first,
// never trust client-side "paid".
//
// The provision dispatch maps product type -> workload engine:
//   hosting   -> web account   (websites.Store + provision_website job)
//   minecraft -> MC instance   (minecraft.Store + mc_install job)
//   discord   -> Discord bot   (discord.Store + bot_install job)
// Each engine applies the Phase 9 plan limits at creation (count gates +
// the engine numbers that ride the job payloads).
// ============================================================================

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	agentpkg "github.com/epicbyte/epicpanel/backend/internal/agent"
	"github.com/epicbyte/epicpanel/backend/internal/audit"
	"github.com/epicbyte/epicpanel/backend/internal/billing"
	"github.com/epicbyte/epicpanel/backend/internal/discord"
	"github.com/epicbyte/epicpanel/backend/internal/httpapi"
	"github.com/epicbyte/epicpanel/backend/internal/jobs"
	"github.com/epicbyte/epicpanel/backend/internal/minecraft"
	"github.com/epicbyte/epicpanel/backend/internal/organizations"
	"github.com/epicbyte/epicpanel/backend/internal/packages"
	"github.com/epicbyte/epicpanel/backend/internal/resources"
	"github.com/epicbyte/epicpanel/backend/internal/secretbox"
	"github.com/epicbyte/epicpanel/backend/internal/servers"
	"github.com/epicbyte/epicpanel/backend/internal/settings"
	"github.com/epicbyte/epicpanel/backend/internal/websites"
)

// registerPhase10 mounts the billing routes + starts the billing loops.
// The coordinator adds the single call line in server.go.
func registerPhase10(s *Server, mux *http.ServeMux) {
	svc := s.newBillingService()
	h := &billingHandler{srv: s, svc: svc, requireOrg: (&servers.Handler{Orgs: s.Orgs}).ResolveOrg}

	// ---- customer (org-scoped) ----
	mux.HandleFunc("GET /v1/organizations/{org_id}/billing/overview", h.wrapOrg(organizations.RoleBilling, h.overview))
	mux.HandleFunc("GET /v1/organizations/{org_id}/billing/subscriptions", h.wrapOrg(organizations.RoleBilling, h.listSubscriptions))
	mux.HandleFunc("GET /v1/organizations/{org_id}/billing/subscriptions/{subscription_id}", h.wrapOrg(organizations.RoleBilling, h.getSubscription))
	mux.HandleFunc("POST /v1/organizations/{org_id}/billing/subscriptions/{subscription_id}/cancel", h.wrapOrg(organizations.RoleAdmin, h.cancelSubscription))

	mux.HandleFunc("GET /v1/organizations/{org_id}/billing/products", h.wrapOrg(organizations.RoleBilling, h.listProducts))
	mux.HandleFunc("POST /v1/organizations/{org_id}/billing/orders", h.wrapOrg(organizations.RoleAdmin, h.createOrder))
	mux.HandleFunc("GET /v1/organizations/{org_id}/billing/orders", h.wrapOrg(organizations.RoleBilling, h.listOrders))
	mux.HandleFunc("POST /v1/organizations/{org_id}/billing/orders/{order_id}/pay", h.wrapOrg(organizations.RoleAdmin, h.payOrder))

	mux.HandleFunc("GET /v1/organizations/{org_id}/billing/invoices", h.wrapOrg(organizations.RoleBilling, h.listInvoices))
	mux.HandleFunc("GET /v1/organizations/{org_id}/billing/invoices/{invoice_id}", h.wrapOrg(organizations.RoleBilling, h.getInvoice))
	mux.HandleFunc("POST /v1/organizations/{org_id}/billing/invoices/{invoice_id}/pay", h.wrapOrg(organizations.RoleAdmin, h.payInvoice))

	mux.HandleFunc("GET /v1/organizations/{org_id}/billing/payment-method", h.wrapOrg(organizations.RoleBilling, h.getPaymentMethod))
	mux.HandleFunc("PUT /v1/organizations/{org_id}/billing/payment-method", h.wrapOrg(organizations.RoleAdmin, h.putPaymentMethod))

	// ---- admin (platform-admin session only) ----
	mux.HandleFunc("GET /v1/admin/billing/plans", h.requireAdmin(h.adminPlans))
	mux.HandleFunc("GET /v1/admin/billing/products", h.requireAdmin(h.adminListProducts))
	mux.HandleFunc("POST /v1/admin/billing/products", h.requireAdmin(h.adminCreateProduct))
	mux.HandleFunc("PATCH /v1/admin/billing/products/{product_id}", h.requireAdmin(h.adminUpdateProduct))

	mux.HandleFunc("GET /v1/admin/billing/invoices", h.requireAdmin(h.adminListInvoices))
	mux.HandleFunc("POST /v1/admin/billing/invoices", h.requireAdmin(h.adminIssueInvoice))
	mux.HandleFunc("POST /v1/admin/billing/invoices/{invoice_id}/void", h.requireAdmin(h.adminVoidInvoice))

	mux.HandleFunc("GET /v1/admin/billing/settings", h.requireAdmin(h.adminGetSettings))
	mux.HandleFunc("PATCH /v1/admin/billing/settings", h.requireAdmin(h.adminPatchSettings))

	mux.HandleFunc("GET /v1/admin/billing/subscriptions", h.requireAdmin(h.adminListSubscriptions))
	mux.HandleFunc("POST /v1/admin/billing/subscriptions/{subscription_id}/retry", h.requireAdmin(h.adminRetrySubscription))
	mux.HandleFunc("POST /v1/admin/billing/subscriptions/{subscription_id}/terminate", h.requireAdmin(h.adminTerminateSubscription))

	// ---- webhook (public; signature IS the authentication) ----
	mux.HandleFunc("POST /v1/billing/webhook/{provider}", h.webhook)

	// Renewal + failure-walk + convergence loops (once per process).
	s.startBillingLoops()
}

// ---------------------------------------------------------------------------
// service construction (wires the control plane to the billing core)
// ---------------------------------------------------------------------------

// newBillingService builds the billing service with the workload engines.
func (s *Server) newBillingService() *billing.Service {
	svc := &billing.Service{
		Store:  &billing.Store{Pool: s.Pool},
		Jobs:   s.Jobs,
		Events: s.Events,
		Audit:  s.Audit,
	}
	svc.SettingsFn = func(ctx context.Context, key string) (string, error) {
		return (&settings.Store{Pool: s.Pool}).Get(ctx, key)
	}
	svc.Provision = func(ctx context.Context, sub *billing.Subscription, product *billing.Product) error {
		return s.billingProvision(ctx, svc, sub, product)
	}
	svc.Suspend = func(ctx context.Context, sub *billing.Subscription) error {
		return s.billingSuspend(ctx, sub)
	}
	svc.Resume = func(ctx context.Context, sub *billing.Subscription) error {
		return s.billingResume(ctx, sub)
	}
	svc.Terminate = func(ctx context.Context, sub *billing.Subscription) error {
		return s.billingTerminate(ctx, sub)
	}
	return svc
}

// setBillingSetting upserts a billing settings key (admin API).
func setBillingSetting(ctx context.Context, pool *pgxpool.Pool, key, value string) error {
	return (&settings.Store{Pool: pool}).Set(ctx, key, value)
}

// ---------------------------------------------------------------------------
// workload engines (product type -> job types with Phase 9 plan limits)
// ---------------------------------------------------------------------------

// applyProductPlan grants the purchased plan to the organization
// (plans <-> products): when the product links a hosting_packages row and
// the org is not on it yet, the plan is assigned and limit enforcement is
// re-converged for the org's existing sites (Phase 9 engine).
func (s *Server) applyProductPlan(ctx context.Context, store *billing.Store, orgID uuid.UUID, product *billing.Product) error {
	if product.PlanID == nil {
		return nil
	}
	var currentID uuid.UUID
	qerr := s.Pool.QueryRow(ctx, `SELECT COALESCE(package_id, '00000000-0000-0000-0000-000000000000'::uuid) FROM organizations WHERE id = $1`, orgID).Scan(&currentID)
	if qerr == nil && currentID == *product.PlanID {
		return nil // already on the purchased plan
	}
	pkgs := &packages.Store{Pool: s.Pool}
	affected, err := pkgs.Assign(ctx, orgID, *product.PlanID)
	if err != nil {
		return err
	}
	for _, site := range affected {
		payload, perr := s.ResourceLimits.EnforcePayloadFor(ctx, orgID, site.ID)
		if perr != nil {
			continue
		}
		_, _ = s.Jobs.EnqueueForWebsite(ctx, site.ID, jobs.TypeEnforceLimits, agentpkg.EnforceJobPayload{
			WebsiteID:      payload.WebsiteID,
			Plan:           payload.Plan,
			CPUPercent:     payload.CPUPercent,
			MemoryMB:       payload.MemoryMB,
			DiskMB:         payload.DiskMB,
			BandwidthMB:    payload.BandwidthMB,
			IOWeight:       resources.ClampIOWeight(payload.IOWeight),
			PidsMax:        payload.PidsMax,
			FpmMaxChildren: payload.FpmMaxChildren,
			CountLimits:    payload.CountLimits,
		})
	}
	_ = store
	slog.Info("billing: purchased plan applied", "org", orgID, "plan", product.PlanName)
	return nil
}

// billingProvision dispatches the workload for a paid subscription by
// product type. Each engine enqueues its idempotent job and records the
// refs + job id on the subscription (convergence -> ACTIVE on job truth).
func (s *Server) billingProvision(ctx context.Context, svc *billing.Service, sub *billing.Subscription, product *billing.Product) error {
	if err := s.applyProductPlan(ctx, svc.Store, sub.OrgID, product); err != nil {
		return err
	}
	switch sub.WorkloadKind {
	case string(billing.KindWeb):
		return s.provisionWebAccount(ctx, svc, sub, product)
	case string(billing.KindMinecraft):
		return s.provisionMinecraft(ctx, svc, sub, product)
	case string(billing.KindDiscord):
		return s.provisionDiscordBot(ctx, svc, sub, product)
	case string(billing.KindService):
		// Services have no node workload: the order is fulfilled by an
		// operator. The subscription stays PROVISIONING until an admin
		// marks it active (POST /admin/billing/subscriptions/{id}/retry
		// with the service-fulfilled path is admin-visible).
		return errors.New("service products are fulfilled manually; an operator must activate the subscription")
	default:
		return errors.New("no provision engine for workload kind " + sub.WorkloadKind)
	}
}

func cfgString(product *billing.Product, key, def string) string {
	if product.Config == nil {
		return def
	}
	var cfg map[string]any
	if err := json.Unmarshal(product.Config, &cfg); err != nil {
		return def
	}
	if v, ok := cfg[key].(string); ok && v != "" {
		return v
	}
	return def
}

// --- web account engine -----------------------------------------------------

func (s *Server) provisionWebAccount(ctx context.Context, svc *billing.Service, sub *billing.Subscription, product *billing.Product) error {
	// Phase 9 plan numbers for the FPM pool + honest limits on the job.
	orgID := sub.OrgID
	runtime := websites.Runtime(cfgString(product, "runtime", "static"))
	runtimeVersion := cfgString(product, "runtime_version", "")
	name := cfgString(product, "site_prefix", "web-"+sub.ID.String()[:8])
	if len(name) > 40 {
		name = name[:40]
	}
	primaryDomain := cfgString(product, "primary_domain", "")

	// Server placement: product pin or auto-pick (runtime-aware).
	serverID, err := s.pickProvisionServer(ctx, cfgString(product, "server_id", ""), string(runtime), runtimeVersion)
	if err != nil {
		return err
	}
	wsStore := &websites.Store{Pool: s.Pool}
	createdBy := uuid.Nil
	ws, unixUser, err := wsStore.Create(ctx, orgID, serverID, createdBy, name, primaryDomain, runtime, runtimeVersion, "")
	if err != nil {
		return err
	}
	payload := websites.DesiredPayload{
		WebsiteID:     ws.ID,
		Organization:  orgID.String(),
		Name:          ws.Name,
		UnixUser:      unixUser,
		Runtime:       string(ws.Runtime),
		RuntimeVersion: ws.RuntimeVersion,
		WebServer:     ws.WebServer,
		DocumentRoot:  websites.DocumentRootFor(ws.ID),
		PrimaryDomain: ws.PrimaryDomain,
	}
	// FPM pool sizing from the SAME Phase 9 engine the usage bars display.
	if limits, err := s.ResourceLimits.LimitsForOrg(ctx, orgID); err == nil {
		if r, ok := limits.Get(resources.ResRAM); ok && r.Limit > 0 {
			payload.FpmMemoryLimitMB = int(r.Limit)
		}
		if r, ok := limits.Get(resources.ResPHPWorkers); ok && r.Limit > 0 {
			payload.FpmMaxChildren = int(r.Limit)
		}
	}
	job, err := s.Jobs.EnqueueIdempotent(ctx, serverID, &ws.ID, jobs.TypeProvisionWebsite, payload,
		"billingprovision-"+sub.ID.String())
	if err != nil {
		return err
	}
	wsID := ws.ID
	jobID := job.ID
	return svc.Store.SetSubscriptionWorkload(ctx, sub.ID, sub.WorkloadKind, &wsID, nil, nil, &jobID, sub.ProvisionAttempts+1)
}

// pickProvisionServer resolves an explicit server or auto-picks one.
// Static sites do not filter by runtime (mirrors the websites API wiring).
func (s *Server) pickProvisionServer(ctx context.Context, serverIDParam, runtime, version string) (uuid.UUID, error) {
	if serverIDParam != "" {
		id, err := uuid.Parse(serverIDParam)
		if err != nil {
			return uuid.Nil, errors.New("product config server_id is not a valid id")
		}
		if _, err := s.Servers.GetByID(ctx, id); err != nil {
			return uuid.Nil, errors.New("product config server_id does not exist")
		}
		return id, nil
	}
	rtFilter := runtime
	if runtime == "static" {
		rtFilter = ""
	}
	return s.Servers.AutoPickServer(ctx, rtFilter, version)
}

// --- minecraft engine --------------------------------------------------------

func (s *Server) provisionMinecraft(ctx context.Context, svc *billing.Service, sub *billing.Subscription, product *billing.Product) error {
	// Phase 9 gates: the org plan must be a Minecraft plan and the port
	// count must fit (fail closed).
	if s.ResourceLimits == nil {
		return errors.New("plan limits unavailable; minecraft provisioning blocked")
	}
	plan, err := s.ResourceLimits.PlanForOrg(ctx, sub.OrgID)
	if err != nil {
		return errors.New("plan limits unavailable; minecraft provisioning blocked")
	}
	if plan.Kind != string(resources.KindMinecraft) {
		return errors.New("the purchased plan is not a Minecraft plan")
	}
	limits, err := s.ResourceLimits.LimitsForOrg(ctx, sub.OrgID)
	if err != nil {
		return errors.New("plan limits unavailable; minecraft provisioning blocked")
	}
	mcStore := &minecraft.Store{Pool: s.Pool}
	count, err := mcStore.CountForOrg(ctx, sub.OrgID)
	if err != nil {
		return err
	}
	if err := resources.CheckCount(limits, resources.ResPorts, count); err != nil {
		return err
	}

	provider := cfgString(product, "provider", "vanilla")
	version := cfgString(product, "version", "")
	if version == "" {
		for _, o := range minecraft.ProviderOffers() {
			if o.Provider == provider && o.Default != "" {
				version = o.Default
				break
			}
		}
	}
	javaMajor, verr := minecraft.ValidateProviderVersion(provider, version)
	if verr != nil {
		return verr
	}
	name := cfgString(product, "instance_prefix", "mc-"+sub.ID.String()[:8])
	serverID, err := s.pickProvisionServer(ctx, cfgString(product, "server_id", ""), "minecraft", "")
	if err != nil {
		return err
	}
	gamePort, rconPort, err := s.allocateMCPorts(ctx, serverID)
	if err != nil {
		return err
	}
	rconPass, err := minecraft.GenerateRCONPassword()
	if err != nil {
		return err
	}
	rconEnc, err := minecraft.EncryptRCONPassword(rconPass)
	if err != nil {
		return err
	}
	inst, err := mcStore.Create(ctx, sub.OrgID, serverID, uuid.Nil, name, provider, version,
		javaMajor, gamePort, rconPort, minecraft.XmxForPlan(plan.MemoryLimitMB),
		"on-failure", 5, []byte("{}"), rconEnc)
	if err != nil {
		return err
	}
	payload := minecraft.MCInstallPayload{
		InstanceID: inst.ID.String(), Provider: inst.Provider, Version: inst.Version,
		JavaMajor: inst.JavaMajor, Port: inst.Port, RCONPort: inst.RCONPort,
		RCONPassEnc: rconEnc, XmxMB: inst.XmxMB, EULA: true,
	}
	job, err := mcEnqueueIdempotent(ctx, s.Jobs, serverID, inst.ID, jobs.Type(minecraft.JobMCInstall), payload,
		"billingprovision-"+sub.ID.String())
	if err != nil {
		return err
	}
	instID := inst.ID
	jobID := job.ID
	return svc.Store.SetSubscriptionWorkload(ctx, sub.ID, sub.WorkloadKind, nil, nil, &instID, &jobID, sub.ProvisionAttempts+1)
}

// allocateMCPorts mirrors the Phase 7 API port allocation (DB-driven used set).
func (s *Server) allocateMCPorts(ctx context.Context, serverID uuid.UUID) (int, int, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT port, rcon_port FROM minecraft_instances
		WHERE server_id = $1 AND status <> 'deleted'`, serverID)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	used := map[int]bool{}
	for rows.Next() {
		var g, rc int
		if err := rows.Scan(&g, &rc); err == nil {
			used[g] = true
			used[rc] = true
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	gamePort, ok := minecraft.AllocPort(used)
	if !ok {
		return 0, 0, errors.New("no free Minecraft ports left on this server")
	}
	used[gamePort] = true
	rconPort, ok := minecraft.AllocRCONPort(used)
	if !ok {
		return 0, 0, errors.New("no free RCON ports left on this server")
	}
	return gamePort, rconPort, nil
}

// --- discord bot engine -------------------------------------------------------

func (s *Server) provisionDiscordBot(ctx context.Context, svc *billing.Service, sub *billing.Subscription, product *billing.Product) error {
	// Phase 9 gates (same as the Phase 8 API create path).
	if s.ResourceLimits == nil {
		return errors.New("plan limits unavailable; bot provisioning blocked")
	}
	plan, err := s.ResourceLimits.PlanForOrg(ctx, sub.OrgID)
	if err != nil {
		return errors.New("plan limits unavailable; bot provisioning blocked")
	}
	if plan.Kind != string(resources.KindDiscord) {
		return errors.New("the purchased plan is not a Discord bot plan")
	}
	botStore := &discord.Store{Pool: s.Pool}
	count, err := botStore.CountForOrg(ctx, sub.OrgID)
	if err != nil {
		return err
	}
	if plan.MaxWebsites > 0 && count >= plan.MaxWebsites {
		return errors.New("plan limit reached: " + plan.Name + " allows " + strconv.Itoa(plan.MaxWebsites) + " bot(s)")
	}

	runtime := cfgString(product, "runtime", "node")
	version := cfgString(product, "runtime_version", "")
	if version == "" {
		// Resolve the runtime's default so auto-placement can filter on it.
		if rt, rerr := discord.RuntimeFor(runtime); rerr == nil {
			version = rt.DefaultVersion()
		}
	}
	serverID, err := s.pickProvisionServer(ctx, cfgString(product, "server_id", ""), runtime, version)
	if err != nil {
		return err
	}
	startupFile := cfgString(product, "startup_file", "index.js")
	name := cfgString(product, "bot_prefix", "bot-"+sub.ID.String()[:8])
	// Env vars are NOT provisioned from the order: secrets enter through
	// the Phase 8 write-only env endpoint, never through billing records.
	bot, err := discord.CreateBot(ctx, botStore, sub.OrgID, serverID, uuid.Nil, discord.CreateInput{
		Name: name, Runtime: runtime, RuntimeVersion: version, StartupFile: startupFile,
	})
	if err != nil {
		if discord.IsValidationError(err) {
			return err
		}
		return err
	}
	payload := discord.BotInstallPayload{BotID: bot.ID.String(), Runtime: bot.Runtime, RuntimeVersion: bot.RuntimeVer}
	job, err := billingBotEnqueue(ctx, s.Jobs, serverID, bot.ID, jobs.Type(discord.JobBotInstall), payload,
		"billingprovision-"+sub.ID.String())
	if err != nil {
		return err
	}
	botID := bot.ID
	jobID := job.ID
	return svc.Store.SetSubscriptionWorkload(ctx, sub.ID, sub.WorkloadKind, nil, &botID, nil, &jobID, sub.ProvisionAttempts+1)
}

// billingBotEnqueue inserts a bot job with an idempotency key (the billing
// replay anchor). Mirrors the Phase 8 enqueue seam.
func billingBotEnqueue(ctx context.Context, js *jobs.Store, serverID, botID uuid.UUID, jobType jobs.Type, payload any, key string) (*jobs.Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	row := js.Pool.QueryRow(ctx, `
		INSERT INTO jobs (server_id, bot_id, type, payload, idempotency_key)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND status IN ('pending', 'running')
		DO UPDATE SET updated_at = now()
		RETURNING id, server_id, website_id, bot_id, type, status, payload, result, error,
			progress, progress_step, attempts, max_attempts, idempotency_key, claimed_at,
			lease_expires_at, finished_at, created_at
	`, serverID, botID, jobType, payloadJSON, key)
	var j jobs.Job
	var wsID, jobBotID *uuid.UUID
	if err := row.Scan(&j.ID, &j.ServerID, &wsID, &jobBotID, &j.Type, &j.Status, &j.Payload, &j.Result,
		&j.Error, &j.Progress, &j.ProgressStep, &j.Attempts, &j.MaxAttempts, &j.IdempotencyKey,
		&j.ClaimedAt, &j.LeaseExpiresAt, &j.FinishedAt, &j.CreatedAt); err != nil {
		return nil, err
	}
	return &j, nil
}

// billingMCEnqueue inserts a minecraft job with an idempotency key.
func billingMCEnqueue(ctx context.Context, js *jobs.Store, serverID, instanceID uuid.UUID, jobType jobs.Type, payload any, key string) (*jobs.Job, error) {
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	row := js.Pool.QueryRow(ctx, `
		INSERT INTO jobs (server_id, minecraft_id, type, payload, idempotency_key)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (idempotency_key) WHERE idempotency_key IS NOT NULL AND status IN ('pending', 'running')
		DO UPDATE SET updated_at = now()
		RETURNING id
	`, serverID, instanceID, jobType, payloadJSON, key)
	var jobID uuid.UUID
	if err := row.Scan(&jobID); err != nil {
		return nil, err
	}
	return &jobs.Job{ID: jobID}, nil
}

// --- suspend / resume / terminate engines -------------------------------------
// Suspend semantics per workload (verbatim from the phase contract):
// web = site offline (suspend_website), minecraft = stop (mc_stop),
// discord = stop (bot_stop). Existing lifecycle jobs only — no new side
// channels — and every step is reversible until TERMINATED.

func (s *Server) billingSuspend(ctx context.Context, sub *billing.Subscription) error {
	switch {
	case sub.WebsiteID != nil:
		ws, err := (&websites.Store{Pool: s.Pool}).GetByIDAny(ctx, *sub.WebsiteID)
		if err != nil {
			return err
		}
		if ws.Status == websites.StatusSuspended {
			return nil // idempotent: already offline
		}
		_, err = s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeSuspendWebsite,
			websites.SuspendPayload{WebsiteID: ws.ID.String()}, "suspend_"+ws.ID.String())
		return err
	case sub.BotID != nil:
		bots := &discord.Store{Pool: s.Pool}
		bot, err := bots.GetByIDAny(ctx, *sub.BotID)
		if err != nil {
			return err
		}
		_ = bots.SetDesiredState(ctx, bot.ID, discord.DesiredStopped)
		_ = bots.MarkStopping(ctx, bot.ID)
		_, err = billingBotEnqueue(ctx, s.Jobs, bot.ServerID, bot.ID, jobs.Type(discord.JobBotStop),
			discord.BotIDPayload{BotID: bot.ID.String()}, "billingsuspend-"+sub.ID.String())
		return err
	case sub.InstanceID != nil:
		mcs := &minecraft.Store{Pool: s.Pool}
		inst, err := mcs.GetByIDAny(ctx, *sub.InstanceID)
		if err != nil {
			return err
		}
		_ = mcs.SetDesiredState(ctx, inst.ID, "stopped")
		_ = mcs.MarkStopping(ctx, inst.ID)
		_, err = billingMCEnqueue(ctx, s.Jobs, inst.ServerID, inst.ID, jobs.Type(minecraft.JobMCStop),
			minecraft.MCIDPayload{InstanceID: inst.ID.String()}, "billingsuspend-"+sub.ID.String())
		return err
	}
	return errors.New("subscription has no workload to suspend")
}

func (s *Server) billingResume(ctx context.Context, sub *billing.Subscription) error {
	switch {
	case sub.WebsiteID != nil:
		ws, err := (&websites.Store{Pool: s.Pool}).GetByIDAny(ctx, *sub.WebsiteID)
		if err != nil {
			return err
		}
		if ws.Status != websites.StatusSuspended {
			return nil // idempotent
		}
		_, err = s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeResumeWebsite,
			websites.ResumePayload{WebsiteID: ws.ID.String()}, "resume_"+ws.ID.String())
		return err
	case sub.BotID != nil:
		bots := &discord.Store{Pool: s.Pool}
		row, err := bots.GetBotRowAny(ctx, *sub.BotID)
		if err != nil {
			return err
		}
		_ = bots.SetDesiredState(ctx, row.Bot.ID, discord.DesiredRunning)
		payload := s.botStartPayloadFor(ctx, bots, &row.Bot)
		if payload == nil {
			return errors.New("bot row unreadable for resume")
		}
		_, err = billingBotEnqueue(ctx, s.Jobs, row.Bot.ServerID, row.Bot.ID, jobs.Type(discord.JobBotStart),
			*payload, "billingresume-"+sub.ID.String())
		return err
	case sub.InstanceID != nil:
		mcs := &minecraft.Store{Pool: s.Pool}
		row, err := mcs.GetRowAny(ctx, *sub.InstanceID)
		if err != nil {
			return err
		}
		_ = mcs.SetDesiredState(ctx, row.ID, "running")
		_, err = billingMCEnqueue(ctx, s.Jobs, row.ServerID, row.ID, jobs.Type(minecraft.JobMCStart),
			s.mcLifecyclePayload(row), "billingresume-"+sub.ID.String())
		return err
	}
	return errors.New("subscription has no workload to resume")
}

// billingTerminate is IRREVERSIBLE and backup-first (Phase 11 rule): a
// backup job is enqueued BEFORE the delete job; jobs claim FIFO per server,
// so the backup runs first. Phase 11's dedicated terminate_backup hook job
// will replace the website create_backup call when it lands.
func (s *Server) billingTerminate(ctx context.Context, sub *billing.Subscription) error {
	switch {
	case sub.WebsiteID != nil:
		ws, err := (&websites.Store{Pool: s.Pool}).GetByIDAny(ctx, *sub.WebsiteID)
		if err != nil {
			return err
		}
		// Backup-first (best effort: a failing backup must not block the
		// irreversible delete of an already-terminated subscription — the
		// delete job is enqueued right after; both are idempotent).
		if ws.Status == websites.StatusReady {
			if _, berr := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeCreateBackup,
				map[string]any{"website_id": ws.ID.String(), "reason": "pre_terminate"},
				"billingtermbackup-"+sub.ID.String()); berr != nil {
				slog.Warn("billing: pre-terminate backup enqueue failed", "website", ws.ID, "err", berr)
			}
		}
		payload := websites.DesiredPayload{
			WebsiteID:     ws.ID,
			Organization:  ws.Organization.String(),
			Name:          ws.Name,
			UnixUser:      ws.UnixUser,
			Runtime:       string(ws.Runtime),
			RuntimeVersion: ws.RuntimeVersion,
			WebServer:     ws.WebServer,
			PrimaryDomain: ws.PrimaryDomain,
		}
		if _, derr := s.Jobs.EnqueueIdempotent(ctx, ws.ServerID, &ws.ID, jobs.TypeDeleteWebsite, payload,
			"billingdelete-"+sub.ID.String()); derr != nil {
			return derr
		}
		return nil
	case sub.BotID != nil:
		bots := &discord.Store{Pool: s.Pool}
		bot, err := bots.GetByIDAny(ctx, *sub.BotID)
		if err != nil {
			return err
		}
		_ = bots.MarkDeleting(ctx, bot.ID)
		if _, derr := billingBotEnqueue(ctx, s.Jobs, bot.ServerID, bot.ID, jobs.Type(discord.JobBotDelete),
			discord.BotIDPayload{BotID: bot.ID.String()}, "billingdelete-"+sub.ID.String()); derr != nil {
			return derr
		}
		return nil
	case sub.InstanceID != nil:
		mcs := &minecraft.Store{Pool: s.Pool}
		inst, err := mcs.GetByIDAny(ctx, *sub.InstanceID)
		if err != nil {
			return err
		}
		// Backup-first: world snapshot before the irreversible delete.
		if _, berr := billingMCEnqueue(ctx, s.Jobs, inst.ServerID, inst.ID, jobs.Type(minecraft.JobMCBackupWorld),
			minecraft.MCBackupPayload{InstanceID: inst.ID.String(), BackupName: "pre-terminate"},
			"billingtermbackup-"+sub.ID.String()); berr != nil {
			slog.Warn("billing: pre-terminate world backup enqueue failed", "instance", inst.ID, "err", berr)
		}
		_ = mcs.MarkDeleting(ctx, inst.ID)
		if _, derr := billingMCEnqueue(ctx, s.Jobs, inst.ServerID, inst.ID, jobs.Type(minecraft.JobMCDelete),
			minecraft.MCIDPayload{InstanceID: inst.ID.String()}, "billingdelete-"+sub.ID.String()); derr != nil {
			return derr
		}
		return nil
	}
	return errors.New("subscription has no workload to terminate")
}

// ---------------------------------------------------------------------------
// handler plumbing
// ---------------------------------------------------------------------------

type billingHandler struct {
	srv        *Server
	svc        *billing.Service
	requireOrg func(r *http.Request, orgIDParam string, min organizations.Role) (uuid.UUID, *httpapi.APIError)
}

func (h *billingHandler) wrapOrg(min organizations.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := httpapi.UserFrom(r.Context()); !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if _, apiErr := h.requireOrg(r, r.PathValue("org_id"), min); apiErr != nil {
			httpapi.RespondError(w, apiErr)
			return
		}
		next(w, r)
	}
}

// requireAdmin gates the WHM billing surface to platform-admin sessions
// (API tokens never inherit platform admin — RBAC v2).
func (h *billingHandler) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, ok := httpapi.UserFrom(r.Context())
		if !ok {
			httpapi.RespondError(w, httpapi.ErrUnauthorized("authentication required"))
			return
		}
		if user.Role != "admin" || httpapi.IsAPIToken(r.Context()) {
			httpapi.RespondError(w, httpapi.ErrForbidden("platform administrator session required"))
			return
		}
		next(w, r)
	}
}

func (h *billingHandler) org(r *http.Request) uuid.UUID {
	id, _ := uuid.Parse(r.PathValue("org_id"))
	return id
}

func (h *billingHandler) actorID(r *http.Request) uuid.UUID {
	user, _ := httpapi.UserFrom(r.Context())
	if user == nil {
		return uuid.Nil
	}
	uid, _ := uuid.Parse(user.ID)
	return uid
}

func (h *billingHandler) audit(r *http.Request, orgID uuid.UUID, action, resourceID string, meta map[string]any) {
	if h.srv.Audit == nil {
		return
	}
	actor := h.actorID(r)
	var orgRef *uuid.UUID
	if orgID != uuid.Nil {
		o := orgID
		orgRef = &o
	}
	h.srv.Audit.RecordBestEffort(r.Context(), audit.Entry{
		OrganizationID: orgRef,
		ActorUserID:    &actor,
		ActorType:      audit.ActorUser,
		Action:         action,
		ResourceType:   "billing",
		ResourceID:     resourceID,
		Metadata:       meta,
	})
}

// ---------------------------------------------------------------------------
// customer handlers
// ---------------------------------------------------------------------------

func subView(sub *billing.Subscription) map[string]any {
	out := map[string]any{
		"id":                   sub.ID,
		"organization_id":      sub.OrgID,
		"product_id":           sub.ProductID,
		"plan_id":              sub.PlanID,
		"product_name":         sub.ProductName,
		"plan_name":            sub.PlanName,
		"status":               sub.Status,
		"provision_state":      string(sub.ProvisionState),
		"billing_period":       sub.BillingPeriod,
		"period_start":         sub.PeriodStart,
		"period_end":           sub.PeriodEnd,
		"cancel_at_period_end": sub.CancelAtPeriodEnd,
		"workload_kind":        sub.WorkloadKind,
		"grace_until":          sub.GraceUntil,
		"last_error":           sub.LastError,
		"provision_attempts":   sub.ProvisionAttempts,
		"created_at":           sub.CreatedAt,
		"updated_at":           sub.UpdatedAt,
	}
	// Renewal status (Phase 5 seam): computed server-side.
	now := time.Now()
	renewal := "ok"
	if sub.CancelAtPeriodEnd {
		renewal = "cancels_at_period_end"
	} else if sub.GraceUntil != nil && sub.ProvisionState == billing.StateActive {
		if now.After(*sub.GraceUntil) {
			renewal = "grace_ended"
		} else {
			renewal = "in_grace"
		}
	} else if !sub.PeriodEnd.After(now) {
		renewal = "renewal_due"
	}
	out["renewal_status"] = renewal
	if sub.WebsiteID != nil {
		out["website_id"] = *sub.WebsiteID
	}
	if sub.BotID != nil {
		out["bot_id"] = *sub.BotID
	}
	if sub.InstanceID != nil {
		out["instance_id"] = *sub.InstanceID
	}
	return out
}

func invoiceView(inv *billing.Invoice) map[string]any {
	return map[string]any{
		"id":              inv.ID,
		"number":          inv.Number,
		"status":          inv.Status,
		"currency":        inv.Currency,
		"subtotal_minor":  inv.SubtotalMinor,
		"tax_minor":       inv.TaxMinor,
		"total_minor":     inv.TotalMinor,
		"line_items":      inv.LineItems,
		"kind":            inv.Kind,
		"subscription_id": inv.SubscriptionID,
		"issued_at":       inv.IssuedAt,
		"due_at":          inv.DueAt,
		"paid_at":         inv.PaidAt,
		"created_at":      inv.CreatedAt,
	}
}

func (h *billingHandler) overview(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	subs, err := h.svc.Store.ListSubscriptionsForOrg(r.Context(), orgID, 100)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	out := make([]map[string]any, 0, len(subs))
	for i := range subs {
		out = append(out, subView(&subs[i]))
	}
	pm, err := h.svc.Store.PaymentMethodRaw(r.Context(), orgID)
	masked := map[string]any{}
	if err == nil && pm != nil {
		masked = map[string]any{"provider": pm.Provider, "brand": pm.Brand, "last4": pm.Last4, "configured": pm.TokenEnc != ""}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"subscriptions":   out,
		"payment_method":  masked,
		"providers":       billing.ProviderNames(),
		"grace_days":      h.svc.SettingInt(r.Context(), "billing.grace_days", billing.DefaultGraceDays),
		"suspend_days":    h.svc.SettingInt(r.Context(), "billing.suspend_terminate_days", billing.DefaultSuspendThenDays),
	})
}

func (h *billingHandler) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, err := h.svc.Store.ListSubscriptionsForOrg(r.Context(), h.org(r), 100)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	out := make([]map[string]any, 0, len(subs))
	for i := range subs {
		out = append(out, subView(&subs[i]))
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

func (h *billingHandler) getSubscription(w http.ResponseWriter, r *http.Request) {
	subID, err := uuid.Parse(r.PathValue("subscription_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid subscription id"))
		return
	}
	sub, err := h.svc.Store.GetSubscription(r.Context(), h.org(r), subID)
	if err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("subscription not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, subView(sub))
}

func (h *billingHandler) cancelSubscription(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	subID, err := uuid.Parse(r.PathValue("subscription_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid subscription id"))
		return
	}
	if err := h.svc.Store.SetSubscriptionCancelFlag(r.Context(), orgID, subID, true); err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("subscription not found"))
		return
	} else if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "billing.subscription_cancel_requested", subID.String(), nil)
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "subscription will terminate at period end"})
}

func (h *billingHandler) listProducts(w http.ResponseWriter, r *http.Request) {
	products, err := h.svc.Store.ListProducts(r.Context(), true)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"products": products})
}

type createOrderRequest struct {
	ProductID     string `json:"product_id"`
	BillingPeriod string `json:"billing_period"`
	Provider      string `json:"provider"`
}

func (h *billingHandler) createOrder(w http.ResponseWriter, r *http.Request) {
	var req createOrderRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	productID, err := uuid.Parse(req.ProductID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid product_id"))
		return
	}
	order, sub, err := h.svc.Checkout(r.Context(), billing.CheckoutInput{
		OrgID: h.org(r), ActorID: h.actorID(r), ProductID: productID,
		Period: req.BillingPeriod, Provider: req.Provider,
	})
	if err != nil {
		if errors.Is(err, billing.ErrNotFound) {
			httpapi.RespondError(w, httpapi.ErrNotFound("product not found"))
			return
		}
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	h.audit(r, h.org(r), "billing.order_created", order.ID.String(), map[string]any{
		"amount_minor": order.AmountMinor, "period": order.BillingPeriod,
	})
	httpapi.WriteJSON(w, http.StatusCreated, map[string]any{"order": order, "subscription": subView(sub)})
}

type payOrderRequest struct {
	MethodRef string `json:"method_ref,omitempty"`
}

// payOrder captures an existing PENDING order (checkout -> pay with the
// fake/manual gateways; real gateways arrive via the webhook instead).
// The provider event id is the replay anchor: paying the same order twice
// is a no-op the second time.
func (h *billingHandler) payOrder(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	orderID, err := uuid.Parse(r.PathValue("order_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid order id"))
		return
	}
	var req payOrderRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	order, err := h.svc.Store.GetOrder(r.Context(), orgID, orderID)
	if err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("order not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if order.Status != billing.OrderPending {
		httpapi.RespondError(w, httpapi.ErrConflict("order is not payable (status "+order.Status+")"))
		return
	}
	if order.SubscriptionID == nil {
		httpapi.RespondError(w, httpapi.ErrInternal(errors.New("order has no subscription")))
		return
	}
	sub, err := h.svc.Store.GetSubscriptionAny(r.Context(), *order.SubscriptionID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	methodRef := req.MethodRef
	if methodRef == "" {
		methodRef = h.storedMethodRef(r.Context(), orgID)
	}
	if methodRef == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("no payment method: send method_ref or save one first"))
		return
	}
	replayed, err := h.svc.CaptureForOrder(r.Context(), order, sub, methodRef, "order:"+order.ID.String(), billing.InvoiceKindPurchase)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrConflict("payment failed: "+err.Error()))
		return
	}
	updated, err := h.svc.Store.GetSubscriptionAny(r.Context(), sub.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "billing.order_paid", order.ID.String(), map[string]any{
		"amount_minor": order.AmountMinor, "replayed": replayed,
	})
	paid, err := h.svc.Store.GetOrder(r.Context(), orgID, order.ID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"order": paid, "subscription": subView(updated), "replayed": replayed,
	})
}

// storedMethodRef decrypts the org's saved instrument token (never logged).
func (h *billingHandler) storedMethodRef(ctx context.Context, orgID uuid.UUID) string {
	pm, err := h.svc.Store.PaymentMethodRaw(ctx, orgID)
	if err != nil || pm == nil || pm.TokenEnc == "" {
		return ""
	}
	raw, derr := base64.StdEncoding.DecodeString(pm.TokenEnc)
	if derr != nil {
		return ""
	}
	plain, err := secretbox.Decrypt(raw)
	if err != nil {
		return ""
	}
	return plain
}

func (h *billingHandler) listOrders(w http.ResponseWriter, r *http.Request) {
	orders, err := h.svc.Store.ListOrdersForOrg(r.Context(), h.org(r), 100)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if orders == nil {
		orders = []billing.Order{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"orders": orders})
}

func (h *billingHandler) listInvoices(w http.ResponseWriter, r *http.Request) {
	invoices, err := h.svc.Store.ListInvoicesForOrg(r.Context(), h.org(r), 100)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if invoices == nil {
		invoices = []billing.Invoice{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"invoices": invoices})
}

func (h *billingHandler) getInvoice(w http.ResponseWriter, r *http.Request) {
	invoiceID, err := uuid.Parse(r.PathValue("invoice_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid invoice id"))
		return
	}
	inv, err := h.svc.Store.GetInvoice(r.Context(), h.org(r), invoiceID)
	if err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("invoice not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, invoiceView(inv))
}

func (h *billingHandler) getPaymentMethod(w http.ResponseWriter, r *http.Request) {
	pm, err := h.svc.Store.PaymentMethodRaw(r.Context(), h.org(r))
	if err != nil && err != billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	out := map[string]any{"configured": false}
	if pm != nil {
		out = map[string]any{
			"configured": pm.TokenEnc != "",
			"provider":   pm.Provider,
			"brand":      pm.Brand,
			"last4":      pm.Last4,
			"note":       "the instrument token is stored encrypted and never returned",
		}
	}
	httpapi.WriteJSON(w, http.StatusOK, out)
}

type putPaymentMethodRequest struct {
	Provider string `json:"provider"`
	Token    string `json:"token"` // gateway instrument token (write-only)
	Brand    string `json:"brand"`
	Last4    string `json:"last4"`
}

func (h *billingHandler) putPaymentMethod(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	var req putPaymentMethodRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	if req.Provider == "" {
		req.Provider = "manual"
	}
	if _, err := billing.ProviderFor(req.Provider); err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation(err.Error()))
		return
	}
	if len(req.Token) > 512 {
		httpapi.RespondError(w, httpapi.ErrValidation("token too long"))
		return
	}
	if _, err := h.svc.Store.EnsureCustomer(r.Context(), orgID, "USD"); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	enc, err := secretbox.Encrypt(req.Token)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.svc.Store.SetPaymentMethod(r.Context(), orgID, billing.PaymentMethod{
		Provider: req.Provider, TokenEnc: base64.StdEncoding.EncodeToString(enc),
		Brand: req.Brand, Last4: req.Last4,
	}); err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// The token NEVER appears in the response or the audit trail.
	h.audit(r, orgID, "billing.payment_method_updated", "", map[string]any{"provider": req.Provider, "brand": req.Brand})
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "payment method stored (encrypted at rest)"})
}

type payInvoiceRequest struct {
	MethodRef string `json:"method_ref,omitempty"`
	Provider  string `json:"provider,omitempty"`
}

// payInvoice settles an OPEN invoice (renewal recovery path: pay -> extend
// -> resume; a suspended workload comes back). Replays are no-ops.
func (h *billingHandler) payInvoice(w http.ResponseWriter, r *http.Request) {
	orgID := h.org(r)
	invoiceID, err := uuid.Parse(r.PathValue("invoice_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid invoice id"))
		return
	}
	var req payInvoiceRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	inv, err := h.svc.PayInvoice(r.Context(), orgID, invoiceID, req.MethodRef, req.Provider, nil)
	if err == billing.ErrInvoiceState {
		httpapi.RespondError(w, httpapi.ErrConflict("invoice is not payable (status not open)"))
		return
	}
	if err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("invoice not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrConflict("payment failed: "+err.Error()))
		return
	}
	h.audit(r, orgID, "billing.invoice_pay_requested", inv.ID.String(), nil)
	httpapi.WriteJSON(w, http.StatusOK, invoiceView(inv))
}

// ---------------------------------------------------------------------------
// admin handlers (WHM)
// ---------------------------------------------------------------------------

func (h *billingHandler) adminPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := h.svc.Store.ListPlans(r.Context())
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	products, err := h.svc.Store.ListProducts(r.Context(), false)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if plans == nil {
		plans = []billing.PlanRow{}
	}
	if products == nil {
		products = []billing.Product{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"plans": plans, "products": products})
}

func (h *billingHandler) adminListProducts(w http.ResponseWriter, r *http.Request) {
	products, err := h.svc.Store.ListProducts(r.Context(), false)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"products": products})
}

type adminProductRequest struct {
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Description string         `json:"description"`
	PlanID      string         `json:"plan_id"`
	PriceMinor  *int64         `json:"price_minor"`
	Currency    string         `json:"currency"`
	Active      *bool          `json:"active"`
	Config      map[string]any `json:"config"`
}

func (h *billingHandler) adminCreateProduct(w http.ResponseWriter, r *http.Request) {
	var req adminProductRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	switch req.Type {
	case "hosting", "minecraft", "discord", "service":
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("type must be hosting, minecraft, discord or service"))
		return
	}
	if req.Name == "" {
		httpapi.RespondError(w, httpapi.ErrValidation("name is required"))
		return
	}
	price := int64(0)
	if req.PriceMinor != nil {
		price = *req.PriceMinor
	}
	if price < 0 {
		httpapi.RespondError(w, httpapi.ErrValidation("price_minor must be >= 0"))
		return
	}
	currency := req.Currency
	if currency == "" {
		currency = "USD"
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	created, err := h.svc.Store.CreateProduct(r.Context(), billing.ProductInput{
		Name: req.Name, Type: req.Type, Description: req.Description,
		PlanID: parseOptUUID(req.PlanID), PriceMinor: price, Currency: currency,
		Active: active, Config: req.Config,
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, uuid.Nil, "billing.product_created", created.ID.String(), map[string]any{"name": created.Name, "type": created.Type})
	httpapi.WriteJSON(w, http.StatusCreated, created)
}

func (h *billingHandler) adminUpdateProduct(w http.ResponseWriter, r *http.Request) {
	productID, err := uuid.Parse(r.PathValue("product_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid product id"))
		return
	}
	current, err := h.svc.Store.GetProduct(r.Context(), productID)
	if err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("product not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	var req adminProductRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	price := current.PriceMinor
	if req.PriceMinor != nil {
		if *req.PriceMinor < 0 {
			httpapi.RespondError(w, httpapi.ErrValidation("price_minor must be >= 0"))
			return
		}
		price = *req.PriceMinor
	}
	currency := current.Currency
	if req.Currency != "" {
		currency = req.Currency
	}
	active := current.Active
	if req.Active != nil {
		active = *req.Active
	}
	desc := current.Description
	if req.Description != "" {
		desc = req.Description
	}
	planID := current.PlanID
	if req.PlanID != "" {
		planID = parseOptUUID(req.PlanID)
	}
	updated, err := h.svc.Store.UpdateProduct(r.Context(), productID, billing.ProductInput{
		Name: current.Name, Type: current.Type, Description: desc, PlanID: planID,
		PriceMinor: price, Currency: currency, Active: active, Config: req.Config,
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, uuid.Nil, "billing.product_updated", productID.String(), nil)
	httpapi.WriteJSON(w, http.StatusOK, updated)
}

func parseOptUUID(s string) *uuid.UUID {
	if s == "" {
		return nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil
	}
	return &id
}

func (h *billingHandler) adminListInvoices(w http.ResponseWriter, r *http.Request) {
	var orgFilter *uuid.UUID
	if v := r.URL.Query().Get("organization_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			httpapi.RespondError(w, httpapi.ErrValidation("invalid organization_id"))
			return
		}
		orgFilter = &id
	}
	invoices, err := h.svc.Store.ListInvoicesAdmin(r.Context(), orgFilter, r.URL.Query().Get("status"), 200)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if invoices == nil {
		invoices = []billing.Invoice{}
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"invoices": invoices})
}

type adminIssueInvoiceRequest struct {
	OrganizationID string `json:"organization_id"`
	Description    string `json:"description"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
}

// adminIssueInvoice issues a manual invoice (operator-recorded charges:
// offline payments, overages). Issued = immutable money fields.
func (h *billingHandler) adminIssueInvoice(w http.ResponseWriter, r *http.Request) {
	var req adminIssueInvoiceRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	orgID, err := uuid.Parse(req.OrganizationID)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid organization_id"))
		return
	}
	if billing.ValidateMinorUnit(req.AmountMinor) != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("amount_minor must be >= 0"))
		return
	}
	currency := req.Currency
	if currency == "" {
		currency = "USD"
	}
	cust, err := h.svc.Store.EnsureCustomer(r.Context(), orgID, currency)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	inv, err := h.svc.Store.IssueInvoice(r.Context(), billing.InvoiceInput{
		CustomerID: cust.ID, OrgID: orgID, Kind: billing.InvoiceKindManual,
		Currency: currency, SubtotalMinor: req.AmountMinor,
		LineItems: []billing.LineItem{{
			Description: req.Description, Quantity: 1,
			UnitMinor: req.AmountMinor, TotalMinor: req.AmountMinor,
		}},
	})
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, orgID, "billing.invoice_issued", inv.ID.String(), map[string]any{"number": inv.Number, "manual": true})
	httpapi.WriteJSON(w, http.StatusCreated, invoiceView(inv))
}

func (h *billingHandler) adminVoidInvoice(w http.ResponseWriter, r *http.Request) {
	invoiceID, err := uuid.Parse(r.PathValue("invoice_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid invoice id"))
		return
	}
	if err := h.svc.Store.VoidInvoice(r.Context(), invoiceID); err == billing.ErrInvoiceState {
		httpapi.RespondError(w, httpapi.ErrConflict("only open invoices can be voided"))
		return
	} else if err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("invoice not found"))
		return
	} else if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, uuid.Nil, "billing.invoice_voided", invoiceID.String(), nil)
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"status": "voided"})
}

func (h *billingHandler) adminGetSettings(w http.ResponseWriter, r *http.Request) {
	get := func(key string, def int) int { return h.svc.SettingInt(r.Context(), key, def) }
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{
		"grace_days":             get("billing.grace_days", billing.DefaultGraceDays),
		"suspend_terminate_days": get("billing.suspend_terminate_days", billing.DefaultSuspendThenDays),
		"invoice_due_days":       get("billing.invoice_due_days", billing.DefaultInvoiceDueDays),
		"webhook_secret_set":     h.svc.WebhookSecret(r.Context()) != "",
		"providers":              billing.ProviderNames(),
	})
}

type adminSettingsRequest struct {
	GraceDays            *int   `json:"grace_days"`
	SuspendTerminateDays *int   `json:"suspend_terminate_days"`
	InvoiceDueDays       *int   `json:"invoice_due_days"`
	WebhookSecret        string `json:"webhook_secret"`
}

func (h *billingHandler) adminPatchSettings(w http.ResponseWriter, r *http.Request) {
	var req adminSettingsRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	set := func(key string, v *int) {
		if v == nil || *v < 0 {
			return
		}
		_ = setBillingSetting(r.Context(), h.srv.Pool, key, strconv.Itoa(*v))
	}
	set("billing.grace_days", req.GraceDays)
	set("billing.suspend_terminate_days", req.SuspendTerminateDays)
	set("billing.invoice_due_days", req.InvoiceDueDays)
	if req.WebhookSecret != "" {
		_ = setBillingSetting(r.Context(), h.srv.Pool, "billing.webhook_secret", req.WebhookSecret)
	}
	h.audit(r, uuid.Nil, "billing.settings_updated", "", nil)
	h.adminGetSettings(w, r)
}

func (h *billingHandler) adminListSubscriptions(w http.ResponseWriter, r *http.Request) {
	subs, err := h.svc.Store.ListSubscriptionsAdmin(r.Context(), r.URL.Query().Get("state"), 200)
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	out := make([]map[string]any, 0, len(subs))
	for i := range subs {
		out = append(out, subView(&subs[i]))
	}
	httpapi.WriteJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
}

type adminRetryRequest struct {
	Action string `json:"action,omitempty"` // provision | suspend | terminate
}

// adminRetrySubscription re-drives a FAILED subscription (artifacts are
// retained exactly so this is possible).
func (h *billingHandler) adminRetrySubscription(w http.ResponseWriter, r *http.Request) {
	subID, err := uuid.Parse(r.PathValue("subscription_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid subscription id"))
		return
	}
	var req adminRetryRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	sub, err := h.svc.Store.GetSubscriptionAny(r.Context(), subID)
	if err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("subscription not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	// Provisions are the default retry; suspend/terminate retries are
	// explicit admin intent.
	switch req.Action {
	case "", "provision":
	case "suspend", "terminate":
		// allowed below via ManualRetry
	default:
		httpapi.RespondError(w, httpapi.ErrValidation("action must be provision, suspend or terminate"))
		return
	}
	if req.Action == "suspend" {
		if err := h.svc.BeginSuspend(r.Context(), sub, "admin retry of suspend"); err != nil {
			httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
			return
		}
	} else if req.Action == "terminate" {
		if err := h.svc.BeginTerminate(r.Context(), sub, "admin retry of terminate"); err != nil {
			httpapi.RespondError(w, httpapi.ErrConflict(err.Error()))
			return
		}
	} else if err := h.svc.ManualRetry(r.Context(), sub, nil); err != nil {
		var terr *billing.TransitionError
		if errors.As(err, &terr) {
			httpapi.RespondError(w, httpapi.ErrConflict(terr.Error()))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, uuid.Nil, "billing.subscription_retry", subID.String(), map[string]any{"action": req.Action})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "retry queued"})
}

type adminTerminateRequest struct {
	Reason string `json:"reason"`
}

func (h *billingHandler) adminTerminateSubscription(w http.ResponseWriter, r *http.Request) {
	subID, err := uuid.Parse(r.PathValue("subscription_id"))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("invalid subscription id"))
		return
	}
	var req adminTerminateRequest
	if apiErr := httpapi.Read(r, &req); apiErr != nil {
		httpapi.RespondError(w, apiErr)
		return
	}
	sub, err := h.svc.Store.GetSubscriptionAny(r.Context(), subID)
	if err == billing.ErrNotFound {
		httpapi.RespondError(w, httpapi.ErrNotFound("subscription not found"))
		return
	}
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	if err := h.svc.BeginTerminate(r.Context(), sub, "admin terminate: "+req.Reason); err != nil {
		var terr *billing.TransitionError
		if errors.As(err, &terr) {
			httpapi.RespondError(w, httpapi.ErrConflict(terr.Error()))
			return
		}
		httpapi.RespondError(w, httpapi.ErrInternal(err))
		return
	}
	h.audit(r, uuid.Nil, "billing.subscription_terminate_requested", subID.String(), map[string]any{"reason": req.Reason})
	httpapi.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "termination queued (backup-first)"})
}

// ---------------------------------------------------------------------------
// webhook (public route; signature IS the authentication)
// ---------------------------------------------------------------------------

func (h *billingHandler) webhook(w http.ResponseWriter, r *http.Request) {
	providerName := r.PathValue("provider")
	secret := h.svc.WebhookSecret(r.Context())
	if secret == "" {
		httpapi.RespondError(w, httpapi.ErrForbidden("billing webhooks are not configured"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpapi.RespondError(w, httpapi.ErrValidation("unreadable body"))
		return
	}
	ev, replayed, err := h.svc.HandleWebhook(r.Context(), providerName, secret, r.Header, body)
	if err != nil {
		// Unverified or malformed: reject. Audit the rejection (no body
		// contents logged — payloads can carry PII).
		slog.Warn("billing webhook rejected", "provider", providerName, "err", err)
		h.audit(r, uuid.Nil, "billing.webhook_rejected", providerName, nil)
		httpapi.RespondError(w, httpapi.ErrUnauthorized("webhook verification failed"))
		return
	}
	status := http.StatusOK
	if replayed {
		status = http.StatusOK // replays answer OK + replayed:true, no side effects
	}
	httpapi.WriteJSON(w, status, map[string]any{
		"received": true, "replayed": replayed, "event_id": ev.EventID, "type": ev.Type,
	})
}
