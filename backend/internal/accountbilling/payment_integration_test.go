package accountbilling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// Set ACCOUNT_BILLING_TEST_DSN to an isolated PostgreSQL test database. Each run
// creates and removes its own schema, and never relies on deployed application
// tables. The test is optional so ordinary unit tests need no database server.
func TestAccountPaymentPostgresConcurrencyAndFulfillment(t *testing.T) {
	dsn := os.Getenv("ACCOUNT_BILLING_TEST_DSN")
	if dsn == "" {
		t.Skip("ACCOUNT_BILLING_TEST_DSN is not set")
	}
	adminDB, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = adminDB.Close() })
	schema := fmt.Sprintf("account_payment_test_%d", time.Now().UnixNano())
	_, err = adminDB.Exec(`CREATE SCHEMA ` + pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, cleanupErr := adminDB.Exec(`DROP SCHEMA ` + pq.QuoteIdentifier(schema) + ` CASCADE`)
		require.NoError(t, cleanupErr)
	})
	db, err := sql.Open("postgres", dsn+" search_path="+schema)
	require.NoError(t, err)
	db.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`CREATE TABLE users(id BIGINT PRIMARY KEY,status TEXT NOT NULL,role TEXT NOT NULL,deleted_at TIMESTAMPTZ,
			balance NUMERIC(20,8) NOT NULL DEFAULT 0,total_recharged NUMERIC(20,8) NOT NULL DEFAULT 0,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());
		CREATE TABLE redeem_codes(id BIGSERIAL PRIMARY KEY,code VARCHAR(32) NOT NULL UNIQUE,type VARCHAR(20) NOT NULL,
			value NUMERIC(20,8) NOT NULL,status VARCHAR(20) NOT NULL,used_by BIGINT REFERENCES users(id),used_at TIMESTAMPTZ,notes TEXT);
		CREATE TABLE accounts(id BIGINT PRIMARY KEY,name TEXT NOT NULL,platform TEXT NOT NULL,expires_at TIMESTAMPTZ,deleted_at TIMESTAMPTZ);
		CREATE TABLE custom_user_accounts(account_id BIGINT PRIMARY KEY REFERENCES accounts(id),user_id BIGINT NOT NULL REFERENCES users(id));
		CREATE TABLE payment_orders(id BIGINT PRIMARY KEY,user_id BIGINT NOT NULL,order_type TEXT NOT NULL,
			status TEXT NOT NULL,amount NUMERIC(20,2) NOT NULL,paid_at TIMESTAMPTZ,expires_at TIMESTAMPTZ NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW());`)
	require.NoError(t, err)
	for _, file := range []string{"253_custom_account_billing.sql", "254_custom_billing_renewal_schedule.sql", "256_custom_account_payment_orders.sql", "258_custom_billing_cost.sql"} {
		migration, readErr := os.ReadFile(filepath.Join("..", "..", "migrations", file))
		require.NoError(t, readErr)
		_, execErr := db.Exec(string(migration))
		require.NoError(t, execErr)
	}
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTimeRaw("2027-01-22") }
	_, err = db.Exec(`INSERT INTO users(id,status,role) VALUES(7,'active','user'),(8,'active','user');
		INSERT INTO accounts(id,name,platform) VALUES(11,'Owned January-30 account','openai'),(12,'Other owner account','openai');
		INSERT INTO custom_user_accounts(account_id,user_id) VALUES(11,7),(12,8);
		INSERT INTO custom_account_billing(account_id,renewal_date,price_cents,history,created_at)
			VALUES(11,'2027-01-30',12345,'[{"month":"2027-01","status":"unpaid","amount":123.45}]','2027-01-01'),
			(12,'2027-01-31',50000,'[{"month":"2027-01","status":"unpaid","amount":500.00}]','2027-01-01');
		INSERT INTO custom_billing_renewal_schedule(account_id,anchor_date) VALUES(11,'2027-01-30'),(12,'2027-01-31');`)
	require.NoError(t, err)
	accessMigration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "257_custom_account_billing_access.sql"))
	require.NoError(t, err)
	_, err = db.Exec(string(accessMigration))
	require.NoError(t, err)
	ctx := context.Background()
	access, err := store.AccessStates(ctx, []int64{11, 12})
	require.NoError(t, err)
	require.Equal(t, paymentDate("2027-01-30"), access[11].ValidThrough, "migration initializes the current displayed date without inventing historical debt")
	require.False(t, access[11].Expired)
	quote, err := store.PaymentQuote(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, Money(12345), quote.AmountDue)
	require.Len(t, quote.Accounts, 1, "another owner's account must not enter the payable quote")
	insertOrder := func(id int64) {
		_, insertErr := db.Exec(`INSERT INTO payment_orders(id,user_id,order_type,status,amount,expires_at) VALUES($1,7,'account','PENDING',123.45,NOW()+INTERVAL '30 minutes')`, id)
		require.NoError(t, insertErr)
	}
	insertOrder(99)
	insertOrder(100)
	var group sync.WaitGroup
	results := make(chan struct {
		id  int64
		err error
	}, 2)
	for _, id := range []int64{99, 100} {
		group.Add(1)
		go func(orderID int64) {
			defer group.Done()
			results <- struct {
				id  int64
				err error
			}{orderID, store.BindPaymentOrder(ctx, orderID, 7, quote.QuoteToken, 123.45)}
		}(id)
	}
	group.Wait()
	close(results)
	var winner int64
	var successes, pending int
	for result := range results {
		if result.err == nil {
			successes++
			winner = result.id
		} else {
			require.ErrorIs(t, result.err, ErrPaymentPending)
			pending++
		}
	}
	require.Equal(t, 1, successes, "row locking must permit only one account payment for a billing month")
	require.Equal(t, 1, pending)
	_, err = db.Exec(`UPDATE payment_orders SET status='EXPIRED',updated_at=NOW() WHERE id=$1`, winner)
	require.NoError(t, err)
	insertOrder(101)
	require.ErrorIs(t, store.BindPaymentOrder(ctx, 101, 7, quote.QuoteToken, 123.45), ErrPaymentPending, "expired callbacks retain a five-minute grace period")
	_, err = db.Exec(`UPDATE payment_orders SET status='CANCELLED' WHERE id=$1`, winner)
	require.NoError(t, err)
	require.NoError(t, store.BindPaymentOrder(ctx, 101, 7, quote.QuoteToken, 123.45), "a cancelled unpaid attempt must not permanently block a new payment")
	_, err = db.Exec(`UPDATE payment_orders SET status='RECHARGING',paid_at=NOW() WHERE id=101`)
	require.NoError(t, err)
	// Simulate the exact persisted 256 format: these fields did not exist when
	// a pre-upgrade pending order was bound, while migration 257 now populated
	// a non-null access deadline. Its paid callback must remain fulfillable.
	_, err = db.Exec(`UPDATE custom_account_payment_orders c SET snapshot=(
		SELECT jsonb_agg(jsonb_set(item-'valid_through'-'expired'-'access_schema_version'-'billing_cost', '{history}', (item->'history')-'cost'))
		FROM jsonb_array_elements(c.snapshot) item) WHERE c.order_id=101`)
	require.NoError(t, err)
	// Simulate the scheduler running before a delayed callback in February.
	store.now = func() time.Time { return renewalTestTimeRaw("2027-02-01") }
	require.NoError(t, store.RollRenewalDates(ctx))
	access, err = store.AccessStates(ctx, []int64{11})
	require.NoError(t, err)
	require.True(t, access[11].Expired, "the new month's display rollover must not restore an unpaid expired account")
	require.Equal(t, paymentDate("2027-01-30"), access[11].ValidThrough)
	fulfillResults := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			fulfillResults <- store.FulfillPaymentOrder(ctx, 101, 7)
		}()
	}
	group.Wait()
	close(fulfillResults)
	for fulfillErr := range fulfillResults {
		require.NoError(t, fulfillErr)
	}
	var renewal, januaryStatus, anchor string
	var januaryAmount string
	err = db.QueryRow(`SELECT b.renewal_date::text,h->>'status',h->>'amount',s.anchor_date::text
		FROM custom_account_billing b JOIN custom_billing_renewal_schedule s USING(account_id),
		LATERAL jsonb_array_elements(b.history) h WHERE b.account_id=11 AND h->>'month'='2027-01'`).Scan(&renewal, &januaryStatus, &januaryAmount, &anchor)
	require.NoError(t, err)
	require.Equal(t, "2027-02-28", renewal, "concurrent fulfillment must advance only the bound January billing month")
	require.Equal(t, "paid", januaryStatus)
	require.Equal(t, "123.45", januaryAmount)
	require.Equal(t, "2027-01-30", anchor)
	access, err = store.AccessStates(ctx, []int64{11})
	require.NoError(t, err)
	require.False(t, access[11].Expired, "successful payment restores API access immediately")
	require.Equal(t, paymentDate("2027-02-28"), access[11].ValidThrough)
	var fulfilledCount int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM custom_account_payment_orders WHERE fulfilled_at IS NOT NULL`).Scan(&fulfilledCount))
	require.Equal(t, 1, fulfilledCount)
	// February remains a separate unpaid month and restores the original day in March.
	february, err := store.PaymentQuote(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, "2027-02", february.Month)
	require.Equal(t, Money(12345), february.AmountDue)
	insertOrder(102)
	require.NoError(t, store.BindPaymentOrder(ctx, 102, 7, february.QuoteToken, 123.45))
	_, err = db.Exec(`UPDATE custom_user_accounts SET user_id=8 WHERE account_id=11;
		UPDATE payment_orders SET status='RECHARGING',paid_at=NOW() WHERE id=102;`)
	require.NoError(t, err)
	require.ErrorIs(t, store.FulfillPaymentOrder(ctx, 102, 7), ErrQuoteChanged, "a paid order must not renew an account transferred to another owner")
	var status string
	require.NoError(t, db.QueryRow(`SELECT h->>'status' FROM custom_account_billing b,LATERAL jsonb_array_elements(b.history) h WHERE b.account_id=11 AND h->>'month'='2027-02'`).Scan(&status))
	require.Equal(t, "unpaid", status)
	_, err = db.Exec(`UPDATE custom_user_accounts SET user_id=7 WHERE account_id=11`)
	require.NoError(t, err)
	require.NoError(t, store.FulfillPaymentOrder(ctx, 102, 7))
	require.NoError(t, db.QueryRow(`SELECT renewal_date::text FROM custom_account_billing WHERE account_id=11`).Scan(&renewal))
	require.Equal(t, "2027-03-30", renewal)
	access, err = store.AccessStates(ctx, []int64{11})
	require.NoError(t, err)
	require.Equal(t, paymentDate("2027-03-30"), access[11].ValidThrough)
	store.now = func() time.Time { return renewalTestTimeRaw("2027-04-01") }
	require.NoError(t, store.RollRenewalDates(ctx))
	access, err = store.AccessStates(ctx, []int64{11})
	require.NoError(t, err)
	require.True(t, access[11].Expired, "a previously paid account still expires when its actual deadline passes")
	// A cancelled order which is paid late is retained for manual review and
	// cannot pay the same January billing month for a second time.
	_, err = db.Exec(`UPDATE payment_orders SET status='RECHARGING',paid_at=NOW() WHERE id=$1`, winner)
	require.NoError(t, err)
	require.True(t, errors.Is(store.FulfillPaymentOrder(ctx, winner, 7), ErrQuoteChanged))
	require.NoError(t, db.QueryRow(`SELECT renewal_date::text FROM custom_account_billing WHERE account_id=11`).Scan(&renewal))
	require.Equal(t, "2027-04-30", renewal)

	// Explicitly selecting the same displayed date must reset the independent
	// deadline, and invalidate a quote even though the display itself is stable.
	beforeDateEdit, err := store.PaymentQuote(ctx, 7)
	require.NoError(t, err)
	changed, unchanged := true, false
	updated, err := store.UpdateAccount(ctx, 11, AccountUpdate{
		RenewalDate: paymentDate("2027-04-30"), RenewalDateChanged: &changed,
		Price: 12345, PaymentStatus: "unpaid",
	})
	require.NoError(t, err)
	require.False(t, updated.Expired)
	require.Equal(t, paymentDate("2027-04-30"), updated.ValidThrough)
	afterDateEdit, err := store.PaymentQuote(ctx, 7)
	require.NoError(t, err)
	require.Equal(t, beforeDateEdit.Accounts[0].RenewalDate, afterDateEdit.Accounts[0].RenewalDate)
	require.NotEqual(t, beforeDateEdit.QuoteToken, afterDateEdit.QuoteToken)
	// A past raw date stays expired even though the legacy display projects it
	// into April; merely saving another field must not grant a fresh deadline.
	updated, err = store.UpdateAccount(ctx, 11, AccountUpdate{
		RenewalDate: paymentDate("2027-03-15"), RenewalDateChanged: &changed,
		Price: 12345, PaymentStatus: "paid",
	})
	require.NoError(t, err)
	require.Equal(t, paymentDate("2027-04-15"), updated.RenewalDate)
	require.Equal(t, paymentDate("2027-03-15"), updated.ValidThrough)
	require.True(t, updated.Expired, "a paid marker cannot bypass the real date")
	updated, err = store.UpdateAccount(ctx, 11, AccountUpdate{
		RenewalDate: updated.RenewalDate, RenewalDateChanged: &unchanged,
		Price: 0, PaymentStatus: "paid",
	})
	require.NoError(t, err)
	require.True(t, updated.Expired, "changing the price to zero and saving a paid status must not bypass expiry")
	updated, err = store.UpdateHistory(ctx, 11, History{Month: "2027-04", Status: "paid", Amount: 0})
	require.NoError(t, err)
	require.True(t, updated.Expired, "monthly status edits also retain the real deadline")
	updated, err = store.UpdateAccount(ctx, 11, AccountUpdate{
		RenewalDate: nil, RenewalDateChanged: &changed, Price: 0, PaymentStatus: "paid",
	})
	require.NoError(t, err)
	require.Nil(t, updated.ValidThrough)
	require.False(t, updated.Expired, "explicitly clearing the date removes the account-billing deadline")
	var nullDeadline bool
	require.NoError(t, db.QueryRow(`SELECT valid_through IS NULL FROM custom_account_billing_access WHERE account_id=11`).Scan(&nullDeadline))
	require.True(t, nullDeadline)

	// New synchronizations save dated and undated accounts once, and repeated
	// syncs preserve the deadline chosen by an administrator.
	_, err = db.Exec(`INSERT INTO accounts(id,name,platform,expires_at)
		VALUES(13,'New dated account','openai','2027-04-20T08:00:00Z'),(14,'New undated account','openai',NULL)`)
	require.NoError(t, err)
	synced, err := store.SyncAccounts(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, synced)
	access, err = store.AccessStates(ctx, []int64{13, 14})
	require.NoError(t, err)
	require.Equal(t, paymentDate("2027-04-20"), access[13].ValidThrough)
	require.Nil(t, access[14].ValidThrough)
	require.False(t, access[14].Expired)
	_, err = store.UpdateAccount(ctx, 13, AccountUpdate{
		RenewalDate: paymentDate("2027-03-10"), RenewalDateChanged: &changed,
		Price: 0, PaymentStatus: "unpaid",
	})
	require.NoError(t, err)
	synced, err = store.SyncAccounts(ctx)
	require.NoError(t, err)
	require.Zero(t, synced)
	access, err = store.AccessStates(ctx, []int64{13})
	require.NoError(t, err)
	require.Equal(t, paymentDate("2027-03-10"), access[13].ValidThrough)
	require.True(t, access[13].Expired)
	// The ordinary-user payload exposes access fields for owned rows only.
	summary, err := store.UserSummary(ctx, 7)
	require.NoError(t, err)
	require.Len(t, summary.Accounts, 1)
	require.Equal(t, int64(11), summary.Accounts[0].AccountID)
	require.Nil(t, summary.Accounts[0].ValidThrough)

	// Nonzero cost scenarios use the same real ledger and gateway transaction.
	// Changing a plan is a catalog edit; existing monthly account snapshots stay
	// fixed until the administrator explicitly changes that account's cost.
	plan, err := store.SavePlan(ctx, 0, Plan{Platform: "openai", Name: "Cost plan", Price: 12345, Cost: 7000})
	require.NoError(t, err)
	payer, err := store.SavePayer(ctx, 0, Payer{Name: "Cost payer"})
	require.NoError(t, err)
	updated, err = store.UpdateAccount(ctx, 11, AccountUpdate{
		RenewalDate: paymentDate("2027-04-30"), RenewalDateChanged: &changed,
		PlanID: &plan.ID, Price: 12345, PayerID: &payer.ID, PaymentStatus: "unpaid",
	})
	require.NoError(t, err)
	require.Equal(t, Money(7000), updated.Cost, "selecting a plan defaults its cost without requiring a client override")
	_, err = store.UpdateAccount(ctx, 12, AccountUpdate{
		RenewalDateChanged: &unchanged, PlanID: &plan.ID, Price: 10000, Cost: costValue(20000),
		PayerID: &payer.ID, PaymentStatus: "paid",
	})
	require.NoError(t, err)
	plan.Cost = 9000
	plan, err = store.SavePlan(ctx, plan.ID, plan)
	require.NoError(t, err)
	catalog, err := store.Catalog(ctx)
	require.NoError(t, err)
	require.Equal(t, Money(9000), catalog.Plans[0].Cost)
	costQuote, err := store.PaymentQuote(ctx, 7)
	require.NoError(t, err)
	rawQuote, err := json.Marshal(costQuote)
	require.NoError(t, err)
	require.NotContains(t, string(rawQuote), "cost", "ordinary users cannot inspect subscription costs")
	_, err = db.Exec(`INSERT INTO payment_orders(id,user_id,order_type,status,amount,expires_at)
		VALUES(103,7,'account','PENDING',123.45,NOW()+INTERVAL '30 minutes')`)
	require.NoError(t, err)
	require.NoError(t, store.BindPaymentOrder(ctx, 103, 7, costQuote.QuoteToken, 123.45))
	_, err = db.Exec(`UPDATE payment_orders SET status='RECHARGING',paid_at=NOW() WHERE id=103`)
	require.NoError(t, err)
	require.NoError(t, store.FulfillPaymentOrder(ctx, 103, 7))
	costList, err := store.List(ctx, "2027-04")
	require.NoError(t, err)
	require.Equal(t, Money(22345), costList.Summary.PaidTotalAmount)
	require.Equal(t, Money(27000), costList.Summary.PaidTotalCost)
	require.Equal(t, Money(-4655), costList.Summary.PaidProfit)
	require.Equal(t, Money(27000), costList.Summary.Payers[0].Total)
	require.Equal(t, Money(7000), costList.Items[0].Cost, "payment preserves the bound monthly cost after catalog edits")
	_, err = store.UpdateHistory(ctx, 11, History{Month: "2027-04", Status: "paid", Amount: 12345})
	require.NoError(t, err)
	costList, err = store.List(ctx, "2027-04")
	require.NoError(t, err)
	require.Equal(t, Money(7000), costList.Items[0].Cost, "older history API clients preserve the stored cost")
	store.now = func() time.Time { return renewalTestTimeRaw("2027-05-01") }
	_, err = store.List(ctx, "2027-05")
	require.NoError(t, err)
	costList, err = store.List(ctx, "2027-04")
	require.NoError(t, err)
	require.Equal(t, Money(27000), costList.Summary.PaidTotalCost, "a new business month retains earlier paid cost snapshots")
}
