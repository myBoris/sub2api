package routes

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/handler/admin"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type userManagedAdminStub struct{ service.AdminService }

func (*userManagedAdminStub) GetGroup(ctx context.Context, id int64) (*service.Group, error) {
	if userID, scoped := ownership.UserID(ctx); scoped && id != userID*10 {
		return nil, service.ErrGroupNotFound
	}
	return &service.Group{ID: id, Name: "test group", Platform: "openai", Status: "active"}, nil
}

func (*userManagedAdminStub) ResourceOwners(ctx context.Context, kind string, ids []int64) ([]service.ResourceOwner, error) {
	userID, scoped := ownership.UserID(ctx)
	if !scoped || kind != "groups" || len(ids) != 1 || ids[0] != userID*10 {
		return nil, service.ErrGroupNotFound
	}
	return []service.ResourceOwner{{ResourceID: ids[0], UserID: &userID}}, nil
}

func (*userManagedAdminStub) SetResourceOwner(context.Context, string, int64, *int64) (*service.ResourceOwner, error) {
	panic("ordinary user routes must never transfer ownership")
}

func TestUserManagedRoutesEnforceSessionAndOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name, role, path string
		status           int
	}{
		{"user reads own group", "user", "/user/managed/groups/10", 200},
		{"user cannot read foreign group", "user", "/user/managed/groups/20", 404},
		{"admin reads every group", "admin", "/user/managed/groups/20", 200},
		{"missing session", "", "/user/managed/groups/10", 401},
		{"user cannot access global reconciliation", "user", "/user/managed/grok/runtime-sanity", 403},
		{"global settings not exposed", "user", "/user/managed/settings", 404},
		{"own owner visible", "user", "/user/managed/resource-ownership/groups?ids=10", 200},
		{"foreign owner hidden", "user", "/user/managed/resource-ownership/groups?ids=20", 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			v1 := router.Group("/api/v1")
			v1.Use(func(c *gin.Context) {
				if tt.role != "" {
					c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
					c.Set(string(middleware.ContextKeyUserRole), tt.role)
				}
			})
			h := &handler.Handlers{Admin: &handler.AdminHandlers{
				Group:   admin.NewGroupHandler(&userManagedAdminStub{}, nil, nil),
				Account: &admin.AccountHandler{},
			}}
			registerUserManagedRoutes(v1, h)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1"+tt.path, nil))
			require.Equal(t, tt.status, recorder.Code, recorder.Body.String())
		})
	}
}

func TestUserManagedOwnershipHasNoTransferRoute(t *testing.T) {
	router := gin.New()
	v1 := router.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1})
		c.Set(string(middleware.ContextKeyUserRole), "user")
	})
	h := &handler.Handlers{Admin: &handler.AdminHandlers{
		Group:   admin.NewGroupHandler(&userManagedAdminStub{}, nil, nil),
		Account: &admin.AccountHandler{},
	}}
	registerUserManagedRoutes(v1, h)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest("PUT", "/api/v1/user/managed/resource-ownership/groups/10", nil))
	require.Equal(t, 404, recorder.Code)
}
