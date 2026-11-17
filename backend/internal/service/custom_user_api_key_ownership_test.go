//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/stretchr/testify/require"
)

type ownedAPIKeyGroupRepo struct {
	GroupRepository
	owners map[int64]int64
	groups []Group
	err    error
}

func (r *ownedAPIKeyGroupRepo) IsOwnedBy(_ context.Context, userID, groupID int64) (bool, error) {
	return r.owners[groupID] == userID, r.err
}

func (r *ownedAPIKeyGroupRepo) ListOwnedGroupIDs(_ context.Context, userID int64) ([]int64, error) {
	ids := []int64{}
	for id, owner := range r.owners {
		if owner == userID {
			ids = append(ids, id)
		}
	}
	return ids, r.err
}

func (r *ownedAPIKeyGroupRepo) ListActive(context.Context) ([]Group, error) {
	return r.groups, nil
}

func (r *ownedAPIKeyGroupRepo) GetByID(_ context.Context, id int64) (*Group, error) {
	for _, group := range r.groups {
		if group.ID == id {
			return &group, nil
		}
	}
	return nil, ErrGroupNotFound
}

func (r *ownedAPIKeyGroupRepo) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return r.GetByID(ctx, id)
}

type ownedAPIKeyRepo struct {
	APIKeyRepository
	key     *APIKey
	created int
	updated int
}

func (r *ownedAPIKeyRepo) Create(_ context.Context, key *APIKey) error {
	r.created++
	r.key = key
	return nil
}

func (r *ownedAPIKeyRepo) GetByID(context.Context, int64) (*APIKey, error) {
	copy := *r.key
	return &copy, nil
}

func (r *ownedAPIKeyRepo) Update(_ context.Context, key *APIKey, _ APIKeyUpdateFields) error {
	r.updated++
	r.key = key
	return nil
}

func TestUserOwnedAPIKeyCreateAndRebind(t *testing.T) {
	ctx := context.Background()
	ownedID, foreignID := int64(10), int64(20)
	groups := &ownedAPIKeyGroupRepo{
		owners: map[int64]int64{ownedID: 1, foreignID: 2},
		groups: []Group{{ID: ownedID, IsExclusive: true}, {ID: foreignID}},
	}
	repo := &ownedAPIKeyRepo{}
	svc := &APIKeyService{
		apiKeyRepo: repo,
		userRepo: &visibilityUserRepo{user: &User{
			ID: 1, Role: RoleUser, AllowedGroups: []int64{foreignID}, RestrictPublicGroups: true,
		}},
		groupRepo: groups,
		cfg:       &config.Config{},
	}
	for _, id := range []*int64{nil, &foreignID} {
		_, err := svc.Create(ctx, 1, CreateAPIKeyRequest{Name: "blocked", GroupID: id})
		require.ErrorIs(t, err, ErrGroupNotAllowed)
	}
	require.Zero(t, repo.created)
	key, err := svc.Create(ctx, 1, CreateAPIKeyRequest{Name: "own exclusive group", GroupID: &ownedID})
	require.NoError(t, err)
	require.Equal(t, &ownedID, key.GroupID)
	require.Equal(t, 1, repo.created)

	_, err = svc.Update(ctx, key.ID, 1, UpdateAPIKeyRequest{GroupID: &foreignID})
	require.ErrorIs(t, err, ErrGroupNotAllowed)
	require.Zero(t, repo.updated)
	_, err = svc.Update(ctx, key.ID, 1, UpdateAPIKeyRequest{GroupID: &ownedID})
	require.NoError(t, err)
	require.Equal(t, 1, repo.updated)
}

func TestUserOwnedAPIKeyAvailableGroupsAndVisibility(t *testing.T) {
	groups := &ownedAPIKeyGroupRepo{
		owners: map[int64]int64{10: 1, 20: 2, 30: 1},
		groups: []Group{{ID: 10, IsExclusive: true}, {ID: 20}, {ID: 40}},
	}
	svc := &APIKeyService{
		userRepo:  &visibilityUserRepo{user: &User{ID: 1, Role: RoleUser, AllowedGroups: []int64{20, 40}}},
		groupRepo: groups,
	}
	available, err := svc.GetAvailableGroups(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, []Group{{ID: 10, IsExclusive: true}}, available)
	visible, restricted, err := svc.GetUserGroupVisibility(context.Background(), 1)
	require.NoError(t, err)
	require.True(t, restricted)
	require.Equal(t, map[int64]struct{}{10: {}, 30: {}}, visible)
	groups.err = errors.New("ownership database unavailable")
	_, err = svc.GetAvailableGroups(context.Background(), 1)
	require.ErrorIs(t, err, groups.err)
	_, _, err = svc.GetUserGroupVisibility(context.Background(), 1)
	require.ErrorIs(t, err, groups.err)
}

type ownedAPIKeyRateRepo struct {
	UserGroupRateRepository
	rates map[int64]float64
}

func (r *ownedAPIKeyRateRepo) GetByUserID(context.Context, int64) (map[int64]float64, error) {
	return r.rates, nil
}

func TestUserOwnedAPIKeyRatesExcludeOtherGroups(t *testing.T) {
	groups := &ownedAPIKeyGroupRepo{owners: map[int64]int64{10: 1, 20: 2}}
	rates := &ownedAPIKeyRateRepo{rates: map[int64]float64{10: 1.5, 20: 2, 30: 3}}
	user := &User{ID: 1, Role: RoleUser}
	svc := &APIKeyService{
		groupRepo: groups, userRepo: &visibilityUserRepo{user: user}, userGroupRateRepo: rates,
	}
	got, err := svc.GetUserGroupRates(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, map[int64]float64{10: 1.5}, got)
	require.Len(t, rates.rates, 3, "filter must not mutate repository results")
	user.Role = RoleAdmin
	got, err = svc.GetUserGroupRates(context.Background(), 1)
	require.NoError(t, err)
	require.Equal(t, rates.rates, got)
}

func TestUserOwnedAPIKeyCachedAuthenticationRechecksOwnership(t *testing.T) {
	groupID, ownFallback, foreignFallback := int64(10), int64(30), int64(20)
	groups := &ownedAPIKeyGroupRepo{owners: map[int64]int64{groupID: 1, ownFallback: 1, foreignFallback: 2}}
	snapshot := &APIKeyAuthSnapshot{
		Version: apiKeyAuthSnapshotVersion, APIKeyID: 7, UserID: 1, GroupID: &groupID,
		User: APIKeyAuthUserSnapshot{ID: 1, Role: RoleUser},
		Group: &APIKeyAuthGroupSnapshot{
			ID: groupID, IsExclusive: true, FallbackGroupID: &foreignFallback,
			FallbackGroupIDOnInvalidRequest: &ownFallback,
		},
	}
	cache := &authCacheStub{getAuthCache: func(context.Context, string) (*APIKeyAuthCacheEntry, error) {
		return &APIKeyAuthCacheEntry{Snapshot: snapshot}, nil
	}}
	svc := &APIKeyService{groupRepo: groups, cache: cache, authCfg: apiKeyAuthCacheConfig{l2TTL: time.Minute}}
	key, err := svc.GetByKey(context.Background(), "cached-key")
	require.NoError(t, err)
	require.True(t, key.UserGroupOwnershipVerified())
	require.Nil(t, key.Group.FallbackGroupID, "foreign fallback must not bypass group ownership")
	require.Equal(t, &ownFallback, key.Group.FallbackGroupIDOnInvalidRequest)
	require.Equal(t, &foreignFallback, snapshot.Group.FallbackGroupID, "request must not change cached snapshot")

	groups.owners[ownFallback] = 2
	key, err = svc.GetByKey(context.Background(), "cached-key")
	require.NoError(t, err)
	require.Nil(t, key.Group.FallbackGroupIDOnInvalidRequest, "a transferred fallback must be removed even from a warmed auth snapshot")
	require.Equal(t, &ownFallback, snapshot.Group.FallbackGroupIDOnInvalidRequest)

	groups.owners[groupID] = 2
	key, err = svc.GetByKey(context.Background(), "cached-key")
	require.Nil(t, key)
	require.ErrorIs(t, err, ErrGroupNotAllowed, "even an existing auth cache entry must obey current ownership")
	groups.owners[groupID] = 1
	groups.err = errors.New("ownership database unavailable")
	key, err = svc.GetByKey(context.Background(), "cached-key")
	require.Nil(t, key)
	require.ErrorIs(t, err, groups.err, "ownership failures must fail closed")
}

func TestUserOwnedAPIKeyCachedAuthenticationRejectsMissingGroup(t *testing.T) {
	groups := &ownedAPIKeyGroupRepo{owners: map[int64]int64{10: 1}}
	cache := &authCacheStub{getAuthCache: func(context.Context, string) (*APIKeyAuthCacheEntry, error) {
		return &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{
			Version: apiKeyAuthSnapshotVersion, UserID: 1, User: APIKeyAuthUserSnapshot{ID: 1, Role: RoleUser},
		}}, nil
	}}
	svc := &APIKeyService{groupRepo: groups, cache: cache, authCfg: apiKeyAuthCacheConfig{l2TTL: time.Minute}}
	_, err := svc.GetByKey(context.Background(), "legacy-no-group-key")
	require.ErrorIs(t, err, ErrGroupNotAllowed)
}

func TestUserOwnedAPIKeyAdminBehaviorUnchanged(t *testing.T) {
	groups := &ownedAPIKeyGroupRepo{owners: map[int64]int64{10: 2}, err: errors.New("must not query ownership")}
	repo := &ownedAPIKeyRepo{}
	svc := &APIKeyService{
		apiKeyRepo: repo, groupRepo: groups, cfg: &config.Config{},
		userRepo: &visibilityUserRepo{user: &User{ID: 1, Role: RoleAdmin}},
	}
	_, err := svc.Create(context.Background(), 1, CreateAPIKeyRequest{Name: "admin no group"})
	require.NoError(t, err)
	cache := &authCacheStub{getAuthCache: func(context.Context, string) (*APIKeyAuthCacheEntry, error) {
		return &APIKeyAuthCacheEntry{Snapshot: &APIKeyAuthSnapshot{
			Version: apiKeyAuthSnapshotVersion, UserID: 1, User: APIKeyAuthUserSnapshot{ID: 1, Role: RoleAdmin},
		}}, nil
	}}
	svc.cache = cache
	svc.authCfg = apiKeyAuthCacheConfig{l2TTL: time.Minute}
	key, err := svc.GetByKey(context.Background(), "admin-no-group-key")
	require.NoError(t, err)
	require.False(t, key.UserGroupOwnershipVerified())
}

func TestUserOwnedAPIKeyGatewayRejectsForeignNestedFallback(t *testing.T) {
	primaryID, ownFallback, foreignFallback := int64(10), int64(30), int64(20)
	groups := &ownedAPIKeyGroupRepo{
		owners: map[int64]int64{primaryID: 1, ownFallback: 1, foreignFallback: 2},
		groups: []Group{
			{ID: primaryID, ClaudeCodeOnly: true, FallbackGroupID: &ownFallback},
			{ID: ownFallback, ClaudeCodeOnly: true, FallbackGroupID: &foreignFallback},
			{ID: foreignFallback, Platform: PlatformAnthropic, Status: StatusActive, Hydrated: true},
		},
	}
	svc := &GatewayService{groupRepo: groups}
	ctx := ownership.WithGroupUser(context.Background(), 1)
	_, scopedAccounts := ownership.UserID(ctx)
	require.False(t, scopedAccounts, "gateway ownership must not change administrator account bindings")
	group, _, err := svc.resolveGatewayGroup(ctx, &primaryID)
	require.Nil(t, group)
	require.ErrorIs(t, err, ErrGroupNotAllowed)

	// A group previously put into the request context cannot bypass the live check.
	ctx = context.WithValue(ctx, ctxkey.Group, &groups.groups[2])
	_, err = svc.resolveGroupByID(ctx, foreignFallback)
	require.ErrorIs(t, err, ErrGroupNotAllowed)

	group, resolved, err := svc.resolveGatewayGroup(context.Background(), &primaryID)
	require.NoError(t, err, "unscoped administrator resolution keeps existing fallback behavior")
	require.Equal(t, foreignFallback, group.ID)
	require.Equal(t, &foreignFallback, resolved)
}

type ownedGatewaySnapshotCache struct {
	SchedulerCache
	accounts map[int64]Account
	buckets  map[int64][]int64
	writeErr error
}

func (c *ownedGatewaySnapshotCache) GetSnapshot(_ context.Context, bucket SchedulerBucket) ([]*Account, bool, error) {
	var accounts []*Account
	for _, id := range c.buckets[bucket.GroupID] {
		account := c.accounts[id]
		accounts = append(accounts, &account)
	}
	return accounts, true, nil
}

func (c *ownedGatewaySnapshotCache) SetAccount(_ context.Context, account *Account) error {
	if c.writeErr != nil {
		return c.writeErr
	}
	c.accounts[account.ID] = *account
	return nil
}

func (c *ownedGatewaySnapshotCache) GetAccount(_ context.Context, id int64) (*Account, error) {
	account := c.accounts[id]
	return &account, nil
}

type ownedGatewayMembershipRepo struct {
	AccountRepository
	groups map[int64][]int64
	err    error
}

func (r *ownedGatewayMembershipRepo) ListGatewayAccountGroupIDs(context.Context, []int64) (map[int64][]int64, error) {
	return r.groups, r.err
}

type ownedGatewaySnapshotValuesCache struct{ *ownedGatewaySnapshotCache }

func (c *ownedGatewaySnapshotValuesCache) GetSnapshotValues(ctx context.Context, bucket SchedulerBucket) ([]Account, bool, error) {
	pointers, hit, err := c.GetSnapshot(ctx, bucket)
	accounts := make([]Account, 0, len(pointers))
	for _, account := range pointers {
		accounts = append(accounts, *account)
	}
	return accounts, hit, err
}

func TestUserOwnedGatewaySnapshotRejectsTransferredAccountBeforeBucketRebuild(t *testing.T) {
	for _, values := range []bool{false, true} {
		name := "pointers"
		if values {
			name = "values"
		}
		t.Run(name, func(t *testing.T) {
			ctx := ownership.WithGroupUser(context.Background(), 1)
			oldGroup, newGroup := int64(10), int64(20)
			cache := &ownedGatewaySnapshotCache{
				accounts: map[int64]Account{
					7: {ID: 7, Status: StatusActive, Schedulable: true, GroupIDs: []int64{oldGroup}},
					8: {ID: 8, Status: StatusActive, Schedulable: true, AccountGroups: []AccountGroup{{GroupID: oldGroup}}},
					9: {ID: 9, Status: StatusActive, Schedulable: true, GroupIDs: []int64{oldGroup}},
				},
				buckets: map[int64][]int64{oldGroup: {7, 8, 9}, newGroup: {7}},
			}
			var reader SchedulerCache = cache
			if values {
				reader = &ownedGatewaySnapshotValuesCache{cache}
			}
			repo := &ownedGatewayMembershipRepo{groups: map[int64][]int64{7: {oldGroup}, 8: {oldGroup}, 9: {oldGroup}}}
			svc := &SchedulerSnapshotService{cache: reader, accountRepo: repo}
			accounts, _, err := svc.ListSchedulableAccounts(ctx, &oldGroup, PlatformAnthropic, false)
			require.NoError(t, err)
			require.Len(t, accounts, 3, "warm the old group before transfer")

			// The transfer publishes fresh account metadata, but its durable
			// outbox has not rebuilt the old group's account-ID bucket yet.
			repo.groups[7] = []int64{newGroup}
			delete(repo.groups, 9)
			require.NoError(t, cache.SetAccount(ctx, &Account{
				ID: 7, Status: StatusActive, Schedulable: true, GroupIDs: []int64{newGroup},
			}))
			require.NoError(t, cache.SetAccount(ctx, &Account{
				ID: 9, Status: StatusActive, Schedulable: true,
			}))
			accounts, _, err = svc.ListSchedulableAccounts(ctx, &oldGroup, PlatformAnthropic, false)
			require.NoError(t, err)
			require.Equal(t, []int64{8}, ownedGatewaySnapshotAccountIDs(accounts))
			accounts, _, err = svc.ListSchedulableAccounts(ctx, &newGroup, PlatformAnthropic, false)
			require.NoError(t, err)
			require.Equal(t, []int64{7}, ownedGatewaySnapshotAccountIDs(accounts))

			accounts, _, err = svc.ListSchedulableAccounts(context.Background(), &oldGroup, PlatformAnthropic, false)
			require.NoError(t, err)
			require.Len(t, accounts, 3, "administrator cache behavior stays unchanged")
			require.Equal(t, []int64{7, 8, 9}, cache.buckets[oldGroup], "filter must not modify shared cache buckets")
			require.Equal(t, []int64{newGroup}, cache.accounts[7].GroupIDs, "filter must not modify cached metadata")

			simpleCfg := &config.Config{RunMode: config.RunModeSimple}
			svc.cfg = simpleCfg
			cache.buckets[0] = []int64{7, 8, 9}
			accounts, _, err = svc.ListSchedulableAccounts(ctx, &oldGroup, PlatformAnthropic, false)
			require.NoError(t, err)
			require.Len(t, accounts, 3, "simple mode's shared bucket keeps its existing behavior")
		})
	}
}

type ownedGatewayStickyCache struct{ GatewayCache }

func (*ownedGatewayStickyCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 7, nil
}

func TestUserOwnedGatewayTransferRejectsStaleCacheWhenRefreshFails(t *testing.T) {
	ctx := ownership.WithGroupUser(context.Background(), 1)
	oldGroup, newGroup := int64(10), int64(20)
	repo := &ownedGatewayMembershipRepo{groups: map[int64][]int64{7: {oldGroup}}}
	cache := &ownedGatewaySnapshotCache{
		accounts: map[int64]Account{
			7: {ID: 7, Status: StatusActive, Platform: PlatformGemini, Schedulable: true,
				GroupIDs: []int64{oldGroup}, AccountGroups: []AccountGroup{{GroupID: oldGroup}}},
		},
		buckets: map[int64][]int64{oldGroup: {7}, newGroup: {7}},
	}
	svc := &SchedulerSnapshotService{cache: cache, accountRepo: repo}
	accounts, _, err := svc.ListSchedulableAccounts(ctx, &oldGroup, PlatformGemini, false)
	require.NoError(t, err)
	require.Len(t, accounts, 1, "warm both membership and account caches before transfer")
	account, err := svc.GetAccount(ctx, 7)
	require.NoError(t, err)
	require.True(t, openAIStickyAccountMatchesGroup(account, &oldGroup))

	repo.groups[7] = []int64{newGroup}
	cache.writeErr = errors.New("redis writes unavailable")
	require.Error(t, cache.SetAccount(ctx, &Account{ID: 7, GroupIDs: []int64{newGroup}}))
	accounts, _, err = svc.ListSchedulableAccounts(ctx, &oldGroup, PlatformGemini, false)
	require.NoError(t, err)
	require.Empty(t, accounts, "old group cannot use a transferred account even when cached membership is unchanged")
	accounts, _, err = svc.ListSchedulableAccounts(ctx, &newGroup, PlatformGemini, false)
	require.NoError(t, err)
	require.Equal(t, []int64{7}, ownedGatewaySnapshotAccountIDs(accounts))

	account, err = svc.GetAccount(ctx, 7)
	require.NoError(t, err)
	require.False(t, (&GatewayService{}).isAccountInGroup(account, &oldGroup), "Anthropic sticky checks receive live membership")
	require.False(t, openAIStickyAccountMatchesGroup(account, &oldGroup), "OpenAI sticky checks receive live membership")
	require.True(t, openAIStickyAccountMatchesGroup(account, &newGroup), "binding can move to the target or fallback group")
	gemini := &GeminiMessagesCompatService{schedulerSnapshot: svc, cache: &ownedGatewayStickyCache{}}
	require.Nil(t, gemini.tryStickySessionHit(ctx, &oldGroup, "session", "key", "model", nil, PlatformGemini, false),
		"Gemini sticky hits also require the live group binding")
	require.Equal(t, []int64{oldGroup}, cache.accounts[7].GroupIDs, "live check must not mutate shared Redis snapshots")
	require.Equal(t, oldGroup, cache.accounts[7].AccountGroups[0].GroupID)

	repo.err = errors.New("membership database unavailable")
	accounts, _, err = svc.ListSchedulableAccounts(ctx, &oldGroup, PlatformGemini, false)
	require.ErrorIs(t, err, repo.err)
	require.Empty(t, accounts, "database failure cannot fall back to stale cached authorization")
	account, err = svc.GetAccount(ctx, 7)
	require.ErrorIs(t, err, repo.err)
	require.Nil(t, account)
	accounts, _, err = svc.ListSchedulableAccounts(context.Background(), &oldGroup, PlatformGemini, false)
	require.NoError(t, err)
	require.Len(t, accounts, 1, "administrator binding behavior does not depend on ownership queries")
}

func ownedGatewaySnapshotAccountIDs(accounts []Account) []int64 {
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	return ids
}
