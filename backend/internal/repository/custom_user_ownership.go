package repository

import (
	"context"
	"fmt"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// Ownership is deliberately kept outside the upstream Ent schemas. An absent
// scope preserves the administrator and background-worker behavior.
func ownedResourcePredicate(userID int64, table, column string) func(*entsql.Selector) {
	return func(s *entsql.Selector) {
		o := entsql.Table(table)
		s.Where(entsql.In(s.C("id"), entsql.Select(o.C(column)).From(o).Where(entsql.EQ(o.C("user_id"), userID))))
	}
}

func scopedGroupQuery(ctx context.Context, q *dbent.GroupQuery) *dbent.GroupQuery {
	if userID, ok := ownership.UserID(ctx); ok {
		q.Where(ownedResourcePredicate(userID, ownership.GroupTable, "group_id"))
	}
	return q
}

func scopedAccountQuery(ctx context.Context, q *dbent.AccountQuery) *dbent.AccountQuery {
	if userID, ok := ownership.UserID(ctx); ok {
		q.Where(ownedResourcePredicate(userID, ownership.AccountTable, "account_id"))
	}
	return q
}

func insertResourceOwner(ctx context.Context, client *dbent.Client, table, column string, id int64) error {
	userID, scoped := ownership.UserID(ctx)
	if !scoped {
		return nil
	}
	_, err := client.ExecContext(ctx, "INSERT INTO "+table+" ("+column+", user_id) VALUES ($1, $2)", id, userID)
	return err
}

func requireOwnedResources(ctx context.Context, exec sqlExecutor, table, column string, ids []int64) error {
	userID, scoped := ownership.UserID(ctx)
	if !scoped || len(ids) == 0 {
		return nil
	}
	if tx := dbent.TxFromContext(ctx); tx != nil {
		exec = tx.Client()
	}
	missing := service.ErrGroupNotFound
	resourceTable := "groups"
	if table == ownership.AccountTable {
		missing = service.ErrAccountNotFound
		resourceTable = "accounts"
	}
	unique := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return missing
		}
		unique[id] = struct{}{}
	}
	var count int
	err := scanSingleRow(ctx, exec, fmt.Sprintf(`SELECT COUNT(*) FROM %s o JOIN %s r ON r.id = o.%s
		WHERE o.user_id = $1 AND o.%s = ANY($2) AND r.deleted_at IS NULL`, table, resourceTable, column, column),
		[]any{userID, pq.Array(ids)}, &count)
	if err != nil {
		return err
	}
	if count != len(unique) {
		return missing
	}
	return nil
}

func requireOwnedGroups(ctx context.Context, exec sqlExecutor, ids ...int64) error {
	return requireOwnedResources(ctx, exec, ownership.GroupTable, "group_id", ids)
}

func requireOwnedAccounts(ctx context.Context, exec sqlExecutor, ids ...int64) error {
	return requireOwnedResources(ctx, exec, ownership.AccountTable, "account_id", ids)
}

func validateOwnedGroupReferences(ctx context.Context, client *dbent.Client, g *service.Group) error {
	if _, scoped := ownership.UserID(ctx); !scoped || g == nil {
		return nil
	}
	groups := make([]int64, 0, 2)
	for _, id := range []*int64{g.FallbackGroupID, g.FallbackGroupIDOnInvalidRequest} {
		if id != nil {
			groups = append(groups, *id)
		}
	}
	if err := requireOwnedGroups(ctx, client, groups...); err != nil {
		return err
	}
	var accounts []int64
	for _, ids := range g.ModelRouting {
		accounts = append(accounts, ids...)
	}
	return requireOwnedAccounts(ctx, client, accounts...)
}

// IsOwnedBy is optional on GroupRepository so upstream repository interfaces
// and existing implementations can remain unchanged.
func (r *groupRepository) IsOwnedBy(ctx context.Context, userID, groupID int64) (bool, error) {
	var owned bool
	err := scanSingleRow(ctx, r.sql, `SELECT EXISTS (SELECT 1 FROM custom_user_groups o
		JOIN groups g ON g.id = o.group_id WHERE o.user_id = $1 AND o.group_id = $2 AND g.deleted_at IS NULL)`,
		[]any{userID, groupID}, &owned)
	return owned, err
}

func (r *groupRepository) ListOwnedGroupIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := r.sql.QueryContext(ctx, `SELECT o.group_id FROM custom_user_groups o
		JOIN groups g ON g.id = o.group_id WHERE o.user_id = $1 AND g.deleted_at IS NULL ORDER BY o.group_id`, userID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
