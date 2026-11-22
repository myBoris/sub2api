//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestApplyWeChatPaymentResumeClaimsRestoresAccountQuote(t *testing.T) {
	req := CreateOrderRequest{PaymentType: "wxpay", AccountQuoteToken: "client-token", Amount: 1}
	err := applyWeChatPaymentResumeClaims(&req, &service.WeChatPaymentResumeClaims{
		OpenID: "openid", OrderType: "account", Amount: "123.45", AccountQuoteToken: "signed-token",
	})
	require.NoError(t, err)
	require.Equal(t, "signed-token", req.AccountQuoteToken)
	require.Equal(t, "account", req.OrderType)
	require.Equal(t, 123.45, req.Amount)
}
