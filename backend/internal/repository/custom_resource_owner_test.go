package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestResourceOwnerBatchHidesForeignIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT r.id, o.user_id.*AND o.user_id = \$2`).WithArgs("{10,20}", int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "username"}).AddRow(10, 7, "own@example.test", "own"))
	owners, err := readResourceOwners(ownership.WithUser(context.Background(), 7), db, "groups", []int64{10, 20})
	require.ErrorIs(t, err, service.ErrGroupNotFound)
	require.Nil(t, owners)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResourceOwnerBatchIncludesLegacyNullOwner(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT r.id, o.user_id`).WithArgs("{10,20,20}").
		WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "username"}).
			AddRow(10, nil, "", "").AddRow(20, 7, "owner@example.test", "Owner"))
	owners, err := readResourceOwners(context.Background(), db, "accounts", []int64{10, 20, 20})
	require.NoError(t, err)
	require.Len(t, owners, 2)
	require.Nil(t, owners[0].UserID)
	require.EqualValues(t, 7, *owners[1].UserID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResourceOwnerTransferRejectsScopedBeforeOpeningTransaction(t *testing.T) {
	r := &groupRepository{}
	_, _, err := r.TransferResourceOwner(ownership.WithUser(context.Background(), 1), "groups", 2, nil)
	require.ErrorIs(t, err, service.ErrResourceOwnerForbidden)
}

func TestResourceOwnerAdvancedReferenceConflictPreventsMutation(t *testing.T) {
	for _, kind := range []string{"groups", "accounts"} {
		t.Run(kind, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			owner := int64(5)
			mock.ExpectQuery(`SELECT EXISTS`).WithArgs(int64(3), owner).
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
			require.ErrorIs(t, checkResourceOwnerReferences(context.Background(), db, kind, 3, &owner), service.ErrResourceOwnerConflict)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestResourceOwnerGatewayBindingReadUsesLiveRelations(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery(`SELECT ag.account_id, ag.group_id.*a.deleted_at IS NULL AND g.deleted_at IS NULL`).WithArgs("{10,20}").
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id"}).AddRow(10, 2).AddRow(10, 3))
	groups, err := newAccountRepositoryWithSQL(nil, db, nil).ListGatewayAccountGroupIDs(context.Background(), []int64{10, 20})
	require.NoError(t, err)
	require.Equal(t, map[int64][]int64{10: {2, 3}}, groups)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResourceOwnerTransferRollsBackBindingsAndOwnerWhenOutboxFails(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(\$1\)`).WithArgs(resourceOwnershipAdvisoryLock).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`LOCK TABLE groups, accounts, account_groups`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT id FROM groups.*FOR UPDATE`).WithArgs(int64(3)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(3))
	mock.ExpectQuery(`SELECT id FROM users.*FOR SHARE`).WithArgs(int64(5)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(5))
	mock.ExpectQuery(`SELECT r.id, o.user_id`).WithArgs("{3}").WillReturnRows(sqlmock.NewRows([]string{"id", "user_id", "email", "username"}).AddRow(3, 1, "old@example.test", "Old"))
	mock.ExpectQuery(`SELECT EXISTS`).WithArgs(int64(3), int64(5)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectQuery(`DELETE FROM account_groups ag`).WithArgs(int64(3), int64(5)).WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id"}).AddRow(10, 3))
	mock.ExpectExec(`INSERT INTO custom_user_groups`).WithArgs(int64(3), int64(5)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT ag.account_id, ag.group_id`).WithArgs("{10}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "group_id"}).AddRow(10, 7))
	mock.ExpectExec(`INSERT INTO scheduler_outbox`).WillReturnError(errors.New("outbox unavailable"))
	mock.ExpectRollback()
	target := int64(5)
	owner, affected, err := newGroupRepositoryWithSQL(client, db).TransferResourceOwner(context.Background(), "groups", 3, &target)
	require.ErrorContains(t, err, "outbox unavailable")
	require.Nil(t, owner)
	require.Nil(t, affected)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResourceOwnerWriteGuardLocksBeforeCheckAndHoldsThroughMutation(t *testing.T) {
	for _, callbackFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "callback failure"}[callbackFails], func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
			t.Cleanup(func() { _ = client.Close() })
			mock.ExpectBegin()
			mock.ExpectExec(`SELECT pg_advisory_xact_lock_shared\(\$1\)`).WithArgs(resourceOwnershipAdvisoryLock).WillReturnResult(sqlmock.NewResult(0, 0))
			called := false
			mock.ExpectRollback()
			err = newGroupRepositoryWithSQL(client, db).WithResourceOwnershipReadLock(context.Background(), func() error {
				called = true
				if callbackFails {
					return errors.New("write failed")
				}
				return nil
			})
			require.True(t, called)
			if callbackFails {
				require.ErrorContains(t, err, "write failed")
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestResourceOwnerWriteGuardReservesCRUDConnections(t *testing.T) {
	db, _, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	_, err = acquireResourceOwnerGuard(context.Background(), db)
	require.ErrorIs(t, err, service.ErrResourceOwnerConnectionPool)
	db.SetMaxOpenConns(2)
	release, err := acquireResourceOwnerGuard(context.Background(), db)
	require.NoError(t, err)
	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = acquireResourceOwnerGuard(waitCtx, db)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	release()
	release, err = acquireResourceOwnerGuard(context.Background(), db)
	require.NoError(t, err)
	release()
}
