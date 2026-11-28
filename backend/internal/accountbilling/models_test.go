package accountbilling

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func billingTestInt(id int64) *int64      { return &id }
func billingTestDate(date string) *string { return &date }

func TestMoneyUsesExactCentsInJSON(t *testing.T) {
	for _, tt := range []struct {
		input  string
		cents  Money
		output string
	}{
		{"0", 0, "0.00"},
		{"0.10", 10, "0.10"},
		{"0.2", 20, "0.20"},
		{"123.45", 12345, "123.45"},
		{"1e2", 10000, "100.00"},
		{"9999999999.99", 999999999999, "9999999999.99"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			var amount Money
			require.NoError(t, json.Unmarshal([]byte(tt.input), &amount))
			require.Equal(t, tt.cents, amount)
			encoded, err := json.Marshal(amount)
			require.NoError(t, err)
			require.Equal(t, tt.output, string(encoded))
		})
	}
	var first, second Money
	require.NoError(t, json.Unmarshal([]byte("0.10"), &first))
	require.NoError(t, json.Unmarshal([]byte("0.20"), &second))
	encoded, err := json.Marshal(first + second)
	require.NoError(t, err)
	require.Equal(t, "0.30", string(encoded))
}

func TestMoneyRejectsInvalidAmounts(t *testing.T) {
	for _, raw := range []string{"null", `"1.20"`, "-0.01", "0.001", "1.234", "10000000000", "1e100", "1e999999999", "1e-999999999", "true", "{}", "[]", "NaN", ""} {
		t.Run(raw, func(t *testing.T) {
			var amount Money
			require.Error(t, json.Unmarshal([]byte(raw), &amount))
		})
	}
}

func TestHistoryRolloverPreservesPriorPaymentsAndFillsSkippedMonths(t *testing.T) {
	location := time.FixedZone("test", 9*60*60)
	account := Account{
		Price:     15000,
		PayerID:   billingTestInt(2),
		PayerName: "New payer",
		createdAt: time.Date(2026, 12, 31, 23, 0, 0, 0, location),
		History:   []History{{Month: "2026-12", Status: "paid", Amount: 12000, PayerID: billingTestInt(1), PayerName: "Original payer"}},
	}
	now := time.Date(2027, 2, 1, 0, 0, 0, 0, location)
	require.True(t, fillHistory(&account, now))
	require.Len(t, account.History, 3)
	require.Equal(t, "2027-02", account.History[0].Month)
	old, exists := historyFor(account, "2026-12")
	require.True(t, exists)
	require.Equal(t, "paid", old.Status)
	require.Equal(t, Money(12000), old.Amount)
	require.EqualValues(t, 1, *old.PayerID)
	require.Equal(t, "Original payer", old.PayerName)
	for _, month := range []string{"2027-01", "2027-02"} {
		entry, exists := historyFor(account, month)
		require.True(t, exists)
		require.Equal(t, "unpaid", entry.Status)
		require.Equal(t, Money(15000), entry.Amount)
		require.EqualValues(t, 2, *entry.PayerID)
	}
	require.False(t, fillHistory(&account, now), "repeated access must not add duplicate entries")
	_, exists = historyFor(account, "2026-11")
	require.False(t, exists, "do not generate bills before the account entered the ledger")
}

func TestHistoryCreationUsesBusinessTimezone(t *testing.T) {
	location := time.FixedZone("test", 9*60*60)
	account := Account{Price: 100, createdAt: time.Date(2026, 12, 31, 16, 0, 0, 0, time.UTC)}
	require.True(t, fillHistory(&account, time.Date(2027, 1, 1, 1, 0, 0, 0, location)))
	require.Equal(t, []History{{Month: "2027-01", Status: "unpaid", Amount: 100}}, account.History)
}

func TestApplyingHistoryKeepsHistoricalPayerNamesAndReflectsCurrentRenames(t *testing.T) {
	for _, tt := range []struct {
		name         string
		current      bool
		accountPayer *int64
		entryPayer   *int64
		wantName     string
	}{
		{name: "current matching payer renamed", current: true, accountPayer: billingTestInt(1), entryPayer: billingTestInt(1), wantName: "Renamed payer"},
		{name: "past month retains name", current: false, accountPayer: billingTestInt(1), entryPayer: billingTestInt(1), wantName: "Original payer"},
		{name: "different payer retains snapshot", current: true, accountPayer: billingTestInt(2), entryPayer: billingTestInt(1), wantName: "Original payer"},
		{name: "unassigned current snapshot", current: true, accountPayer: billingTestInt(2), entryPayer: nil, wantName: "Original payer"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			account := Account{PayerID: tt.accountPayer, PayerName: "Renamed payer", Price: 999, PaymentStatus: "unpaid"}
			entry := History{Month: "2026-11", Status: "paid", Amount: 123, PayerID: tt.entryPayer, PayerName: "Original payer"}
			applyHistory(&account, entry, tt.current)
			require.Equal(t, tt.wantName, account.PayerName)
			require.Equal(t, entry.PayerID, account.PayerID)
			require.Equal(t, entry.Amount, account.Price)
			require.Equal(t, "paid", account.PaymentStatus)
		})
	}
}

func TestSummaryCountsThreeDaysAcrossMonthEndAndRetainsZeroPayers(t *testing.T) {
	now := time.Date(2026, 11, 30, 23, 59, 0, 0, time.FixedZone("test", 9*60*60))
	accounts := []Account{
		{PaymentStatus: "paid", Price: 10, Cost: 3, PayerID: billingTestInt(1), RenewalDate: billingTestDate("2026-11-30")},
		{PaymentStatus: "unpaid", Price: 20, Cost: 7, PayerID: billingTestInt(1), RenewalDate: billingTestDate("2026-12-03")},
		{PaymentStatus: "unpaid", Price: 99, Cost: 51, PayerID: billingTestInt(1), RenewalDate: billingTestDate("2026-12-04")},
		{PaymentStatus: "paid", Price: 50, Cost: 17, RenewalDate: billingTestDate("2026-11-29")},
		{PaymentStatus: "unpaid", Price: 70, Cost: 23, RenewalDate: nil},
		{PaymentStatus: "unpaid", Price: 5, Cost: 2, PayerID: billingTestInt(9), PayerName: "Historical payer", RenewalDate: billingTestDate("2026-12-01")},
	}
	summary := summarize(accounts, now, []Payer{{ID: 1, Name: "Assigned"}, {ID: 2, Name: "No accounts"}})
	require.Equal(t, 2, summary.PaidCount)
	require.Equal(t, 4, summary.UnpaidCount)
	require.Equal(t, 3, summary.DueSoonCount)
	require.Len(t, summary.Payers, 4)
	require.Equal(t, Money(61), summary.Payers[0].Total)
	require.EqualValues(t, 1, *summary.Payers[0].PayerID)
	require.Equal(t, Money(0), summary.Payers[1].Total)
	require.EqualValues(t, 2, *summary.Payers[1].PayerID)
	require.Nil(t, summary.Payers[2].PayerID)
	require.Equal(t, Money(40), summary.Payers[2].Total)
	require.EqualValues(t, 9, *summary.Payers[3].PayerID)
	require.Equal(t, "Historical payer", summary.Payers[3].PayerName)
}

func TestValidateUpdateCalendarAndReferences(t *testing.T) {
	valid := AccountUpdate{PaymentStatus: "unpaid", Price: 0}
	require.NoError(t, ValidateUpdate(valid))
	for _, date := range []string{"2028-02-29", "2026-12-31"} {
		update := valid
		update.RenewalDate = billingTestDate(date)
		require.NoError(t, ValidateUpdate(update))
	}
	for _, date := range []string{"2027-02-29", "2026-13-01", "2026-01-32", "2026-1-01", "1999-12-31", ""} {
		update := valid
		update.RenewalDate = billingTestDate(date)
		require.ErrorIs(t, ValidateUpdate(update), ErrInvalid, date)
	}
	for _, update := range []AccountUpdate{
		{PaymentStatus: "paid", Price: -1},
		{PaymentStatus: "paid", Price: 1000000000000},
		{PaymentStatus: "paid", Cost: costValue(-1)},
		{PaymentStatus: "paid", Cost: costValue(1000000000000)},
		{PaymentStatus: "pending", Price: 100},
		{PaymentStatus: "paid", PlanID: billingTestInt(0)},
		{PaymentStatus: "paid", PayerID: billingTestInt(-1)},
	} {
		require.ErrorIs(t, ValidateUpdate(update), ErrInvalid)
	}
	for _, month := range []string{"2026-01", "2028-02", "2000-01"} {
		require.True(t, ValidMonth(month), month)
	}
	for _, month := range []string{"2026-1", "2026-00", "2026-13", "1999-12", "2026-01-01", ""} {
		require.False(t, ValidMonth(month), month)
	}
}
