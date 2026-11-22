package accountbilling

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

// projectRenewalDate preserves the original day in each billing month, clamping
// only when that month is shorter. A future starting month is never pulled back.
func projectRenewalDate(anchor, now time.Time) time.Time {
	if anchor.Format("2006-01") >= now.Format("2006-01") {
		return anchor
	}
	day := anchor.Day()
	lastDay := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(now.Year(), now.Month(), day, 0, 0, 0, 0, now.Location())
}

// refreshRenewalDates requires callers to hold the billing row locks. Fetching
// anchors after obtaining those locks keeps a waiting reader from using an old
// anchor concurrently replaced by an administrator. Payment history is untouched.
func refreshRenewalDates(ctx context.Context, tx *sql.Tx, accounts []Account, now time.Time) error {
	monthStart := now.Format("2006-01") + "-01"
	ids := []int64{}
	for _, a := range accounts {
		if a.RenewalDate != nil && *a.RenewalDate < monthStart {
			ids = append(ids, a.AccountID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT account_id,anchor_date FROM custom_billing_renewal_schedule WHERE account_id=ANY($1)`, pq.Array(ids))
	if err != nil {
		return err
	}
	anchors := make(map[int64]time.Time, len(ids))
	for rows.Next() {
		var id int64
		var anchor time.Time
		if err = rows.Scan(&id, &anchor); err != nil {
			_ = rows.Close()
			return err
		}
		anchors[id] = anchor
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for i := range accounts {
		a := &accounts[i]
		if a.RenewalDate == nil || *a.RenewalDate >= monthStart {
			continue
		}
		anchor, exists := anchors[a.AccountID]
		if !exists {
			// Migration and synchronization normally create the schedule. Repair
			// missing schedules without overwriting an existing original anchor.
			if err = tx.QueryRowContext(ctx, `INSERT INTO custom_billing_renewal_schedule(account_id,anchor_date)
				VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE
				SET anchor_date=custom_billing_renewal_schedule.anchor_date RETURNING anchor_date`, a.AccountID, a.RenewalDate).Scan(&anchor); err != nil {
				return err
			}
		}
		date := projectRenewalDate(anchor, now).Format("2006-01-02")
		if date != *a.RenewalDate {
			if _, err = tx.ExecContext(ctx, `UPDATE custom_account_billing SET renewal_date=$1,updated_at=NOW() WHERE account_id=$2`, date, a.AccountID); err != nil {
				return err
			}
			a.RenewalDate = &date
		}
	}
	return nil
}

func sameRenewalDate(a, b *string) bool {
	return a != nil && b != nil && *a == *b
}

// applyRenewalDateUpdate distinguishes editing a date from submitting an
// unchanged edit form. The original pre-rollover value is also accepted so a
// form opened just before a month boundary cannot reset a January 30 anchor to
// February 28. Changing only price/status must retain that original day.
func applyRenewalDateUpdate(ctx context.Context, tx *sql.Tx, a *Account, requested, beforeRollover *string, now time.Time) error {
	if requested == nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM custom_billing_renewal_schedule WHERE account_id=$1`, a.AccountID); err != nil {
			return err
		}
		a.RenewalDate = nil
		return nil
	}
	if sameRenewalDate(requested, a.RenewalDate) || sameRenewalDate(requested, beforeRollover) {
		return nil
	}
	anchor, err := time.Parse("2006-01-02", *requested)
	if err != nil {
		return ErrInvalid
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO custom_billing_renewal_schedule(account_id,anchor_date)
		VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET anchor_date=EXCLUDED.anchor_date`, a.AccountID, *requested); err != nil {
		return err
	}
	date := projectRenewalDate(anchor, now).Format("2006-01-02")
	a.RenewalDate = &date
	return nil
}

// RollRenewalDates advances existing schedules into the current business month.
// It is safe to run at startup and periodically; read/write APIs also perform
// the same correction immediately. It never creates or changes payment history.
func (s *Store) RollRenewalDates(ctx context.Context) error {
	now := s.Now()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT b.account_id,b.renewal_date
		FROM custom_account_billing b JOIN accounts a ON a.id=b.account_id
		WHERE a.deleted_at IS NULL AND b.renewal_date < $1::date
		ORDER BY b.account_id FOR UPDATE OF b`, now.Format("2006-01")+"-01")
	if err != nil {
		return err
	}
	accounts := []Account{}
	for rows.Next() {
		var a Account
		var date time.Time
		if err = rows.Scan(&a.AccountID, &date); err != nil {
			_ = rows.Close()
			return err
		}
		formatted := date.Format("2006-01-02")
		a.RenewalDate = &formatted
		accounts = append(accounts, a)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if err = refreshRenewalDates(ctx, tx, accounts, now); err != nil {
		return err
	}
	return tx.Commit()
}
