package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// UserOwnedResources runs before handlers that may read caches, run background
// work, or write batches directly. Repository scoping also protects CRUD itself.
func (h *GroupHandler) UserOwnedResources(c *gin.Context) {
	_, scoped := ownership.UserID(c.Request.Context())
	if scoped && userOwnedRequestNeedsGuard(c) {
		if guard, ok := h.adminService.(interface {
			WithResourceOwnershipReadLock(context.Context, func() error) error
		}); ok {
			if err := guard.WithResourceOwnershipReadLock(c.Request.Context(), func() error {
				h.userOwnedResources(c)
				return nil
			}); err != nil {
				response.ErrorFrom(c, err)
				c.Abort()
			}
			return
		}
	}
	h.userOwnedResources(c)
}

func userOwnedRequestNeedsGuard(c *gin.Context) bool {
	switch c.Request.Method {
	case "GET":
		path := strings.TrimPrefix(c.FullPath(), "/api/v1/user/managed")
		switch path {
		case "/accounts/:id/usage":
			// Active usage can refresh credentials, persist quota observations,
			// and clear recoverable errors. Passive usage only reads snapshots.
			return c.DefaultQuery("source", "active") != "passive"
		case "/accounts/:id/models", "/accounts/:id/claude/reset-credits":
			// Remote discovery may refresh an OAuth access token.
			return true
		}
		return false
	case "HEAD", "OPTIONS":
		return false
	default:
		return true
	}
}

func (h *GroupHandler) userOwnedResources(c *gin.Context) {
	ctx := c.Request.Context()
	if _, scoped := ownership.UserID(ctx); !scoped {
		c.Next()
		return
	}
	path := strings.TrimPrefix(c.FullPath(), "/api/v1/user/managed")
	if path == "/grok/oauth/reconcile" || path == "/grok/runtime-sanity" {
		response.Forbidden(c, "Administrator access required")
		c.Abort()
		return
	}
	check := &userResourceCheck{ctx: ctx, service: h.adminService, groups: map[int64]bool{}, accounts: map[int64]bool{}}
	if raw := c.Param("id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		isGroup := strings.HasPrefix(path, "/groups/")
		if err != nil || !check.owned(id, isGroup) {
			response.NotFound(c, "Resource not found")
			c.Abort()
			return
		}
	}
	// Handlers receive the same parsed JSON that was checked here. Re-encoding
	// prevents duplicate object keys from being merged differently by struct binding.
	if c.Request.Body != nil && c.Request.Method != "GET" && c.Request.ContentLength != 0 {
		data, err := io.ReadAll(io.LimitReader(c.Request.Body, (16<<20)+1))
		if err != nil || len(data) > 16<<20 {
			response.BadRequest(c, "Invalid request body")
			c.Abort()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(data))
		if len(bytes.TrimSpace(data)) > 0 {
			var payload any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if decoder.Decode(&payload) != nil || !check.payload(payload, strings.HasPrefix(path, "/groups/")) {
				response.Forbidden(c, "Request references an unavailable resource")
				c.Abort()
				return
			}
			if path == "/accounts/bulk-update" {
				body, ok := payload.(map[string]any)
				var ids []any
				for key, value := range body {
					if strings.EqualFold(key, "account_ids") {
						ids, _ = value.([]any)
					}
				}
				if !ok || len(ids) == 0 {
					response.BadRequest(c, "Explicit account_ids are required")
					c.Abort()
					return
				}
			}
			canonical, err := json.Marshal(payload)
			if err != nil {
				response.BadRequest(c, "Invalid request body")
				c.Abort()
				return
			}
			c.Request.Body = io.NopCloser(bytes.NewReader(canonical))
			c.Request.ContentLength = int64(len(canonical))
			c.Set(gin.BodyBytesKey, canonical)
		}
	}
	c.Next()
}

type userResourceReader interface {
	GetGroup(context.Context, int64) (*service.Group, error)
	GetAccount(context.Context, int64) (*service.Account, error)
}

type userResourceCheck struct {
	ctx              context.Context
	service          userResourceReader
	groups, accounts map[int64]bool
}

func (v *userResourceCheck) owned(id int64, group bool) bool {
	if id <= 0 {
		return false
	}
	cache := v.accounts
	if group {
		cache = v.groups
	}
	if result, ok := cache[id]; ok {
		return result
	}
	var ok bool
	if group {
		resource, err := v.service.GetGroup(v.ctx, id)
		ok = err == nil && resource != nil
	} else {
		resource, err := v.service.GetAccount(v.ctx, id)
		ok = err == nil && resource != nil
	}
	cache[id] = ok
	return ok
}

func (v *userResourceCheck) id(value any, group bool, optional bool) bool {
	if value == nil {
		return optional
	}
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	id, err := number.Int64()
	return err == nil && ((optional && id == 0) || v.owned(id, group))
}

func (v *userResourceCheck) ids(value any, group bool) bool {
	if value == nil {
		return true
	}
	ids, ok := value.([]any)
	if !ok {
		return false
	}
	for _, id := range ids {
		if !v.id(id, group, false) {
			return false
		}
	}
	return true
}

func (v *userResourceCheck) payload(value any, group bool) bool {
	if values, ok := value.([]any); ok {
		for _, item := range values {
			if !v.payload(item, group) {
				return false
			}
		}
		return true
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return false
	}
	seen := make(map[string]bool, len(fields))
	for key, value := range fields {
		valid := true
		// Match encoding/json's case-insensitive struct field names, including
		// Unicode folds such as long s. Reject ambiguous field aliases, whose
		// result otherwise depends on the order in the original JSON document.
		key = strings.ToLower(strings.ToUpper(key))
		if seen[key] {
			return false
		}
		seen[key] = true
		switch key {
		case "proxy_id":
			valid = value == nil || value == json.Number("0")
		case "group_ids", "copy_accounts_from_group_ids":
			valid = v.ids(value, true)
		case "account_ids":
			valid = v.ids(value, false)
		case "ids":
			valid = v.ids(value, group)
		case "id":
			valid = v.id(value, group, false)
		case "group_id", "fallback_group_id", "fallback_group_id_on_invalid_request", "source_group_id":
			valid = v.id(value, true, true)
		case "account_id", "parent_account_id", "exclude_account_id", "source_account_id":
			valid = v.id(value, false, true)
		case "model_routing":
			if value != nil {
				routes, ok := value.(map[string]any)
				valid = ok
				for _, ids := range routes {
					valid = valid && v.ids(ids, false)
				}
			}
		case "accounts", "updates":
			valid = v.payload(value, group)
		}
		if !valid {
			return false
		}
	}
	return true
}

// GetOwnedUsageSummary filters the upstream aggregate before it reaches users.
func (h *GroupHandler) GetOwnedUsageSummary(c *gin.Context) {
	ctx := c.Request.Context()
	groups, err := h.adminService.GetAllGroupsIncludingInactive(ctx)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	allowed := make(map[int64]bool, len(groups))
	for _, group := range groups {
		allowed[group.ID] = true
	}
	results, err := h.dashboardService.GetGroupUsageSummary(ctx, service.GroupUsageTodayStart(time.Now()))
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	filtered := make([]usagestats.GroupUsageSummary, 0, len(groups))
	for _, result := range results {
		if allowed[result.GroupID] {
			filtered = append(filtered, result)
		}
	}
	response.Success(c, filtered)
}
