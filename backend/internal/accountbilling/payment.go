package accountbilling

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

var (
	ErrQuoteChanged         = errors.New("account billing quote changed; refresh and confirm the latest amount")
	ErrPaymentPending       = errors.New("an account payment is already pending or awaiting fulfillment")
	ErrNoAmountDue          = errors.New("there is no unpaid account amount for this month")
	ErrRenewalNotConfigured = errors.New("an unpaid account has no renewal date; contact the administrator")
)

type PaymentQuoteAccount struct {
	AccessState
	AccountID   int64   `json:"account_id"`
	AccountName string  `json:"account_name"`
	Price       Money   `json:"price"`
	RenewalDate *string `json:"renewal_date"`
}

type PaymentQuote struct {
	Month      string                `json:"month"`
	AmountDue  Money                 `json:"amount_due"`
	Currency   string                `json:"currency"`
	QuoteToken string                `json:"quote_token"`
	Accounts   []PaymentQuoteAccount `json:"accounts"`
}

type paymentAccountSnapshot struct {
	PaymentQuoteAccount
	AccessSchemaVersion int     `json:"access_schema_version,omitempty"`
	BillingPrice        Money   `json:"billing_price"`
	BillingCost         Money   `json:"billing_cost,omitempty"`
	AnchorDate          *string `json:"anchor_date"`
	History             History `json:"history"`
}

// Quotes hold shared owner locks. Fulfillment takes the user write lock before
// billing rows so simultaneous orders cannot deadlock while upgrading locks to
// replenish the same user's balance. Ownership itself is never edited here.
func lockPaymentOwner(ctx context.Context, tx *sql.Tx, userID int64, replenishBalance ...bool) error {
	if userID <= 0 {
		return ErrForbidden
	}
	var id int64
	query := `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL AND status='active' FOR SHARE`
	if len(replenishBalance) > 0 && replenishBalance[0] {
		query = `SELECT id FROM users WHERE id=$1 AND deleted_at IS NULL AND status='active' FOR NO KEY UPDATE`
	}
	err := tx.QueryRowContext(ctx, query, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrForbidden
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT account_id FROM custom_user_accounts WHERE user_id=$1 ORDER BY account_id FOR SHARE`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err = rows.Scan(&id); err != nil {
			return err
		}
	}
	return rows.Err()
}

func paymentAnchors(ctx context.Context, tx *sql.Tx, accounts []Account) (map[int64]string, error) {
	anchors := make(map[int64]string, len(accounts))
	if len(accounts) == 0 {
		return anchors, nil
	}
	ids := make([]int64, 0, len(accounts))
	for _, a := range accounts {
		ids = append(ids, a.AccountID)
	}
	rows, err := tx.QueryContext(ctx, `SELECT account_id,anchor_date FROM custom_billing_renewal_schedule WHERE account_id=ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var date time.Time
		if err = rows.Scan(&id, &date); err != nil {
			return nil, err
		}
		anchors[id] = date.Format("2006-01-02")
	}
	return anchors, rows.Err()
}

func buildPaymentQuote(userID int64, month string, accounts []Account, anchors map[int64]string) (*PaymentQuote, []paymentAccountSnapshot, error) {
	quote := &PaymentQuote{Month: month, Currency: "CNY", Accounts: []PaymentQuoteAccount{}}
	snapshots := []paymentAccountSnapshot{}
	for _, a := range accounts {
		history, ok := historyFor(a, month)
		if !ok || history.Status == "paid" || history.Amount == 0 {
			continue
		}
		if history.Amount < 0 || history.Amount > 999999999999 || quote.AmountDue > Money(math.MaxInt64)-history.Amount {
			return nil, nil, ErrInvalid
		}
		item := PaymentQuoteAccount{AccessState: a.AccessState, AccountID: a.AccountID, AccountName: a.AccountName, Price: history.Amount, RenewalDate: a.RenewalDate}
		var anchor *string
		if value, exists := anchors[a.AccountID]; exists {
			anchor = &value
		} else if a.RenewalDate != nil {
			value := *a.RenewalDate
			anchor = &value
		}
		quote.Accounts = append(quote.Accounts, item)
		quote.AmountDue += item.Price
		snapshots = append(snapshots, paymentAccountSnapshot{PaymentQuoteAccount: item, AccessSchemaVersion: 1, BillingPrice: a.Price, BillingCost: a.Cost, AnchorDate: anchor, History: history})
	}
	raw, err := json.Marshal(struct {
		UserID    int64                    `json:"user_id"`
		Month     string                   `json:"month"`
		Snapshots []paymentAccountSnapshot `json:"snapshots"`
	}{userID, month, snapshots})
	if err != nil {
		return nil, nil, err
	}
	digest := sha256.Sum256(raw)
	quote.QuoteToken = hex.EncodeToString(digest[:])
	return quote, snapshots, nil
}

func (s *Store) paymentQuoteInTx(ctx context.Context, tx *sql.Tx, userID int64) (*PaymentQuote, []paymentAccountSnapshot, error) {
	if err := lockPaymentOwner(ctx, tx, userID); err != nil {
		return nil, nil, err
	}
	accounts, err := loadAccounts(ctx, tx, userID, 0)
	if err != nil {
		return nil, nil, err
	}
	now := s.Now()
	if err = refreshRenewalDates(ctx, tx, accounts, now); err != nil {
		return nil, nil, err
	}
	if err = refreshHistory(ctx, tx, accounts, now); err != nil {
		return nil, nil, err
	}
	if err = attachAccessStates(ctx, tx, accounts, now); err != nil {
		return nil, nil, err
	}
	anchors, err := paymentAnchors(ctx, tx, accounts)
	if err != nil {
		return nil, nil, err
	}
	return buildPaymentQuote(userID, now.Format("2006-01"), accounts, anchors)
}

// PaymentQuote returns only the current owner's positive unpaid monthly prices.
// Its token is a digest, not an authority: BindPaymentOrder always rebuilds and
// verifies the quote while holding the underlying billing and ownership locks.
func (s *Store) PaymentQuote(ctx context.Context, userID int64) (*PaymentQuote, error) {
	if userID <= 0 {
		return nil, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	quote, _, err := s.paymentQuoteInTx(ctx, tx, userID)
	if err != nil {
		return nil, err
	}
	return quote, tx.Commit()
}

func exactPaymentCents(amount float64) (Money, error) {
	if math.IsNaN(amount) || math.IsInf(amount, 0) || amount <= 0 {
		return 0, ErrInvalid
	}
	cents := decimal.NewFromFloat(amount).Shift(2)
	if !cents.Equal(cents.Truncate(0)) || cents.GreaterThan(decimal.NewFromInt(math.MaxInt64)) {
		return 0, ErrInvalid
	}
	return Money(cents.IntPart()), nil
}

// BindPaymentOrder runs before contacting the provider. The gateway order must
// already exist so unsuccessful/cancelled/expired attempts can be distinguished
// from orders which have actually received money and still need fulfillment.
func (s *Store) BindPaymentOrder(ctx context.Context, orderID, userID int64, quoteToken string, amountCNY float64) error {
	amount, err := exactPaymentCents(amountCNY)
	if err != nil || orderID <= 0 || len(quoteToken) != 64 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	quote, snapshots, err := s.paymentQuoteInTx(ctx, tx, userID)
	if err != nil {
		return err
	}
	if quote.AmountDue <= 0 {
		return ErrNoAmountDue
	}
	if quote.QuoteToken != quoteToken || amount != quote.AmountDue {
		return ErrQuoteChanged
	}
	ids := make([]int64, 0, len(snapshots))
	for _, item := range snapshots {
		if item.RenewalDate == nil {
			return ErrRenewalNotConfigured
		}
		ids = append(ids, item.AccountID)
	}
	var validOrder bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM payment_orders WHERE id=$1 AND user_id=$2
		AND order_type='account' AND status='PENDING' AND expires_at>NOW() AND amount=$3)`, orderID, userID, amountCNY).Scan(&validOrder)
	if err != nil {
		return err
	}
	if !validOrder {
		return ErrQuoteChanged
	}
	var pending bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM custom_account_payment_orders c
		JOIN payment_orders p ON p.id=c.order_id WHERE c.billing_month=$1 AND c.account_ids && $2::bigint[]
		AND c.order_id<>$3 AND c.fulfilled_at IS NULL AND (p.paid_at IS NOT NULL
		OR p.status IN ('PAID','RECHARGING') OR (p.status='PENDING' AND p.expires_at>NOW())
		OR (p.status='EXPIRED' AND p.updated_at>NOW()-INTERVAL '5 minutes')))`, quote.Month, pq.Array(ids), orderID).Scan(&pending)
	if err != nil {
		return err
	}
	if pending {
		return ErrPaymentPending
	}
	raw, err := json.Marshal(snapshots)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO custom_account_payment_orders
		(order_id,user_id,billing_month,quote_token,amount_cents,account_ids,snapshot)
		VALUES($1,$2,$3,$4,$5,$6,$7::jsonb)`, orderID, userID, quote.Month, quote.QuoteToken, quote.AmountDue, pq.Array(ids), string(raw))
	if err != nil {
		return databaseError(err)
	}
	return tx.Commit()
}

func nextPaidRenewalDate(month string, snapshot paymentAccountSnapshot, current *string) (string, error) {
	if !ValidMonth(month) || snapshot.RenewalDate == nil || snapshot.AnchorDate == nil {
		return "", ErrRenewalNotConfigured
	}
	anchor, err := time.Parse("2006-01-02", *snapshot.AnchorDate)
	if err != nil {
		return "", ErrInvalid
	}
	billed, _ := time.Parse("2006-01", month)
	nextMonth := billed.AddDate(0, 1, 0)
	target := projectRenewalDate(anchor, nextMonth).Format("2006-01-02")
	if current != nil && *current > target {
		target = *current
	}
	return target, nil
}

func paymentSnapshotStillValid(snapshot paymentAccountSnapshot, account Account, month string, anchor string, now time.Time) bool {
	if snapshot.BillingPrice != account.Price || snapshot.BillingCost != account.Cost || snapshot.RenewalDate == nil || account.RenewalDate == nil || snapshot.AnchorDate == nil || *snapshot.AnchorDate != anchor {
		return false
	}
	// Orders bound before migration 257 never captured an independent access
	// deadline. Preserve their original fulfillment contract instead of treating
	// the migration's newly initialized date as an administrator edit. Every new
	// snapshot explicitly versions the deadline, including an intentional null.
	if (snapshot.AccessSchemaVersion > 0 || snapshot.ValidThrough != nil) && !sameAccessDate(snapshot.ValidThrough, account.ValidThrough) {
		return false
	}
	history, ok := historyFor(account, month)
	samePayer := (history.PayerID == nil && snapshot.History.PayerID == nil) ||
		(history.PayerID != nil && snapshot.History.PayerID != nil && *history.PayerID == *snapshot.History.PayerID)
	if !ok || history.Status != "unpaid" || history.Amount != snapshot.Price || history.Cost != snapshot.History.Cost || !samePayer || history.PayerName != snapshot.History.PayerName {
		return false
	}
	if sameRenewalDate(snapshot.RenewalDate, account.RenewalDate) {
		return true
	}
	// A delayed callback can cross a month while the independent scheduler has
	// advanced the date. Accept that deterministic projection, but no admin edit.
	parsed, err := time.Parse("2006-01-02", anchor)
	return err == nil && projectRenewalDate(parsed, now).Format("2006-01-02") == *account.RenewalDate
}

// FulfillPaymentOrder uses the bound month and immutable prices even when the
// callback arrives in a later month. All account updates and the idempotency
// marker commit together, so a repeated callback never advances a second time.
func (s *Store) FulfillPaymentOrder(ctx context.Context, orderID, userID int64) error {
	if orderID <= 0 || userID <= 0 {
		return ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var month string
	var amount Money
	var raw []byte
	var fulfilled sql.NullTime
	err = tx.QueryRowContext(ctx, `SELECT billing_month,amount_cents,snapshot,fulfilled_at
		FROM custom_account_payment_orders WHERE order_id=$1 AND user_id=$2 FOR UPDATE`, orderID, userID).Scan(&month, &amount, &raw, &fulfilled)
	if err != nil {
		return databaseError(err)
	}
	if fulfilled.Valid {
		return tx.Commit()
	}
	var snapshots []paymentAccountSnapshot
	if err = json.Unmarshal(raw, &snapshots); err != nil {
		return fmt.Errorf("decode account payment snapshot: %w", err)
	}
	if len(snapshots) == 0 || !ValidMonth(month) || amount <= 0 {
		return ErrInvalid
	}
	// Never allow a new unpaid order to call this method directly. The common
	// payment flow verifies the provider first, and the stored paid marker gives
	// this isolated module an independent check before altering billing records.
	var paidOrder bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM payment_orders WHERE id=$1 AND user_id=$2
		AND order_type='account' AND paid_at IS NOT NULL AND status IN ('PAID','RECHARGING','FAILED') AND amount=$3)`, orderID, userID, decimal.NewFromInt(int64(amount)).Shift(-2).StringFixed(2)).Scan(&paidOrder)
	if err != nil {
		return err
	}
	if !paidOrder {
		return ErrQuoteChanged
	}
	if err = lockPaymentOwner(ctx, tx, userID, true); err != nil {
		return err
	}
	accounts, err := loadAccounts(ctx, tx, userID, 0)
	if err != nil {
		return err
	}
	if err = attachAccessStates(ctx, tx, accounts, s.Now()); err != nil {
		return err
	}
	anchors, err := paymentAnchors(ctx, tx, accounts)
	if err != nil {
		return err
	}
	byID := make(map[int64]Account, len(accounts))
	for _, account := range accounts {
		byID[account.AccountID] = account
	}
	var total Money
	seen := make(map[int64]bool, len(snapshots))
	for _, snapshot := range snapshots {
		account, exists := byID[snapshot.AccountID]
		anchor := anchors[snapshot.AccountID]
		if anchor == "" && account.RenewalDate != nil {
			anchor = *account.RenewalDate
		}
		if !exists || seen[snapshot.AccountID] || snapshot.Price <= 0 || !paymentSnapshotStillValid(snapshot, account, month, anchor, s.Now()) || total > Money(math.MaxInt64)-snapshot.Price {
			return ErrQuoteChanged
		}
		seen[snapshot.AccountID] = true
		total += snapshot.Price
	}
	if total != amount {
		return ErrQuoteChanged
	}
	for _, snapshot := range snapshots {
		account := byID[snapshot.AccountID]
		date, dateErr := nextPaidRenewalDate(month, snapshot, account.RenewalDate)
		if dateErr != nil {
			return dateErr
		}
		entry := snapshot.History
		entry.Status = "paid"
		entry.Amount = snapshot.Price
		setHistory(&account, entry)
		history, marshalErr := historyJSON(account.History)
		if marshalErr != nil {
			return marshalErr
		}
		if _, err = tx.ExecContext(ctx, `UPDATE custom_account_billing SET renewal_date=$1,history=$2::jsonb,updated_at=NOW() WHERE account_id=$3`, date, string(history), account.AccountID); err != nil {
			return err
		}
		if err = extendAccessDate(ctx, tx, account.AccountID, date); err != nil {
			return err
		}
		// Repair a missing schedule while keeping its original day; the updated
		// displayed date must never replace a January-30 anchor with February-28.
		if _, err = tx.ExecContext(ctx, `INSERT INTO custom_billing_renewal_schedule(account_id,anchor_date)
			VALUES($1,$2) ON CONFLICT(account_id) DO NOTHING`, account.AccountID, *snapshot.AnchorDate); err != nil {
			return err
		}
	}
	if err = replenishAccountPaymentBalance(ctx, tx, orderID, userID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE custom_account_payment_orders SET fulfilled_at=NOW() WHERE order_id=$1 AND user_id=$2`, orderID, userID); err != nil {
		return err
	}
	return tx.Commit()
}
