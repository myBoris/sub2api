//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type renewalAccessStoreStub struct {
	states map[int64]accountbilling.AccessState
	err    error
	ids    []int64
	now    time.Time
}

func (s *renewalAccessStoreStub) AccessStates(_ context.Context, ids []int64) (map[int64]accountbilling.AccessState, error) {
	s.ids = append([]int64(nil), ids...)
	return s.states, s.err
}
func (s *renewalAccessStoreStub) Now() time.Time { return s.now }

type renewalAccountRepositoryStub struct {
	AccountRepository
	accounts []Account
}

func (r *renewalAccountRepositoryStub) GetByID(_ context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			account := r.accounts[i]
			return &account, nil
		}
	}
	return nil, ErrAccountNotFound
}
func (r *renewalAccountRepositoryStub) ListSchedulableByGroupIDAndPlatform(context.Context, int64, string) ([]Account, error) {
	return r.accounts, nil
}
func (r *renewalAccountRepositoryStub) ListSchedulableByGroupIDAndPlatforms(context.Context, int64, []string) ([]Account, error) {
	return r.accounts, nil
}
func (r *renewalAccountRepositoryStub) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return r.accounts, nil
}
func (r *renewalAccountRepositoryStub) ListModelAvailabilityCandidates(context.Context, *int64, []string, bool) ([]Account, error) {
	return r.accounts, nil
}

func renewalTestAccounts() []Account {
	return []Account{
		{ID: 1, Platform: PlatformOpenAI, Status: StatusActive, Type: AccountTypeAPIKey, Schedulable: true, Credentials: map[string]any{"api_key": "not-a-real-key"}},
		{ID: 2, Platform: PlatformOpenAI, Status: StatusActive, Type: AccountTypeAPIKey, Schedulable: true, Credentials: map[string]any{"api_key": "not-a-real-key"}},
	}
}

func TestAccountRenewalGuardFiltersOnlyExpiredAndRestoresWithoutMutatingSharedAccounts(t *testing.T) {
	ctx := context.Background()
	store := &renewalAccessStoreStub{states: map[int64]accountbilling.AccessState{1: {Expired: true}}}
	guard := NewAccountRenewalGuard(store)
	accounts := renewalTestAccounts()
	parent := int64(1)
	accounts = append(accounts, Account{ID: 3, ParentAccountID: &parent, Schedulable: true}, Account{ID: 4, Schedulable: true})
	filtered, err := guard.FilterAccounts(ctx, accounts)
	require.NoError(t, err)
	require.Equal(t, []int64{2, 4}, []int64{filtered[0].ID, filtered[1].ID})
	require.Equal(t, int64(1), accounts[0].ID)
	require.True(t, accounts[0].Schedulable)
	require.ElementsMatch(t, []int64{1, 2, 3, 4}, store.ids)
	require.ErrorIs(t, guard.CheckAccount(ctx, &accounts[2]), ErrAccountRenewalExpired, "an unpaid parent cannot be bypassed by its shadow")
	store.states[1] = accountbilling.AccessState{}
	filtered, err = guard.FilterAccounts(ctx, accounts)
	require.NoError(t, err)
	require.Len(t, filtered, 4, "payment restores the next read without changing the scheduler cache")
}

func TestAccountRenewalGuardAllExpiredIsPaymentRequiredAndDatabaseFailureCannotAllowAccess(t *testing.T) {
	ctx := context.Background()
	store := &renewalAccessStoreStub{states: map[int64]accountbilling.AccessState{1: {Expired: true}, 2: {Expired: true}}}
	guard := NewAccountRenewalGuard(store)
	accounts, err := guard.FilterAccounts(ctx, renewalTestAccounts())
	require.Nil(t, accounts)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
	require.True(t, IsAccountRenewalExpired(err))
	require.Equal(t, http.StatusPaymentRequired, infraerrors.Code(err))
	require.Equal(t, AccountRenewalExpiredCode, infraerrors.Reason(err))
	store.err = errors.New("database unavailable")
	_, err = guard.FilterAccounts(ctx, renewalTestAccounts())
	require.Equal(t, http.StatusServiceUnavailable, infraerrors.Code(err))
	require.Error(t, guard.CheckAccount(ctx, &Account{ID: 1}))
}

func TestAccountRenewalSnapshotCacheAndDBReadsRecheckLiveDeadline(t *testing.T) {
	for _, cached := range []bool{true, false} {
		t.Run(map[bool]string{true: "cached", false: "database"}[cached], func(t *testing.T) {
			ctx := context.Background()
			accounts := renewalTestAccounts()
			repo := &renewalAccountRepositoryStub{accounts: accounts}
			store := &renewalAccessStoreStub{states: map[int64]accountbilling.AccessState{1: {Expired: true}}}
			var cache SchedulerCache
			if cached {
				cache = &snapshotHydrationCache{snapshot: []*Account{&accounts[0], &accounts[1]}, accounts: map[int64]*Account{1: &accounts[0], 2: &accounts[1]}}
			}
			snapshot := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
			snapshot.SetAccountRenewalGuard(NewAccountRenewalGuard(store))
			group := int64(7)
			selected, _, err := snapshot.ListSchedulableAccounts(ctx, &group, PlatformOpenAI, false)
			require.NoError(t, err)
			require.Len(t, selected, 1)
			require.Equal(t, int64(2), selected[0].ID)
			_, err = snapshot.GetAccount(ctx, 1)
			require.ErrorIs(t, err, ErrAccountRenewalExpired, "sticky cache/previous response reads must be checked")
			store.states[1] = accountbilling.AccessState{}
			account, err := snapshot.GetAccount(ctx, 1)
			require.NoError(t, err)
			require.Equal(t, int64(1), account.ID)
			require.True(t, accounts[0].Schedulable)
		})
	}
}

func TestAccountRenewalGatewayFallbackAndFinalForwardRejectExpiredAccounts(t *testing.T) {
	ctx := context.Background()
	accounts := renewalTestAccounts()
	repo := &renewalAccountRepositoryStub{accounts: accounts}
	store := &renewalAccessStoreStub{states: map[int64]accountbilling.AccessState{1: {Expired: true}}}
	guard := NewAccountRenewalGuard(store)
	gw := &GatewayService{accountRepo: repo}
	gw.SetAccountRenewalGuard(guard)
	openai := &OpenAIGatewayService{accountRepo: repo}
	openai.SetAccountRenewalGuard(guard)
	group := int64(7)
	selected, _, err := gw.listSchedulableAccounts(ctx, &group, PlatformOpenAI, true)
	require.NoError(t, err)
	require.Len(t, selected, 1)
	selected, err = openai.listSchedulableAccounts(ctx, &group, PlatformOpenAI)
	require.NoError(t, err)
	require.Len(t, selected, 1)
	_, err = gw.getSchedulableAccount(ctx, 1)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	_, err = openai.getSchedulableAccount(ctx, 1)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	_, _, err = gw.GetAccessToken(ctx, &accounts[0])
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	_, _, err = openai.GetRequestCredential(ctx, nil, &accounts[0])
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	_, err = gw.Forward(ctx, nil, &accounts[0], nil)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	_, err = openai.Forward(ctx, nil, &accounts[0], nil)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	_, err = gw.ForwardSystemOne(ctx, nil, &accounts[0], nil)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	store.states[1] = accountbilling.AccessState{}
	_, _, err = gw.GetAccessToken(ctx, &accounts[0])
	require.NoError(t, err)
	_, _, err = openai.GetRequestCredential(ctx, nil, &accounts[0])
	require.NoError(t, err)
	// Clearing the custom restriction never clears the upstream account expiry.
	past := time.Now().Add(-time.Hour)
	accounts[0].ExpiresAt, accounts[0].AutoPauseOnExpired = &past, true
	require.NoError(t, guard.CheckAccount(ctx, &accounts[0]))
	require.False(t, accounts[0].IsSchedulable())
}

func TestAccountRenewalDeadlineUsesBillingTimezoneAndShadowParent(t *testing.T) {
	location := time.FixedZone("billing-zone", 9*60*60)
	date := "2026-11-22"
	parentDate := "2026-11-23"
	store := &renewalAccessStoreStub{now: time.Date(2026, 11, 22, 23, 59, 0, 0, location), states: map[int64]accountbilling.AccessState{1: {ValidThrough: &date}, 2: {ValidThrough: &parentDate}}}
	guard := NewAccountRenewalGuard(store)
	parent := int64(2)
	deadline, err := guard.Deadline(context.Background(), &Account{ID: 1, ParentAccountID: &parent})
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 23, 0, 0, 0, 0, location), deadline)
	store.states[1] = accountbilling.AccessState{}
	deadline, err = guard.Deadline(context.Background(), &Account{ID: 1, ParentAccountID: &parent})
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 24, 0, 0, 0, 0, location), deadline)
	deadline, err = (*AccountRenewalGuard)(nil).Deadline(context.Background(), &Account{ID: 1})
	require.NoError(t, err)
	require.True(t, deadline.IsZero())
}

func TestAccountRenewalFinalAdmissionWritesActionable402(t *testing.T) {
	gin.SetMode(gin.TestMode)
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	store := &renewalAccessStoreStub{states: map[int64]accountbilling.AccessState{1: {Expired: true}}}
	svc := &OpenAIGatewayService{}
	svc.SetAccountRenewalGuard(NewAccountRenewalGuard(store))
	_, err := svc.Forward(context.Background(), c, &Account{ID: 1}, nil)
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
	require.Equal(t, http.StatusPaymentRequired, response.Code)
	require.Contains(t, response.Body.String(), `"code":"ACCOUNT_RENEWAL_EXPIRED"`)
	require.True(t, c.IsAborted())
	require.LessOrEqual(t, len(AccountRenewalExpiredWSReason), 123, "WebSocket close reasons must fit the protocol limit")
}

type renewalContextStoreStub struct {
	phase    atomic.Int32
	calls    atomic.Int64
	location *time.Location
}

func (s *renewalContextStoreStub) Now() time.Time {
	day := 22
	if s.phase.Load() > 0 {
		day = 23
	}
	return time.Date(2026, 11, day, 23, 59, 59, 980000000, s.location)
}
func (s *renewalContextStoreStub) AccessStates(context.Context, []int64) (map[int64]accountbilling.AccessState, error) {
	s.calls.Add(1)
	date := "2026-11-22"
	phase := s.phase.Load()
	if phase > 0 {
		date = "2026-11-23"
	}
	return map[int64]accountbilling.AccessState{1: {ValidThrough: &date, Expired: phase == 2}}, nil
}

func TestAccountRenewalContextRechecksPaymentBeforeClosingAtDeadline(t *testing.T) {
	store := &renewalContextStoreStub{location: time.FixedZone("billing-zone", 9*60*60)}
	svc := &OpenAIGatewayService{}
	svc.SetAccountRenewalGuard(NewAccountRenewalGuard(store))
	ctx, cancel, err := svc.WithAccountRenewalContext(context.Background(), &Account{ID: 1})
	require.NoError(t, err)
	defer cancel()
	store.phase.Store(1) // Payment extends the account while the relay is open.
	require.Eventually(t, func() bool { return store.calls.Load() >= 2 }, time.Second, time.Millisecond)
	require.NoError(t, ctx.Err(), "an extended deadline must not close at the old date")
	store.phase.Store(2)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("expired relay was not cancelled")
	}
	require.ErrorIs(t, context.Cause(ctx), ErrAccountRenewalExpired)

	svc.SetAccountRenewalGuard(NewAccountRenewalGuard(&renewalAccessStoreStub{}))
	parent := context.Background()
	unrestricted, stop, err := svc.WithAccountRenewalContext(parent, &Account{ID: 1})
	require.NoError(t, err)
	defer stop()
	require.Equal(t, parent, unrestricted, "unmanaged accounts need no deadline watcher")
}

func TestAccountRenewalDeadlineRejectsPastDateEvenIfStateSnapshotCrossedMidnight(t *testing.T) {
	date := "2026-11-22"
	store := &renewalAccessStoreStub{now: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC), states: map[int64]accountbilling.AccessState{1: {ValidThrough: &date}}}
	_, err := NewAccountRenewalGuard(store).Deadline(context.Background(), &Account{ID: 1})
	require.ErrorIs(t, err, ErrAccountRenewalExpired)
}
