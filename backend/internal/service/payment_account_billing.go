package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/shopspring/decimal"
)

// Account billing is an extension of the existing gateway lifecycle. Its ledger
// stays in custom tables and never uses balance redemption or API subscriptions.
type accountBillingPayments interface {
	PaymentQuote(context.Context, int64) (*accountbilling.PaymentQuote, error)
	BindPaymentOrder(context.Context, int64, int64, string, float64) error
	FulfillPaymentOrder(context.Context, int64, int64) error
}

func (s *PaymentService) GetAccountPaymentQuote(ctx context.Context, userID int64) (*accountbilling.PaymentQuote, error) {
	cfg, err := s.configService.GetPaymentConfig(ctx)
	if err != nil {
		return nil, err
	}
	if !cfg.Enabled || !cfg.IsOrderTypeVisible(accountPaymentOrderType) {
		return nil, infraerrors.Forbidden("PAYMENT_ENTRY_DISABLED", "按账号支付暂未开放")
	}
	if s.accountBilling == nil {
		return nil, infraerrors.ServiceUnavailable("ACCOUNT_PAYMENT_UNAVAILABLE", "账号账单暂不可用")
	}
	quote, err := s.accountBilling.PaymentQuote(ctx, userID)
	if err != nil {
		return nil, accountPaymentError(err)
	}
	return quote, nil
}

func (s *PaymentService) validateAccountPaymentQuote(ctx context.Context, req CreateOrderRequest) error {
	if s.accountBilling == nil {
		return infraerrors.ServiceUnavailable("ACCOUNT_PAYMENT_UNAVAILABLE", "账号账单暂不可用")
	}
	quote, err := s.accountBilling.PaymentQuote(ctx, req.UserID)
	if err != nil {
		return accountPaymentError(err)
	}
	if quote.AmountDue <= 0 {
		return accountPaymentError(accountbilling.ErrNoAmountDue)
	}
	if strings.TrimSpace(req.AccountQuoteToken) != quote.QuoteToken || !accountPaymentAmountMatches(req.Amount, quote.AmountDue) {
		return accountPaymentError(accountbilling.ErrQuoteChanged)
	}
	for _, account := range quote.Accounts {
		if account.RenewalDate == nil || *account.RenewalDate == "" {
			return accountPaymentError(accountbilling.ErrRenewalNotConfigured)
		}
	}
	return nil
}

func accountPaymentAmountMatches(amount float64, cents accountbilling.Money) bool {
	return isValidProviderAmount(amount) && decimal.NewFromFloat(amount).Shift(2).Equal(decimal.NewFromInt(int64(cents)))
}

func validateAccountPaymentCurrency(orderType, currency string) error {
	if orderType == accountPaymentOrderType && strings.ToUpper(strings.TrimSpace(currency)) != payment.DefaultPaymentCurrency {
		return infraerrors.BadRequest("ACCOUNT_PAYMENT_CNY_REQUIRED", "账号售价以人民币统计，请选择人民币支付渠道")
	}
	return nil
}

func accountPaymentError(err error) error {
	switch {
	case errors.Is(err, accountbilling.ErrQuoteChanged):
		return infraerrors.Conflict("ACCOUNT_QUOTE_CHANGED", "账号归属、售价或支付状态已变化，请刷新账单后重试")
	case errors.Is(err, accountbilling.ErrPaymentPending):
		return infraerrors.Conflict("ACCOUNT_PAYMENT_PENDING", "这些账号已有待处理的支付订单，请先完成或取消原订单")
	case errors.Is(err, accountbilling.ErrNoAmountDue):
		return infraerrors.BadRequest("ACCOUNT_NOTHING_DUE", "本月没有待支付的账号费用")
	case errors.Is(err, accountbilling.ErrRenewalNotConfigured):
		return infraerrors.BadRequest("ACCOUNT_RENEWAL_NOT_CONFIGURED", "部分账号未设置续费时间，请联系管理员设置后再支付")
	case errors.Is(err, accountbilling.ErrForbidden):
		return infraerrors.Forbidden("ACCOUNT_PAYMENT_FORBIDDEN", "只能支付自己所属账号的费用")
	case errors.Is(err, accountbilling.ErrInvalid):
		return infraerrors.BadRequest("ACCOUNT_PAYMENT_INVALID", "账号支付金额或账单无效，请刷新后重试")
	case errors.Is(err, accountbilling.ErrConflict):
		return infraerrors.Conflict("ACCOUNT_PAYMENT_CONFLICT", "账号账单已发生变化，请刷新后重试")
	default:
		return fmt.Errorf("account payment: %w", err)
	}
}

func (s *PaymentService) ExecuteAccountPaymentFulfillment(ctx context.Context, orderID int64) error {
	o, err := s.entClient.PaymentOrder.Get(ctx, orderID)
	if err != nil {
		return fmt.Errorf("get account payment order: %w", err)
	}
	if o.OrderType != accountPaymentOrderType {
		return infraerrors.BadRequest("INVALID_ORDER_TYPE", "order is not an account payment")
	}
	if o.Status == OrderStatusCompleted {
		return nil
	}
	if psIsRefundStatus(o.Status) || (o.Status != OrderStatusPaid && o.Status != OrderStatusFailed && o.Status != OrderStatusRecharging) || o.PaidAt == nil {
		return infraerrors.BadRequest("INVALID_STATUS", "account payment order has not been paid")
	}
	lease, err := s.acquirePaymentFulfillmentLease(ctx, o)
	if err != nil || lease == nil {
		return err
	}
	if s.accountBilling == nil {
		err = errors.New("account billing store is unavailable")
	} else {
		err = s.accountBilling.FulfillPaymentOrder(ctx, o.ID, o.UserID)
	}
	if err != nil {
		s.markFailed(ctx, o.ID, lease, err)
		return accountPaymentError(err)
	}
	return s.markCompleted(ctx, o, lease, "ACCOUNT_PAYMENT_SUCCESS")
}
