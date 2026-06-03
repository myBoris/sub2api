-- Add current balance buckets and group balance tier.
-- total_recharged/total_gifted are cumulative; paid_balance/gift_balance track what is currently available.

ALTER TABLE users
    ADD COLUMN IF NOT EXISTS paid_balance DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS gift_balance DECIMAL(20,8) NOT NULL DEFAULT 0;

-- Existing mixed balances cannot be reconstructed perfectly. Treat remaining balance as paid up to
-- total_recharged, then the rest as gift so plus groups stay available to users with historical paid top-ups.
UPDATE users
SET
    paid_balance = GREATEST(LEAST(COALESCE(total_recharged, 0), COALESCE(balance, 0)), 0),
    gift_balance = GREATEST(COALESCE(balance, 0) - GREATEST(LEAST(COALESCE(total_recharged, 0), COALESCE(balance, 0)), 0), 0)
WHERE COALESCE(balance, 0) > 0
  AND COALESCE(paid_balance, 0) = 0
  AND COALESCE(gift_balance, 0) = 0;

ALTER TABLE groups
    ADD COLUMN IF NOT EXISTS balance_tier VARCHAR(20) NOT NULL DEFAULT 'free';

UPDATE groups
SET balance_tier = 'free'
WHERE balance_tier IS NULL OR balance_tier NOT IN ('free', 'plus');
