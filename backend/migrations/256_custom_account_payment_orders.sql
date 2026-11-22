-- Immutable account-billing snapshots linked to the existing payment gateway.
-- This extension does not change any upstream table, column or constraint.
CREATE TABLE IF NOT EXISTS custom_account_payment_orders (
    order_id BIGINT PRIMARY KEY REFERENCES payment_orders(id) ON DELETE RESTRICT,
    user_id BIGINT NOT NULL,
    billing_month VARCHAR(7) NOT NULL CHECK (billing_month ~ '^[0-9]{4}-[0-9]{2}$'),
    quote_token VARCHAR(64) NOT NULL,
    amount_cents BIGINT NOT NULL CHECK (amount_cents > 0),
    account_ids BIGINT[] NOT NULL CHECK (cardinality(account_ids) > 0),
    snapshot JSONB NOT NULL CHECK (jsonb_typeof(snapshot) = 'array'),
    fulfilled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_custom_account_payment_orders_user_month
    ON custom_account_payment_orders(user_id, billing_month);
CREATE INDEX IF NOT EXISTS idx_custom_account_payment_orders_accounts
    ON custom_account_payment_orders USING GIN(account_ids);
