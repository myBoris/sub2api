package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
)

// Keep the ownership extension separate from the upstream repository interface.
// The production group repository implements it; alternate repository adapters
// retain their existing authorization contract until they implement ownership.
type userOwnedGroupsRepository interface {
	IsOwnedBy(ctx context.Context, userID, groupID int64) (bool, error)
	ListOwnedGroupIDs(ctx context.Context, userID int64) ([]int64, error)
}

func (s *APIKeyService) validateOwnedGroup(ctx context.Context, user *User, groupID *int64) (bool, error) {
	repo, enabled := s.groupRepo.(userOwnedGroupsRepository)
	if !enabled || user == nil || user.IsAdmin() {
		return false, nil
	}
	if groupID == nil || *groupID <= 0 {
		return true, ErrGroupNotAllowed
	}
	owned, err := repo.IsOwnedBy(ctx, user.ID, *groupID)
	if err != nil {
		return true, fmt.Errorf("check group ownership: %w", err)
	}
	if !owned {
		return true, ErrGroupNotAllowed
	}
	return true, nil
}

func (s *APIKeyService) ownedGroupIDs(ctx context.Context, user *User) (map[int64]struct{}, bool, error) {
	repo, enabled := s.groupRepo.(userOwnedGroupsRepository)
	if !enabled || user == nil || user.IsAdmin() {
		return nil, false, nil
	}
	ids, err := repo.ListOwnedGroupIDs(ctx, user.ID)
	if err != nil {
		return nil, true, fmt.Errorf("list owned groups: %w", err)
	}
	owned := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		owned[id] = struct{}{}
	}
	return owned, true, nil
}

func (s *APIKeyService) filterOwnedGroupRates(ctx context.Context, userID int64, rates map[int64]float64) (map[int64]float64, error) {
	if _, enabled := s.groupRepo.(userOwnedGroupsRepository); !enabled {
		return rates, nil
	}
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	owned, enforced, err := s.ownedGroupIDs(ctx, user)
	if err != nil {
		return nil, err
	}
	if !enforced {
		return rates, nil
	}
	filtered := make(map[int64]float64)
	for id, rate := range rates {
		if _, ok := owned[id]; ok {
			filtered[id] = rate
		}
	}
	return filtered, nil
}

// GetByKey checks live ownership after the normal authentication lookup,
// including L1/L2 cache hits and keys created before ownership was introduced.
func (s *APIKeyService) GetByKey(ctx context.Context, key string) (*APIKey, error) {
	apiKey, err := s.getByKey(ctx, key)
	if err != nil || apiKey == nil {
		return apiKey, err
	}
	enforced, err := s.validateOwnedGroup(ctx, apiKey.User, apiKey.GroupID)
	if err != nil {
		return nil, err
	}
	if !enforced {
		return apiKey, nil
	}
	if apiKey.User.ID != apiKey.UserID || apiKey.Group == nil || apiKey.Group.ID != *apiKey.GroupID {
		return nil, ErrGroupNotAllowed
	}
	apiKey.userGroupOwnershipVerified = true

	// A fallback must obey the same ownership boundary as the key's main group.
	// Only mutate this request's group copy, leaving cached snapshots untouched.
	group := *apiKey.Group
	for _, fallback := range []**int64{&group.FallbackGroupID, &group.FallbackGroupIDOnInvalidRequest} {
		if *fallback == nil {
			continue
		}
		if _, err := s.validateOwnedGroup(ctx, apiKey.User, *fallback); err != nil {
			if !errors.Is(err, ErrGroupNotAllowed) {
				return nil, err
			}
			*fallback = nil
		}
	}
	apiKey.Group = &group
	return apiKey, nil
}

// UserGroupOwnershipVerified is request-local and is never stored in the auth
// cache. It allows an owner to use their own exclusive group without requiring
// a second grant through the upstream user_allowed_groups mechanism.
func (k *APIKey) UserGroupOwnershipVerified() bool {
	return k != nil && k.userGroupOwnershipVerified
}

func (s *GatewayService) checkOwnedGatewayGroup(ctx context.Context, groupID int64) error {
	userID, scoped := ownership.GroupUserID(ctx)
	if !scoped {
		return nil
	}
	repo, supported := s.groupRepo.(userOwnedGroupsRepository)
	if !supported {
		return ErrGroupNotAllowed
	}
	owned, err := repo.IsOwnedBy(ctx, userID, groupID)
	if err != nil {
		return fmt.Errorf("check gateway group ownership: %w", err)
	}
	if !owned {
		return ErrGroupNotAllowed
	}
	return nil
}

type gatewayAccountGroupsRepository interface {
	ListGatewayAccountGroupIDs(context.Context, []int64) (map[int64][]int64, error)
}

// Membership is checked against PostgreSQL even if Redis still contains the
// previous binding after a transfer. Account ownership itself is independent:
// administrators may explicitly bind another owner's account to a group.
func (s *SchedulerSnapshotService) filterOwnedGatewaySnapshotAccounts(ctx context.Context, groupID int64, accounts []Account) ([]Account, error) {
	if _, scoped := ownership.GroupUserID(ctx); !scoped || groupID <= 0 {
		return accounts, nil
	}
	if len(accounts) == 0 {
		return accounts, nil
	}
	ids := make([]int64, 0, len(accounts))
	for _, account := range accounts {
		ids = append(ids, account.ID)
	}
	groups, err := s.liveGatewayAccountGroups(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := accounts[:0]
	for i := range accounts {
		member := false
		for _, id := range groups[accounts[i].ID] {
			member = member || id == groupID
		}
		if member {
			out = append(out, accountWithLiveGatewayGroups(accounts[i], groups[accounts[i].ID]))
		}
	}
	clear(accounts[len(out):])
	return out, nil
}

func (s *SchedulerSnapshotService) liveGatewayAccountGroups(ctx context.Context, ids []int64) (map[int64][]int64, error) {
	repo, supported := s.accountRepo.(gatewayAccountGroupsRepository)
	if !supported {
		return nil, ErrGroupNotAllowed
	}
	groups, err := repo.ListGatewayAccountGroupIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("check gateway account membership: %w", err)
	}
	return groups, nil
}

// Sticky selection already checks an account's group IDs. Replace only this
// request's cached membership so it cannot authorize through stale Redis data.
func (s *SchedulerSnapshotService) refreshOwnedGatewayAccountGroups(ctx context.Context, account *Account) (*Account, error) {
	if _, scoped := ownership.GroupUserID(ctx); !scoped || s.isRunModeSimple() || account == nil {
		return account, nil
	}
	groups, err := s.liveGatewayAccountGroups(ctx, []int64{account.ID})
	if err != nil {
		return nil, err
	}
	copy := accountWithLiveGatewayGroups(*account, groups[account.ID])
	return &copy, nil
}

func accountWithLiveGatewayGroups(account Account, ids []int64) Account {
	account.GroupIDs = append([]int64(nil), ids...)
	groups := make([]AccountGroup, 0, len(ids))
	for _, id := range ids {
		group := AccountGroup{AccountID: account.ID, GroupID: id}
		for _, old := range account.AccountGroups {
			if old.GroupID == id {
				group = old
				break
			}
		}
		groups = append(groups, group)
	}
	account.AccountGroups = groups
	return account
}

func ownedGatewayStickyAccountAllowed(ctx context.Context, groupID *int64, account *Account, simple bool) bool {
	if _, scoped := ownership.GroupUserID(ctx); !scoped || simple || groupID == nil || *groupID <= 0 {
		return true
	}
	return openAIStickyAccountMatchesGroup(account, groupID)
}
