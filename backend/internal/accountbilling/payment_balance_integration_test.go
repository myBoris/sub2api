package accountbilling

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func balancePaymentTestDatabase(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	dsn := os.Getenv("ACCOUNT_BILLING_TEST_DSN")
	if dsn == "" {
		t.Skip("ACCOUNT_BILLING_TEST_DSN is not set")
	}
	adminDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminDB.Close() })
	schema := fmt.Sprintf("account_balance_test_%d", time.Now().UnixNano())
	_, err = adminDB.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := adminDB.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		require.NoError(t, cleanupErr)
	})
	db, err := sql.Open("postgres", dsn+" search_path="+schema)
	require.NoError(t, err)
	db.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE users(id BIGINT PRIMARY KEY,status TEXT NOT NULL,role TEXT NOT NULL,deleted_at TIMESTAMPTZ,
		balance NUMERIC(20,8) NOT NULL DEFAULT 0,total_recharged NUMERIC(20,8) NOT NULL DEFAULT 0,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
		CREATE TABLE redeem_codes(id BIGSERIAL PRIMARY KEY,code VARCHAR(32) NOT NULL UNIQUE,type VARCHAR(20) NOT NULL,
		value NUMERIC(20,8) NOT NULL,status VARCHAR(20) NOT NULL,used_by BIGINT REFERENCES users(id),used_at TIMESTAMPTZ,notes TEXT);
		CREATE TABLE accounts(id BIGINT PRIMARY KEY,name TEXT NOT NULL,platform TEXT NOT NULL,expires_at TIMESTAMPTZ,deleted_at TIMESTAMPTZ);
		CREATE TABLE custom_user_accounts(account_id BIGINT PRIMARY KEY REFERENCES accounts(id),user_id BIGINT NOT NULL REFERENCES users(id));
		CREATE TABLE payment_orders(id BIGINT PRIMARY KEY,user_id BIGINT NOT NULL,order_type TEXT NOT NULL,status TEXT NOT NULL,
		amount NUMERIC(20,2) NOT NULL,paid_at TIMESTAMPTZ,expires_at TIMESTAMPTZ NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());`)
	require.NoError(t, err)
	for _, name := range []string{"253_custom_account_billing.sql", "254_custom_billing_renewal_schedule.sql", "256_custom_account_payment_orders.sql", "257_custom_account_billing_access.sql", "258_custom_billing_cost.sql"} {
		migration, readErr := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		require.NoError(t, readErr)
		_, err = db.Exec(string(migration))
		require.NoError(t, err)
	}
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTimeRaw("2026-11-22") }
	return db, store
}

func seedBalancePayment(t *testing.T, db *sql.DB, userID, orderID int64, balance string) {
	t.Helper()
	account := paymentTestAccount(orderID, 1000, "unpaid")
	history, err := json.Marshal(account.History)
	require.NoError(t, err)
	quote, snapshots, err := buildPaymentQuote(userID, "2026-11", []Account{account}, map[int64]string{orderID: "2026-11-30"})
	require.NoError(t, err)
	raw, err := json.Marshal(snapshots)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO users(id,status,role,balance,total_recharged) VALUES($1,'active','user',$2,25.125) ON CONFLICT(id) DO NOTHING`, userID, balance)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO accounts(id,name,platform) VALUES($1,'Owned account','openai')`, orderID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO custom_user_accounts(account_id,user_id) VALUES($1,$2)`, orderID, userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO custom_account_billing(account_id,renewal_date,price_cents,history,created_at) VALUES($1,'2026-11-30',1000,$2::jsonb,'2026-11-01')`, orderID, string(history))
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO custom_billing_renewal_schedule(account_id,anchor_date) VALUES($1,'2026-11-30')`, orderID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO payment_orders(id,user_id,order_type,status,amount,paid_at,expires_at) VALUES($1,$2,'account','PAID',10,NOW(),NOW()+INTERVAL '1 hour')`, orderID, userID)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO custom_account_payment_orders(order_id,user_id,billing_month,quote_token,amount_cents,account_ids,snapshot) VALUES($1,$2,'2026-11',$3,1000,$4,$5::jsonb)`, orderID, userID, quote.QuoteToken, pq.Array([]int64{orderID}), string(raw))
	require.NoError(t, err)
}

func TestAccountPaymentBalancePostgresBoundariesAndReplay(t *testing.T) {
	db, store := balancePaymentTestDatabase(t)
	for i, initial := range []string{"-12.34567890", "0", "123.45678901", "9999", "12000"} {
		t.Run(initial, func(t *testing.T) {
			id := int64(100 + i)
			seedBalancePayment(t, db, id, id, initial)
			require.NoError(t, store.FulfillPaymentOrder(context.Background(), id, id))
			before := decimal.RequireFromString(initial)
			after := decimal.Max(before, decimal.NewFromInt(9999))
			delta := after.Sub(before)
			var balance, total, renewal, status string
			var fulfilled bool
			require.NoError(t, db.QueryRow(`SELECT balance::text,total_recharged::text FROM users WHERE id=$1`, id).Scan(&balance, &total))
			require.Equal(t, after.StringFixed(8), balance)
			require.Equal(t, delta.Add(decimal.RequireFromString("25.125")).StringFixed(8), total)
			require.NoError(t, db.QueryRow(`SELECT renewal_date::text,history->0->>'status' FROM custom_account_billing WHERE account_id=$1`, id).Scan(&renewal, &status))
			require.Equal(t, "2026-12-30", renewal)
			require.Equal(t, "paid", status)
			require.NoError(t, db.QueryRow(`SELECT fulfilled_at IS NOT NULL FROM custom_account_payment_orders WHERE order_id=$1`, id).Scan(&fulfilled))
			require.True(t, fulfilled)
			var count int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM redeem_codes WHERE used_by=$1`, id).Scan(&count))
			if delta.IsPositive() {
				require.Equal(t, 1, count)
				var credited, codeType, codeStatus, notes string
				var usedAt bool
				require.NoError(t, db.QueryRow(`SELECT value::text,type,status,notes,used_at IS NOT NULL FROM redeem_codes WHERE used_by=$1`, id).Scan(&credited, &codeType, &codeStatus, &notes, &usedAt))
				require.Equal(t, delta.StringFixed(8), credited)
				require.Equal(t, "balance", codeType)
				require.Equal(t, "used", codeStatus)
				require.Contains(t, notes, strconv.FormatInt(id, 10))
				require.True(t, usedAt)
			} else {
				require.Zero(t, count)
			}
			_, err := db.Exec(`UPDATE users SET balance=balance-1.25 WHERE id=$1`, id)
			require.NoError(t, err)
			require.NoError(t, store.FulfillPaymentOrder(context.Background(), id, id))
			require.NoError(t, db.QueryRow(`SELECT balance::text,total_recharged::text FROM users WHERE id=$1`, id).Scan(&balance, &total))
			require.Equal(t, after.Sub(decimal.RequireFromString("1.25")).StringFixed(8), balance, "replaying an already fulfilled order cannot refill subsequent consumption")
			require.Equal(t, delta.Add(decimal.RequireFromString("25.125")).StringFixed(8), total)
		})
	}
}

func TestAccountPaymentBalancePostgresRollsBackAuditAndMarkerFailures(t *testing.T) {
	db, store := balancePaymentTestDatabase(t)
	for i, failure := range []string{"audit collision", "fulfillment marker failure"} {
		t.Run(failure, func(t *testing.T) {
			id := int64(200 + i)
			seedBalancePayment(t, db, id, id, "100")
			code := "ACCOUNT-PAYMENT-" + strconv.FormatInt(id, 36)
			if i == 0 {
				_, err := db.Exec(`INSERT INTO redeem_codes(code,type,value,status,used_by) VALUES($1,'balance',1,'used',$2)`, code, id)
				require.NoError(t, err)
			} else {
				_, err := db.Exec(`CREATE FUNCTION fail_balance_marker() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected marker failure'; END $$;
					CREATE TRIGGER fail_balance_marker BEFORE UPDATE ON custom_account_payment_orders FOR EACH ROW EXECUTE FUNCTION fail_balance_marker();`)
				require.NoError(t, err)
			}
			require.Error(t, store.FulfillPaymentOrder(context.Background(), id, id))
			var balance, total, renewal, status string
			var fulfilled bool
			require.NoError(t, db.QueryRow(`SELECT balance::text,total_recharged::text FROM users WHERE id=$1`, id).Scan(&balance, &total))
			require.Equal(t, "100.00000000", balance)
			require.Equal(t, "25.12500000", total)
			require.NoError(t, db.QueryRow(`SELECT renewal_date::text,history->0->>'status' FROM custom_account_billing WHERE account_id=$1`, id).Scan(&renewal, &status))
			require.Equal(t, "2026-11-30", renewal)
			require.Equal(t, "unpaid", status)
			require.NoError(t, db.QueryRow(`SELECT fulfilled_at IS NOT NULL FROM custom_account_payment_orders WHERE order_id=$1`, id).Scan(&fulfilled))
			require.False(t, fulfilled)
			var audits int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM redeem_codes WHERE used_by=$1`, id).Scan(&audits))
			if i == 0 {
				require.Equal(t, 1, audits, "only the colliding record remains")
				_, err := db.Exec(`DELETE FROM redeem_codes WHERE code=$1`, code)
				require.NoError(t, err)
			} else {
				require.Zero(t, audits, "the newly inserted recharge record rolls back with the marker")
				_, err := db.Exec(`DROP TRIGGER fail_balance_marker ON custom_account_payment_orders`)
				require.NoError(t, err)
			}
			require.NoError(t, store.FulfillPaymentOrder(context.Background(), id, id), "a rolled-back attempt can retry normally")
		})
	}
}

func TestAccountPaymentBalancePostgresConcurrentCallbacksAndDeductions(t *testing.T) {
	db, store := balancePaymentTestDatabase(t)
	seedBalancePayment(t, db, 300, 300, "100")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var group sync.WaitGroup
	errors := make(chan error, 16)
	for i := 0; i < 12; i++ {
		group.Add(1)
		go func() { defer group.Done(); errors <- store.FulfillPaymentOrder(ctx, 300, 300) }()
	}
	group.Wait()
	for i := 0; i < 12; i++ {
		require.NoError(t, <-errors)
	}
	var count int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM redeem_codes WHERE used_by=300`).Scan(&count))
	require.Equal(t, 1, count)
	seedBalancePayment(t, db, 400, 400, "100")
	seedBalancePayment(t, db, 400, 401, "100")
	for _, id := range []int64{400, 401} {
		group.Add(1)
		go func(orderID int64) { defer group.Done(); errors <- store.FulfillPaymentOrder(ctx, orderID, 400) }(id)
	}
	group.Add(1)
	go func() {
		defer group.Done()
		_, err := db.ExecContext(ctx, `UPDATE users SET balance=balance-1 WHERE id=400`)
		errors <- err
	}()
	group.Wait()
	for i := 0; i < 3; i++ {
		require.NoError(t, <-errors, "same-user orders must serialize without lock-upgrade deadlocks")
	}
	var balance, total, credited string
	require.NoError(t, db.QueryRow(`SELECT balance::text,total_recharged::text FROM users WHERE id=400`).Scan(&balance, &total))
	require.NoError(t, db.QueryRow(`SELECT COALESCE(SUM(value),0)::text FROM redeem_codes WHERE used_by=400`).Scan(&credited))
	delta := decimal.RequireFromString(credited)
	require.Equal(t, decimal.NewFromInt(99).Add(delta).StringFixed(8), balance, "concurrent deductions are never lost")
	require.Equal(t, decimal.RequireFromString("25.125").Add(delta).StringFixed(8), total)
	require.True(t, balance == "9999.00000000" || balance == "9998.00000000", balance)
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM custom_account_payment_orders WHERE user_id=400 AND fulfilled_at IS NOT NULL`).Scan(&count))
	require.Equal(t, 2, count)
}
