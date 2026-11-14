-- FeiraNexus initial schema (PostgreSQL 17)
-- UUID primary keys via gen_random_uuid() (pgcrypto built-in on PG13+).

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ------------------------------------------------------------------
-- users
-- ------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS users (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email              TEXT NOT NULL UNIQUE,
    password_hash      TEXT NOT NULL,
    role               TEXT NOT NULL DEFAULT 'customer'
                       CHECK (role IN ('customer', 'admin')),
    stripe_customer_id TEXT,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_users_email ON users (email);
CREATE INDEX IF NOT EXISTS idx_users_stripe_customer_id ON users (stripe_customer_id);

-- ------------------------------------------------------------------
-- sessions (refresh token storage; only SHA-256 digest is persisted)
-- ------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS sessions (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id            UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    refresh_token_hash TEXT NOT NULL UNIQUE,
    user_agent         TEXT,
    client_ip          TEXT,
    is_revoked         BOOLEAN NOT NULL DEFAULT false,
    expires_at         TIMESTAMPTZ NOT NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_sessions_user_id ON sessions (user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires_at ON sessions (expires_at);

-- ------------------------------------------------------------------
-- products
-- ------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS products (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name           TEXT NOT NULL,
    description    TEXT,
    price_cents    BIGINT NOT NULL CHECK (price_cents > 0),
    currency       VARCHAR(3) NOT NULL DEFAULT 'usd',
    stock_quantity INT NOT NULL CHECK (stock_quantity >= 0),
    is_active      BOOLEAN NOT NULL DEFAULT true,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_products_is_active ON products (is_active);

-- ------------------------------------------------------------------
-- orders
-- ------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS orders (
    id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id                 UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status                  TEXT NOT NULL DEFAULT 'pending'
                            CHECK (status IN ('pending', 'paid', 'failed', 'canceled')),
    total_cents             BIGINT NOT NULL,
    currency                VARCHAR(3) NOT NULL DEFAULT 'usd',
    stripe_payment_intent_id TEXT UNIQUE,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_orders_user_id ON orders (user_id);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders (status);

-- ------------------------------------------------------------------
-- order_items (unit price snapshotted at checkout)
-- ------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS order_items (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    order_id         UUID NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
    product_id       UUID NOT NULL REFERENCES products (id),
    quantity         INT NOT NULL CHECK (quantity > 0),
    unit_price_cents BIGINT NOT NULL CHECK (unit_price_cents > 0)
);

CREATE INDEX IF NOT EXISTS idx_order_items_order_id ON order_items (order_id);
CREATE INDEX IF NOT EXISTS idx_order_items_product_id ON order_items (product_id);
