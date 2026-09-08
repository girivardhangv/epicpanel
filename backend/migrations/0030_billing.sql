-- Phase 10: Billing & Provisioning.
--
-- Entities from Phase 2 (products, customers, subscriptions, invoices) gain
-- the billing business columns; orders and payments are new. Money is ALWAYS
-- integer minor units + ISO-4217-ish currency code ([A-Z]{3}).
--
-- The subscription state machine is the verbatim master-doc machine:
--   PENDING -> PROVISIONING -> ACTIVE -> SUSPENDING -> SUSPENDED
--           -> TERMINATING -> TERMINATED -> FAILED
-- carried in subscriptions.provision_state (the legacy Phase 2 `status`
-- column stays in sync as a compatibility projection).
--
-- Every statement is re-runnable (IF NOT EXISTS / DO blocks): test harnesses
-- drop schema_migrations and re-apply the whole chain.

-- 1. Orders (Order -> Payment -> Invoice -> Provision -> Active).
CREATE TABLE IF NOT EXISTS billing_orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    subscription_id UUID REFERENCES subscriptions (id) ON DELETE SET NULL,
    product_id UUID NOT NULL REFERENCES products (id) ON DELETE RESTRICT,
    plan_id UUID REFERENCES hosting_packages (id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'paid', 'cancelled', 'failed')),
    billing_period TEXT NOT NULL DEFAULT 'monthly'
        CHECK (billing_period IN ('monthly', 'quarterly', 'yearly')),
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    amount_minor BIGINT NOT NULL DEFAULT 0 CHECK (amount_minor >= 0),
    provider TEXT NOT NULL DEFAULT 'manual',
    provider_ref TEXT NOT NULL DEFAULT '',
    paid_at TIMESTAMPTZ,
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_billing_orders_org ON billing_orders (organization_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_billing_orders_subscription ON billing_orders (subscription_id);

-- 2. Payments. provider_event_id is the gateway's idempotency key: a webhook
-- replay collapses onto the existing row (UNIQUE partial index) and the
-- billing service never double-captures or double-provisions.
CREATE TABLE IF NOT EXISTS billing_payments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL REFERENCES organizations (id) ON DELETE CASCADE,
    order_id UUID REFERENCES billing_orders (id) ON DELETE SET NULL,
    invoice_id UUID REFERENCES invoices (id) ON DELETE SET NULL,
    subscription_id UUID REFERENCES subscriptions (id) ON DELETE SET NULL,
    provider TEXT NOT NULL,
    provider_ref TEXT NOT NULL DEFAULT '',
    provider_event_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT 'purchase'
        CHECK (kind IN ('purchase', 'renewal', 'manual')),
    status TEXT NOT NULL DEFAULT 'authorized'
        CHECK (status IN ('authorized', 'captured', 'failed', 'refunded')),
    amount_minor BIGINT NOT NULL DEFAULT 0 CHECK (amount_minor >= 0),
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    failure_reason TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_billing_payments_org ON billing_payments (organization_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_billing_payments_order ON billing_payments (order_id);
-- Replay guard: at most one payment per provider event.
CREATE UNIQUE INDEX IF NOT EXISTS uq_billing_payments_provider_event
    ON billing_payments (provider, provider_event_id)
    WHERE provider_event_id <> '';

-- 3. Subscriptions: the verbatim provisioning state machine + workload refs.
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS provision_state TEXT NOT NULL DEFAULT 'PENDING'
    CHECK (provision_state IN
        ('PENDING','PROVISIONING','ACTIVE','SUSPENDING','SUSPENDED','TERMINATING','TERMINATED','FAILED'));
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS state_changed_at TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS workload_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS website_id UUID REFERENCES websites (id) ON DELETE SET NULL;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS bot_id UUID REFERENCES bot_instances (id) ON DELETE SET NULL;
-- Minecraft instances live in 0028 (P7); a plain UUID keeps this migration
-- independent of when that table lands.
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS instance_id UUID;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS grace_until TIMESTAMPTZ;
-- The period_end whose renewal was last invoiced/attempted (the renewal
-- idempotency anchor; pure period boundaries, never wall-clock).
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS last_renewal_period_end TIMESTAMPTZ;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS last_invoice_id UUID REFERENCES invoices (id) ON DELETE SET NULL;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS last_job_id UUID REFERENCES jobs (id) ON DELETE SET NULL;
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE subscriptions ADD COLUMN IF NOT EXISTS provision_attempts INT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_subscriptions_state ON subscriptions (provision_state, period_end);

-- 4. Products: plan link (plans <-> products) + explicit currency.
ALTER TABLE products ADD COLUMN IF NOT EXISTS plan_id UUID REFERENCES hosting_packages (id) ON DELETE SET NULL;
ALTER TABLE products ADD COLUMN IF NOT EXISTS currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$');
CREATE INDEX IF NOT EXISTS idx_products_plan ON products (plan_id);

-- 5. Invoices: subscription link + immutability once issued.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS subscription_id UUID REFERENCES subscriptions (id) ON DELETE SET NULL;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'purchase'
    CHECK (kind IN ('purchase', 'renewal', 'manual'));

CREATE OR REPLACE FUNCTION billing_invoice_no_mutate() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'invoices are immutable once issued (id %)', OLD.id;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS invoices_immutable_once_issued ON invoices;
CREATE TRIGGER invoices_immutable_once_issued
    BEFORE UPDATE ON invoices
    FOR EACH ROW
    WHEN (OLD.issued_at IS NOT NULL AND (
        NEW.line_items   IS DISTINCT FROM OLD.line_items OR
        NEW.subtotal_cents IS DISTINCT FROM OLD.subtotal_cents OR
        NEW.tax_cents    IS DISTINCT FROM OLD.tax_cents OR
        NEW.total_cents  IS DISTINCT FROM OLD.total_cents OR
        NEW.currency     IS DISTINCT FROM OLD.currency OR
        NEW.customer_id  IS DISTINCT FROM OLD.customer_id))
    EXECUTE FUNCTION billing_invoice_no_mutate();

-- Invoice numbers: INV-<year>-<seq>, allocated from a DB sequence.
CREATE SEQUENCE IF NOT EXISTS billing_invoice_number_seq START 1;

-- 6. Customers: payment method on file (token is stored as secretbox
-- ciphertext; display fields are brand/last4 only).
ALTER TABLE customers ADD COLUMN IF NOT EXISTS payment_method JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 7. Job types the billing dispatch path may enqueue for Minecraft products
-- (Phase 7 contract). Guarded: enum values are additive and idempotent.
DO $$ BEGIN
    ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_install';
EXCEPTION WHEN duplicate_object THEN NULL; WHEN undefined_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_stop';
EXCEPTION WHEN duplicate_object THEN NULL; WHEN undefined_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_start';
EXCEPTION WHEN duplicate_object THEN NULL; WHEN undefined_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_delete';
EXCEPTION WHEN duplicate_object THEN NULL; WHEN undefined_object THEN NULL; END $$;
DO $$ BEGIN
    ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'mc_backup_world';
EXCEPTION WHEN duplicate_object THEN NULL; WHEN undefined_object THEN NULL; END $$;
