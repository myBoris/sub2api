package admin

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// The extension stays optional on AdminService to avoid changing the upstream
// service interface and its many adapters.
type resourceOwnerService interface {
	ResourceOwners(context.Context, string, []int64) ([]service.ResourceOwner, error)
	SetResourceOwner(context.Context, string, int64, *int64) (*service.ResourceOwner, error)
}

func resourceOwnerKind(c *gin.Context) (string, bool) {
	kind := c.Param("kind")
	if kind != "groups" && kind != "accounts" {
		response.BadRequest(c, "Resource kind must be groups or accounts")
		return "", false
	}
	return kind, true
}

func (h *GroupHandler) ResourceOwners(c *gin.Context) {
	subject, authenticated := middleware.GetAuthSubjectFromContext(c)
	role, hasRole := middleware.GetUserRoleFromContext(c)
	if !authenticated || !hasRole || subject.UserID <= 0 {
		response.Unauthorized(c, "Authentication required")
		return
	}
	if adminaccess.RestrictedFromContext(c.Request.Context()) != nil {
		response.Forbidden(c, "Resource ownership access is not authorized")
		return
	}
	kind, ok := resourceOwnerKind(c)
	if !ok {
		return
	}
	raw := c.Query("ids")
	parts := strings.Split(raw, ",")
	if raw == "" || len(parts) > 1000 {
		response.BadRequest(c, "Between 1 and 1000 resource IDs are required")
		return
	}
	ids := make([]int64, 0, len(parts))
	seen := make(map[int64]bool, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			response.BadRequest(c, "Invalid resource ID")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	api, available := h.adminService.(resourceOwnerService)
	if !available {
		response.Error(c, 503, "Resource ownership service is unavailable")
		return
	}
	ctx := c.Request.Context()
	if role != service.RoleAdmin {
		ctx = ownership.WithUser(ctx, subject.UserID)
	}
	owners, err := api.ResourceOwners(ctx, kind, ids)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, owners)
}

func (h *GroupHandler) SetResourceOwner(c *gin.Context) {
	subject, authenticated := middleware.GetAuthSubjectFromContext(c)
	role, hasRole := middleware.GetUserRoleFromContext(c)
	_, scoped := ownership.UserID(c.Request.Context())
	if !authenticated || !hasRole || subject.UserID <= 0 || role != service.RoleAdmin || scoped || adminaccess.RestrictedFromContext(c.Request.Context()) != nil {
		response.Forbidden(c, "Only administrators can change resource ownership")
		return
	}
	kind, ok := resourceOwnerKind(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid resource ID")
		return
	}
	var request struct {
		UserID json.RawMessage `json:"user_id"`
	}
	if c.ShouldBindJSON(&request) != nil || len(request.UserID) == 0 {
		response.BadRequest(c, "user_id is required; use null to clear ownership")
		return
	}
	var userID *int64
	if json.Unmarshal(request.UserID, &userID) != nil || (userID != nil && *userID <= 0) {
		response.BadRequest(c, "user_id must be a positive user ID or null")
		return
	}
	api, available := h.adminService.(resourceOwnerService)
	if !available {
		response.Error(c, 503, "Resource ownership service is unavailable")
		return
	}
	owner, err := api.SetResourceOwner(c.Request.Context(), kind, id, userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, owner)
}
