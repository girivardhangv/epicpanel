-- Phase 2: Core platform entities from the master-doc list.
-- Mapping (decided once, documented in EPICPANEL.md):
--   Node = servers table, Account = websites table, Plan = hosting_packages
--   table (existing); this migration adds the genuinely missing entities.

CREATE TABLE customers (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id UUID NOT NULL UNIQUE REFERENCES organizations (id) ON DELETE CASCADE,
    reseller_id UUID REFERENCES users (id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'closed')),
    balance_cents BIGINT NOT NULL DEFAULT 0,
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    notes TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_customers_reseller ON customers (reseller_id);

CREATE TABLE products (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('hosting', 'minecraft', 'discord', 'service')),
    description TEXT NOT NULL DEFAULT '',
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    price_cents BIGINT NOT NULL DEFAULT 0 CHECK (price_cents >= 0),
    active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (name, type)
);

CREATE TABLE services (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
    product_id UUID REFERENCES products (id) ON DELETE SET NULL,
    server_id UUID REFERENCES servers (id) ON DELETE SET NULL,
    website_id UUID REFERENCES websites (id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'provisioning', 'active', 'suspended', 'cancelled')),
    suspend_reason TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_services_customer ON services (customer_id, status);

CREATE TABLE subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
    plan_id UUID REFERENCES hosting_packages (id) ON DELETE SET NULL,
    product_id UUID REFERENCES products (id) ON DELETE SET NULL,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('trialing', 'active', 'past_due', 'cancelled')),
    billing_period TEXT NOT NULL DEFAULT 'monthly' CHECK (billing_period IN ('monthly', 'quarterly', 'yearly')),
    period_start TIMESTAMPTZ NOT NULL DEFAULT now(),
    period_end TIMESTAMPTZ NOT NULL DEFAULT now() + interval '1 month',
    cancel_at_period_end BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_subscriptions_customer ON subscriptions (customer_id, status);

CREATE TABLE invoices (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
    number TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'paid', 'void', 'refunded')),
    currency TEXT NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    subtotal_cents BIGINT NOT NULL DEFAULT 0,
    tax_cents BIGINT NOT NULL DEFAULT 0 CHECK (tax_cents >= 0),
    total_cents BIGINT NOT NULL DEFAULT 0 CHECK (total_cents >= 0),
    line_items JSONB NOT NULL DEFAULT '[]'::jsonb,
    issued_at TIMESTAMPTZ,
    due_at TIMESTAMPTZ,
    paid_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_invoices_customer ON invoices (customer_id, status, created_at DESC);

CREATE TABLE resources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL REFERENCES customers (id) ON DELETE CASCADE,
    type TEXT NOT NULL CHECK (type IN ('disk_mb', 'bandwidth_gb', 'websites', 'databases', 'domains', 'cpu_percent', 'memory_bytes')),
    used NUMERIC NOT NULL DEFAULT 0 CHECK (used >= 0),
    allowed NUMERIC NOT NULL DEFAULT 0 CHECK (allowed >= 0),
    unit TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (customer_id, type)
);

-- Durable event log backing the event bus / WS stream (Phase 3 consumes).
CREATE TABLE events (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type TEXT NOT NULL,
    organization_id UUID REFERENCES organizations (id) ON DELETE CASCADE,
    actor_type TEXT NOT NULL DEFAULT 'system',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    payload JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_events_org_created ON events (organization_id, created_at DESC);
