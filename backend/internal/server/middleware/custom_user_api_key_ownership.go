package middleware

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func setAPIKeyGroupOwnershipContext(c *gin.Context, key *service.APIKey) {
	if key.UserGroupOwnershipVerified() {
		c.Request = c.Request.WithContext(ownership.WithGroupUser(c.Request.Context(), key.UserID))
	}
}
