-- Remove the retired collection images and payment-page visibility settings.
-- Account prices, payment history and renewal schedules are retained.
DROP TABLE IF EXISTS custom_billing_user_access;
DROP TABLE IF EXISTS custom_billing_settings;
