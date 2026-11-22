//go:build unit

package handler

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteAccountRenewalErrorPreservesGatewayProtocols(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, path := range []string{"/v1/messages", "/v1/chat/completions", "/v1beta/cachedContents/demo", "/antigravity/v1beta/models/demo:generateContent"} {
		t.Run(path, func(t *testing.T) {
			writer := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(writer)
			c.Request = httptest.NewRequest(http.MethodPost, path, nil)
			require.True(t, writeAccountRenewalError(c, fmt.Errorf("bound account: %w", service.ErrAccountRenewalExpired)))
			require.Equal(t, http.StatusPaymentRequired, writer.Code)
			require.Contains(t, writer.Body.String(), service.AccountRenewalExpiredCode)
			require.Contains(t, writer.Body.String(), service.AccountRenewalExpiredMessage)
			require.True(t, c.IsAborted())
		})
	}
}

func TestWriteAccountRenewalErrorLeavesOtherFailuresToOriginalHandler(t *testing.T) {
	writer := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(writer)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	require.False(t, writeAccountRenewalError(c, errors.New("upstream failure")))
	require.False(t, writer.Flushed)
	require.Zero(t, writer.Body.Len())
	require.False(t, c.IsAborted())
}

func TestAccountRenewalClassificationWritesStableCode(t *testing.T) {
	for _, render := range []func(*gin.Context){
		func(c *gin.Context) {
			(&GatewayHandler{}).handleStreamingAwareError(c, 402, service.AccountRenewalExpiredCode, service.AccountRenewalExpiredMessage, false)
		},
		func(c *gin.Context) {
			(&OpenAIGatewayHandler{}).handleStreamingAwareError(c, 402, service.AccountRenewalExpiredCode, service.AccountRenewalExpiredMessage, false)
		},
		func(c *gin.Context) {
			(&OpenAIGatewayHandler{}).anthropicStreamingAwareError(c, 402, service.AccountRenewalExpiredCode, service.AccountRenewalExpiredMessage, false)
		},
		func(c *gin.Context) {
			googleErrorWithType(c, 402, service.AccountRenewalExpiredCode, "", service.AccountRenewalExpiredMessage)
		},
	} {
		writer := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(writer)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		render(c)
		require.Equal(t, http.StatusPaymentRequired, writer.Code)
		require.Contains(t, writer.Body.String(), `"code":"ACCOUNT_RENEWAL_EXPIRED"`)
	}
}
