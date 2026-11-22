package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/accountbilling"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountBillingAdminGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name      string
		role      string
		scoped    bool
		principal *adminaccess.Principal
		want      int
	}{
		{name: "anonymous", want: http.StatusForbidden},
		{name: "ordinary user", role: "user", want: http.StatusForbidden},
		{name: "ownership scoped admin", role: "admin", scoped: true, want: http.StatusForbidden},
		{name: "restricted admin key", role: "admin", principal: &adminaccess.Principal{KeyID: "restricted"}, want: http.StatusForbidden},
		{name: "administrator", role: "admin", want: http.StatusOK},
		{name: "full access admin key", role: "admin", principal: &adminaccess.Principal{KeyID: "full", Policy: adminaccess.Policy{FullAccess: true}}, want: http.StatusOK},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				if tt.role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
					c.Set(string(middleware.ContextKeyUserRole), tt.role)
				}
				if tt.scoped {
					c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), 7))
				}
				if tt.principal != nil {
					c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), tt.principal))
				}
			})
			called := false
			handler := &AccountBillingHandler{}
			router.GET("/billing", handler.RequireAdmin, func(c *gin.Context) { called = true; c.Status(http.StatusOK) })
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/billing", nil))
			require.Equal(t, tt.want, recorder.Code, recorder.Body.String())
			require.Equal(t, tt.want == http.StatusOK, called)
		})
	}
}

func TestAccountBillingSummaryRequiresOrdinaryAuthenticatedSession(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name, role string
		userID     int64
		scoped     bool
		restricted bool
		want       int
	}{
		{name: "anonymous", want: http.StatusUnauthorized},
		{name: "invalid subject", role: "user", userID: -1, want: http.StatusUnauthorized},
		{name: "missing role", userID: 7, want: http.StatusForbidden},
		{name: "administrator", role: "admin", userID: 7, want: http.StatusForbidden},
		{name: "scoped admin context cannot impersonate user", role: "user", userID: 7, scoped: true, want: http.StatusForbidden},
		{name: "restricted key cannot read summary", role: "user", userID: 7, restricted: true, want: http.StatusForbidden},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				if tt.userID != 0 {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: tt.userID})
				}
				if tt.role != "" {
					c.Set(string(middleware.ContextKeyUserRole), tt.role)
				}
				if tt.scoped {
					c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), 99))
				}
				if tt.restricted {
					c.Request = c.Request.WithContext(adminaccess.WithPrincipal(c.Request.Context(), &adminaccess.Principal{KeyID: "restricted"}))
				}
			})
			// A nil store ensures rejected sessions cannot reach database operations.
			h := &AccountBillingHandler{}
			router.GET("/account-billing/summary", h.UserSummary)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/account-billing/summary", nil))
			require.Equal(t, tt.want, recorder.Code, recorder.Body.String())
		})
	}
}

func TestAccountBillingSummaryUsesSubjectOwnershipAndOmitsCollectionAndAccountDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	month := now.Format("2006-01")
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role='user'\)`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery(`(?s)SELECT b.account_id.*WHERE a.deleted_at IS NULL AND EXISTS\(SELECT 1 FROM custom_user_accounts o WHERE o.account_id=b.account_id AND o.user_id=\$1\).*FOR UPDATE OF b`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id", "name", "platform", "renewal_date", "plan_id", "plan_name", "price_cents", "payer_id", "payer_name", "history", "created_at"}).
		AddRow(11, "Private account name", "openai", nil, nil, "Private plan", 123, nil, "Private payer", `[{"month":"`+month+`","status":"unpaid","amount":1.23}]`, now))
	mock.ExpectQuery(`(?s)SELECT account_id,valid_through FROM custom_account_billing_access.*WHERE account_id=ANY\(\$1\) ORDER BY account_id`).
		WithArgs("{11}").WillReturnRows(sqlmock.NewRows([]string{"account_id", "valid_through"}).AddRow(11, nil))
	mock.ExpectCommit()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Set(string(middleware.ContextKeyUserRole), "user")
	})
	h := &AccountBillingHandler{store: accountbilling.NewStore(db, "UTC")}
	router.GET("/account-billing/summary", h.UserSummary)
	recorder := httptest.NewRecorder()
	// Client-supplied owners and selected months must not change this summary.
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/account-billing/summary?user_id=99&account_id=88&month=2000-01", nil))
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.JSONEq(t, `{"code":0,"message":"success","data":{"month":"`+month+`","amount_due":1.23,"total_amount":1.23,"accounts":[{"account_id":11,"renewal_date":null,"price":1.23,"payment_status":"unpaid","valid_through":null,"expired":false}]}}`, recorder.Body.String())
	require.NoError(t, mock.ExpectationsWereMet(), "no allowlist or image settings may be read")
}

func TestAccountBillingSummaryRejectsUserDeactivatedSinceAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT EXISTS\(SELECT 1 FROM users WHERE id=\$1 AND deleted_at IS NULL AND status='active' AND role='user'\)`).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
		c.Set(string(middleware.ContextKeyUserRole), "user")
	})
	h := &AccountBillingHandler{store: accountbilling.NewStore(db, "UTC")}
	router.GET("/account-billing/summary", h.UserSummary)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/account-billing/summary", nil))
	require.Equal(t, http.StatusForbidden, recorder.Code, recorder.Body.String())
	require.NoError(t, mock.ExpectationsWereMet())
}
