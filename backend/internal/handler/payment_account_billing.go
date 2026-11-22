package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// GetAccountPaymentQuote returns only the authenticated user's payable accounts.
func (h *PaymentHandler) GetAccountPaymentQuote(c *gin.Context) {
	subject, ok := requireAuth(c)
	if !ok {
		return
	}
	quote, err := h.paymentService.GetAccountPaymentQuote(c.Request.Context(), subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, quote)
}
