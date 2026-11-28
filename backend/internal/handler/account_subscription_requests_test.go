package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionRequestHandlersRejectImpersonationAndWrongRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AccountBillingHandler{}
	for _, role := range []string{"user", "admin"} {
		for _, rejected := range []string{"anonymous", "wrong role", "ownership scope", "restricted key", "full key as user"} {
			if role == "admin" && rejected == "full key as user" {
				continue
			}
			t.Run(role+"/"+rejected, func(t *testing.T) {
				router := gin.New()
				router.Use(func(c *gin.Context) {
					if rejected != "anonymous" {
						c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
						actualRole := role
						if rejected == "wrong role" {
							if role == "user" {
								actualRole = "admin"
							} else {
								actualRole = "user"
							}
						}
						c.Set(string(middleware.ContextKeyUserRole), actualRole)
					}
					if rejected == "ownership scope" {
						c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), 7))
					}
					if rejected == "restricted key" || rejected == "full key as user" {
						c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), &adminaccess.Principal{KeyID: "key", Policy: adminaccess.Policy{FullAccess: rejected == "full key as user"}}))
					}
				})
				if role == "user" {
					router.GET("/requests", h.UserSubscriptionRequests)
				} else {
					router.GET("/requests", h.AdminSubscriptionRequests)
				}
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/requests", nil))
				want := http.StatusForbidden
				if rejected == "anonymous" {
					want = http.StatusUnauthorized
				}
				require.Equal(t, want, recorder.Code, recorder.Body.String())
			})
		}
	}
}

func TestSubscriptionRequestUserPlanListRechecksDatabaseAndNeverSelectsCost(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer db.Close()
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role=\$2 FOR SHARE`).
		WithArgs(int64(7), "user").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(7))
	mock.ExpectQuery(`SELECT id,platform,name,price_cents FROM custom_billing_plans ORDER BY platform,name,id`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "platform", "name", "price_cents"}).AddRow(5, "openai", "Plus", 12345))
	mock.ExpectCommit()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Set(string(middleware.ContextKeyUserRole), "user")
	})
	h := &AccountBillingHandler{store: accountbilling.NewStore(db, "UTC")}
	router.GET("/plans", h.SubscriptionPlans)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/plans?user_id=8", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"code":0,"message":"success","data":{"plans":[{"id":5,"platform":"openai","name":"Plus","price":123.45}]}}`, recorder.Body.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestSubscriptionRequestHandlerRejectsInvalidPlanWithoutDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, body := range []string{`{}`, `{"plan_id":0}`, `{"plan_id":-1}`, `{"plan_id":"5"}`, `{"plan_id":2.5}`} {
		router := gin.New()
		router.Use(func(c *gin.Context) {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), "user")
		})
		router.POST("/requests", (&AccountBillingHandler{}).CreateSubscriptionRequest)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/requests", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code, recorder.Body.String())
	}
}
