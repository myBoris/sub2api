package accountbilling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func renewalTestTime(t *testing.T, date string) time.Time {
	t.Helper()
	value, err := time.Parse("2006-01-02", date)
	require.NoError(t, err)
	return value
}

func TestProjectRenewalDateUsesOriginalDayAcrossShortMonthsAndMissedMonths(t *testing.T) {
	for _, tt := range []struct {
		name, anchor, today, want string
	}{
		{"short February", "2027-01-30", "2027-02-01", "2027-02-28"},
		{"restore day after February", "2027-01-30", "2027-03-01", "2027-03-30"},
		{"31st in April", "2027-01-31", "2027-04-01", "2027-04-30"},
		{"leap February", "2028-01-31", "2028-02-01", "2028-02-29"},
		{"leap anchor in non leap year", "2028-02-29", "2029-02-01", "2029-02-28"},
		{"restore leap anchor day", "2028-02-29", "2029-03-01", "2029-03-29"},
		{"century is not a leap year", "2099-12-31", "2100-02-01", "2100-02-28"},
		{"skip months across year", "2026-12-31", "2027-04-20", "2027-04-30"},
		{"expired within current month", "2027-03-03", "2027-03-20", "2027-03-03"},
		{"future day in current month", "2027-03-30", "2027-03-02", "2027-03-30"},
		{"future configured month", "2027-08-30", "2027-03-02", "2027-08-30"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := projectRenewalDate(renewalTestTime(t, tt.anchor), renewalTestTime(t, tt.today))
			require.Equal(t, tt.want, got.Format("2006-01-02"))
		})
	}
}

func TestRefreshRenewalDatesOnlyChangesOldDatedRowsAndNeverPaymentHistory(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	now := renewalTestTime(t, "2027-02-20")
	history := []History{{Month: "2027-01", Status: "paid", Amount: 12000, PayerName: "Original payer"}}
	accounts := []Account{
		{AccountID: 11, RenewalDate: billingTestDate("2027-01-30"), History: history},
		{AccountID: 12},
		{AccountID: 13, RenewalDate: billingTestDate("2027-02-01")},
		{AccountID: 14, RenewalDate: billingTestDate("2027-05-31")},
	}
	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)
	mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule WHERE account_id=ANY\(\$1\)`).
		WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTime(t, "2027-01-30")))
	mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,updated_at=NOW\(\) WHERE account_id=\$2`).
		WithArgs("2027-02-28", int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	require.NoError(t, refreshRenewalDates(ctx, tx, accounts, now))
	require.NoError(t, tx.Commit())
	require.Equal(t, "2027-02-28", *accounts[0].RenewalDate)
	require.Equal(t, history, accounts[0].History)
	require.Nil(t, accounts[1].RenewalDate)
	require.Equal(t, "2027-02-01", *accounts[2].RenewalDate, "an overdue day within this month stays in this month")
	require.Equal(t, "2027-05-31", *accounts[3].RenewalDate, "future dates must not be pulled back")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRefreshRenewalDatesDoesNotCreateSchedulesForMissingOrCurrentDates(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	tx, err := db.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	mock.ExpectCommit()
	accounts := []Account{
		{AccountID: 1},
		{AccountID: 2, RenewalDate: billingTestDate("2027-02-01")},
		{AccountID: 3, RenewalDate: billingTestDate("2027-03-31")},
	}
	require.NoError(t, refreshRenewalDates(context.Background(), tx, accounts, renewalTestTime(t, "2027-02-20")))
	require.NoError(t, tx.Commit())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRenewalDateEditsPreserveAnUnchangedAnchorOrReplaceAnExplicitNewDate(t *testing.T) {
	for _, tt := range []struct {
		name, requested, before, current, want, write string
	}{
		{"same displayed date keeps January 30 anchor", "2027-02-28", "2027-02-28", "2027-02-28", "2027-02-28", ""},
		{"same newly projected date keeps anchor", "2027-03-30", "2027-02-28", "2027-03-30", "2027-03-30", ""},
		{"form opened before rollover keeps anchor", "2027-02-28", "2027-02-28", "2027-03-30", "2027-03-30", ""},
		{"new day becomes anchor", "2027-03-12", "2027-03-30", "2027-03-30", "2027-03-12", "upsert"},
		{"past date projects into business month", "2026-12-31", "2027-03-30", "2027-03-30", "2027-03-31", "upsert"},
		{"future month is preserved", "2027-06-30", "2027-03-30", "2027-03-30", "2027-06-30", "upsert"},
		{"clearing deletes schedule", "", "2027-03-30", "2027-03-30", "", "delete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			ctx := context.Background()
			mock.ExpectBegin()
			tx, err := db.BeginTx(ctx, nil)
			require.NoError(t, err)
			a := Account{AccountID: 11, RenewalDate: billingTestDate(tt.current)}
			var requested *string
			if tt.requested != "" {
				requested = billingTestDate(tt.requested)
			}
			switch tt.write {
			case "upsert":
				mock.ExpectExec(`(?s)INSERT INTO custom_billing_renewal_schedule.*SET anchor_date=EXCLUDED.anchor_date`).
					WithArgs(int64(11), tt.requested).WillReturnResult(sqlmock.NewResult(0, 1))
			case "delete":
				mock.ExpectExec(`DELETE FROM custom_billing_renewal_schedule WHERE account_id=\$1`).
					WithArgs(int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectCommit()
			now := renewalTestTime(t, "2027-03-20")
			if tt.current == "2027-02-28" {
				now = renewalTestTime(t, "2027-02-20")
			}
			require.NoError(t, applyRenewalDateUpdate(ctx, tx, &a, requested, billingTestDate(tt.before), now))
			require.NoError(t, tx.Commit())
			if tt.want == "" {
				require.Nil(t, a.RenewalDate)
			} else {
				require.Equal(t, tt.want, *a.RenewalDate)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestRollRenewalDatesUsesBusinessMonthAndRestoresDayAfterShortMonth(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "Asia/Tokyo")
	// February has begun in Tokyo while the host UTC date is still in January.
	now := time.Date(2027, 1, 31, 15, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for _, tt := range []struct{ current, monthStart, want string }{
		{"2027-01-30", "2027-02-01", "2027-02-28"},
		{"2027-02-28", "2027-03-01", "2027-03-30"},
	} {
		mock.ExpectBegin()
		mock.ExpectQuery(`(?s)SELECT b.account_id,b.renewal_date.*b.renewal_date < \$1::date.*ORDER BY b.account_id FOR UPDATE OF b`).
			WithArgs(tt.monthStart).WillReturnRows(sqlmock.NewRows([]string{"account_id", "renewal_date"}).AddRow(11, renewalTestTime(t, tt.current)))
		mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule`).
			WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTime(t, "2027-01-30")))
		mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,updated_at=NOW\(\) WHERE account_id=\$2`).
			WithArgs(tt.want, int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		require.NoError(t, store.RollRenewalDates(context.Background()))
		now = time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	}
	require.NoError(t, mock.ExpectationsWereMet(), "rollover must not write anchors or payment history")
}

func TestRollRenewalDatesRollsBackFailedUpdates(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTime(t, "2027-03-01") }
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT b.account_id,b.renewal_date.*FOR UPDATE OF b`).
		WithArgs("2027-03-01").WillReturnRows(sqlmock.NewRows([]string{"account_id", "renewal_date"}).AddRow(11, renewalTestTime(t, "2027-02-28")))
	mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule`).
		WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTime(t, "2027-01-30")))
	wantErr := errors.New("database unavailable")
	mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,updated_at=NOW\(\) WHERE account_id=\$2`).
		WithArgs("2027-03-30", int64(11)).WillReturnError(wantErr)
	mock.ExpectRollback()
	require.ErrorIs(t, store.RollRenewalDates(context.Background()), wantErr)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRollRenewalDatesIsIdleOnceDatesAreInCurrentOrFutureMonths(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTime(t, "2027-03-20") }
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT b.account_id,b.renewal_date.*b.renewal_date < \$1::date.*FOR UPDATE OF b`).
		WithArgs("2027-03-01").WillReturnRows(sqlmock.NewRows([]string{"account_id", "renewal_date"}))
	mock.ExpectCommit()
	require.NoError(t, store.RollRenewalDates(context.Background()))
	require.NoError(t, mock.ExpectationsWereMet(), "repeated scheduler ticks should not rewrite current dates or create schedules")
}

func TestSyncAccountsCapturesOnlyNewAccountAnchorsAndCountsUndatedRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "Asia/Tokyo")
	mock.ExpectQuery(`(?s)WITH inserted AS \(.*ON CONFLICT\(account_id\) DO NOTHING RETURNING account_id,renewal_date.*INSERT INTO custom_billing_renewal_schedule.*FROM inserted WHERE renewal_date IS NOT NULL.*SELECT count\(\*\) FROM inserted`).
		WithArgs("Asia/Tokyo").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(3))
	count, err := store.SyncAccounts(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func renewalTestAccountRows(t *testing.T, date string) *sqlmock.Rows {
	t.Helper()
	return sqlmock.NewRows([]string{"account_id", "name", "platform", "renewal_date", "plan_id", "plan_name", "price_cents", "payer_id", "payer_name", "history", "created_at"}).
		AddRow(11, "Owned account", "openai", renewalTestTime(t, date), nil, "", 15000, nil, "",
			`[{"month":"2027-02","status":"unpaid","amount":150.00},{"month":"2027-01","status":"paid","amount":120.00}]`, renewalTestTime(t, "2027-01-01"))
}

func TestUpdateAccountPreservesAnchorWhenOtherFieldsSubmitProjectedDate(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTime(t, "2027-02-21") }
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT b.account_id.*b.account_id=\$1.*FOR UPDATE OF b`).
		WithArgs(int64(11)).WillReturnRows(renewalTestAccountRows(t, "2027-01-30"))
	mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule`).
		WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTime(t, "2027-01-30")))
	mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,updated_at=NOW\(\) WHERE account_id=\$2`).
		WithArgs("2027-02-28", int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT platform FROM accounts WHERE id=\$1`).
		WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
	mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,plan_id=\$2,price_cents=\$3,payer_id=\$4,history=\$5::jsonb,updated_at=NOW\(\) WHERE account_id=\$6`).
		WithArgs("2027-02-28", nil, int64(17500), nil, sqlmock.AnyArg(), int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	expectNoAccessRows(mock, "{11}")
	mock.ExpectCommit()
	account, err := store.UpdateAccount(context.Background(), 11, AccountUpdate{RenewalDate: billingTestDate("2027-02-28"), Price: 17500, PaymentStatus: "paid"})
	require.NoError(t, err)
	require.Equal(t, "2027-02-28", *account.RenewalDate)
	old, exists := historyFor(account, "2027-01")
	require.True(t, exists)
	require.Equal(t, History{Month: "2027-01", Status: "paid", Amount: 12000}, old)
	require.Equal(t, "paid", account.PaymentStatus)
	require.NoError(t, mock.ExpectationsWereMet(), "editing price/status must not replace the original day anchor")
}

func TestUpdateAccountDateChangeFlagProtectsStaleFormsAndKeepsLegacyRequestsCompatible(t *testing.T) {
	changed, unchanged := true, false
	for _, tt := range []struct {
		name, stored string
		requested    *string
		changed      *bool
		want         *string
		write        string
	}{
		{
			name:   "scheduler already rolled March 30 before old February 28 form is submitted",
			stored: "2027-03-30", requested: billingTestDate("2027-02-28"), changed: &unchanged,
			want: billingTestDate("2027-03-30"),
		},
		{
			name:   "unchanged flag still allows immediate rollover before the scheduler runs",
			stored: "2027-02-28", requested: billingTestDate("2027-02-28"), changed: &unchanged,
			want: billingTestDate("2027-03-30"),
		},
		{
			name:   "unchanged flag ignores a stale empty date too",
			stored: "2027-03-30", changed: &unchanged, want: billingTestDate("2027-03-30"),
		},
		{
			name:   "explicit changed flag clears the date and schedule",
			stored: "2027-03-30", changed: &changed, write: "delete",
		},
		{
			name:   "explicit changed flag replaces the day anchor",
			stored: "2027-03-30", requested: billingTestDate("2027-03-12"), changed: &changed,
			want: billingTestDate("2027-03-12"), write: "upsert",
		},
		{
			name:   "existing API client without flag can still clear a date",
			stored: "2027-03-30", write: "delete",
		},
		{
			name:   "existing API client without flag can still change a date",
			stored: "2027-03-30", requested: billingTestDate("2027-03-12"),
			want: billingTestDate("2027-03-12"), write: "upsert",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			store := NewStore(db, "UTC")
			store.now = func() time.Time { return renewalTestTime(t, "2027-03-20") }
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT b.account_id.*b.account_id=\$1.*FOR UPDATE OF b`).
				WithArgs(int64(11)).WillReturnRows(renewalTestAccountRows(t, tt.stored))
			if tt.stored == "2027-02-28" {
				mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule`).
					WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTime(t, "2027-01-30")))
				mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,updated_at=NOW\(\) WHERE account_id=\$2`).
					WithArgs("2027-03-30", int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			mock.ExpectQuery(`SELECT platform FROM accounts WHERE id=\$1`).
				WithArgs(int64(11)).WillReturnRows(sqlmock.NewRows([]string{"platform"}).AddRow("openai"))
			switch tt.write {
			case "upsert":
				mock.ExpectExec(`(?s)INSERT INTO custom_billing_renewal_schedule.*SET anchor_date=EXCLUDED.anchor_date`).
					WithArgs(int64(11), *tt.requested).WillReturnResult(sqlmock.NewResult(0, 1))
			case "delete":
				mock.ExpectExec(`DELETE FROM custom_billing_renewal_schedule WHERE account_id=\$1`).
					WithArgs(int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			var storedDate any
			if tt.want != nil {
				storedDate = *tt.want
			}
			mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,plan_id=\$2,price_cents=\$3,payer_id=\$4,history=\$5::jsonb,updated_at=NOW\(\) WHERE account_id=\$6`).
				WithArgs(storedDate, nil, int64(17500), nil, sqlmock.AnyArg(), int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
			if tt.write != "" {
				mock.ExpectExec(`(?s)INSERT INTO custom_account_billing_access.*SET valid_through=EXCLUDED.valid_through,updated_at=NOW\(\)`).
					WithArgs(int64(11), tt.requested).WillReturnResult(sqlmock.NewResult(0, 1))
			}
			expectNoAccessRows(mock, "{11}")
			mock.ExpectCommit()
			a, err := store.UpdateAccount(context.Background(), 11, AccountUpdate{
				RenewalDate: tt.requested, RenewalDateChanged: tt.changed, Price: 17500, PaymentStatus: "paid",
			})
			require.NoError(t, err)
			require.Equal(t, tt.want, a.RenewalDate)
			require.Equal(t, Money(17500), a.Price)
			require.Equal(t, "paid", a.PaymentStatus)
			old, exists := historyFor(a, "2027-01")
			require.True(t, exists)
			require.Equal(t, History{Month: "2027-01", Status: "paid", Amount: 12000}, old)
			require.NoError(t, mock.ExpectationsWereMet(), "unchanged dates must never write or remove the anchor")
		})
	}
}

func TestUserSummaryImmediatelyProjectsRenewalDateWithoutChangingPriorPayments(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTime(t, "2027-02-21") }
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role='user'\)`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`(?s)SELECT b.account_id.*o.user_id=\$1.*FOR UPDATE OF b`).
		WithArgs(int64(7)).WillReturnRows(renewalTestAccountRows(t, "2027-01-30"))
	mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule`).
		WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTime(t, "2027-01-30")))
	mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,updated_at=NOW\(\) WHERE account_id=\$2`).
		WithArgs("2027-02-28", int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	expectNoAccessRows(mock, "{11}")
	mock.ExpectCommit()
	page, err := store.UserSummary(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, Money(15000), page.AmountDue)
	require.Equal(t, "2027-02-28", *page.Accounts[0].RenewalDate)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListImmediatelyProjectsDatesAndPreservesSelectedMonthPaymentSnapshots(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTime(t, "2027-02-21") }
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT b.account_id.*ORDER BY b.account_id FOR UPDATE OF b`).
		WillReturnRows(renewalTestAccountRows(t, "2027-01-30"))
	mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule`).
		WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTime(t, "2027-01-30")))
	mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,updated_at=NOW\(\) WHERE account_id=\$2`).
		WithArgs("2027-02-28", int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	expectNoAccessRows(mock, "{11}")
	mock.ExpectCommit()
	mock.ExpectQuery(`SELECT id,platform,name,price_cents FROM custom_billing_plans`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "name", "price_cents"}))
	mock.ExpectQuery(`SELECT id,name FROM custom_billing_payers`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "name"}))
	list, err := store.List(context.Background(), "2027-01")
	require.NoError(t, err)
	require.Len(t, list.Items, 1)
	require.Equal(t, "2027-02-28", *list.Items[0].RenewalDate)
	require.Equal(t, Money(12000), list.Items[0].Price)
	require.Equal(t, "paid", list.Items[0].PaymentStatus)
	require.Equal(t, 1, list.Summary.PaidCount)
	require.NoError(t, mock.ExpectationsWereMet(), "renewal projection must not rewrite historical prices or payment statuses")
}

func TestUpdateHistoryReturnsCurrentProjectedDate(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTime(t, "2027-02-21") }
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT b.account_id.*b.account_id=\$1.*FOR UPDATE OF b`).
		WithArgs(int64(11)).WillReturnRows(renewalTestAccountRows(t, "2027-01-30"))
	mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule`).
		WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTime(t, "2027-01-30")))
	mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,updated_at=NOW\(\) WHERE account_id=\$2`).
		WithArgs("2027-02-28", int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE custom_account_billing SET history=\$1::jsonb,updated_at=NOW\(\) WHERE account_id=\$2`).
		WithArgs(sqlmock.AnyArg(), int64(11)).WillReturnResult(sqlmock.NewResult(0, 1))
	expectNoAccessRows(mock, "{11}")
	mock.ExpectCommit()
	account, err := store.UpdateHistory(context.Background(), 11, History{Month: "2027-01", Status: "paid", Amount: 12500})
	require.NoError(t, err)
	require.Equal(t, "2027-02-28", *account.RenewalDate)
	current, exists := historyFor(account, "2027-02")
	require.True(t, exists)
	require.Equal(t, History{Month: "2027-02", Status: "unpaid", Amount: 15000}, current)
	require.NoError(t, mock.ExpectationsWereMet())
}
