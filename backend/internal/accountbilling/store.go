package accountbilling

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

type Store struct {
	db       *sql.DB
	location *time.Location
	now      func() time.Time
}

func NewStore(db *sql.DB, timezone string) *Store {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		location = time.FixedZone("Asia/Shanghai", 8*60*60)
	}
	return &Store{db: db, location: location, now: time.Now}
}

func (s *Store) Now() time.Time { return s.now().In(s.location) }

func databaseError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	var pg *pq.Error
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505", "23503", "23001":
			return ErrConflict
		case "23514", "22003", "22001":
			return ErrInvalid
		}
	}
	return err
}

func (s *Store) Catalog(ctx context.Context) (Catalog, error) {
	c := Catalog{Plans: []Plan{}, Payers: []Payer{}}
	rows, err := s.db.QueryContext(ctx, `SELECT id,platform,name,price_cents FROM custom_billing_plans ORDER BY platform,name,id`)
	if err != nil {
		return c, err
	}
	for rows.Next() {
		var p Plan
		if err = rows.Scan(&p.ID, &p.Platform, &p.Name, &p.Price); err != nil {
			_ = rows.Close()
			return c, err
		}
		c.Plans = append(c.Plans, p)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return c, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT id,name FROM custom_billing_payers ORDER BY name,id`)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Payer
		if err = rows.Scan(&p.ID, &p.Name); err != nil {
			return c, err
		}
		c.Payers = append(c.Payers, p)
	}
	return c, rows.Err()
}

func (s *Store) SavePlan(ctx context.Context, id int64, p Plan) (Plan, error) {
	p.Platform = strings.TrimSpace(p.Platform)
	p.Name = strings.TrimSpace(p.Name)
	if !validName(p.Platform) || !validName(p.Name) || p.Price < 0 || p.Price > 999999999999 {
		return p, ErrInvalid
	}
	var err error
	if id == 0 {
		err = s.db.QueryRowContext(ctx, `INSERT INTO custom_billing_plans(platform,name,price_cents) VALUES($1,$2,$3) RETURNING id`, p.Platform, p.Name, p.Price).Scan(&p.ID)
	} else {
		err = s.db.QueryRowContext(ctx, `UPDATE custom_billing_plans SET platform=$1,name=$2,price_cents=$3 WHERE id=$4 RETURNING id`, p.Platform, p.Name, p.Price, id).Scan(&p.ID)
	}
	return p, databaseError(err)
}

func (s *Store) SavePayer(ctx context.Context, id int64, p Payer) (Payer, error) {
	p.Name = strings.TrimSpace(p.Name)
	if !validName(p.Name) {
		return p, ErrInvalid
	}
	var err error
	if id == 0 {
		err = s.db.QueryRowContext(ctx, `INSERT INTO custom_billing_payers(name) VALUES($1) RETURNING id`, p.Name).Scan(&p.ID)
	} else {
		err = s.db.QueryRowContext(ctx, `UPDATE custom_billing_payers SET name=$1 WHERE id=$2 RETURNING id`, p.Name, id).Scan(&p.ID)
	}
	return p, databaseError(err)
}

func (s *Store) DeleteCatalog(ctx context.Context, kind string, id int64) error {
	query := `DELETE FROM custom_billing_plans WHERE id=$1`
	if kind == "payers" {
		query = `DELETE FROM custom_billing_payers WHERE id=$1`
	}
	r, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return databaseError(err)
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) SyncAccounts(ctx context.Context) (int64, error) {
	// Only new rows are inserted. Existing renewal dates, prices, plans, payers
	// and historical payment records are never overwritten by synchronization.
	// Capture each new row's initial date atomically, including accounts without
	// a date in the returned count and without touching existing schedules.
	var count int64
	err := s.db.QueryRowContext(ctx, `WITH inserted AS (
		INSERT INTO custom_account_billing(account_id,renewal_date)
		SELECT id,(expires_at AT TIME ZONE $1)::date FROM accounts WHERE deleted_at IS NULL
		ON CONFLICT(account_id) DO NOTHING RETURNING account_id,renewal_date
	), schedules AS (
		INSERT INTO custom_billing_renewal_schedule(account_id,anchor_date)
		SELECT account_id,renewal_date FROM inserted WHERE renewal_date IS NOT NULL
		ON CONFLICT(account_id) DO NOTHING
	), access_dates AS (
		INSERT INTO custom_account_billing_access(account_id,valid_through)
		SELECT account_id,renewal_date FROM inserted
		ON CONFLICT(account_id) DO NOTHING
	) SELECT count(*) FROM inserted`, s.location.String()).Scan(&count)
	return count, err
}

func loadAccounts(ctx context.Context, tx *sql.Tx, userID, accountID int64) ([]Account, error) {
	query := `SELECT b.account_id,a.name,COALESCE(p.platform,a.platform),b.renewal_date,
		b.plan_id,COALESCE(p.name,''),b.price_cents,b.payer_id,COALESCE(y.name,''),b.history,b.created_at
		FROM custom_account_billing b JOIN accounts a ON a.id=b.account_id
		LEFT JOIN custom_billing_plans p ON p.id=b.plan_id
		LEFT JOIN custom_billing_payers y ON y.id=b.payer_id
		WHERE a.deleted_at IS NULL`
	args := []any{}
	if userID > 0 {
		args = append(args, userID)
		query += fmt.Sprintf(` AND EXISTS(SELECT 1 FROM custom_user_accounts o WHERE o.account_id=b.account_id AND o.user_id=$%d)`, len(args))
	}
	if accountID > 0 {
		args = append(args, accountID)
		query += fmt.Sprintf(` AND b.account_id=$%d`, len(args))
	}
	query += ` ORDER BY b.account_id FOR UPDATE OF b`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := []Account{}
	for rows.Next() {
		var a Account
		var date sql.NullTime
		var raw []byte
		if err = rows.Scan(&a.AccountID, &a.AccountName, &a.Platform, &date, &a.PlanID, &a.PlanName, &a.Price, &a.PayerID, &a.PayerName, &raw, &a.createdAt); err != nil {
			return nil, err
		}
		if date.Valid {
			formatted := date.Time.Format("2006-01-02")
			a.RenewalDate = &formatted
		}
		if err = json.Unmarshal(raw, &a.History); err != nil {
			return nil, fmt.Errorf("decode billing history: %w", err)
		}
		if a.History == nil {
			a.History = []History{}
		}
		accounts = append(accounts, a)
	}
	return accounts, rows.Err()
}

func saveHistory(ctx context.Context, tx *sql.Tx, a Account) error {
	raw, err := historyJSON(a.History)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE custom_account_billing SET history=$1::jsonb,updated_at=NOW() WHERE account_id=$2`, string(raw), a.AccountID)
	return err
}

func refreshHistory(ctx context.Context, tx *sql.Tx, accounts []Account, now time.Time) error {
	for i := range accounts {
		if fillHistory(&accounts[i], now) {
			if err := saveHistory(ctx, tx, accounts[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// List freezes each month's initial amount and payer before exposing derived
// statuses. Rows are locked so edits and concurrent rollover cannot lose history.
func (s *Store) List(ctx context.Context, month string) (AccountList, error) {
	now := s.Now()
	if month == "" {
		month = now.Format("2006-01")
	}
	result := AccountList{Month: month, Items: []Account{}}
	if !ValidMonth(month) || month > now.Format("2006-01") {
		return result, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	accounts, err := loadAccounts(ctx, tx, 0, 0)
	if err != nil {
		return result, err
	}
	if err = refreshRenewalDates(ctx, tx, accounts, now); err != nil {
		return result, err
	}
	if err = refreshHistory(ctx, tx, accounts, now); err != nil {
		return result, err
	}
	if err = attachAccessStates(ctx, tx, accounts, now); err != nil {
		return result, err
	}
	for _, a := range accounts {
		h, ok := historyFor(a, month)
		if !ok {
			continue
		}
		applyHistory(&a, h, month == now.Format("2006-01"))
		result.Items = append(result.Items, a)
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	catalog, err := s.Catalog(ctx)
	if err != nil {
		return result, err
	}
	result.Summary = summarize(result.Items, now, catalog.Payers)
	return result, nil
}

func (s *Store) UpdateAccount(ctx context.Context, id int64, u AccountUpdate) (Account, error) {
	if err := ValidateUpdate(u); err != nil {
		return Account{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	accounts, err := loadAccounts(ctx, tx, 0, id)
	if err != nil {
		return Account{}, err
	}
	if len(accounts) == 0 {
		return Account{}, ErrNotFound
	}
	now := s.Now()
	beforeRollover := accounts[0].RenewalDate
	if err = refreshRenewalDates(ctx, tx, accounts, now); err != nil {
		return Account{}, err
	}
	a := accounts[0]
	fillHistory(&a, now)
	// Validate references before writing, including friendly errors for stale forms.
	if u.PlanID != nil {
		if err = tx.QueryRowContext(ctx, `SELECT platform,name FROM custom_billing_plans WHERE id=$1`, *u.PlanID).Scan(&a.Platform, &a.PlanName); err != nil {
			return a, databaseError(err)
		}
	} else {
		a.PlanName = ""
		if err = tx.QueryRowContext(ctx, `SELECT platform FROM accounts WHERE id=$1`, id).Scan(&a.Platform); err != nil {
			return a, err
		}
	}
	a.PayerName = ""
	if u.PayerID != nil {
		if err = tx.QueryRowContext(ctx, `SELECT name FROM custom_billing_payers WHERE id=$1`, *u.PayerID).Scan(&a.PayerName); err != nil {
			return a, databaseError(err)
		}
	}
	a.PlanID = u.PlanID
	a.Price = u.Price
	a.PayerID = u.PayerID
	accessChanged := accessDateExplicitlyChanged(u, beforeRollover, a.RenewalDate)
	// A form can remain open while the background scheduler rolls the stored
	// date. An explicit unchanged flag retains that current date and its anchor
	// even when the stale form's date no longer matches either value loaded here.
	if u.RenewalDateChanged == nil || *u.RenewalDateChanged {
		if err = applyRenewalDateUpdate(ctx, tx, &a, u.RenewalDate, beforeRollover, now); err != nil {
			return a, databaseError(err)
		}
	}
	a.PaymentStatus = u.PaymentStatus
	setHistory(&a, History{Month: now.Format("2006-01"), Status: u.PaymentStatus, Amount: u.Price, PayerID: u.PayerID, PayerName: a.PayerName})
	raw, err := historyJSON(a.History)
	if err != nil {
		return a, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE custom_account_billing SET renewal_date=$1,plan_id=$2,price_cents=$3,payer_id=$4,history=$5::jsonb,updated_at=NOW() WHERE account_id=$6`, a.RenewalDate, u.PlanID, u.Price, u.PayerID, string(raw), id)
	if err != nil {
		return a, databaseError(err)
	}
	if accessChanged {
		if err = setAccessDate(ctx, tx, id, u.RenewalDate); err != nil {
			return a, err
		}
	}
	updated := []Account{a}
	if err = attachAccessStates(ctx, tx, updated, now); err != nil {
		return a, err
	}
	a.AccessState = updated[0].AccessState
	return a, tx.Commit()
}

func (s *Store) UpdateHistory(ctx context.Context, id int64, entry History) (Account, error) {
	now := s.Now()
	if !ValidMonth(entry.Month) || entry.Month > now.Format("2006-01") || !validStatus(entry.Status) || entry.Amount < 0 || entry.Amount > 999999999999 {
		return Account{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	accounts, err := loadAccounts(ctx, tx, 0, id)
	if err != nil {
		return Account{}, err
	}
	if len(accounts) == 0 {
		return Account{}, ErrNotFound
	}
	if err = refreshRenewalDates(ctx, tx, accounts, now); err != nil {
		return Account{}, err
	}
	a := accounts[0]
	fillHistory(&a, now)
	if old, ok := historyFor(a, entry.Month); ok {
		entry.PayerID = old.PayerID
		entry.PayerName = old.PayerName
	} else {
		entry.PayerID = a.PayerID
		entry.PayerName = a.PayerName
	}
	setHistory(&a, entry)
	if entry.Month == now.Format("2006-01") {
		a.Price = entry.Amount
		if _, err = tx.ExecContext(ctx, `UPDATE custom_account_billing SET price_cents=$1 WHERE account_id=$2`, entry.Amount, id); err != nil {
			return a, err
		}
	}
	if err = saveHistory(ctx, tx, a); err != nil {
		return a, err
	}
	a.PaymentStatus = "unpaid"
	if current, ok := historyFor(a, now.Format("2006-01")); ok {
		a.PaymentStatus = current.Status
	}
	updated := []Account{a}
	if err = attachAccessStates(ctx, tx, updated, now); err != nil {
		return a, err
	}
	a.AccessState = updated[0].AccessState
	return a, tx.Commit()
}

// UserSummary exposes only the current owner's monthly amounts and renewal dates.
// It deliberately has no collection-image or payment-page visibility dependency.
func (s *Store) UserSummary(ctx context.Context, userID int64) (UserBillingSummary, error) {
	now := s.Now()
	result := UserBillingSummary{Month: now.Format("2006-01"), Accounts: []UserBillingAccount{}}
	if userID <= 0 {
		return result, ErrForbidden
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var activeUser bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND deleted_at IS NULL AND status='active' AND role='user')`, userID).Scan(&activeUser); err != nil {
		return result, err
	}
	if !activeUser {
		return result, ErrForbidden
	}
	accounts, err := loadAccounts(ctx, tx, userID, 0)
	if err != nil {
		return result, err
	}
	if err = refreshRenewalDates(ctx, tx, accounts, now); err != nil {
		return result, err
	}
	if err = refreshHistory(ctx, tx, accounts, now); err != nil {
		return result, err
	}
	if err = attachAccessStates(ctx, tx, accounts, now); err != nil {
		return result, err
	}
	for _, a := range accounts {
		h, ok := historyFor(a, result.Month)
		if !ok {
			continue
		}
		applyHistory(&a, h, true)
		result.TotalAmount += a.Price
		if a.PaymentStatus != "paid" {
			result.AmountDue += a.Price
		}
		result.Accounts = append(result.Accounts, UserBillingAccount{
			AccessState: a.AccessState,
			AccountID:   a.AccountID, RenewalDate: a.RenewalDate, Price: a.Price, PaymentStatus: a.PaymentStatus,
		})
	}
	return result, tx.Commit()
}
