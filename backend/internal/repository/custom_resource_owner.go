package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

func resourceOwnerTable(kind string) (table, column string, missing error) {
	switch kind {
	case "groups":
		return ownership.GroupTable, "group_id", service.ErrGroupNotFound
	case "accounts":
		return ownership.AccountTable, "account_id", service.ErrAccountNotFound
	default:
		return "", "", service.ErrResourceOwnerInvalid
	}
}

func (r *groupRepository) ResourceOwners(ctx context.Context, kind string, ids []int64) ([]service.ResourceOwner, error) {
	return readResourceOwners(ctx, r.sql, kind, ids)
}

// WithResourceOwnershipReadLock stabilizes ownership for a complete ordinary
// management write, including the existing handlers' preflight checks. The
// advisory lock lives on a separate connection so upstream CRUD transaction
// boundaries and side effects remain unchanged. It does not block their SQL.
const resourceOwnershipAdvisoryLock int64 = 0x53324150494f574e // "S2APIOWN"

func (r *groupRepository) WithResourceOwnershipReadLock(ctx context.Context, fn func() error) error {
	release, err := acquireResourceOwnerGuard(ctx, r.sql)
	if err != nil {
		return err
	}
	defer release()
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Client().ExecContext(ctx, `SELECT pg_advisory_xact_lock_shared($1)`, resourceOwnershipAdvisoryLock); err != nil {
		return err
	}
	// This transaction only holds a lock. Rollback releases it after the
	// callback, avoiding a post-response commit failure from a completed handler.
	return fn()
}

type resourceOwnerGuardPool struct {
	mu      sync.Mutex
	active  int
	changed chan struct{}
}

var resourceOwnerGuardPools sync.Map

// A guard holds one connection while existing CRUD obtains another. Reserve at
// least half of a bounded pool for that CRUD, avoiding N guard transactions
// exhausting every connection and waiting on each other. Concurrent ordinary
// writes share the database lock; only an ownership transfer waits for them.
func acquireResourceOwnerGuard(ctx context.Context, exec sqlExecutor) (func(), error) {
	db, ok := exec.(*sql.DB)
	if !ok {
		return nil, service.ErrResourceOwnerConnectionPool
	}
	value, _ := resourceOwnerGuardPools.LoadOrStore(db, &resourceOwnerGuardPool{changed: make(chan struct{})})
	pool := value.(*resourceOwnerGuardPool)
	for {
		limit := 16
		if maxOpen := db.Stats().MaxOpenConnections; maxOpen > 0 {
			if maxOpen < 2 {
				return nil, service.ErrResourceOwnerConnectionPool
			}
			limit = maxOpen / 2
		}
		pool.mu.Lock()
		if pool.active < limit {
			pool.active++
			pool.mu.Unlock()
			return func() {
				pool.mu.Lock()
				pool.active--
				close(pool.changed)
				pool.changed = make(chan struct{})
				pool.mu.Unlock()
			}, nil
		}
		changed := pool.changed
		pool.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func readResourceOwners(ctx context.Context, exec sqlExecutor, kind string, ids []int64) ([]service.ResourceOwner, error) {
	table, column, missing := resourceOwnerTable(kind)
	if table == "" {
		return nil, missing
	}
	out := make([]service.ResourceOwner, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 1000 {
		return nil, service.ErrResourceOwnerInvalid
	}
	unique := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, missing
		}
		unique[id] = struct{}{}
	}
	query := fmt.Sprintf(`SELECT r.id, o.user_id, COALESCE(u.email, ''), COALESCE(u.username, '')
		FROM %s r LEFT JOIN %s o ON o.%s = r.id
		LEFT JOIN users u ON u.id = o.user_id AND u.deleted_at IS NULL
		WHERE r.deleted_at IS NULL AND r.id = ANY($1)`, kind, table, column)
	args := []any{pq.Array(ids)}
	if userID, scoped := ownership.UserID(ctx); scoped {
		query += " AND o.user_id = $2"
		args = append(args, userID)
	}
	rows, err := exec.QueryContext(ctx, query+" ORDER BY r.id", args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var owner service.ResourceOwner
		var userID sql.NullInt64
		if err := rows.Scan(&owner.ResourceID, &userID, &owner.Email, &owner.Username); err != nil {
			return nil, err
		}
		if userID.Valid {
			id := userID.Int64
			owner.UserID = &id
		}
		out = append(out, owner)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Fail as one request rather than disclosing another owner's identity or
	// returning a partial answer to a mixed-owner batch.
	if len(out) != len(unique) {
		return nil, missing
	}
	return out, nil
}

// TransferResourceOwner returns affected accounts only after its transaction
// commits, so the service can refresh their scheduler snapshots safely.
func (r *groupRepository) TransferResourceOwner(ctx context.Context, kind string, id int64, userID *int64) (*service.ResourceOwner, []int64, error) {
	if _, scoped := ownership.UserID(ctx); scoped {
		return nil, nil, service.ErrResourceOwnerForbidden
	}
	table, column, missing := resourceOwnerTable(kind)
	if table == "" || id <= 0 {
		return nil, nil, missing
	}
	if userID != nil {
		if *userID < 0 {
			return nil, nil, service.ErrResourceOwnerInvalid
		}
		if *userID == 0 {
			userID = nil
		}
	}
	release, err := acquireResourceOwnerGuard(ctx, r.sql)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	exec := tx.Client()
	// The advisory guard precedes table locks: a queued transfer must not
	// block an in-flight guarded request's actual writes on another connection.
	if _, err := exec.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, resourceOwnershipAdvisoryLock); err != nil {
		return nil, nil, err
	}
	// Ownership edits are rare. A short, fixed-order table-lock protocol also
	// covers existing upstream binding/JSON writers, without modifying them or
	// adding triggers. SELECTs remain available throughout this transaction.
	if _, err := exec.ExecContext(ctx, `LOCK TABLE groups, accounts, account_groups,
		custom_user_groups, custom_user_accounts IN SHARE ROW EXCLUSIVE MODE`); err != nil {
		return nil, nil, err
	}
	var resourceID int64
	if err := scanSingleRow(ctx, exec, "SELECT id FROM "+kind+" WHERE id = $1 AND deleted_at IS NULL FOR UPDATE", []any{id}, &resourceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, missing
		}
		return nil, nil, err
	}
	if userID != nil {
		var target int64
		if err := scanSingleRow(ctx, exec, "SELECT id FROM users WHERE id = $1 AND deleted_at IS NULL AND status = 'active' FOR SHARE", []any{*userID}, &target); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil, service.ErrUserNotFound
			}
			return nil, nil, err
		}
	}
	current, err := readResourceOwners(ctx, exec, kind, []int64{id})
	if err != nil {
		return nil, nil, err
	}
	if equalOwner(current[0].UserID, userID) {
		if err := tx.Commit(); err != nil {
			return nil, nil, err
		}
		return &current[0], nil, nil
	}
	if err := checkResourceOwnerReferences(ctx, exec, kind, id, userID); err != nil {
		return nil, nil, err
	}
	accounts, groups, err := detachResourceOwnerBindings(ctx, exec, kind, id, userID)
	if err != nil {
		return nil, nil, err
	}
	if userID == nil {
		_, err = exec.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+column+" = $1", id)
	} else {
		_, err = exec.ExecContext(ctx, "INSERT INTO "+table+" ("+column+", user_id) VALUES ($1, $2) ON CONFLICT ("+column+") DO UPDATE SET user_id = EXCLUDED.user_id", id, *userID)
	}
	if err != nil {
		return nil, nil, err
	}
	if kind == "groups" {
		groups = mergeGroupIDs(groups, []int64{id})
	} else {
		accounts = mergeGroupIDs(accounts, []int64{id})
	}
	liveGroups, err := readGatewayAccountGroupIDs(ctx, exec, accounts)
	if err != nil {
		return nil, nil, err
	}
	for _, ids := range liveGroups {
		groups = mergeGroupIDs(groups, ids)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	for _, accountID := range accounts {
		if err := enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload(groups)); err != nil {
			return nil, nil, err
		}
	}
	for _, groupID := range groups {
		if err := enqueueSchedulerOutbox(ctx, exec, service.SchedulerOutboxEventGroupChanged, nil, &groupID, nil); err != nil {
			return nil, nil, err
		}
	}
	updated, err := readResourceOwners(ctx, exec, kind, []int64{id})
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return &updated[0], accounts, nil
}

func equalOwner(a, b *int64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func (r *accountRepository) RefreshResourceOwnerAccounts(ctx context.Context, ids []int64) {
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	r.syncSchedulerAccountSnapshots(refreshCtx, ids)
}

// ListGatewayAccountGroupIDs is a narrow optional capability for ordinary-user
// gateway reads. Read live bindings even when Redis still holds a pre-transfer
// snapshot. Deliberate administrator cross-owner bindings remain supported.
func (r *accountRepository) ListGatewayAccountGroupIDs(ctx context.Context, ids []int64) (map[int64][]int64, error) {
	return readGatewayAccountGroupIDs(ctx, r.sql, ids)
}

func readGatewayAccountGroupIDs(ctx context.Context, exec sqlExecutor, ids []int64) (map[int64][]int64, error) {
	groups := make(map[int64][]int64, len(ids))
	if len(ids) == 0 {
		return groups, nil
	}
	rows, err := exec.QueryContext(ctx, `SELECT ag.account_id, ag.group_id
		FROM account_groups ag JOIN accounts a ON a.id = ag.account_id
		JOIN groups g ON g.id = ag.group_id
		WHERE ag.account_id = ANY($1) AND a.deleted_at IS NULL AND g.deleted_at IS NULL
		ORDER BY ag.account_id, ag.group_id`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var accountID, groupID int64
		if err := rows.Scan(&accountID, &groupID); err != nil {
			return nil, err
		}
		groups[accountID] = append(groups[accountID], groupID)
	}
	return groups, rows.Err()
}

// Direct references are not silently rewritten: the administrator must remove
// them explicitly. account_groups is the common, safely detachable relation.
func checkResourceOwnerReferences(ctx context.Context, exec sqlExecutor, kind string, id int64, userID *int64) error {
	var conflict bool
	var query string
	if kind == "accounts" {
		query = `SELECT EXISTS (
			SELECT 1 FROM accounts a WHERE a.deleted_at IS NULL
			AND ((a.id = $1 AND a.parent_account_id IS NOT NULL) OR a.parent_account_id = $1)
			UNION ALL
			SELECT 1 FROM groups g LEFT JOIN custom_user_groups o ON o.group_id = g.id
			WHERE g.deleted_at IS NULL AND o.user_id IS DISTINCT FROM $2::bigint
			AND (EXISTS (SELECT 1 FROM jsonb_each(CASE WHEN jsonb_typeof(g.model_routing) = 'object' THEN g.model_routing ELSE '{}'::jsonb END) routes,
				jsonb_array_elements_text(CASE WHEN jsonb_typeof(routes.value) = 'array' THEN routes.value ELSE '[]'::jsonb END) ref WHERE ref.value = $1::text)
				OR COALESCE(g.codex_models_manifest_config->'account_ids', '[]'::jsonb) @> to_jsonb(ARRAY[$1::bigint])))`
	} else {
		query = `SELECT EXISTS (
			SELECT 1 FROM groups g CROSS JOIN LATERAL
				(SELECT ref.value FROM jsonb_each(CASE WHEN jsonb_typeof(g.model_routing) = 'object' THEN g.model_routing ELSE '{}'::jsonb END) routes,
					jsonb_array_elements_text(CASE WHEN jsonb_typeof(routes.value) = 'array' THEN routes.value ELSE '[]'::jsonb END) ref
				 UNION ALL SELECT ref.value FROM jsonb_array_elements_text(
					CASE WHEN jsonb_typeof(g.codex_models_manifest_config->'account_ids') = 'array'
					THEN g.codex_models_manifest_config->'account_ids' ELSE '[]'::jsonb END) ref) refs
			LEFT JOIN custom_user_accounts o ON o.account_id::text = refs.value
			WHERE g.id = $1 AND o.user_id IS DISTINCT FROM $2::bigint
			UNION ALL
			SELECT 1 FROM groups g LEFT JOIN custom_user_groups o ON o.group_id = g.id
			WHERE g.deleted_at IS NULL AND g.id <> $1 AND o.user_id IS DISTINCT FROM $2::bigint
			AND (g.fallback_group_id = $1 OR g.fallback_group_id_on_invalid_request = $1
				OR g.id IN (SELECT fallback_group_id FROM groups WHERE id = $1
					UNION SELECT fallback_group_id_on_invalid_request FROM groups WHERE id = $1)))`
	}
	if err := scanSingleRow(ctx, exec, query, []any{id, userID}, &conflict); err != nil {
		return err
	}
	if conflict {
		return service.ErrResourceOwnerConflict
	}
	return nil
}

func detachResourceOwnerBindings(ctx context.Context, exec sqlExecutor, kind string, id int64, userID *int64) ([]int64, []int64, error) {
	foreignTable, foreignColumn, filter := ownership.GroupTable, "group_id", "ag.account_id"
	if kind == "groups" {
		foreignTable, foreignColumn, filter = ownership.AccountTable, "account_id", "ag.group_id"
	}
	rows, err := exec.QueryContext(ctx, fmt.Sprintf(`DELETE FROM account_groups ag
		WHERE %s = $1 AND NOT EXISTS (SELECT 1 FROM %s o WHERE o.%s = ag.%s AND o.user_id IS NOT DISTINCT FROM $2::bigint)
		AND ($2::bigint IS NOT NULL OR EXISTS (SELECT 1 FROM %s o WHERE o.%s = ag.%s))
		RETURNING ag.account_id, ag.group_id`, filter, foreignTable, foreignColumn, foreignColumn, foreignTable, foreignColumn, foreignColumn), id, userID)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	accountSet, groupSet := map[int64]struct{}{}, map[int64]struct{}{}
	for rows.Next() {
		var accountID, groupID int64
		if err := rows.Scan(&accountID, &groupID); err != nil {
			return nil, nil, err
		}
		accountSet[accountID], groupSet[groupID] = struct{}{}, struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	accounts, groups := make([]int64, 0, len(accountSet)), make([]int64, 0, len(groupSet))
	for id := range accountSet {
		accounts = append(accounts, id)
	}
	for id := range groupSet {
		groups = append(groups, id)
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i] < accounts[j] })
	sort.Slice(groups, func(i, j int) bool { return groups[i] < groups[j] })
	return accounts, groups, nil
}
