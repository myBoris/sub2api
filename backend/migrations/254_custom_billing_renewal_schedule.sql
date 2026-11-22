-- Keep the original billing renewal day separately so a short month does not
-- turn January 30 -> February 28 -> March 28. Existing tables are unchanged.
CREATE TABLE IF NOT EXISTS custom_billing_renewal_schedule (
    account_id BIGINT PRIMARY KEY REFERENCES custom_account_billing(account_id) ON DELETE CASCADE,
    anchor_date DATE NOT NULL
);

-- Preserve the exact meaning of dates already configured before this feature.
-- The service advances their displayed renewal month using its business timezone.
INSERT INTO custom_billing_renewal_schedule(account_id, anchor_date)
SELECT account_id, renewal_date
FROM custom_account_billing
WHERE renewal_date IS NOT NULL
ON CONFLICT(account_id) DO NOTHING;
