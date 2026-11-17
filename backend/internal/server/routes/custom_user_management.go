package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// Keep the user extension separate from the upstream administrator routes.
func registerUserManagedRoutes(authenticated *gin.RouterGroup, h *handler.Handlers) {
	if h.Admin == nil || h.Admin.Group == nil || h.Admin.Account == nil {
		return
	}
	managed := authenticated.Group("/user/managed")
	managed.Use(func(c *gin.Context) {
		subject, ok := middleware.GetAuthSubjectFromContext(c)
		role, hasRole := middleware.GetUserRoleFromContext(c)
		if !ok || !hasRole || subject.UserID <= 0 {
			response.Unauthorized(c, "Authentication required")
			c.Abort()
			return
		}
		if role != "admin" {
			c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), subject.UserID))
		}
		c.Next()
	})
	managed.Use(h.Admin.Group.UserOwnedResources)
	managed.GET("/resource-ownership/:kind", h.Admin.Group.ResourceOwners)

	groups := managed.Group("/groups")
	groups.GET("", h.Admin.Group.List)
	groups.GET("/all", h.Admin.Group.GetAll)
	groups.GET("/usage-summary", h.Admin.Group.GetOwnedUsageSummary)
	groups.GET("/capacity-summary", h.Admin.Group.GetCapacitySummary)
	groups.GET("/live-capability", h.Admin.Group.GetLiveCapability)
	groups.PUT("/sort-order", h.Admin.Group.UpdateSortOrder)
	groups.GET("/:id", h.Admin.Group.GetByID)
	groups.GET("/:id/model-allowlist-candidates", h.Admin.Group.GetGroupModelAllowlistCandidates)
	groups.GET("/:id/stats", h.Admin.Group.GetStats)
	groups.POST("", h.Admin.Group.Create)
	groups.POST("/:id/duplicate", h.Admin.Group.Duplicate)
	groups.PUT("/:id", h.Admin.Group.Update)
	groups.DELETE("/:id", h.Admin.Group.Delete)

	accounts := managed.Group("/accounts")
	accounts.GET("", h.Admin.Account.List)
	accounts.GET("/upstream-billing-rates", h.Admin.Account.GetUpstreamBillingRates)
	accounts.GET("/antigravity/default-model-mapping", h.Admin.Account.GetAntigravityDefaultModelMapping)
	accounts.GET("/:id", h.Admin.Account.GetByID)
	accounts.POST("", h.Admin.Account.Create)
	accounts.PUT("/:id", h.Admin.Account.Update)
	accounts.DELETE("/:id", h.Admin.Account.Delete)
	accounts.POST("/:id/duplicate", h.Admin.Account.Duplicate)
	accounts.GET("/:id/claude/reset-credits", h.Admin.Account.ClaudeResetCredits)
	accounts.POST("/:id/claude/reset-credits/redeem", h.Admin.Account.RedeemClaudeResetCredit)
	accounts.GET("/:id/grok-media-eligibility", h.Admin.Account.GetGrokMediaEligibility)
	accounts.PUT("/:id/grok-media-eligibility", h.Admin.Account.UpdateGrokMediaEligibility)
	accounts.PUT("/:id/upstream-billing-probe", h.Admin.Account.SetUpstreamBillingProbeEnabled)
	accounts.POST("/:id/upstream-billing-probe", h.Admin.Account.ProbeUpstreamBilling)
	accounts.POST("/upstream-billing-probe/batch", h.Admin.Account.ProbeUpstreamBillingBatch)
	accounts.GET("/:id/ollama-cloud-usage", h.Admin.Account.GetOllamaCloudUsage)
	accounts.PUT("/:id/ollama-cloud-usage/session", h.Admin.Account.SaveOllamaCloudUsageSession)
	accounts.DELETE("/:id/ollama-cloud-usage/session", h.Admin.Account.DeleteOllamaCloudUsageSession)
	accounts.PUT("/:id/ollama-cloud-usage/auto-refresh", h.Admin.Account.SetOllamaCloudUsageAutoRefresh)
	accounts.POST("/:id/ollama-cloud-usage/refresh", h.Admin.Account.RefreshOllamaCloudUsage)
	accounts.GET("/:id/opencode-go-usage", h.Admin.Account.GetOpenCodeGoUsage)
	accounts.PUT("/:id/opencode-go-usage/auto-refresh", h.Admin.Account.SetOpenCodeGoUsageAutoRefresh)
	accounts.POST("/:id/opencode-go-usage/refresh", h.Admin.Account.RefreshOpenCodeGoUsage)
	accounts.POST("/check-mixed-channel", h.Admin.Account.CheckMixedChannel)
	accounts.POST("/import/codex-session", h.Admin.Account.ImportCodexSession)
	accounts.POST("/batch", h.Admin.Account.BatchCreate)
	accounts.POST("/bulk-update", h.Admin.Account.BulkUpdate)
	accounts.POST("/batch-delete", h.Admin.Account.BatchDelete)
	accounts.POST("/batch-clear-error", h.Admin.Account.BatchClearError)
	accounts.POST("/batch-refresh", h.Admin.Account.BatchRefresh)
	accounts.POST("/batch-refresh-tier", h.Admin.Account.BatchRefreshTier)
	accounts.POST("/batch-update-credentials", h.Admin.Account.BatchUpdateCredentials)
	accounts.POST("/usage/batch", h.Admin.Account.GetBatchUsage)
	accounts.POST("/today-stats/batch", h.Admin.Account.GetBatchTodayStats)
	accounts.POST("/:id/test", h.Admin.Account.Test)
	accounts.POST("/:id/refresh", h.Admin.Account.Refresh)
	accounts.POST("/:id/apply-oauth-credentials", h.Admin.Account.ApplyOAuthCredentials)
	accounts.POST("/:id/set-privacy", h.Admin.Account.SetPrivacy)
	accounts.POST("/:id/refresh-tier", h.Admin.Account.RefreshTier)
	accounts.POST("/:id/clear-error", h.Admin.Account.ClearError)
	accounts.POST("/:id/recover-state", h.Admin.Account.RecoverState)
	accounts.GET("/:id/stats", h.Admin.Account.GetStats)
	accounts.GET("/:id/usage", h.Admin.Account.GetUsage)
	accounts.GET("/:id/today-stats", h.Admin.Account.GetTodayStats)
	accounts.POST("/:id/clear-rate-limit", h.Admin.Account.ClearRateLimit)
	accounts.POST("/:id/reset-quota", h.Admin.Account.ResetQuota)
	accounts.GET("/:id/temp-unschedulable", h.Admin.Account.GetTempUnschedulable)
	accounts.DELETE("/:id/temp-unschedulable", h.Admin.Account.ClearTempUnschedulable)
	accounts.POST("/:id/schedulable", h.Admin.Account.SetSchedulable)
	accounts.GET("/:id/models", h.Admin.Account.GetAvailableModels)
	accounts.POST("/:id/models/sync-upstream", h.Admin.Account.SyncUpstreamModels)
	accounts.POST("/models/sync-upstream-preview", h.Admin.Account.SyncUpstreamModelsPreview)
	accounts.POST("/generate-auth-url", h.Admin.OAuth.GenerateAuthURL)
	accounts.POST("/generate-setup-token-url", h.Admin.OAuth.GenerateSetupTokenURL)
	accounts.POST("/exchange-code", h.Admin.OAuth.ExchangeCode)
	accounts.POST("/exchange-setup-token-code", h.Admin.OAuth.ExchangeSetupTokenCode)
	accounts.POST("/cookie-auth", h.Admin.OAuth.CookieAuth)
	accounts.POST("/setup-token-cookie-auth", h.Admin.OAuth.SetupTokenCookieAuth)
	registerOpenAIOAuthRoutes(managed, h)
	registerGeminiOAuthRoutes(managed, h)
	registerAntigravityOAuthRoutes(managed, h)
	registerGrokOAuthRoutes(managed, h)
	registerCNProviderRoutes(managed, h)
}
