package admin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type userOwnedAdminStub struct{ service.AdminService }

func (*userOwnedAdminStub) GetGroup(ctx context.Context, id int64) (*service.Group, error) {
	if owner, ok := ownership.UserID(ctx); ok && owner == 1 && id == 11 {
		return &service.Group{ID: id}, nil
	}
	return nil, errors.New("group unavailable")
}

func (*userOwnedAdminStub) GetAccount(ctx context.Context, id int64) (*service.Account, error) {
	if owner, ok := ownership.UserID(ctx); ok && owner == 1 && id == 101 {
		return &service.Account{ID: id}, nil
	}
	return nil, errors.New("account unavailable")
}

func TestUserOwnedResourcesRejectsForeignIDsBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name, route, path, body string
		status                  int
	}{
		{"own account", "/accounts/:id", "/accounts/101", "", 200},
		{"foreign account", "/accounts/:id", "/accounts/102", "", 404},
		{"own group", "/groups/:id", "/groups/11", `{"fallback_group_id":null}`, 200},
		{"foreign group", "/groups/:id", "/groups/12", "", 404},
		{"mixed cached statistics", "/accounts/today-stats/batch", "/accounts/today-stats/batch", `{"account_ids":[101,102]}`, 403},
		{"uppercase cached statistics", "/accounts/today-stats/batch", "/accounts/today-stats/batch", `{"ACCOUNT_IDS":[102]}`, 403},
		{"mixed case duplicate statistics", "/accounts/today-stats/batch", "/accounts/today-stats/batch", `{"account_ids":[101],"ACCOUNT_IDS":[102]}`, 403},
		{"unicode folded cached statistics", "/accounts/today-stats/batch", "/accounts/today-stats/batch", `{"account_idſ":[102]}`, 403},
		{"own cached statistics", "/accounts/today-stats/batch", "/accounts/today-stats/batch", `{"account_ids":[101]}`, 200},
		{"foreign group binding", "/accounts", "/accounts", `{"group_ids":[12]}`, 403},
		{"foreign group copy", "/groups", "/groups", `{"copy_accounts_from_group_ids":[12]}`, 403},
		{"foreign routing account", "/groups", "/groups", `{"model_routing":{"model":[102]}}`, 403},
		{"foreign fallback", "/groups", "/groups", `{"fallback_group_id":12}`, 403},
		{"mixed sorting", "/groups/sort-order", "/groups/sort-order", `{"updates":[{"id":11},{"id":12}]}`, 403},
		{"nested foreign group", "/accounts/batch", "/accounts/batch", `{"accounts":[{"group_ids":[12]}]}`, 403},
		{"uppercase nested group", "/accounts/batch", "/accounts/batch", `{"ACCOUNTS":[{"GROUP_IDS":[12]}]}`, 403},
		{"uppercase sorting", "/groups/sort-order", "/groups/sort-order", `{"UPDATES":[{"ID":12}]}`, 403},
		{"global proxy", "/accounts", "/accounts", `{"proxy_id":5}`, 403},
		{"uppercase global proxy", "/accounts", "/accounts", `{"PROXY_ID":5}`, 403},
		{"own explicit bulk selection", "/accounts/bulk-update", "/accounts/bulk-update", `{"account_ids":[101],"status":"inactive"}`, 200},
		{"uppercase own bulk selection", "/accounts/bulk-update", "/accounts/bulk-update", `{"ACCOUNT_IDS":[101],"status":"inactive"}`, 200},
		{"duplicate empty bulk selection", "/accounts/bulk-update", "/accounts/bulk-update", `{"account_ids":[101],"ACCOUNT_IDS":[],"filters":{}}`, 403},
		{"global bulk filter", "/accounts/bulk-update", "/accounts/bulk-update", `{"filters":{},"status":"inactive"}`, 400},
		{"global reconciliation", "/grok/oauth/reconcile", "/grok/oauth/reconcile", `{}`, 403},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), 1))
			})
			h := &GroupHandler{adminService: &userOwnedAdminStub{}}
			called := false
			router.POST("/api/v1/user/managed"+tt.route, h.UserOwnedResources, func(c *gin.Context) {
				called = true
				body, err := io.ReadAll(c.Request.Body)
				require.NoError(t, err)
				if tt.body == "" {
					require.Empty(t, body)
				} else {
					require.JSONEq(t, tt.body, string(body), "guard must preserve the checked JSON meaning")
				}
				c.Status(http.StatusOK)
			})
			request := httptest.NewRequest("POST", "/api/v1/user/managed"+tt.path, strings.NewReader(tt.body))
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			require.Equal(t, tt.status, recorder.Code, recorder.Body.String())
			require.Equal(t, tt.status == 200, called)
		})
	}
}

func TestUserOwnedResourcesPreservesUnscopedAdministrator(t *testing.T) {
	router := gin.New()
	h := &GroupHandler{}
	router.POST("/api/v1/user/managed/accounts/:id", h.UserOwnedResources, func(c *gin.Context) { c.Status(200) })
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/v1/user/managed/accounts/102", strings.NewReader(`{"proxy_id":5}`)))
	require.Equal(t, 200, recorder.Code)
}

type userOwnedGuardStub struct {
	userOwnedAdminStub
	locked     bool
	guardCalls int
	err        error
}

func (s *userOwnedGuardStub) WithResourceOwnershipReadLock(_ context.Context, fn func() error) error {
	s.guardCalls++
	if s.err != nil {
		return s.err
	}
	s.locked = true
	defer func() { s.locked = false }()
	return fn()
}

func (s *userOwnedGuardStub) GetGroup(ctx context.Context, id int64) (*service.Group, error) {
	if !s.locked {
		return nil, errors.New("ownership guard must precede resource validation")
	}
	return s.userOwnedAdminStub.GetGroup(ctx, id)
}

func TestUserOwnedWriteGuardSpansValidationAndMutation(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "locked", true: "lock failure"}[failure], func(t *testing.T) {
			stub := &userOwnedGuardStub{}
			if failure {
				stub.err = errors.New("ownership database unavailable")
			}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), 1))
			})
			h := &GroupHandler{adminService: stub}
			called := false
			router.PUT("/api/v1/user/managed/groups/:id", h.UserOwnedResources, func(c *gin.Context) {
				called = true
				require.True(t, stub.locked, "ownership must stay stable until the write handler completes")
				c.Status(200)
			})
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest("PUT", "/api/v1/user/managed/groups/11", strings.NewReader(`{"description":"checked"}`)))
			require.Equal(t, 1, stub.guardCalls)
			require.False(t, stub.locked, "guard must release after the complete request")
			require.Equal(t, !failure, called)
			if failure {
				require.Equal(t, 500, recorder.Code)
			} else {
				require.Equal(t, 200, recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestUserOwnedWriteGuardSkipsReadsAndAdministrators(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		stub := &userOwnedGuardStub{}
		router := gin.New()
		router.Use(func(c *gin.Context) {
			if scoped {
				c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), 1))
			}
		})
		h := &GroupHandler{adminService: stub}
		router.GET("/api/v1/user/managed/groups", h.UserOwnedResources, func(c *gin.Context) { c.Status(200) })
		if !scoped {
			router.PUT("/api/v1/user/managed/groups", h.UserOwnedResources, func(c *gin.Context) { c.Status(200) })
		}
		method := "GET"
		if !scoped {
			method = "PUT"
		}
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, "/api/v1/user/managed/groups", nil))
		require.Equal(t, 200, recorder.Code)
		require.Zero(t, stub.guardCalls)
	}
}

func TestUserOwnedResourcesCanonicalizesDuplicateBatchFields(t *testing.T) {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), 1))
	})
	h := &GroupHandler{adminService: &userOwnedAdminStub{}}
	router.POST("/api/v1/user/managed/accounts/batch", h.UserOwnedResources, func(c *gin.Context) {
		var request struct {
			Accounts []struct {
				Name    string `json:"name"`
				ProxyID *int64 `json:"proxy_id"`
			} `json:"accounts"`
		}
		require.NoError(t, c.ShouldBindJSON(&request))
		require.Len(t, request.Accounts, 1)
		require.Equal(t, "checked", request.Accounts[0].Name)
		require.Nil(t, request.Accounts[0].ProxyID, "earlier unchecked duplicate must not survive struct slice reuse")
		c.Status(200)
	})
	recorder := httptest.NewRecorder()
	body := `{"accounts":[{"name":"unchecked","proxy_id":5}],"accounts":[{"name":"checked"}]}`
	router.ServeHTTP(recorder, httptest.NewRequest("POST", "/api/v1/user/managed/accounts/batch", strings.NewReader(body)))
	require.Equal(t, 200, recorder.Code, recorder.Body.String())
}

func TestUserOwnedResourceCheckDoesNotInterpretCredentialsAsResourceReferences(t *testing.T) {
	v := &userResourceCheck{ctx: ownership.WithUser(context.Background(), 1), service: &userOwnedAdminStub{}, groups: map[int64]bool{}, accounts: map[int64]bool{}}
	var payload any
	decoder := json.NewDecoder(strings.NewReader(`{"credentials":{"account_id":"upstream-account-id"},"group_ids":[11]}`))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&payload))
	require.True(t, v.payload(payload, false))
}
