package accountbilling

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func costValue(value Money) *Money { return &value }

func TestPaidFinancialSummaryUsesExactMonthlyCostsAndOnlyPaidRevenue(t *testing.T) {
	now := renewalTestTimeRaw("2026-11-22")
	accounts := []Account{
		{PaymentStatus: "paid", Price: 10, Cost: 5, PayerID: billingTestInt(1)},
		{PaymentStatus: "paid", Price: 20, Cost: 40, PayerID: billingTestInt(1)},
		{PaymentStatus: "unpaid", Price: 90000, Cost: 70000, PayerID: billingTestInt(1)},
		{PaymentStatus: "paid", Price: 0, Cost: 3},
	}
	summary := summarize(accounts, now, []Payer{{ID: 1, Name: "Payer"}, {ID: 2, Name: "Unused"}})
	require.Equal(t, 3, summary.PaidCount)
	require.Equal(t, 1, summary.UnpaidCount)
	require.Equal(t, Money(30), summary.PaidTotalAmount)
	require.Equal(t, Money(48), summary.PaidTotalCost)
	require.Equal(t, Money(-18), summary.PaidProfit)
	require.Equal(t, Money(70045), summary.Payers[0].Total, "payer costs include paid and unpaid subscriptions")
	require.Zero(t, summary.Payers[1].Total)
	require.Equal(t, Money(3), summary.Payers[2].Total)
	raw, err := json.Marshal(summary)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"paid_profit":-0.18`)
	for _, value := range []Money{-1, -101, Money(math.MinInt64)} {
		raw, err = json.Marshal(value)
		require.NoError(t, err)
		require.True(t, json.Valid(raw), string(raw))
	}
}

func TestCostHistoryPreservesExistingAndLegacySnapshotsAcrossRollovers(t *testing.T) {
	account := Account{Price: 15000, Cost: 9000, createdAt: renewalTestTimeRaw("2026-10-01")}
	require.NoError(t, json.Unmarshal([]byte(`[{"month":"2026-10","status":"paid","amount":120.00}]`), &account.History))
	require.True(t, fillHistory(&account, renewalTestTimeRaw("2026-11-22")))
	old, _ := historyFor(account, "2026-10")
	require.Zero(t, old.Cost, "unknown historical costs cannot be inferred from today's account or plan")
	current, _ := historyFor(account, "2026-11")
	require.Equal(t, Money(9000), current.Cost)
	account.Cost = 10000
	require.True(t, fillHistory(&account, renewalTestTimeRaw("2026-12-01")))
	current, _ = historyFor(account, "2026-11")
	require.Equal(t, Money(9000), current.Cost)
	applyHistory(&account, old, false)
	require.Zero(t, account.Cost)
	require.Equal(t, Money(12000), account.Price)
}

func TestPaymentCostSnapshotIsPrivateAndChangesInvalidateQuote(t *testing.T) {
	account := paymentTestAccount(11, 12345, "unpaid")
	account.Cost = 7000
	account.History[0].Cost = 7000
	quote, snapshots, err := buildPaymentQuote(7, "2026-11", []Account{account}, map[int64]string{11: "2026-11-30"})
	require.NoError(t, err)
	require.Equal(t, Money(7000), snapshots[0].History.Cost)
	raw, err := json.Marshal(quote)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "cost")
	require.True(t, paymentSnapshotStillValid(snapshots[0], account, "2026-11", "2026-11-30", renewalTestTimeRaw("2026-11-22")))
	for _, changeBase := range []bool{false, true} {
		changed := account
		changed.History = append([]History(nil), account.History...)
		if changeBase {
			changed.Cost++
		} else {
			changed.History[0].Cost++
		}
		require.False(t, paymentSnapshotStillValid(snapshots[0], changed, "2026-11", "2026-11-30", renewalTestTimeRaw("2026-11-22")))
		changedQuote, _, buildErr := buildPaymentQuote(7, "2026-11", []Account{changed}, map[int64]string{11: "2026-11-30"})
		require.NoError(t, buildErr)
		require.NotEqual(t, quote.QuoteToken, changedQuote.QuoteToken)
	}
}

func TestLegacyPendingPaymentWithoutCostCanStillBeFulfilled(t *testing.T) {
	account := paymentTestAccount(11, 12345, "unpaid")
	var snapshot paymentAccountSnapshot
	require.NoError(t, json.Unmarshal([]byte(`{"account_id":11,"price":123.45,"billing_price":123.45,"renewal_date":"2026-11-30","anchor_date":"2026-11-30","history":{"month":"2026-11","status":"unpaid","amount":123.45}}`), &snapshot))
	require.True(t, paymentSnapshotStillValid(snapshot, account, "2026-11", "2026-11-30", renewalTestTimeRaw("2026-11-22")))
}

func costTestAccountRows(a Account) *sqlmock.Rows {
	raw, _ := json.Marshal(a.History)
	return sqlmock.NewRows([]string{"account_id", "name", "platform", "renewal_date", "plan_id", "plan_name", "price_cents", "payer_id", "payer_name", "history", "created_at", "cost_cents"}).
		AddRow(a.AccountID, "Account", "openai", nil, a.PlanID, "Plan", int64(a.Price), nil, "", string(raw), a.createdAt, int64(a.Cost))
}

func TestAccountCostDefaultsToNewPlanAndPreservesExistingWhenOmitted(t *testing.T) {
	for _, tt := range []struct {
		name          string
		oldPlan, plan *int64
		cost          *Money
		want          Money
	}{
		{name: "older client keeps custom account cost", want: 4500},
		{name: "explicit zero clears cost", cost: costValue(0), want: 0},
		{name: "newly selected plan supplies cost", plan: billingTestInt(5), want: 7000},
		{name: "same plan keeps custom account cost", oldPlan: billingTestInt(5), plan: billingTestInt(5), want: 4500},
		{name: "explicit custom cost overrides selected plan", plan: billingTestInt(5), cost: costValue(3500), want: 3500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			store := NewStore(db, "UTC")
			store.now = func() time.Time { return renewalTestTimeRaw("2026-11-22") }
			account := paymentTestAccount(11, 12345, "unpaid")
			account.RenewalDate = nil
			account.Cost = 4500
			account.PlanID = tt.oldPlan
			account.History[0].Cost = 4500
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT b.account_id.*b.account_id=\$1.*FOR UPDATE OF b`).WithArgs(int64(11)).WillReturnRows(costTestAccountRows(account))
			if tt.plan != nil {
				mock.ExpectQuery(`SELECT platform,name,cost_cents FROM custom_billing_plans WHERE id=\$1`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"platform", "name", "cost_cents"}).AddRow("openai", "Plan", 7000))
			} else {
				mock.ExpectQuery(`SELECT platform FROM accounts WHERE id=\$1`).WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
			}
			mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,plan_id=\$2,price_cents=\$3,payer_id=\$4,history=\$5::jsonb,cost_cents=\$6,updated_at=NOW\(\) WHERE account_id=\$7`).
				WithArgs(nil, tt.plan, int64(12345), nil, sqlmock.AnyArg(), int64(tt.want), int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
			expectNoAccessRows(mock, "{11}")
			mock.ExpectCommit()
			unchanged := false
			updated, err := store.UpdateAccount(context.Background(), 11, AccountUpdate{RenewalDateChanged: &unchanged, PlanID: tt.plan, Price: 12345, Cost: tt.cost, PaymentStatus: "paid"})
			require.NoError(t, err)
			require.Equal(t, tt.want, updated.Cost)
			history, exists := historyFor(updated, "2026-11")
			require.True(t, exists)
			require.Equal(t, tt.want, history.Cost)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestHistoryCostRevisionPreservesOmittedValueAndAcceptsExplicitZero(t *testing.T) {
	for _, tt := range []struct {
		name string
		cost *Money
		want Money
	}{
		{name: "older client keeps snapshot", want: 4500},
		{name: "explicit zero", cost: costValue(0)},
		{name: "explicit replacement", cost: costValue(3500), want: 3500},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			store := NewStore(db, "UTC")
			store.now = func() time.Time { return renewalTestTimeRaw("2026-11-22") }
			account := paymentTestAccount(11, 12345, "unpaid")
			account.RenewalDate = nil
			account.Cost = 4500
			account.History[0].Cost = 4500
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT b.account_id.*b.account_id=\$1.*FOR UPDATE OF b`).WithArgs(int64(11)).WillReturnRows(costTestAccountRows(account))
			mock.ExpectExec(`UPDATE custom_account_billing SET price_cents=\$1,cost_cents=\$2 WHERE account_id=\$3`).
				WithArgs(int64(12000), int64(tt.want), int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(`UPDATE custom_account_billing SET history=\$1::jsonb,updated_at=NOW\(\) WHERE account_id=\$2`).WithArgs(sqlmock.AnyArg(), int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
			expectNoAccessRows(mock, "{11}")
			mock.ExpectCommit()
			updated, err := store.UpdateHistory(context.Background(), 11, History{Month: "2026-11", Status: "paid", Amount: 12000}, tt.cost)
			require.NoError(t, err)
			require.Equal(t, tt.want, updated.Cost)
			history, _ := historyFor(updated, "2026-11")
			require.Equal(t, tt.want, history.Cost)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPlanUpdateFromOlderClientKeepsCost(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectQuery(`UPDATE custom_billing_plans SET platform=\$1,name=\$2,price_cents=\$3,cost_cents=COALESCE\(\$4,cost_cents\) WHERE id=\$5 RETURNING id,cost_cents`).
		WithArgs("openai", "Plan", int64(12000), nil, int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id", "cost_cents"}).AddRow(5, 7000))
	plan, err := NewStore(db, "UTC").SavePlan(context.Background(), 5, Plan{Platform: "openai", Name: "Plan", Price: 12000}, false)
	require.NoError(t, err)
	require.Equal(t, Money(7000), plan.Cost)
	require.NoError(t, mock.ExpectationsWereMet())
}
