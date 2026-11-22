//go:build unit

package service

import (
	"context"
	"math"
	"net/url"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
)

type accountBillingPaymentsStub struct {
	quote        *accountbilling.PaymentQuote
	err          error
	fulfillCalls int
}

func (s *accountBillingPaymentsStub) PaymentQuote(context.Context, int64) (*accountbilling.PaymentQuote, error) {
	return s.quote, s.err
}
func (s *accountBillingPaymentsStub) BindPaymentOrder(context.Context, int64, int64, string, float64) error {
	return s.err
}
func (s *accountBillingPaymentsStub) FulfillPaymentOrder(context.Context, int64, int64) error {
	s.fulfillCalls++
	return s.err
}

func TestAccountPaymentQuoteValidation(t *testing.T) {
	date := "2026-11-30"
	store := &accountBillingPaymentsStub{quote: &accountbilling.PaymentQuote{
		AmountDue: 12345, QuoteToken: "current-quote",
		Accounts: []accountbilling.PaymentQuoteAccount{{AccountID: 9, Price: 12345, RenewalDate: &date}},
	}}
	svc := &PaymentService{accountBilling: store}
	req := CreateOrderRequest{UserID: 7, OrderType: accountPaymentOrderType, Amount: 123.45, AccountQuoteToken: "current-quote"}
	require.NoError(t, svc.validateAccountPaymentQuote(context.Background(), req))
	for _, amount := range []float64{123.44, 123.451, 0, math.NaN(), math.Inf(1)} {
		req.Amount = amount
		require.Equal(t, "ACCOUNT_QUOTE_CHANGED", infraerrors.Reason(svc.validateAccountPaymentQuote(context.Background(), req)))
	}
	req.Amount = 123.45
	req.AccountQuoteToken = "old-quote"
	require.Equal(t, "ACCOUNT_QUOTE_CHANGED", infraerrors.Reason(svc.validateAccountPaymentQuote(context.Background(), req)))
	req.AccountQuoteToken = "current-quote"
	store.quote.Accounts[0].RenewalDate = nil
	require.Equal(t, "ACCOUNT_RENEWAL_NOT_CONFIGURED", infraerrors.Reason(svc.validateAccountPaymentQuote(context.Background(), req)))
	store.quote.AmountDue = 0
	require.Equal(t, "ACCOUNT_NOTHING_DUE", infraerrors.Reason(svc.validateAccountPaymentQuote(context.Background(), req)))
}

func TestAccountPaymentEntryVisibilityAndCurrency(t *testing.T) {
	svc := &PaymentService{}
	req := CreateOrderRequest{OrderType: accountPaymentOrderType, Amount: 100, AccountQuoteToken: "quote"}
	_, err := svc.validateOrderInput(context.Background(), req, &PaymentConfig{})
	require.Equal(t, "PAYMENT_ENTRY_DISABLED", infraerrors.Reason(err))
	_, err = svc.validateOrderInput(context.Background(), req, &PaymentConfig{BalanceDisabled: true, VisibleOrderTypes: []string{accountPaymentOrderType}})
	require.NoError(t, err, "account payment must work independently of the balance switch")
	req.AccountQuoteToken = ""
	_, err = svc.validateOrderInput(context.Background(), req, &PaymentConfig{VisibleOrderTypes: []string{accountPaymentOrderType}})
	require.Equal(t, "ACCOUNT_QUOTE_REQUIRED", infraerrors.Reason(err))
	require.NoError(t, validateAccountPaymentCurrency(accountPaymentOrderType, "CNY"))
	require.Equal(t, "ACCOUNT_PAYMENT_CNY_REQUIRED", infraerrors.Reason(validateAccountPaymentCurrency(accountPaymentOrderType, "USD")))
	require.NoError(t, validateAccountPaymentCurrency(payment.OrderTypeBalance, "USD"))
	_, amount, err := calculateCreateOrderPayAmountForOrderType(123.45, 3, "CNY", accountPaymentOrderType, 7.2)
	require.NoError(t, err)
	require.Equal(t, 127.16, amount, "only the existing 3%% gateway fee applies")
}

func TestAccountPaymentFulfillmentUsesLedgerAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(accountPaymentOrderType).SetPaidAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	store := &accountBillingPaymentsStub{}
	// Nil balance and subscription services ensure this path cannot credit either.
	svc := &PaymentService{entClient: client, accountBilling: store}
	require.NoError(t, svc.executeFulfillment(ctx, order.ID))
	require.NoError(t, svc.executeFulfillment(ctx, order.ID))
	require.Equal(t, 1, store.fulfillCalls)
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusCompleted, reloaded.Status)
	_, _, err = svc.PrepareRefund(ctx, order.ID, 0, "refund", false, true)
	require.Equal(t, "ACCOUNT_REFUND_MANUAL_REQUIRED", infraerrors.Reason(err))
}

func TestAccountPaymentFulfillmentFailureCanRetry(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	order := createPaymentFulfillmentSubscriptionOrder(t, ctx, client, OrderStatusPaid, time.Now())
	order, err := client.PaymentOrder.UpdateOneID(order.ID).SetOrderType(accountPaymentOrderType).SetPaidAt(time.Now()).Save(ctx)
	require.NoError(t, err)
	store := &accountBillingPaymentsStub{err: accountbilling.ErrQuoteChanged}
	svc := &PaymentService{entClient: client, accountBilling: store}
	require.Equal(t, "ACCOUNT_QUOTE_CHANGED", infraerrors.Reason(svc.executeFulfillment(ctx, order.ID)))
	reloaded, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, OrderStatusFailed, reloaded.Status)
	store.err = nil
	require.NoError(t, svc.RetryFulfillment(ctx, order.ID))
	require.Equal(t, 2, store.fulfillCalls)
}

func TestAccountPaymentOAuthCarriesQuote(t *testing.T) {
	response := buildCreateOrderResponse(&dbent.PaymentOrder{OrderType: accountPaymentOrderType}, CreateOrderRequest{OrderType: payment.OrderTypeBalance}, 123.45, &payment.InstanceSelection{}, &payment.CreatePaymentResponse{}, payment.CreatePaymentResultOrderCreated)
	require.Equal(t, accountPaymentOrderType, response.OrderType, "response must identify the authoritative order type after signed OAuth resume")
	start, err := buildWeChatPaymentOAuthStartURL(CreateOrderRequest{
		PaymentType: payment.TypeWxpay, Amount: 123.45, OrderType: accountPaymentOrderType, AccountQuoteToken: "quoted-snapshot",
	}, "snsapi_base")
	require.NoError(t, err)
	parsed, err := url.Parse(start)
	require.NoError(t, err)
	require.Equal(t, "quoted-snapshot", parsed.Query().Get("account_quote_token"))
	resume := NewPaymentResumeService([]byte("account-payment-test-signing-key"))
	token, err := resume.CreateWeChatPaymentResumeToken(WeChatPaymentResumeClaims{
		OpenID: "openid", OrderType: accountPaymentOrderType, Amount: "123.45", AccountQuoteToken: "quoted-snapshot",
	})
	require.NoError(t, err)
	claims, err := resume.ParseWeChatPaymentResumeToken(token)
	require.NoError(t, err)
	require.Equal(t, "quoted-snapshot", claims.AccountQuoteToken)
}
