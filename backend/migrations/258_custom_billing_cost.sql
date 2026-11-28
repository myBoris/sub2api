-- Costs belong to the isolated billing extension and use exact CNY cents.
-- Existing costs were never recorded: preserve history and initialize them to
-- zero instead of estimating them from selling prices or today's plan prices.
ALTER TABLE custom_billing_plans
    ADD COLUMN cost_cents BIGINT NOT NULL DEFAULT 0
    CHECK (cost_cents BETWEEN 0 AND 999999999999);

ALTER TABLE custom_account_billing
    ADD COLUMN cost_cents BIGINT NOT NULL DEFAULT 0
    CHECK (cost_cents BETWEEN 0 AND 999999999999);
