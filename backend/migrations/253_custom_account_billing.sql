-- Subscription collection is an isolated extension of the upstream account data.
-- Amounts are stored as integer CNY cents to prevent floating point rounding.
CREATE TABLE IF NOT EXISTS custom_billing_plans (
    id BIGSERIAL PRIMARY KEY,
    platform VARCHAR(100) NOT NULL CHECK (length(trim(platform)) > 0),
    name VARCHAR(100) NOT NULL CHECK (length(trim(name)) > 0),
    price_cents BIGINT NOT NULL CHECK (price_cents BETWEEN 0 AND 999999999999),
    UNIQUE (platform, name)
);

CREATE TABLE IF NOT EXISTS custom_billing_payers (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL UNIQUE CHECK (length(trim(name)) > 0)
);

CREATE TABLE IF NOT EXISTS custom_account_billing (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    renewal_date DATE,
    plan_id BIGINT REFERENCES custom_billing_plans(id) ON DELETE RESTRICT,
    price_cents BIGINT NOT NULL DEFAULT 0 CHECK (price_cents BETWEEN 0 AND 999999999999),
    payer_id BIGINT REFERENCES custom_billing_payers(id) ON DELETE RESTRICT,
    history JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(history) = 'array'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_custom_account_billing_payer ON custom_account_billing(payer_id);
CREATE INDEX IF NOT EXISTS idx_custom_account_billing_renewal ON custom_account_billing(renewal_date);

-- Images are deliberately kept in the database so backups and container restarts
-- preserve them without exposing an unauthenticated static upload directory.
CREATE TABLE IF NOT EXISTS custom_billing_settings (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    wechat_image TEXT NOT NULL DEFAULT '',
    alipay_image TEXT NOT NULL DEFAULT '',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
INSERT INTO custom_billing_settings (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS custom_billing_user_access (
    user_id BIGINT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE
);
