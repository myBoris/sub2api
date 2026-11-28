package handler

import (
	"database/sql"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

type AccountBillingHandler struct {
	store     *accountbilling.Store
	scheduler *accountBillingScheduler
}

func NewAccountBillingHandler(db *sql.DB, cfg *config.Config) *AccountBillingHandler {
	timezone := "Asia/Shanghai"
	if cfg != nil {
		timezone = cfg.Timezone
	}
	store := accountbilling.NewStore(db, timezone)
	return &AccountBillingHandler{store: store, scheduler: newAccountBillingScheduler(store.RollRenewalDates)}
}

// RequireAdmin is also applied explicitly to this extension, preventing scoped
// admin keys and user ownership contexts from gaining access to financial data.
func (h *AccountBillingHandler) RequireAdmin(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	role, hasRole := middleware.GetUserRoleFromContext(c)
	_, scoped := ownership.UserID(c.Request.Context())
	if !ok || !hasRole || subject.UserID <= 0 || role != "admin" || scoped || adminaccess.RestrictedFromContext(c.Request.Context()) != nil {
		response.Forbidden(c, "只有管理员可以管理订阅收款")
		c.Abort()
		return
	}
	c.Next()
}

func billingError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, accountbilling.ErrNotFound):
		response.NotFound(c, "记录不存在，请刷新后重试")
	case errors.Is(err, accountbilling.ErrConflict):
		response.Error(c, http.StatusConflict, "名称已存在，或记录仍被账号使用，暂时不能删除")
	case errors.Is(err, accountbilling.ErrInvalid):
		response.BadRequest(c, "输入无效，请检查金额（最多两位小数）、日期、套餐和代付者")
	case errors.Is(err, accountbilling.ErrForbidden):
		response.Forbidden(c, "仅活跃普通用户可以查看自己的费用汇总")
	default:
		log.Printf("account billing request failed: %v", err)
		response.InternalError(c, "订阅收款服务暂时不可用")
	}
}

func billingID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "无效的记录 ID")
		return 0, false
	}
	return id, true
}

func (h *AccountBillingHandler) Catalog(c *gin.Context) {
	catalog, err := h.store.Catalog(c.Request.Context())
	if err != nil {
		billingError(c, err)
		return
	}
	response.Success(c, catalog)
}

func (h *AccountBillingHandler) SavePlan(c *gin.Context) {
	id := int64(0)
	if c.Param("id") != "" {
		var ok bool
		id, ok = billingID(c)
		if !ok {
			return
		}
	}
	var request struct {
		Platform string                `json:"platform"`
		Name     string                `json:"name"`
		Price    *accountbilling.Money `json:"price"`
		Cost     *accountbilling.Money `json:"cost"`
	}
	if c.ShouldBindJSON(&request) != nil || request.Price == nil {
		response.BadRequest(c, "请填写平台、套餐名称和人民币售价（最多两位小数）")
		return
	}
	cost := accountbilling.Money(0)
	if request.Cost != nil {
		cost = *request.Cost
	}
	p, err := h.store.SavePlan(c.Request.Context(), id, accountbilling.Plan{Platform: request.Platform, Name: request.Name, Price: *request.Price, Cost: cost}, request.Cost != nil)
	if err != nil {
		billingError(c, err)
		return
	}
	response.Success(c, p)
}

func (h *AccountBillingHandler) SavePayer(c *gin.Context) {
	id := int64(0)
	if c.Param("id") != "" {
		var ok bool
		id, ok = billingID(c)
		if !ok {
			return
		}
	}
	var p accountbilling.Payer
	if c.ShouldBindJSON(&p) != nil {
		response.BadRequest(c, "请填写代付者姓名")
		return
	}
	p, err := h.store.SavePayer(c.Request.Context(), id, p)
	if err != nil {
		billingError(c, err)
		return
	}
	response.Success(c, p)
}

func (h *AccountBillingHandler) DeletePlan(c *gin.Context)  { h.deleteCatalog(c, "plans") }
func (h *AccountBillingHandler) DeletePayer(c *gin.Context) { h.deleteCatalog(c, "payers") }
func (h *AccountBillingHandler) deleteCatalog(c *gin.Context, kind string) {
	id, ok := billingID(c)
	if !ok {
		return
	}
	if err := h.store.DeleteCatalog(c.Request.Context(), kind, id); err != nil {
		billingError(c, err)
		return
	}
	response.Success(c, gin.H{"deleted": true})
}

func (h *AccountBillingHandler) List(c *gin.Context) {
	list, err := h.store.List(c.Request.Context(), c.Query("month"))
	if err != nil {
		billingError(c, err)
		return
	}
	// Statistics describe the whole month; table filters do not alter totals.
	search := strings.ToLower(strings.TrimSpace(c.Query("search")))
	status := c.Query("payment_status")
	if status != "" && status != "paid" && status != "unpaid" {
		response.BadRequest(c, "无效的支付状态")
		return
	}
	var payerID *int64
	if raw := c.Query("payer_id"); raw != "" {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || id < 0 {
			response.BadRequest(c, "无效的代付者 ID")
			return
		}
		payerID = &id
	}
	filtered := make([]accountbilling.Account, 0, len(list.Items))
	for _, a := range list.Items {
		if search != "" && !strings.Contains(strings.ToLower(a.AccountName+" "+a.Platform+" "+a.PlanName), search) {
			continue
		}
		if status != "" && status != a.PaymentStatus {
			continue
		}
		if payerID != nil {
			id := int64(0)
			if a.PayerID != nil {
				id = *a.PayerID
			}
			if id != *payerID {
				continue
			}
		}
		filtered = append(filtered, a)
	}
	list.Items = filtered
	response.Success(c, list)
}

func (h *AccountBillingHandler) Sync(c *gin.Context) {
	inserted, err := h.store.SyncAccounts(c.Request.Context())
	if err != nil {
		billingError(c, err)
		return
	}
	response.Success(c, gin.H{"inserted": inserted})
}

func (h *AccountBillingHandler) Update(c *gin.Context) {
	id, ok := billingID(c)
	if !ok {
		return
	}
	var request struct {
		RenewalDate        *string               `json:"renewal_date"`
		RenewalDateChanged *bool                 `json:"renewal_date_changed"`
		PlanID             *int64                `json:"plan_id"`
		Price              *accountbilling.Money `json:"price"`
		Cost               *accountbilling.Money `json:"cost"`
		PayerID            *int64                `json:"payer_id"`
		PaymentStatus      string                `json:"payment_status"`
	}
	if c.ShouldBindJSON(&request) != nil || request.Price == nil {
		response.BadRequest(c, "请填写完整账号支付信息，金额最多两位小数")
		return
	}
	a, err := h.store.UpdateAccount(c.Request.Context(), id, accountbilling.AccountUpdate{RenewalDate: request.RenewalDate, RenewalDateChanged: request.RenewalDateChanged, PlanID: request.PlanID, Price: *request.Price, Cost: request.Cost, PayerID: request.PayerID, PaymentStatus: request.PaymentStatus})
	if err != nil {
		billingError(c, err)
		return
	}
	response.Success(c, a)
}

func (h *AccountBillingHandler) UpdateHistory(c *gin.Context) {
	id, ok := billingID(c)
	if !ok {
		return
	}
	var request struct {
		Status string                `json:"status"`
		Amount *accountbilling.Money `json:"amount"`
		Cost   *accountbilling.Money `json:"cost"`
	}
	if c.ShouldBindJSON(&request) != nil || request.Amount == nil {
		response.BadRequest(c, "请填写支付状态和金额（最多两位小数）")
		return
	}
	a, err := h.store.UpdateHistory(c.Request.Context(), id, accountbilling.History{Month: c.Param("month"), Status: request.Status, Amount: *request.Amount}, request.Cost)
	if err != nil {
		billingError(c, err)
		return
	}
	response.Success(c, a)
}

// UserSummary uses only the authenticated ordinary user's ID. Query parameters
// cannot select another owner; the store also rechecks active database status.
func (h *AccountBillingHandler) UserSummary(c *gin.Context) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "请先登录")
		return
	}
	role, hasRole := middleware.GetUserRoleFromContext(c)
	_, scoped := ownership.UserID(c.Request.Context())
	if !hasRole || role != "user" || scoped || adminaccess.RestrictedFromContext(c.Request.Context()) != nil {
		response.Forbidden(c, "仅普通用户可以查看自己的费用汇总")
		return
	}
	summary, err := h.store.UserSummary(c.Request.Context(), subject.UserID)
	if err != nil {
		billingError(c, err)
		return
	}
	response.Success(c, summary)
}
