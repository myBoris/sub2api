//go:build integration

package repository

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestResourceOwnerTransfersPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	groups := newGroupRepositoryWithSQL(client, integrationDB)
	accounts := newAccountRepositoryWithSQL(client, integrationDB, nil)
	suffix := fmt.Sprintf("owner-transfer-%d", time.Now().UnixNano())
	var userIDs, groupIDs, accountIDs []int64
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM scheduler_outbox WHERE account_id = ANY($1) OR group_id = ANY($2)", pq.Array(accountIDs), pq.Array(groupIDs))
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM account_groups WHERE account_id = ANY($1)", pq.Array(accountIDs))
		_, _ = integrationDB.ExecContext(ctx, "UPDATE accounts SET parent_account_id = null, quota_dimension = 'global' WHERE id = ANY($1)", pq.Array(accountIDs))
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id = ANY($1)", pq.Array(accountIDs))
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = ANY($1)", pq.Array(groupIDs))
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = ANY($1)", pq.Array(userIDs))
	})
	for _, label := range []string{"a", "b", "inactive"} {
		user, err := client.User.Create().SetEmail(suffix + "-" + label + "@example.test").SetUsername(label).SetPasswordHash("test-hash").Save(ctx)
		require.NoError(t, err)
		userIDs = append(userIDs, user.ID)
	}
	_, err := integrationDB.ExecContext(ctx, "UPDATE users SET status = 'disabled' WHERE id = $1", userIDs[2])
	require.NoError(t, err)
	ctxA, ctxB := ownership.WithUser(ctx, userIDs[0]), ownership.WithUser(ctx, userIDs[1])
	newGroup := func(ownerCtx context.Context, name string) int64 {
		g := &service.Group{Name: suffix + "-" + name, Platform: service.PlatformOpenAI, RateMultiplier: 1,
			Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard}
		require.NoError(t, groups.Create(ownerCtx, g))
		groupIDs = append(groupIDs, g.ID)
		return g.ID
	}
	newAccount := func(ownerCtx context.Context, name string) int64 {
		a := &service.Account{Name: suffix + "-" + name, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
			Status: service.StatusActive, Schedulable: true, Concurrency: 1}
		require.NoError(t, accounts.Create(ownerCtx, a))
		accountIDs = append(accountIDs, a.ID)
		return a.ID
	}
	gA, gB, gLegacy := newGroup(ctxA, "group-a"), newGroup(ctxB, "group-b"), newGroup(ctx, "group-legacy")
	aA, aB, aLegacy := newAccount(ctxA, "account-a"), newAccount(ctxB, "account-b"), newAccount(ctx, "account-legacy")
	require.NoError(t, groups.BindAccountsToGroup(ctx, gA, []int64{aA, aB, aLegacy}))
	owners, err := groups.ResourceOwners(ctx, "groups", []int64{gA, gB, gLegacy})
	require.NoError(t, err)
	require.Len(t, owners, 3)
	require.Nil(t, owners[2].UserID)
	require.Equal(t, "a", owners[0].Username)
	owners, err = groups.ResourceOwners(ctxA, "groups", []int64{gA, gB})
	require.ErrorIs(t, err, service.ErrGroupNotFound)
	require.Nil(t, owners)
	_, _, err = groups.TransferResourceOwner(ctxA, "groups", gA, &userIDs[1])
	require.ErrorIs(t, err, service.ErrResourceOwnerForbidden)

	// Group reassignment preserves only accounts already owned by the target.
	owner, affected, err := groups.TransferResourceOwner(ctx, "groups", gA, &userIDs[1])
	require.NoError(t, err)
	require.Equal(t, userIDs[1], *owner.UserID)
	require.Equal(t, "b", owner.Username)
	require.ElementsMatch(t, []int64{aA, aLegacy}, affected)
	var remaining []int64
	rows, err := integrationDB.QueryContext(ctx, "SELECT account_id FROM account_groups WHERE group_id = $1", gA)
	require.NoError(t, err)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		remaining = append(remaining, id)
	}
	require.NoError(t, rows.Close())
	require.Equal(t, []int64{aB}, remaining)
	owned, err := groups.IsOwnedBy(ctx, userIDs[0], gA)
	require.NoError(t, err)
	require.False(t, owned, "the live check used by cached API keys revokes the previous owner")
	owned, err = groups.IsOwnedBy(ctx, userIDs[1], gA)
	require.NoError(t, err)
	require.True(t, owned)
	_, err = groups.GetByID(ctxA, gA)
	require.ErrorIs(t, err, service.ErrGroupNotFound)
	_, err = groups.GetByID(ctxB, gA)
	require.NoError(t, err)
	a, err := accounts.GetByID(ctxA, aA)
	require.NoError(t, err)
	require.Empty(t, a.GroupIDs, "group transfer never transfers account ownership")
	var outbox int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1 AND event_type = $2", aA, service.SchedulerOutboxEventAccountGroupsChanged).Scan(&outbox))
	require.Positive(t, outbox)

	// Account reassignment similarly keeps matching groups and removes both
	// foreign-owned and administrator-only bindings.
	gOld := newGroup(ctxA, "old-account-group")
	require.NoError(t, accounts.BindGroups(ctx, aA, []int64{gA, gB, gLegacy, gOld}))
	_, affected, err = groups.TransferResourceOwner(ctx, "accounts", aA, &userIDs[1])
	require.NoError(t, err)
	require.Equal(t, []int64{aA}, affected)
	a, err = accounts.GetByID(ctxB, aA)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{gA, gB}, a.GroupIDs)
	liveGroups, err := accounts.ListGatewayAccountGroupIDs(ctx, []int64{aA, aB})
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{gA, gB}, liveGroups[aA])
	_, err = accounts.GetByID(ctxA, aA)
	require.ErrorIs(t, err, service.ErrAccountNotFound)

	// Clearing an owner is normalized to null and preserves only legacy links.
	require.NoError(t, accounts.BindGroups(ctx, aA, []int64{gB, gLegacy}))
	zero := int64(0)
	owner, _, err = groups.TransferResourceOwner(ctx, "accounts", aA, &zero)
	require.NoError(t, err)
	require.Nil(t, owner.UserID)
	a, err = accounts.GetByID(ctx, aA)
	require.NoError(t, err)
	require.Equal(t, []int64{gLegacy}, a.GroupIDs)
	_, _, err = groups.TransferResourceOwner(ctx, "groups", gOld, &userIDs[2])
	require.ErrorIs(t, err, service.ErrUserNotFound)
	_, _, err = groups.TransferResourceOwner(ctx, "groups", gOld, &zero)
	require.NoError(t, err)

	// Rare direct references reject the operation before detaching any binding.
	gRoute := newGroup(ctxA, "route-group")
	require.NoError(t, accounts.BindGroups(ctx, aB, []int64{gRoute, gB}))
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET model_routing = jsonb_build_object('test', jsonb_build_array($1::bigint)) WHERE id = $2", aB, gRoute)
	require.NoError(t, err)
	_, _, err = groups.TransferResourceOwner(ctx, "accounts", aB, nil)
	require.ErrorIs(t, err, service.ErrResourceOwnerConflict)
	a, err = accounts.GetByID(ctx, aB)
	require.NoError(t, err)
	require.ElementsMatch(t, []int64{gRoute, gB}, a.GroupIDs)
	_, _, err = groups.TransferResourceOwner(ctx, "groups", gRoute, nil)
	require.ErrorIs(t, err, service.ErrResourceOwnerConflict)
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET model_routing = '{\"empty\":null}'::jsonb, codex_models_manifest_config = jsonb_build_object('enabled',false,'account_ids',jsonb_build_array($1::bigint)) WHERE id = $2", aB, gRoute)
	require.NoError(t, err)
	_, _, err = groups.TransferResourceOwner(ctx, "accounts", aB, nil)
	require.ErrorIs(t, err, service.ErrResourceOwnerConflict, "disabled pinned configurations still contain a direct account reference")
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET model_routing = null, codex_models_manifest_config = '{\"account_ids\":null}'::jsonb, fallback_group_id = $1 WHERE id = $2", gB, gRoute)
	require.NoError(t, err)
	_, _, err = groups.TransferResourceOwner(ctx, "groups", gRoute, nil)
	require.ErrorIs(t, err, service.ErrResourceOwnerConflict)
	_, err = integrationDB.ExecContext(ctx, "UPDATE groups SET fallback_group_id = null WHERE id = $1", gRoute)
	require.NoError(t, err)
	_, _, err = groups.TransferResourceOwner(ctx, "groups", gRoute, nil)
	require.NoError(t, err, "null JSON arrays do not prevent a valid transfer")

	child := newAccount(ctxB, "child-account")
	_, err = integrationDB.ExecContext(ctx, "UPDATE accounts SET parent_account_id = $1, quota_dimension = 'spark' WHERE id = $2", aB, child)
	require.NoError(t, err)
	_, _, err = groups.TransferResourceOwner(ctx, "accounts", child, nil)
	require.ErrorIs(t, err, service.ErrResourceOwnerConflict)
	_, _, err = groups.TransferResourceOwner(ctx, "accounts", aB, nil)
	require.ErrorIs(t, err, service.ErrResourceOwnerConflict)

	// A request guard spans both the original ownership check and its later
	// mutation, even though the existing CRUD code uses another connection.
	guardedGroup := newGroup(ctxA, "guarded-write")
	guarded := make(chan struct{})
	release := make(chan struct{})
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- groups.WithResourceOwnershipReadLock(ctxA, func() error {
			_, err := groups.GetByID(ctxA, guardedGroup)
			close(guarded)
			if err != nil {
				return err
			}
			<-release
			_, err = integrationDB.ExecContext(ctxA, "UPDATE groups SET description = 'guarded ordinary write' WHERE id = $1", guardedGroup)
			return err
		})
	}()
	select {
	case <-guarded:
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary write did not acquire ownership guard")
	}
	defer close(release)
	transferCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	_, _, err = groups.TransferResourceOwner(transferCtx, "groups", guardedGroup, &userIDs[1])
	cancel()
	require.Error(t, err, "a transfer must wait for an in-flight ordinary ownership check/write")
	var pgError *pq.Error
	require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.As(err, &pgError), "cancellation should come from context or PostgreSQL")
	// Release without closing twice, including if an earlier assertion failed.
	release <- struct{}{}
	require.NoError(t, <-writeDone)
	owner, _, err = groups.TransferResourceOwner(ctx, "groups", guardedGroup, &userIDs[1])
	require.NoError(t, err)
	require.Equal(t, userIDs[1], *owner.UserID)
	_, err = groups.GetByID(ctxA, guardedGroup)
	require.ErrorIs(t, err, service.ErrGroupNotFound)

	// A queued transfer must not prevent the guarded callback from creating
	// and deleting ownership rows on its separate CRUD connection.
	queuedGroup := newGroup(ctxA, "queued-transfer")
	guardReady, allowCRUD := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-allowCRUD:
		default:
			close(allowCRUD)
		}
	}()
	writeResult, transferResult := make(chan error, 1), make(chan error, 1)
	var createdAccountID int64
	writeCtx, cancelWrite := context.WithTimeout(ctxA, 5*time.Second)
	defer cancelWrite()
	go func() {
		writeResult <- groups.WithResourceOwnershipReadLock(writeCtx, func() error {
			close(guardReady)
			select {
			case <-allowCRUD:
			case <-writeCtx.Done():
				return writeCtx.Err()
			}
			account := &service.Account{Name: suffix + "-guard-create", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Concurrency: 1}
			if err := accounts.Create(writeCtx, account); err != nil {
				return err
			}
			createdAccountID = account.ID
			if err := accounts.Delete(writeCtx, account.ID); err != nil {
				return err
			}
			_, err := integrationDB.ExecContext(writeCtx, "DELETE FROM custom_user_accounts WHERE account_id = $1", account.ID)
			return err
		})
	}()
	<-guardReady
	queuedCtx, cancelQueued := context.WithTimeout(ctx, 5*time.Second)
	defer cancelQueued()
	go func() {
		_, _, err := groups.TransferResourceOwner(queuedCtx, "groups", queuedGroup, &userIDs[1])
		transferResult <- err
	}()
	var waiting bool
	require.Eventually(t, func() bool {
		if err := integrationDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype = 'advisory' AND NOT granted)").Scan(&waiting); err != nil {
			return false
		}
		return waiting
	}, 2*time.Second, 10*time.Millisecond, "transfer should queue behind the ordinary request guard")
	close(allowCRUD)
	select {
	case err := <-writeResult:
		if createdAccountID > 0 {
			accountIDs = append(accountIDs, createdAccountID)
		}
		require.NoError(t, err, "queued transfer must not block guarded CREATE/DELETE")
	case <-time.After(3 * time.Second):
		t.Fatal("queued ownership transfer blocked the guarded CRUD connection")
	}
	require.NoError(t, <-transferResult)
}
