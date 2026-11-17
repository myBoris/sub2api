package repository

import (
	"context"
	"errors"
	"regexp"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/DATA-DOG/go-sqlmock"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUserOwnershipScopesGroupPagination(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newGroupRepositoryWithSQL(client, db)
	ctx := ownership.WithUser(context.Background(), 42)
	// The owner predicate must apply to the count and the paginated SQL query,
	// otherwise a user could infer totals or receive empty pages of foreign rows.
	mock.ExpectQuery(`SELECT COUNT\(.*custom_user_groups.*user_id.*`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT .*custom_user_groups.*user_id.*LIMIT`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	groups, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", nil)
	require.NoError(t, err)
	require.Empty(t, groups)
	require.Zero(t, page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserOwnershipScopesAccountPagination(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)
	ctx := ownership.WithUser(context.Background(), 42)
	mock.ExpectQuery(`SELECT COUNT\(.*custom_user_accounts.*user_id.*`).
		WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery(`SELECT .*custom_user_accounts.*user_id.*LIMIT`).
		WillReturnRows(sqlmock.NewRows([]string{"id"}))
	accounts, page, err := repo.ListWithFilters(ctx, pagination.PaginationParams{Page: 1, PageSize: 10}, "", "", "", "", 0, "")
	require.NoError(t, err)
	require.Empty(t, accounts)
	require.Zero(t, page.Total)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserOwnershipRejectsForeignWritesBeforeMutation(t *testing.T) {
	for _, operation := range []string{"group update", "group delete", "account delete", "account bulk update", "bind foreign account"} {
		t.Run(operation, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { _ = db.Close() })
			ctx := ownership.WithUser(context.Background(), 42)
			groups := newGroupRepositoryWithSQL(nil, db)
			accounts := newAccountRepositoryWithSQL(nil, db, nil)
			table := "custom_user_groups"
			if operation == "account delete" || operation == "account bulk update" || operation == "bind foreign account" {
				table = "custom_user_accounts"
			}
			mock.ExpectQuery(`SELECT COUNT\(\*\) FROM `+table).
				WithArgs(int64(42), "{99}").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
			switch operation {
			case "group update":
				err = groups.Update(ctx, &service.Group{ID: 99})
			case "group delete":
				err = groups.Delete(ctx, 99)
			case "account delete":
				err = accounts.Delete(ctx, 99)
			case "account bulk update":
				_, err = accounts.BulkUpdate(ctx, []int64{99}, service.AccountBulkUpdate{})
			case "bind foreign account":
				err = accounts.BindGroups(ctx, 99, []int64{1})
			}
			require.Error(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestUserOwnershipRejectsForeignGroupReferences(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := ownership.WithUser(context.Background(), 42)
	foreignGroupID := int64(99)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM custom_user_groups`).
		WithArgs(int64(42), "{99}").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	err = validateOwnedGroupReferences(ctx, client, &service.Group{FallbackGroupID: &foreignGroupID})
	require.ErrorIs(t, err, service.ErrGroupNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserOwnershipCreateRollsBackOnOwnerInsertFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	repo := newAccountRepositoryWithSQL(client, db, nil)
	ctx := ownership.WithUser(context.Background(), 42)
	mock.ExpectBegin()
	mock.ExpectQuery(`INSERT INTO "accounts"`).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(77))
	failure := errors.New("owner insert unavailable")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO custom_user_accounts (account_id, user_id) VALUES ($1, $2)")).
		WithArgs(int64(77), int64(42)).WillReturnError(failure)
	mock.ExpectRollback()
	err = repo.Create(ctx, &service.Account{Name: "owned", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
	require.ErrorIs(t, err, failure)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserOwnershipUnscopedAdministratorNeedsNoOwnerLookup(t *testing.T) {
	// A nil executor proves no ownership SQL was added to the upstream path.
	require.NoError(t, requireOwnedGroups(context.Background(), nil, 99))
	require.NoError(t, requireOwnedAccounts(context.Background(), nil, 99))
}
