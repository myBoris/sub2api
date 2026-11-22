-- The monthly display date may roll forward without a payment. Keep the
-- actual API access deadline independently so a new month never restores an
-- expired account. Existing displayed dates retain their current meaning.
CREATE TABLE IF NOT EXISTS custom_account_billing_access (
    account_id BIGINT PRIMARY KEY REFERENCES custom_account_billing(account_id) ON DELETE CASCADE,
    valid_through DATE,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO custom_account_billing_access(account_id,valid_through)
SELECT account_id,renewal_date FROM custom_account_billing
ON CONFLICT(account_id) DO NOTHING;
