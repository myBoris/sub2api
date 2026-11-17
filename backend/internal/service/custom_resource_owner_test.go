package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/stretchr/testify/require"
)

type resourceOwnerRepositoryStub struct {
	GroupRepository
	transfer func(context.Context, string, int64, *int64) (*ResourceOwner, []int64, error)
	read     func(context.Context, string, []int64) ([]ResourceOwner, error)
}

func (r *resourceOwnerRepositoryStub) ResourceOwners(ctx context.Context, kind string, ids []int64) ([]ResourceOwner, error) {
	return r.read(ctx, kind, ids)
}

func (r *resourceOwnerRepositoryStub) TransferResourceOwner(ctx context.Context, kind string, id int64, owner *int64) (*ResourceOwner, []int64, error) {
	return r.transfer(ctx, kind, id, owner)
}

type resourceOwnerAccountRefresher struct {
	AccountRepository
	refreshed []int64
}

func (r *resourceOwnerAccountRefresher) RefreshResourceOwnerAccounts(_ context.Context, ids []int64) {
	r.refreshed = append(r.refreshed, ids...)
}

func TestResourceOwnerServiceForbidsScopedTransferBeforeRepositoryAccess(t *testing.T) {
	s := &adminServiceImpl{}
	_, err := s.SetResourceOwner(ownership.WithUser(context.Background(), 1), "accounts", 2, nil)
	require.ErrorIs(t, err, ErrResourceOwnerForbidden)
}

func TestResourceOwnerServicePreservesScopeForBatchRead(t *testing.T) {
	s := &adminServiceImpl{groupRepo: &resourceOwnerRepositoryStub{read: func(ctx context.Context, kind string, ids []int64) ([]ResourceOwner, error) {
		userID, scoped := ownership.UserID(ctx)
		require.True(t, scoped)
		require.EqualValues(t, 9, userID)
		require.Equal(t, "groups", kind)
		require.Equal(t, []int64{2, 3}, ids)
		return nil, ErrGroupNotFound
	}}}
	_, err := s.ResourceOwners(ownership.WithUser(context.Background(), 9), "groups", []int64{2, 3})
	require.ErrorIs(t, err, ErrGroupNotFound)
}

func TestResourceOwnerServiceRefreshesOnlyAfterSuccessfulTransfer(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "rolled back"}[failure], func(t *testing.T) {
			refresher := &resourceOwnerAccountRefresher{}
			s := &adminServiceImpl{accountRepo: refresher, groupRepo: &resourceOwnerRepositoryStub{
				transfer: func(context.Context, string, int64, *int64) (*ResourceOwner, []int64, error) {
					if failure {
						return nil, []int64{4}, errors.New("transaction failed")
					}
					return &ResourceOwner{ResourceID: 2}, []int64{4}, nil
				},
			}}
			_, err := s.SetResourceOwner(context.Background(), "accounts", 2, nil)
			if failure {
				require.Error(t, err)
				require.Empty(t, refresher.refreshed)
			} else {
				require.NoError(t, err)
				require.Equal(t, []int64{4}, refresher.refreshed)
			}
		})
	}
}
