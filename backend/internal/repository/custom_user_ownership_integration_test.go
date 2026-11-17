//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestUserOwnershipRepositoryIsolationPostgres(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	groups := newGroupRepositoryWithSQL(client, integrationDB)
	accounts := newAccountRepositoryWithSQL(client, integrationDB, nil)
	suffix := fmt.Sprintf("ownership-%d", time.Now().UnixNano())
	var userIDs, groupIDs, accountIDs []int64
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM account_groups WHERE account_id = ANY($1)", pq.Array(accountIDs))
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM accounts WHERE id = ANY($1)", pq.Array(accountIDs))
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = ANY($1)", pq.Array(groupIDs))
		_, _ = integrationDB.ExecContext(ctx, "DELETE FROM users WHERE id = ANY($1)", pq.Array(userIDs))
	})
	for _, label := range []string{"a", "b"} {
		u, err := client.User.Create().SetEmail(suffix + label + "@example.test").SetPasswordHash("test-hash").Save(ctx)
		require.NoError(t, err)
		userIDs = append(userIDs, u.ID)
	}
	ctxA, ctxB := ownership.WithUser(ctx, userIDs[0]), ownership.WithUser(ctx, userIDs[1])
	for i, ownerCtx := range []context.Context{ctxA, ctxB} {
		g := &service.Group{Name: fmt.Sprintf("%s-group-%d", suffix, i), Platform: service.PlatformOpenAI,
			RateMultiplier: 1, ImageRateMultiplier: 1, Status: service.StatusActive, SubscriptionType: service.SubscriptionTypeStandard}
		require.NoError(t, groups.Create(ownerCtx, g))
		groupIDs = append(groupIDs, g.ID)
		a := &service.Account{Name: fmt.Sprintf("%s-account-%d", suffix, i), Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1}
		require.NoError(t, accounts.Create(ownerCtx, a))
		accountIDs = append(accountIDs, a.ID)
		require.NoError(t, accounts.BindGroups(ownerCtx, a.ID, []int64{g.ID}))
	}

	params := pagination.PaginationParams{Page: 1, PageSize: 10}
	for i, ownerCtx := range []context.Context{ctxA, ctxB} {
		listedGroups, page, err := groups.ListWithFilters(ownerCtx, params, "", "", suffix, nil)
		require.NoError(t, err)
		require.EqualValues(t, 1, page.Total)
		require.Len(t, listedGroups, 1)
		require.Equal(t, groupIDs[i], listedGroups[0].ID)
		listedAccounts, page, err := accounts.ListWithFilters(ownerCtx, params, "", "", "", suffix, 0, "")
		require.NoError(t, err)
		require.EqualValues(t, 1, page.Total)
		require.Len(t, listedAccounts, 1)
		require.Equal(t, accountIDs[i], listedAccounts[0].ID)
		require.Equal(t, []int64{groupIDs[i]}, listedAccounts[0].GroupIDs)
	}
	_, err := groups.GetByID(ctxA, groupIDs[1])
	require.ErrorIs(t, err, service.ErrGroupNotFound)
	_, err = accounts.GetByID(ctxA, accountIDs[1])
	require.ErrorIs(t, err, service.ErrAccountNotFound)
	require.ErrorIs(t, groups.Update(ctxA, &service.Group{ID: groupIDs[1]}), service.ErrGroupNotFound)
	require.ErrorIs(t, accounts.Delete(ctxA, accountIDs[1]), service.ErrAccountNotFound)
	require.ErrorIs(t, accounts.BindGroups(ctxA, accountIDs[0], []int64{groupIDs[1]}), service.ErrGroupNotFound)
	require.ErrorIs(t, groups.BindAccountsToGroup(ctxA, groupIDs[0], []int64{accountIDs[1]}), service.ErrAccountNotFound)
	foreignFallback := groupIDs[1]
	require.ErrorIs(t, groups.Create(ctxA, &service.Group{Name: suffix + "-invalid", Platform: service.PlatformOpenAI,
		FallbackGroupID: &foreignFallback}), service.ErrGroupNotFound)

	// The same repository retains the full existing administrative view.
	allGroups, page, err := groups.ListWithFilters(ctx, params, "", "", suffix, nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, allGroups, 2)
	allAccounts, page, err := accounts.ListWithFilters(ctx, params, "", "", "", suffix, 0, "")
	require.NoError(t, err)
	require.EqualValues(t, 2, page.Total)
	require.Len(t, allAccounts, 2)
	owned, err := groups.IsOwnedBy(ctx, userIDs[0], groupIDs[0])
	require.NoError(t, err)
	require.True(t, owned)
	owned, err = groups.IsOwnedBy(ctx, userIDs[0], groupIDs[1])
	require.NoError(t, err)
	require.False(t, owned)
	ownedIDs, err := groups.ListOwnedGroupIDs(ctx, userIDs[0])
	require.NoError(t, err)
	require.Equal(t, []int64{groupIDs[0]}, ownedIDs)

	// Successful writes/deletes remain available for the current owner.
	account, err := accounts.GetByID(ctxA, accountIDs[0])
	require.NoError(t, err)
	account.Name += "-updated"
	require.NoError(t, accounts.Update(ctxA, account))
	require.NoError(t, accounts.Delete(ctxA, accountIDs[0]))
	require.NoError(t, groups.Delete(ctxA, groupIDs[0]))
	_, err = groups.GetByID(ctxA, groupIDs[0])
	require.ErrorIs(t, err, service.ErrGroupNotFound)
}
