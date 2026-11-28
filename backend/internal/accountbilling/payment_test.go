package accountbilling

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func paymentDate(value string) *string { return &value }

func paymentTestAccount(id int64, amount Money, status string) Account {
	return Account{
		AccountID: id, AccountName: "Owned account", Price: amount,
		RenewalDate: paymentDate("2026-11-30"),
		History:     []History{{Month: "2026-11", Amount: amount, Status: status}},
		createdAt:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
	}
}

func paymentTestRows(accounts ...Account) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"account_id", "name", "platform", "renewal_date", "plan_id", "plan_name", "price_cents", "payer_id", "payer_name", "history", "created_at", "cost_cents"})
	for _, a := range accounts {
		var date any
		if a.RenewalDate != nil {
			date, _ = time.Parse("2006-01-02", *a.RenewalDate)
		}
		raw, _ := json.Marshal(a.History)
		rows.AddRow(a.AccountID, a.AccountName, "openai", date, nil, "", int64(a.Price), nil, "", string(raw), a.createdAt, int64(a.Cost))
	}
	return rows
}

func expectPaymentOwner(mock sqlmock.Sqlmock, replenish ...bool) {
	lock := "FOR SHARE"
	if len(replenish) > 0 && replenish[0] {
		lock = "FOR NO KEY UPDATE"
	}
	mock.ExpectQuery(`SELECT id FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' ` + lock).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT account_id FROM custom_user_accounts WHERE user_id=\$1 ORDER BY account_id FOR SHARE`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(11))
}

func expectPaymentAccounts(mock sqlmock.Sqlmock, accounts ...Account) {
	mock.ExpectQuery(`(?s)SELECT b.account_id.*AND EXISTS\(SELECT 1 FROM custom_user_accounts o WHERE o.account_id=b.account_id AND o.user_id=\$1\).*FOR UPDATE OF b`).
		WithArgs(int64(7)).WillReturnRows(paymentTestRows(accounts...))
}

func expectPaymentAnchors(mock sqlmock.Sqlmock, date string) {
	expectNoAccessRows(mock, "{11}")
	mock.ExpectQuery(`SELECT account_id,anchor_date FROM custom_billing_renewal_schedule WHERE account_id=ANY\(\$1\)`).
		WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "anchor_date"}).AddRow(11, renewalTestTimeRaw(date)))
}

func renewalTestTimeRaw(date string) time.Time {
	value, _ := time.Parse("2006-01-02", date)
	return value
}

func TestPaymentQuoteUsesOnlyCurrentMonthPositiveUnpaidPricesAndBindsDigestToOwner(t *testing.T) {
	unpaid := paymentTestAccount(11, 101, "unpaid")
	unpaid.History = append(unpaid.History, History{Month: "2026-10", Amount: 9000, Status: "unpaid"})
	paid := paymentTestAccount(12, 20000, "paid")
	free := paymentTestAccount(13, 0, "unpaid")
	quote, snapshots, err := buildPaymentQuote(7, "2026-11", []Account{unpaid, paid, free}, map[int64]string{11: "2026-01-30"})
	require.NoError(t, err)
	require.Equal(t, Money(101), quote.AmountDue)
	require.Equal(t, "CNY", quote.Currency)
	require.Len(t, quote.Accounts, 1)
	require.Len(t, snapshots, 1)
	require.Equal(t, int64(11), quote.Accounts[0].AccountID)
	require.Len(t, quote.QuoteToken, 64)
	for _, change := range []string{"owner", "amount", "renewal", "anchor", "status", "base_price"} {
		t.Run(change, func(t *testing.T) {
			account := unpaid
			account.History = append([]History(nil), unpaid.History...)
			owner := int64(7)
			anchor := "2026-01-30"
			switch change {
			case "owner":
				owner = 8
			case "amount":
				account.History[0].Amount++
			case "renewal":
				account.RenewalDate = paymentDate("2026-11-29")
			case "anchor":
				anchor = "2026-01-31"
			case "status":
				account.History[0].Status = "paid"
			case "base_price":
				account.Price++
			}
			changed, _, buildErr := buildPaymentQuote(owner, "2026-11", []Account{account}, map[int64]string{11: anchor})
			require.NoError(t, buildErr)
			require.NotEqual(t, quote.QuoteToken, changed.QuoteToken)
		})
	}
}

func TestPaymentQuoteScopesAndLocksOwnerBeforeShowingNamesAndAmounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	store := NewStore(db, "UTC")
	store.now = func() time.Time { return renewalTestTimeRaw("2026-11-22") }
	mock.ExpectBegin()
	expectPaymentOwner(mock)
	expectPaymentAccounts(mock, paymentTestAccount(11, 12345, "unpaid"))
	expectPaymentAnchors(mock, "2026-01-30")
	mock.ExpectCommit()
	quote, err := store.PaymentQuote(context.Background(), 7)
	require.NoError(t, err)
	require.Equal(t, Money(12345), quote.AmountDue)
	require.Equal(t, "Owned account", quote.Accounts[0].AccountName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExactAccountPaymentAmountRejectsFractionalCentsAndNonFiniteNumbers(t *testing.T) {
	for _, amount := range []float64{0.01, 0.1, 100.05, 9999999999.99} {
		cents, err := exactPaymentCents(amount)
		require.NoError(t, err)
		require.Equal(t, Money(math.Round(amount*100)), cents)
	}
	for _, amount := range []float64{0, -1, 0.001, 10.101, math.NaN(), math.Inf(1), math.Inf(-1), 1e30} {
		_, err := exactPaymentCents(amount)
		require.ErrorIs(t, err, ErrInvalid)
	}
}

func TestPaidRenewalUsesBilledMonthAndOriginalDayAcrossShortMonths(t *testing.T) {
	for _, test := range []struct{ month, anchor, current, want string }{
		{"2027-01", "2027-01-30", "2027-01-30", "2027-02-28"},
		{"2027-02", "2027-01-30", "2027-02-28", "2027-03-30"},
		{"2028-01", "2027-01-31", "2028-01-31", "2028-02-29"},
		{"2028-02", "2027-01-31", "2028-02-29", "2028-03-31"},
		{"2026-12", "2026-12-15", "2026-12-15", "2027-01-15"},
		{"2027-01", "2027-01-30", "2027-03-30", "2027-03-30"},
	} {
		t.Run(test.month+test.anchor+test.current, func(t *testing.T) {
			snapshot := paymentAccountSnapshot{PaymentQuoteAccount: PaymentQuoteAccount{RenewalDate: paymentDate(test.current)}, AnchorDate: paymentDate(test.anchor)}
			got, err := nextPaidRenewalDate(test.month, snapshot, paymentDate(test.current))
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
	_, err := nextPaidRenewalDate("2027-01", paymentAccountSnapshot{}, nil)
	require.ErrorIs(t, err, ErrRenewalNotConfigured)
}

func TestBindAccountPaymentRejectsChangedQuotesMissingDatesAndExistingPayments(t *testing.T) {
	for _, reason := range []string{"amount_changed", "token_changed", "no_amount", "missing_date", "pending", "invalid_order", "success"} {
		t.Run(reason, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			store := NewStore(db, "UTC")
			store.now = func() time.Time { return renewalTestTimeRaw("2026-11-22") }
			account := paymentTestAccount(11, 1000, "unpaid")
			if reason == "missing_date" {
				account.RenewalDate = nil
			}
			if reason == "no_amount" {
				account.History[0].Status = "paid"
			}
			quote, _, err := buildPaymentQuote(7, "2026-11", []Account{account}, map[int64]string{11: "2026-01-30"})
			require.NoError(t, err)
			amount, token := 10.0, quote.QuoteToken
			if reason == "amount_changed" {
				amount = 9.99
			}
			if reason == "token_changed" {
				token = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}
			mock.ExpectBegin()
			expectPaymentOwner(mock)
			expectPaymentAccounts(mock, account)
			expectPaymentAnchors(mock, "2026-01-30")
			expectedErr := ErrQuoteChanged
			switch reason {
			case "no_amount":
				expectedErr = ErrNoAmountDue
			case "missing_date":
				expectedErr = ErrRenewalNotConfigured
			case "pending", "invalid_order", "success":
				mock.ExpectQuery(`(?s)SELECT EXISTS\(SELECT 1 FROM payment_orders WHERE id=\$1 AND user_id=\$2.*order_type='account'.*status='PENDING'.*amount=\$3`).
					WithArgs(int64(99), int64(7), 10.0).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(reason != "invalid_order"))
				if reason != "invalid_order" {
					mock.ExpectQuery(`(?s)SELECT EXISTS\(SELECT 1 FROM custom_account_payment_orders c.*c.account_ids && \$2::bigint\[\].*p.paid_at IS NOT NULL.*p.status='EXPIRED'.*INTERVAL '5 minutes'`).
						WithArgs("2026-11", "{11}", int64(99)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(reason == "pending"))
				}
				if reason == "pending" {
					expectedErr = ErrPaymentPending
				}
				if reason == "success" {
					expectedErr = nil
					mock.ExpectExec(`(?s)INSERT INTO custom_account_payment_orders.*order_id,user_id,billing_month,quote_token,amount_cents,account_ids,snapshot`).
						WithArgs(int64(99), int64(7), "2026-11", token, int64(1000), "{11}", sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
				}
			}
			if expectedErr == nil {
				mock.ExpectCommit()
			} else {
				mock.ExpectRollback()
			}
			err = store.BindPaymentOrder(context.Background(), 99, 7, token, amount)
			require.ErrorIs(t, err, expectedErr)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAccountPaymentSnapshotAcceptsSchedulerRolloverButRejectsAdminChanges(t *testing.T) {
	account := paymentTestAccount(11, 1000, "unpaid")
	_, snapshots, err := buildPaymentQuote(7, "2026-11", []Account{account}, map[int64]string{11: "2026-01-30"})
	require.NoError(t, err)
	snapshot := snapshots[0]
	account.RenewalDate = paymentDate("2026-12-30")
	now := renewalTestTimeRaw("2026-12-03")
	require.True(t, paymentSnapshotStillValid(snapshot, account, "2026-11", "2026-01-30", now))
	for _, change := range []string{"price", "paid", "history_price", "date", "anchor", "payer"} {
		t.Run(change, func(t *testing.T) {
			a := account
			a.History = append([]History(nil), account.History...)
			anchor := "2026-01-30"
			switch change {
			case "price":
				a.Price++
			case "paid":
				a.History[0].Status = "paid"
			case "history_price":
				a.History[0].Amount++
			case "date":
				a.RenewalDate = paymentDate("2026-12-29")
			case "anchor":
				anchor = "2026-02-28"
			case "payer":
				a.History[0].PayerName = "Changed payer"
			}
			require.False(t, paymentSnapshotStillValid(snapshot, a, "2026-11", anchor, now))
		})
	}
}

func TestFulfillAccountPaymentIsAtomicUsesSnapshotMonthAndDoesNotRepeatRenewal(t *testing.T) {
	for _, scenario := range []string{"success", "repeat", "transferred", "price_changed", "unpaid_gateway_order", "write_failure"} {
		t.Run(scenario, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			defer db.Close()
			store := NewStore(db, "UTC")
			store.now = func() time.Time { return renewalTestTimeRaw("2026-12-01") }
			account := paymentTestAccount(11, 1000, "unpaid")
			_, snapshots, err := buildPaymentQuote(7, "2026-11", []Account{account}, map[int64]string{11: "2026-01-30"})
			require.NoError(t, err)
			raw, err := json.Marshal(snapshots)
			require.NoError(t, err)
			var fulfilled any
			if scenario == "repeat" {
				fulfilled = store.Now()
			}
			mock.ExpectBegin()
			mock.ExpectQuery(`(?s)SELECT billing_month,amount_cents,snapshot,fulfilled_at.*WHERE order_id=\$1 AND user_id=\$2 FOR UPDATE`).
				WithArgs(int64(99), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"month", "amount", "snapshot", "fulfilled_at"}).AddRow("2026-11", 1000, string(raw), fulfilled))
			if scenario == "repeat" {
				mock.ExpectCommit()
				require.NoError(t, store.FulfillPaymentOrder(context.Background(), 99, 7))
				require.NoError(t, mock.ExpectationsWereMet())
				return
			}
			mock.ExpectQuery(`(?s)SELECT EXISTS\(SELECT 1 FROM payment_orders WHERE id=\$1 AND user_id=\$2.*paid_at IS NOT NULL.*amount=\$3`).
				WithArgs(int64(99), int64(7), "10.00").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(scenario != "unpaid_gateway_order"))
			if scenario == "unpaid_gateway_order" {
				mock.ExpectRollback()
				require.ErrorIs(t, store.FulfillPaymentOrder(context.Background(), 99, 7), ErrQuoteChanged)
				require.NoError(t, mock.ExpectationsWereMet())
				return
			}
			expectPaymentOwner(mock, true)
			account.RenewalDate = paymentDate("2026-12-30")
			account.History = append(account.History, History{Month: "2026-12", Amount: 1000, Status: "unpaid"})
			if scenario == "price_changed" {
				account.Price++
			}
			if scenario == "transferred" {
				expectPaymentAccounts(mock)
			} else {
				expectPaymentAccounts(mock, account)
				expectPaymentAnchors(mock, "2026-01-30")
			}
			switch scenario {
			case "transferred", "price_changed":
				mock.ExpectRollback()
				require.ErrorIs(t, store.FulfillPaymentOrder(context.Background(), 99, 7), ErrQuoteChanged)
			case "success", "write_failure":
				account.History[0].Status = "paid"
				history, marshalErr := json.Marshal(account.History)
				require.NoError(t, marshalErr)
				write := mock.ExpectExec(`UPDATE custom_account_billing SET renewal_date=\$1,history=\$2::jsonb,updated_at=NOW\(\) WHERE account_id=\$3`).
					WithArgs("2026-12-30", string(history), int64(11))
				if scenario == "write_failure" {
					write.WillReturnError(errors.New("database write failed"))
					mock.ExpectRollback()
					require.ErrorContains(t, store.FulfillPaymentOrder(context.Background(), 99, 7), "database write failed")
				} else {
					write.WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectExec(`(?s)INSERT INTO custom_account_billing_access.*GREATEST\(custom_account_billing_access.valid_through,EXCLUDED.valid_through\)`).
						WithArgs(int64(11), "2026-12-30").WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectExec(`(?s)INSERT INTO custom_billing_renewal_schedule.*ON CONFLICT\(account_id\) DO NOTHING`).
						WithArgs(int64(11), "2026-01-30").WillReturnResult(sqlmock.NewResult(0, 0))
					mock.ExpectQuery(`(?s)WITH current_balance AS.*UPDATE users AS u SET balance=\$2.*RETURNING \(\$2-current_balance.balance\)::text`).
						WithArgs(int64(7), accountPaymentBalanceTarget).WillReturnRows(sqlmock.NewRows([]string{"credited"}))
					mock.ExpectExec(`UPDATE custom_account_payment_orders SET fulfilled_at=NOW\(\) WHERE order_id=\$1 AND user_id=\$2`).
						WithArgs(int64(99), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
					mock.ExpectCommit()
					require.NoError(t, store.FulfillPaymentOrder(context.Background(), 99, 7))
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
