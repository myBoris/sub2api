package accountbilling

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func expectNoAccessRows(mock sqlmock.Sqlmock, ids string) {
	mock.ExpectQuery(`(?s)SELECT account_id,valid_through FROM custom_account_billing_access.*WHERE account_id=ANY\(\$1\) ORDER BY account_id`).
		WithArgs(ids).WillReturnRows(sqlmock.NewRows([]string{"account_id", "valid_through"}))
}

func TestAccessStatesAllowsTheWholeBusinessDateAndDoesNotDependOnBillingPricesOrPaidStatus(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	store := NewStore(db, "Asia/Tokyo")
	// UTC is still November 21, but the business calendar has crossed midnight.
	store.now = func() time.Time { return time.Date(2026, 11, 21, 15, 0, 0, 0, time.UTC) }
	mock.ExpectQuery(`(?s)SELECT account_id,valid_through FROM custom_account_billing_access.*WHERE account_id=ANY\(\$1\)`).
		WithArgs("{11,12,13,14,15}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "valid_through"}).
		AddRow(11, renewalTestTimeRaw("2026-11-21")).
		AddRow(12, renewalTestTimeRaw("2026-11-22")).
		AddRow(13, renewalTestTimeRaw("2026-11-23")).
		AddRow(14, nil))
	states, err := store.AccessStates(context.Background(), []int64{11, 12, 13, 14, 15, 11, 0, -1})
	require.NoError(t, err)
	require.True(t, states[11].Expired)
	require.False(t, states[12].Expired, "the deadline date itself remains usable")
	require.False(t, states[13].Expired)
	require.False(t, states[14].Expired, "null deadlines have no access restriction")
	require.False(t, states[15].Expired, "accounts outside this extension have no restriction")
	require.Equal(t, paymentDate("2026-11-22"), states[12].ValidThrough)
	require.Len(t, states, 5)
	require.NoError(t, mock.ExpectationsWereMet(), "the query must read only the access table, never roll dates or consult price/payment-status exceptions")
}

func TestAccessStatesEmptyInputIsReadFreeAndDatabaseErrorsAreNotTreatedAsAllowed(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	store := NewStore(db, "UTC")
	states, err := store.AccessStates(context.Background(), []int64{0, -1})
	require.NoError(t, err)
	require.Empty(t, states)
	mock.ExpectQuery(`SELECT account_id,valid_through FROM custom_account_billing_access`).
		WithArgs("{11}").WillReturnError(errors.New("access lookup unavailable"))
	states, err = store.AccessStates(context.Background(), []int64{11})
	require.ErrorContains(t, err, "access lookup unavailable")
	require.Nil(t, states)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestExplicitAccessDateChangeIsDistinctFromSubmittingAnUnchangedDisplayDate(t *testing.T) {
	changed, unchanged := true, false
	for _, test := range []struct {
		name     string
		update   AccountUpdate
		before   *string
		current  *string
		expected bool
	}{
		{"explicit same display date resets the real deadline", AccountUpdate{RenewalDate: paymentDate("2027-03-30"), RenewalDateChanged: &changed}, paymentDate("2027-03-30"), paymentDate("2027-03-30"), true},
		{"stale unchanged form must not restore expired access", AccountUpdate{RenewalDate: paymentDate("2027-02-28"), RenewalDateChanged: &unchanged}, paymentDate("2027-03-30"), paymentDate("2027-03-30"), false},
		{"legacy same date must not reset access", AccountUpdate{RenewalDate: paymentDate("2027-03-30")}, paymentDate("2027-03-30"), paymentDate("2027-03-30"), false},
		{"legacy pre-rollover date must not reset access", AccountUpdate{RenewalDate: paymentDate("2027-02-28")}, paymentDate("2027-02-28"), paymentDate("2027-03-30"), false},
		{"clear configured date", AccountUpdate{}, paymentDate("2027-03-30"), paymentDate("2027-03-30"), true},
		{"already null stays null", AccountUpdate{}, nil, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.expected, accessDateExplicitlyChanged(test.update, test.before, test.current))
		})
	}
}

func TestPaymentQuoteAndSnapshotBecomeInvalidWhenOnlyTheRealDeadlineChanges(t *testing.T) {
	account := paymentTestAccount(11, 1000, "unpaid")
	account.ValidThrough = paymentDate("2026-10-30")
	account.Expired = true
	quote, snapshots, err := buildPaymentQuote(7, "2026-11", []Account{account}, map[int64]string{11: "2026-01-30"})
	require.NoError(t, err)
	account.ValidThrough = paymentDate("2026-11-30")
	account.Expired = false
	changed, _, err := buildPaymentQuote(7, "2026-11", []Account{account}, map[int64]string{11: "2026-01-30"})
	require.NoError(t, err)
	require.NotEqual(t, quote.QuoteToken, changed.QuoteToken)
	require.False(t, paymentSnapshotStillValid(snapshots[0], account, "2026-11", "2026-01-30", renewalTestTimeRaw("2026-11-22")))
	// Merely crossing midnight must not reject a paid callback: the purchased
	// deadline is unchanged, even though the derived expired boolean has changed.
	account.ValidThrough = snapshots[0].ValidThrough
	account.Expired = !snapshots[0].Expired
	require.True(t, paymentSnapshotStillValid(snapshots[0], account, "2026-11", "2026-01-30", renewalTestTimeRaw("2026-11-22")))
}

func TestLegacyPaymentSnapshotsRemainFulfillableAfterAccessDeadlineMigration(t *testing.T) {
	var legacy paymentAccountSnapshot
	require.NoError(t, json.Unmarshal([]byte(`{"account_id":11,"account_name":"Owned account","price":10.00,"renewal_date":"2026-11-30","billing_price":10.00,"anchor_date":"2026-01-30","history":{"month":"2026-11","status":"unpaid","amount":10.00}}`), &legacy))
	require.Zero(t, legacy.AccessSchemaVersion)
	require.Nil(t, legacy.ValidThrough)
	account := paymentTestAccount(11, 1000, "unpaid")
	account.ValidThrough = paymentDate("2026-11-30")
	require.True(t, paymentSnapshotStillValid(legacy, account, "2026-11", "2026-01-30", renewalTestTimeRaw("2026-11-22")),
		"migration initialization must not reject the normal paid callback of a pre-upgrade order")
	account.RenewalDate = paymentDate("2026-12-30")
	account.ValidThrough = paymentDate("2026-12-30")
	require.True(t, paymentSnapshotStillValid(legacy, account, "2026-11", "2026-01-30", renewalTestTimeRaw("2026-12-01")),
		"a legacy callback may cross a display rollover and migration boundary")
	account.History[0].Status = "paid"
	require.False(t, paymentSnapshotStillValid(legacy, account, "2026-11", "2026-01-30", renewalTestTimeRaw("2026-12-01")),
		"legacy compatibility must retain the duplicate-billing protection")
	account.History[0].Status = "unpaid"
	account.Price++
	require.False(t, paymentSnapshotStillValid(legacy, account, "2026-11", "2026-01-30", renewalTestTimeRaw("2026-12-01")),
		"legacy compatibility must retain all original price checks")
	// The null captured by a new snapshot is meaningful and must not be mistaken
	// for a missing field from the old snapshot format.
	account.Price--
	account.RenewalDate = paymentDate("2026-11-30")
	account.ValidThrough = nil
	_, snapshots, err := buildPaymentQuote(7, "2026-11", []Account{account}, map[int64]string{11: "2026-01-30"})
	require.NoError(t, err)
	require.Equal(t, 1, snapshots[0].AccessSchemaVersion)
	account.ValidThrough = paymentDate("2026-11-30")
	require.False(t, paymentSnapshotStillValid(snapshots[0], account, "2026-11", "2026-01-30", renewalTestTimeRaw("2026-11-22")))
}
