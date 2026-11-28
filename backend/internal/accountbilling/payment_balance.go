package accountbilling

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const accountPaymentBalanceTarget = 9999

// replenishAccountPaymentBalance runs only inside verified account fulfillment.
// The caller owns the user row's write lock and the order's fulfillment lock.
// Balance, recharge history and the fulfillment marker therefore commit once;
// a repeated callback cannot replenish funds spent after the first callback.
func replenishAccountPaymentBalance(ctx context.Context, tx *sql.Tx, orderID, userID int64) error {
	var credited string
	err := tx.QueryRowContext(ctx, `WITH current_balance AS (
		SELECT id,balance FROM users WHERE id=$1 AND deleted_at IS NULL FOR NO KEY UPDATE
	) UPDATE users AS u SET balance=$2,
		total_recharged=u.total_recharged+($2-current_balance.balance),updated_at=NOW()
		FROM current_balance WHERE u.id=current_balance.id AND current_balance.balance<$2
		RETURNING ($2-current_balance.balance)::text`, userID, accountPaymentBalanceTarget).Scan(&credited)
	if errors.Is(err, sql.ErrNoRows) {
		// A verified owner at or above the target keeps both balance and recharge
		// total. The caller still records fulfillment so future retries do nothing.
		return nil
	}
	if err != nil {
		return fmt.Errorf("replenish account payment balance: %w", err)
	}
	// Base 36 keeps even the largest int64 order ID within the upstream
	// redeem_codes.code limit of 32 characters. Notes retain the decimal ID.
	code := "ACCOUNT-PAYMENT-" + strconv.FormatInt(orderID, 36)
	_, err = tx.ExecContext(ctx, `INSERT INTO redeem_codes(code,type,value,status,used_by,used_at,notes)
		VALUES($1,'balance',$2,'used',$3,NOW(),$4)`, code, credited, userID,
		fmt.Sprintf("账号支付订单 %d 成功后补足余额至 %d", orderID, accountPaymentBalanceTarget))
	if err != nil {
		return fmt.Errorf("record account payment balance recharge: %w", err)
	}
	return nil
}
