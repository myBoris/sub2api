package handler

import (
	"errors"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func subscriptionActor(c *gin.Context, role string) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "请先登录")
		return 0, false
	}
	actualRole, hasRole := middleware.GetUserRoleFromContext(c)
	_, scoped := ownership.UserID(c.Request.Context())
	principal := adminaccess.FromContext(c.Request.Context())
	if !hasRole || actualRole != role || scoped || (role == "user" && principal != nil) || (role == "admin" && adminaccess.RestrictedFromContext(c.Request.Context()) != nil) {
		response.Forbidden(c, "当前账号无权访问订阅申请")
		return 0, false
	}
	return subject.UserID, true
}

func subscriptionRequestError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, accountbilling.ErrSubscriptionRequestPending):
		response.Error(c, http.StatusConflict, "已有待处理的申请，请修改该申请的套餐")
	case errors.Is(err, accountbilling.ErrSubscriptionRequestClosed):
		response.Error(c, http.StatusConflict, "申请已完成，不能再修改套餐")
	case errors.Is(err, accountbilling.ErrNotFound):
		response.NotFound(c, "申请或套餐不存在，请刷新后重试")
	case errors.Is(err, accountbilling.ErrForbidden):
		response.Forbidden(c, "当前账号无权访问订阅申请")
	case errors.Is(err, accountbilling.ErrInvalid):
		response.BadRequest(c, "请选择有效套餐或申请状态")
	case errors.Is(err, accountbilling.ErrConflict):
		response.Error(c, http.StatusConflict, "申请已变化，请刷新后重试")
	default:
		log.Printf("subscription request failed: %v", err)
		response.InternalError(c, "订阅申请暂时不可用")
	}
}

func (h *AccountBillingHandler) SubscriptionPlans(c *gin.Context) {
	id, ok := subscriptionActor(c, "user")
	if !ok {
		return
	}
	plans, err := h.store.SubscriptionPlans(c.Request.Context(), id)
	if err != nil {
		subscriptionRequestError(c, err)
		return
	}
	response.Success(c, gin.H{"plans": plans})
}

func (h *AccountBillingHandler) UserSubscriptionRequests(c *gin.Context) {
	id, ok := subscriptionActor(c, "user")
	if !ok {
		return
	}
	requests, err := h.store.UserSubscriptionRequests(c.Request.Context(), id)
	if err != nil {
		subscriptionRequestError(c, err)
		return
	}
	response.Success(c, gin.H{"items": requests})
}

func (h *AccountBillingHandler) CreateSubscriptionRequest(c *gin.Context) {
	h.saveSubscriptionRequest(c, false)
}

func (h *AccountBillingHandler) UpdateSubscriptionRequest(c *gin.Context) {
	h.saveSubscriptionRequest(c, true)
}

func (h *AccountBillingHandler) saveSubscriptionRequest(c *gin.Context, update bool) {
	userID, ok := subscriptionActor(c, "user")
	if !ok {
		return
	}
	var body struct {
		PlanID int64 `json:"plan_id"`
	}
	if c.ShouldBindJSON(&body) != nil || body.PlanID <= 0 {
		response.BadRequest(c, "请选择有效套餐")
		return
	}
	var request accountbilling.SubscriptionRequest
	var err error
	if update {
		id, valid := billingID(c)
		if !valid {
			return
		}
		request, err = h.store.UpdateSubscriptionRequest(c.Request.Context(), userID, id, body.PlanID)
	} else {
		request, err = h.store.CreateSubscriptionRequest(c.Request.Context(), userID, body.PlanID)
	}
	if err != nil {
		subscriptionRequestError(c, err)
		return
	}
	response.Success(c, request)
}

func (h *AccountBillingHandler) AdminSubscriptionRequests(c *gin.Context) {
	id, ok := subscriptionActor(c, "admin")
	if !ok {
		return
	}
	requests, err := h.store.AdminSubscriptionRequests(c.Request.Context(), id, c.Query("status"), c.Query("search"))
	if err != nil {
		subscriptionRequestError(c, err)
		return
	}
	response.Success(c, gin.H{"items": requests})
}

func (h *AccountBillingHandler) CompleteSubscriptionRequest(c *gin.Context) {
	adminID, ok := subscriptionActor(c, "admin")
	if !ok {
		return
	}
	id, ok := billingID(c)
	if !ok {
		return
	}
	var body struct {
		UpdatedAt *time.Time `json:"updated_at"`
	}
	if err := c.ShouldBindJSON(&body); (err != nil && !errors.Is(err, io.EOF)) || (body.UpdatedAt != nil && body.UpdatedAt.IsZero()) {
		response.BadRequest(c, "无效的申请更新时间，请刷新后重试")
		return
	}
	var expected []time.Time
	if body.UpdatedAt != nil {
		expected = append(expected, *body.UpdatedAt)
	}
	request, err := h.store.CompleteSubscriptionRequest(c.Request.Context(), adminID, id, expected...)
	if err != nil {
		subscriptionRequestError(c, err)
		return
	}
	response.Success(c, request)
}
