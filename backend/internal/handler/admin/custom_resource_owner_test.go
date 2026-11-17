package admin

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/adminaccess"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type resourceOwnerAdminStub struct {
	service.AdminService
	read  func(context.Context, string, []int64) ([]service.ResourceOwner, error)
	write func(context.Context, string, int64, *int64) (*service.ResourceOwner, error)
}

func (s *resourceOwnerAdminStub) ResourceOwners(ctx context.Context, kind string, ids []int64) ([]service.ResourceOwner, error) {
	return s.read(ctx, kind, ids)
}

func (s *resourceOwnerAdminStub) SetResourceOwner(ctx context.Context, kind string, id int64, userID *int64) (*service.ResourceOwner, error) {
	return s.write(ctx, kind, id, userID)
}

func TestResourceOwnerReadPermissionsAndValidation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name, role, path string
		restricted       bool
		status           int
	}{
		{"anonymous", "", "/groups?ids=11", false, 401},
		{"ordinary own", "user", "/groups?ids=11,11", false, 200},
		{"ordinary mixed owners", "user", "/groups?ids=11,12", false, 404},
		{"admin unassigned", "admin", "/accounts?ids=12", false, 200},
		{"restricted principal", "admin", "/groups?ids=11", true, 403},
		{"invalid kind", "admin", "/users?ids=11", false, 400},
		{"missing ids", "admin", "/groups", false, 400},
		{"invalid id", "admin", "/groups?ids=11,-1", false, 400},
		{"empty id", "admin", "/groups?ids=11,", false, 400},
		{"oversized batch", "admin", "/groups?ids=" + strings.Repeat("11,", 1000) + "11", false, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			stub := &resourceOwnerAdminStub{read: func(ctx context.Context, kind string, ids []int64) ([]service.ResourceOwner, error) {
				called = true
				userID, scoped := ownership.UserID(ctx)
				require.Equal(t, tt.role != "admin", scoped)
				if scoped {
					require.EqualValues(t, 7, userID)
					if len(ids) > 1 {
						return nil, service.ErrGroupNotFound
					}
					require.Equal(t, []int64{11}, ids, "resource IDs must be deduplicated")
					return []service.ResourceOwner{{ResourceID: 11, UserID: &userID, Email: "self@example.test"}}, nil
				}
				require.Equal(t, "accounts", kind)
				return []service.ResourceOwner{{ResourceID: 12}}, nil
			}}
			router := resourceOwnerTestRouter(tt.role, tt.restricted, false, stub)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest("GET", "/owners"+tt.path, nil))
			require.Equal(t, tt.status, recorder.Code, recorder.Body.String())
			require.Equal(t, tt.status == 200 || tt.status == 404, called)
			if tt.status == 404 {
				require.NotContains(t, recorder.Body.String(), "@example.test")
			}
			if tt.name == "admin unassigned" {
				require.Contains(t, recorder.Body.String(), `"user_id":null`)
			}
		})
	}
}

func TestResourceOwnerWritePermissionsAndValidation(t *testing.T) {
	for _, tt := range []struct {
		name, role, path, body string
		restricted, scoped     bool
		status                 int
	}{
		{"ordinary denied", "user", "/groups/11", `{"user_id":8}`, false, false, 403},
		{"anonymous denied", "", "/groups/11", `{"user_id":8}`, false, false, 403},
		{"restricted admin denied", "admin", "/groups/11", `{"user_id":8}`, true, false, 403},
		{"scoped admin denied", "admin", "/groups/11", `{"user_id":8}`, false, true, 403},
		{"missing owner", "admin", "/groups/11", `{}`, false, false, 400},
		{"zero owner", "admin", "/groups/11", `{"user_id":0}`, false, false, 400},
		{"negative owner", "admin", "/groups/11", `{"user_id":-2}`, false, false, 400},
		{"string owner", "admin", "/groups/11", `{"user_id":"8"}`, false, false, 400},
		{"invalid kind", "admin", "/users/11", `{"user_id":8}`, false, false, 400},
		{"invalid resource id", "admin", "/groups/0", `{"user_id":8}`, false, false, 400},
		{"admin transfer", "admin", "/groups/11", `{"user_id":8}`, false, false, 200},
		{"admin clear", "admin", "/accounts/12", `{"user_id":null}`, false, false, 200},
		{"conflict propagated", "admin", "/groups/13", `{"user_id":8}`, false, false, 409},
	} {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			stub := &resourceOwnerAdminStub{write: func(ctx context.Context, kind string, id int64, userID *int64) (*service.ResourceOwner, error) {
				called = true
				_, scoped := ownership.UserID(ctx)
				require.False(t, scoped)
				if id == 13 {
					return nil, service.ErrResourceOwnerConflict
				}
				if kind == "accounts" {
					require.Nil(t, userID)
				} else {
					require.EqualValues(t, 8, *userID)
				}
				return &service.ResourceOwner{ResourceID: id, UserID: userID}, nil
			}}
			router := resourceOwnerTestRouter(tt.role, tt.restricted, tt.scoped, stub)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("PUT", "/owners"+tt.path, strings.NewReader(tt.body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)
			require.Equal(t, tt.status, recorder.Code, recorder.Body.String())
			require.Equal(t, tt.status == 200 || tt.status == 409, called)
			if tt.status == 409 {
				require.Contains(t, recorder.Body.String(), "RESOURCE_OWNER_REFERENCES_CONFLICT")
			}
		})
	}
}

func resourceOwnerTestRouter(role string, restricted, scoped bool, stub *resourceOwnerAdminStub) *gin.Engine {
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if role != "" {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 7})
			c.Set(string(middleware.ContextKeyUserRole), role)
		}
		ctx := c.Request.Context()
		if restricted {
			ctx = adminaccess.WithPrincipal(ctx, &adminaccess.Principal{})
		}
		if scoped {
			ctx = ownership.WithUser(ctx, 7)
		}
		c.Request = c.Request.WithContext(ctx)
	})
	h := &GroupHandler{adminService: stub}
	router.GET("/owners/:kind", h.ResourceOwners)
	router.PUT("/owners/:kind/:id", h.SetResourceOwner)
	return router
}
