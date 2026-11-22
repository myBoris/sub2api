package accountbilling

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

// AccessState uses the business calendar date: access is allowed throughout
// ValidThrough and expires on the following day. A null date has no deadline.
type AccessState struct {
	ValidThrough *string `json:"valid_through"`
	Expired      bool    `json:"expired"`
}

type accessQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readAccessStates(ctx context.Context, db accessQuerier, ids []int64, now time.Time) (map[int64]AccessState, error) {
	states := make(map[int64]AccessState, len(ids))
	selected := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			if _, exists := states[id]; !exists {
				states[id] = AccessState{}
				selected = append(selected, id)
			}
		}
	}
	if len(selected) == 0 {
		return states, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT account_id,valid_through FROM custom_account_billing_access
		WHERE account_id=ANY($1) ORDER BY account_id`, pq.Array(selected))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	today := now.Format("2006-01-02")
	for rows.Next() {
		var id int64
		var date sql.NullTime
		if err = rows.Scan(&id, &date); err != nil {
			return nil, err
		}
		state := AccessState{}
		if date.Valid {
			value := date.Time.Format("2006-01-02")
			state.ValidThrough = &value
			state.Expired = value < today
		}
		states[id] = state
	}
	return states, rows.Err()
}

// AccessStates is a read-only batch lookup. It deliberately does not refresh
// monthly billing history or the display date. Unmanaged accounts and null
// deadlines have no account-billing access restriction.
func (s *Store) AccessStates(ctx context.Context, ids []int64) (map[int64]AccessState, error) {
	return readAccessStates(ctx, s.db, ids, s.Now())
}

func attachAccessStates(ctx context.Context, tx *sql.Tx, accounts []Account, now time.Time) error {
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.AccountID)
	}
	states, err := readAccessStates(ctx, tx, ids, now)
	if err != nil {
		return err
	}
	for i := range accounts {
		accounts[i].AccessState = states[accounts[i].AccountID]
	}
	return nil
}

func sameAccessDate(first, second *string) bool {
	return (first == nil && second == nil) || sameRenewalDate(first, second)
}

func accessDateExplicitlyChanged(u AccountUpdate, before *string, current *string) bool {
	if u.RenewalDateChanged != nil {
		return *u.RenewalDateChanged
	}
	return !sameAccessDate(u.RenewalDate, before) && !sameAccessDate(u.RenewalDate, current)
}

// An administrator's explicit raw date is authoritative, even when the old
// monthly display projection happens to equal the submitted form value.
func setAccessDate(ctx context.Context, tx *sql.Tx, accountID int64, date *string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO custom_account_billing_access(account_id,valid_through)
		VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE SET valid_through=EXCLUDED.valid_through,updated_at=NOW()`, accountID, date)
	return err
}

func extendAccessDate(ctx context.Context, tx *sql.Tx, accountID int64, date string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO custom_account_billing_access(account_id,valid_through)
		VALUES($1,$2) ON CONFLICT(account_id) DO UPDATE
		SET valid_through=GREATEST(custom_account_billing_access.valid_through,EXCLUDED.valid_through),updated_at=NOW()`, accountID, date)
	return err
}
