-- Isolated extension: do not add columns, triggers, or constraints to upstream tables.
CREATE TABLE IF NOT EXISTS custom_user_groups (
    group_id BIGINT PRIMARY KEY REFERENCES groups(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_custom_user_groups_user_id ON custom_user_groups(user_id, group_id);

CREATE TABLE IF NOT EXISTS custom_user_accounts (
    account_id BIGINT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_custom_user_accounts_user_id ON custom_user_accounts(user_id, account_id);

-- Existing resources stay under administrator control. Do not infer an owner
-- from allowed_groups, subscriptions, account_groups, or past API-key usage.
