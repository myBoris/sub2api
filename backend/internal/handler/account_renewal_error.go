package handler

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// Preserve each gateway's protocol while giving clients a stable billing code.
func writeAccountRenewalError(c *gin.Context, err error) bool {
	if !service.IsAccountRenewalExpired(err) {
		return false
	}
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalPolicyDenied)
	service.SetOpsLocalErrorType(c, "billing_error", service.AccountRenewalExpiredCode)
	if strings.Contains(c.Request.URL.Path, "/v1beta/") || strings.Contains(c.Request.URL.Path, "/v1alpha/") {
		c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"error": gin.H{
			"code":    http.StatusPaymentRequired,
			"status":  "FAILED_PRECONDITION",
			"message": service.AccountRenewalExpiredMessage,
			"details": []gin.H{{"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": service.AccountRenewalExpiredCode}},
		}})
	} else {
		c.AbortWithStatusJSON(http.StatusPaymentRequired, gin.H{"type": "error", "error": gin.H{
			"type": "billing_error", "code": service.AccountRenewalExpiredCode, "message": service.AccountRenewalExpiredMessage,
		}})
	}
	return true
}
