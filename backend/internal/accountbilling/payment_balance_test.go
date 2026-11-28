package accountbilling

import (
	"context"
	"errors"
	"math"
	"strconv"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/require"
)

func TestAccountPaymentBalanceUsesExactRechargeDeltaAndPropagatesAuditFailure(t *testing.T) {
	for _, failAudit := range []bool{false, true} {
		db, mock, err := sqlmock.New()
		require.NoError(t, err)
		mock.ExpectBegin()
		mock.ExpectQuery(`(?s)WITH current_balance AS.*FOR NO KEY UPDATE.*total_recharged=u.total_recharged.*current_balance.balance<\$2`).
			WithArgs(int64(7), 9999).WillReturnRows(sqlmock.NewRows([]string{"credited"}).AddRow("10011.34567890"))
		code := "ACCOUNT-PAYMENT-" + strconv.FormatInt(math.MaxInt64, 36)
		require.LessOrEqual(t, len(code), 32)
		insert := mock.ExpectExec(`(?s)INSERT INTO redeem_codes.*VALUES\(\$1,'balance',\$2,'used',\$3,NOW\(\),\$4\)`).
			WithArgs(code, "10011.34567890", int64(7), "账号支付订单 9223372036854775807 成功后补足余额至 9999")
		if failAudit {
			insert.WillReturnError(errors.New("audit insert rejected"))
		} else {
			insert.WillReturnResult(sqlmock.NewResult(1, 1))
		}
		mock.ExpectRollback()
		tx, err := db.BeginTx(context.Background(), nil)
		require.NoError(t, err)
		err = replenishAccountPaymentBalance(context.Background(), tx, math.MaxInt64, 7)
		if failAudit {
			require.ErrorContains(t, err, "audit insert rejected")
		} else {
			require.NoError(t, err)
		}
		require.NoError(t, tx.Rollback())
		require.NoError(t, mock.ExpectationsWereMet())
		_ = db.Close()
	}
}
