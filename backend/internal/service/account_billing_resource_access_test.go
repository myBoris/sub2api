//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	"github.com/stretchr/testify/require"
)

func TestBatchImageCandidatesHonorRenewalAndRestoreImmediately(t *testing.T) {
	store := &renewalAccessStoreStub{states: map[int64]accountbilling.AccessState{1: {Expired: true}}}
	svc := &BatchImagePublicService{AccountRepo: &publicBatchImageAccountRepo{accounts: []Account{
		{ID: 1, Platform: PlatformGemini}, {ID: 2, Platform: PlatformGemini},
	}}, accountRenewalGuard: NewAccountRenewalGuard(store)}
	accounts, err := svc.listCandidateAccounts(context.Background(), nil, PlatformGemini)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(2), accounts[0].ID)
	store.states[2] = accountbilling.AccessState{Expired: true}
	_, err = svc.listCandidateAccounts(context.Background(), nil, PlatformGemini)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	delete(store.states, 1)
	accounts, err = svc.listCandidateAccounts(context.Background(), nil, PlatformGemini)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(1), accounts[0].ID)
}

func TestGeminiCacheCannotCreateOrExtendAfterAccountRenewalExpiry(t *testing.T) {
	store := &renewalAccessStoreStub{states: map[int64]accountbilling.AccessState{1: {Expired: true}}}
	svc := &GeminiCachedContentService{accountRenewalGuard: NewAccountRenewalGuard(store)}
	account := &Account{ID: 1}
	// No upstream client or payload is supplied: rejection precedes all access.
	_, err := svc.CreateUpstream(context.Background(), account, nil, "", "", time.Minute)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	require.ErrorIs(t, err, ErrGeminiCachedContentRequestNotSent)
	_, err = svc.PatchUpstream(context.Background(), account, nil, time.Minute)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	require.ErrorIs(t, err, ErrGeminiCachedContentRequestNotSent)
}
