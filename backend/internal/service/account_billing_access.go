package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
)

const AccountRenewalExpiredCode = "ACCOUNT_RENEWAL_EXPIRED"
const AccountRenewalExpiredMessage = "账号续费已到期，请在充值/订阅页面按账号支付续费后重试"
const AccountRenewalExpiredWSReason = "ACCOUNT_RENEWAL_EXPIRED: 账号续费已到期，请支付后重连"

// Keep this business restriction separate from upstream token/account expiry.
var ErrAccountRenewalExpired = infraerrors.New(http.StatusPaymentRequired, AccountRenewalExpiredCode, AccountRenewalExpiredMessage).WithCause(ErrNoAvailableAccounts)

type AccountRenewalAccessStore interface {
	AccessStates(context.Context, []int64) (map[int64]accountbilling.AccessState, error)
}

// AccountRenewalGuard reads custom billing deadlines live. Never put these
// restrictions into shared scheduler/auth caches: paying must restore access
// on the next request, without an outbox event or a cache TTL.
type AccountRenewalGuard struct {
	store AccountRenewalAccessStore
	now   func() time.Time
}

func NewAccountRenewalGuard(store AccountRenewalAccessStore) *AccountRenewalGuard {
	guard := &AccountRenewalGuard{store: store, now: time.Now}
	if clock, ok := store.(interface{ Now() time.Time }); ok {
		guard.now = clock.Now
	}
	return guard
}

func IsAccountRenewalExpired(err error) bool { return errors.Is(err, ErrAccountRenewalExpired) }

// WriteAccountRenewalError preserves a final admission failure when older
// protocol handlers otherwise classify all forwarding errors as upstream 502s.
func WriteAccountRenewalError(c *gin.Context, err error) bool {
	if c == nil || !IsAccountRenewalExpired(err) {
		return false
	}
	SetOpsLocalErrorType(c, "permission_error", AccountRenewalExpiredCode)
	body := gin.H{"type": "error", "error": gin.H{
		"type": "permission_error", "code": AccountRenewalExpiredCode, "message": AccountRenewalExpiredMessage,
	}}
	if c.Writer.Written() {
		c.SSEvent("error", body)
		c.Writer.Flush()
		c.Abort()
		return true
	}
	c.AbortWithStatusJSON(http.StatusPaymentRequired, body)
	return true
}

func renewalAccountIDs(accounts []Account) []int64 {
	ids := make([]int64, 0, len(accounts)*2)
	seen := make(map[int64]bool, len(accounts)*2)
	for _, account := range accounts {
		parentID := int64(0)
		if account.ParentAccountID != nil {
			parentID = *account.ParentAccountID
		}
		for _, id := range []int64{account.ID, parentID} {
			if id > 0 && !seen[id] {
				ids = append(ids, id)
				seen[id] = true
			}
		}
	}
	return ids
}

func renewalAccountExpired(account Account, states map[int64]accountbilling.AccessState) bool {
	return states[account.ID].Expired || (account.ParentAccountID != nil && states[*account.ParentAccountID].Expired)
}

func (g *AccountRenewalGuard) states(ctx context.Context, accounts []Account) (map[int64]accountbilling.AccessState, error) {
	if g == nil || g.store == nil {
		return nil, nil
	}
	ids := renewalAccountIDs(accounts)
	if len(ids) == 0 {
		return nil, nil
	}
	states, err := g.store.AccessStates(ctx, ids)
	if err != nil {
		return nil, infraerrors.ServiceUnavailable("ACCOUNT_RENEWAL_UNAVAILABLE", "账号续费状态暂不可用，请稍后重试").WithCause(err)
	}
	return states, nil
}

func (g *AccountRenewalGuard) CheckAccount(ctx context.Context, account *Account) error {
	if g == nil || account == nil {
		return nil
	}
	states, err := g.states(ctx, []Account{*account})
	if err != nil {
		return err
	}
	if renewalAccountExpired(*account, states) {
		return ErrAccountRenewalExpired
	}
	return nil
}

func (g *AccountRenewalGuard) FilterAccounts(ctx context.Context, accounts []Account) ([]Account, error) {
	if g == nil || g.store == nil || len(accounts) == 0 {
		return accounts, nil
	}
	states, err := g.states(ctx, accounts)
	if err != nil {
		return nil, err
	}
	// Account maps/pointers may be shared by Redis and coalesced DB fallbacks.
	// Only construct a request-local slice; never mark shared accounts disabled.
	filtered := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if !renewalAccountExpired(account, states) {
			filtered = append(filtered, account)
		}
	}
	if len(filtered) == 0 {
		return nil, ErrAccountRenewalExpired
	}
	return filtered, nil
}

// Deadline is exclusive midnight after the displayed date in the billing
// timezone. A shadow also inherits its credential parent's earliest deadline.
func (g *AccountRenewalGuard) Deadline(ctx context.Context, account *Account) (time.Time, error) {
	if g == nil || account == nil {
		return time.Time{}, nil
	}
	states, err := g.states(ctx, []Account{*account})
	if err != nil {
		return time.Time{}, err
	}
	if renewalAccountExpired(*account, states) {
		return time.Time{}, ErrAccountRenewalExpired
	}
	location := g.now().Location()
	var earliest time.Time
	for _, id := range renewalAccountIDs([]Account{*account}) {
		validThrough := states[id].ValidThrough
		if validThrough == nil {
			continue
		}
		date, parseErr := time.ParseInLocation("2006-01-02", *validThrough, location)
		if parseErr != nil {
			return time.Time{}, infraerrors.ServiceUnavailable("ACCOUNT_RENEWAL_UNAVAILABLE", "账号续费日期无效，请联系管理员")
		}
		deadline := date.AddDate(0, 0, 1)
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	if !earliest.IsZero() && !earliest.After(g.now()) {
		return time.Time{}, ErrAccountRenewalExpired
	}
	return earliest, nil
}

func (s *SchedulerSnapshotService) SetAccountRenewalGuard(guard *AccountRenewalGuard) {
	s.accountRenewalGuard = guard
}
func (s *GatewayService) SetAccountRenewalGuard(guard *AccountRenewalGuard) {
	s.accountRenewalGuard = guard
}
func (s *OpenAIGatewayService) SetAccountRenewalGuard(guard *AccountRenewalGuard) {
	s.accountRenewalGuard = guard
}

func (s *GatewayService) CheckAccountRenewal(ctx context.Context, account *Account) error {
	return s.accountRenewalGuard.CheckAccount(ctx, account)
}
func (s *OpenAIGatewayService) CheckAccountRenewal(ctx context.Context, account *Account) error {
	return s.accountRenewalGuard.CheckAccount(ctx, account)
}
func (s *OpenAIGatewayService) AccountRenewalDeadline(ctx context.Context, account *Account) (time.Time, error) {
	return s.accountRenewalGuard.Deadline(ctx, account)
}

// WithAccountRenewalContext closes relays without turn boundaries at the real
// deadline. Re-read before cancelling so a payment made during the connection
// extends it immediately rather than closing at an obsolete deadline.
func (s *OpenAIGatewayService) WithAccountRenewalContext(ctx context.Context, account *Account) (context.Context, context.CancelFunc, error) {
	deadline, err := s.AccountRenewalDeadline(ctx, account)
	if err != nil {
		return ctx, func() {}, err
	}
	if deadline.IsZero() {
		return ctx, func() {}, nil
	}
	guarded, cancel := context.WithCancelCause(ctx)
	go func() {
		for {
			delay := time.Until(deadline)
			if s.accountRenewalGuard != nil && s.accountRenewalGuard.now != nil {
				delay = deadline.Sub(s.accountRenewalGuard.now())
			}
			if delay <= 0 {
				// The clock may cross midnight between the state lookup and
				// timer setup. Recheck once; Deadline rejects stale past dates.
				deadline, err = s.AccountRenewalDeadline(guarded, account)
				if err != nil {
					cancel(err)
					return
				}
				if deadline.IsZero() {
					return
				}
				continue
			}
			timer := time.NewTimer(delay)
			select {
			case <-guarded.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			deadline, err = s.AccountRenewalDeadline(guarded, account)
			if err != nil {
				cancel(err)
				return
			}
			if deadline.IsZero() {
				return
			}
		}
	}()
	return guarded, func() { cancel(context.Canceled) }, nil
}

func (s *OpenAIGatewayService) LiveCallRenewalAccount(ctx context.Context, record *LiveCallRecord) (*Account, error) {
	if record == nil || s.accountRepo == nil {
		return nil, ErrLiveCallNotFound
	}
	account, err := s.accountRepo.GetByID(ctx, record.AccountID)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrLiveCallNotFound
	}
	if err = s.CheckAccountRenewal(ctx, account); err != nil {
		return nil, err
	}
	return account, nil
}

// Optional diagnostic hooks preserve the existing ModelAvailabilityDiagnoser
// interface while keeping a fully expired pool's response actionable.
func (s *GatewayService) CheckAccountRenewalAvailability(ctx context.Context, groupID *int64, platform string) error {
	if s.accountRenewalGuard == nil {
		return nil
	}
	_, _, err := s.listSchedulableAccounts(ctx, groupID, platform, false)
	return err
}
func (s *OpenAIGatewayService) CheckAccountRenewalAvailability(ctx context.Context, groupID *int64, platform string) error {
	if s.accountRenewalGuard == nil {
		return nil
	}
	_, err := s.listSchedulableAccounts(ctx, groupID, platform)
	return err
}
