package accountbilling

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// PostgreSQL reports ON DELETE RESTRICT as restrict_violation (23001), which
// must become an actionable conflict instead of an internal server error.
func TestDeleteReferencedCatalogRecordReturnsConflict(t *testing.T) {
	for _, kind := range []string{"plans", "payers"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			store := NewStore(db, "UTC")
			mock.ExpectExec(`DELETE FROM custom_billing_` + kind + ` WHERE id=\$1`).
				WithArgs(int64(5)).WillReturnError(&pq.Error{Code: "23001", Message: "update or delete violates RESTRICT setting"})
			require.ErrorIs(t, store.DeleteCatalog(context.Background(), kind, 5), ErrConflict)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserSummaryChecksActiveOrdinaryUserBeforeLoadingAccounts(t *testing.T) {
	for _, reason := range []string{"deleted", "disabled", "administrator", "nonexistent"} {
		t.Run(reason, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			store := NewStore(db, "UTC")
			mock.ExpectBegin()
			mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role='user'\)`).
				WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectRollback()
			summary, err := store.UserSummary(context.Background(), 7)
			require.ErrorIs(t, err, ErrForbidden)
			require.Empty(t, summary.Accounts)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserSummaryScopesRowsToCurrentOwnerAndAddsExactUnpaidAmountWithoutCollectionTables(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role='user'\)`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	// Requiring the ownership EXISTS clause is the regression assertion: a user
	// must never receive another user's or an unassigned account's billing row.
	mock.ExpectQuery(`(?s)SELECT b.account_id.*WHERE a.deleted_at IS NULL AND EXISTS\(SELECT 1 FROM custom_user_accounts o WHERE o.account_id=b.account_id AND o.user_id=\$1\).*FOR UPDATE OF b`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id", "name", "platform", "renewal_date", "plan_id", "plan_name", "price_cents", "payer_id", "payer_name", "history", "created_at", "cost_cents"}).
		AddRow(11, "Owned unpaid", "openai", nil, nil, "", 10, nil, "", `[{"month":"2026-11","status":"unpaid","amount":0.10}]`, now, 0).
		AddRow(12, "Owned paid", "anthropic", nil, nil, "", 20, nil, "", `[{"month":"2026-11","status":"paid","amount":0.20}]`, now, 0))
	expectNoAccessRows(mock, "{11,12}")
	mock.ExpectCommit()
	page, err := store.UserSummary(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, Money(10), page.AmountDue)
	require.Equal(t, Money(30), page.TotalAmount)
	require.Len(t, page.Accounts, 2)
	require.Equal(t, "2026-11", page.Month)
	raw, err := json.Marshal(page)
	require.NoError(t, err)
	require.JSONEq(t, `{"month":"2026-11","amount_due":0.10,"total_amount":0.30,"accounts":[{"account_id":11,"renewal_date":null,"price":0.10,"payment_status":"unpaid","valid_through":null,"expired":false},{"account_id":12,"renewal_date":null,"price":0.20,"payment_status":"paid","valid_through":null,"expired":false}]}`, string(raw))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListRejectsFutureMonthBeforeAccessingDatabase(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC) }
	_, err = store.List(context.Background(), "2027-01")
	require.ErrorIs(t, err, ErrInvalid)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserSummaryRejectsMissingUserBeforeAnyDatabaseQuery(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	for _, userID := range []int64{0, -1} {
		_, err := store.UserSummary(context.Background(), userID)
		require.ErrorIs(t, err, ErrForbidden)
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserSummaryHasEmptyAccountsAndZeroDueWithoutOwnedLedgerRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC) }
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role='user'\)`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`(?s)SELECT b.account_id.*o.user_id=\$1.*FOR UPDATE OF b`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id", "name", "platform", "renewal_date", "plan_id", "plan_name", "price_cents", "payer_id", "payer_name", "history", "created_at", "cost_cents"}))
	mock.ExpectCommit()
	summary, err := store.UserSummary(context.Background(), 7)
	require.NoError(t, err)
	raw, err := json.Marshal(summary)
	require.NoError(t, err)
	require.JSONEq(t, `{"month":"2027-03","amount_due":0,"total_amount":0,"accounts":[]}`, string(raw))
	require.NoError(t, mock.ExpectationsWereMet(), "summary must not query removed collection or visibility tables")
}

func TestUserSummaryCreatesNewBusinessMonthUnpaidHistoryWithoutOverwritingPriorPayment(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "Asia/Tokyo")
	// The host UTC date is still February, while the billing business month is March.
	store.now = func() time.Time { return time.Date(2027, 2, 28, 15, 0, 0, 0, time.UTC) }
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role='user'\)`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`(?s)SELECT b.account_id.*o.user_id=\$1.*FOR UPDATE OF b`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id", "name", "platform", "renewal_date", "plan_id", "plan_name", "price_cents", "payer_id", "payer_name", "history", "created_at", "cost_cents"}).
		AddRow(11, "Private name", "openai", nil, nil, "", 15000, nil, "Private payer", `[{"month":"2027-02","status":"paid","amount":120.00,"payer_name":"Previous payer"}]`, time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC), 0))
	mock.ExpectExec(`UPDATE custom_account_billing SET history=\$1::jsonb,updated_at=NOW\(\) WHERE account_id=\$2`).
		WithArgs(`[{"month":"2027-03","status":"unpaid","amount":150.00,"cost":0.00,"payer_name":"Private payer"},{"month":"2027-02","status":"paid","amount":120.00,"cost":0.00,"payer_name":"Previous payer"}]`, int64(11)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	expectNoAccessRows(mock, "{11}")
	mock.ExpectCommit()
	summary, err := store.UserSummary(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, "2027-03", summary.Month)
	require.Equal(t, Money(15000), summary.AmountDue)
	require.Equal(t, "unpaid", summary.Accounts[0].PaymentStatus)
	require.NoError(t, mock.ExpectationsWereMet())
}
