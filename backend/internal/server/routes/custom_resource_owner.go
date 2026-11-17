package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerResourceOwnerRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	owners := admin.Group("/resource-ownership")
	owners.GET("/:kind", h.Admin.Group.ResourceOwners)
	owners.PUT("/:kind/:id", h.Admin.Group.SetResourceOwner)
}
