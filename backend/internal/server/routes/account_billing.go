package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerAccountBillingAdminRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h.AccountBilling == nil {
		return
	}
	billing := admin.Group("/account-billing")
	billing.Use(h.AccountBilling.RequireAdmin)
	billing.GET("/catalog", h.AccountBilling.Catalog)
	billing.POST("/plans", h.AccountBilling.SavePlan)
	billing.PUT("/plans/:id", h.AccountBilling.SavePlan)
	billing.DELETE("/plans/:id", h.AccountBilling.DeletePlan)
	billing.POST("/payers", h.AccountBilling.SavePayer)
	billing.PUT("/payers/:id", h.AccountBilling.SavePayer)
	billing.DELETE("/payers/:id", h.AccountBilling.DeletePayer)
	billing.GET("/accounts", h.AccountBilling.List)
	billing.POST("/accounts/sync", h.AccountBilling.Sync)
	billing.PUT("/accounts/:id", h.AccountBilling.Update)
	billing.PUT("/accounts/:id/history/:month", h.AccountBilling.UpdateHistory)
}

func registerAccountBillingUserRoutes(authenticated *gin.RouterGroup, h *handler.Handlers) {
	if h.AccountBilling == nil {
		return
	}
	authenticated.GET("/account-billing/summary", h.AccountBilling.UserSummary)
}
