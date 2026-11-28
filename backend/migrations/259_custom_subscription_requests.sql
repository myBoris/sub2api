-- Subscription requests are manual tasks with immutable completed snapshots.
-- All schema changes stay inside this isolated extension table.
CREATE TABLE custom_subscription_requests (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    plan_id BIGINT NOT NULL REFERENCES custom_billing_plans(id) ON DELETE RESTRICT,
    platform VARCHAR(100) NOT NULL,
    plan_name VARCHAR(100) NOT NULL,
    price_cents BIGINT NOT NULL CHECK (price_cents BETWEEN 0 AND 999999999999),
    cost_cents BIGINT NOT NULL CHECK (cost_cents BETWEEN 0 AND 999999999999),
    status VARCHAR(20) NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'completed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    completed_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    CHECK ((status = 'pending' AND completed_at IS NULL AND completed_by IS NULL)
        OR (status = 'completed' AND completed_at IS NOT NULL))
);

CREATE UNIQUE INDEX idx_custom_subscription_requests_pending_user
    ON custom_subscription_requests(user_id) WHERE status = 'pending';
CREATE INDEX idx_custom_subscription_requests_status_created
    ON custom_subscription_requests(status, created_at DESC, id DESC);
CREATE INDEX idx_custom_subscription_requests_user_created
    ON custom_subscription_requests(user_id, created_at DESC, id DESC);
