package admin

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ownership"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUserOwnedReadGuardCoversUsageAndCredentialRefresh(t *testing.T) {
	cases := []struct {
		name, route, path string
		guarded           bool
	}{
		{"active usage by default", "/accounts/:id/usage", "/accounts/101/usage", true},
		{"forced active usage", "/accounts/:id/usage", "/accounts/101/usage?source=active&force=true", true},
		{"unknown source follows active handler", "/accounts/:id/usage", "/accounts/101/usage?source=unknown", true},
		{"passive usage", "/accounts/:id/usage", "/accounts/101/usage?source=passive&force=true", false},
		{"remote model discovery", "/accounts/:id/models", "/accounts/101/models", true},
		{"reset credit token refresh", "/accounts/:id/claude/reset-credits", "/accounts/101/claude/reset-credits", true},
		{"account details", "/accounts/:id", "/accounts/101", false},
		{"usage statistics", "/accounts/:id/stats", "/accounts/101/stats", false},
		{"ollama stored state", "/accounts/:id/ollama-cloud-usage", "/accounts/101/ollama-cloud-usage", false},
		{"opencode stored state", "/accounts/:id/opencode-go-usage", "/accounts/101/opencode-go-usage", false},
		{"account list", "/accounts", "/accounts", false},
		{"group list", "/groups", "/groups", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			for _, scoped := range []bool{true, false} {
				stub := &userOwnedGuardStub{}
				h := &GroupHandler{adminService: stub}
				router := gin.New()
				router.Use(func(c *gin.Context) {
					if scoped {
						c.Request = c.Request.WithContext(ownership.WithUser(c.Request.Context(), 1))
					}
				})
				called := false
				router.GET("/api/v1/user/managed"+tt.route, h.UserOwnedResources, func(c *gin.Context) {
					called = true
					require.Equal(t, scoped && tt.guarded, stub.locked, "lock must span the complete handler")
					c.Status(200)
				})
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, httptest.NewRequest("GET", "/api/v1/user/managed"+tt.path, nil))
				require.Equal(t, 200, recorder.Code, recorder.Body.String())
				require.True(t, called)
				if scoped && tt.guarded {
					require.Equal(t, 1, stub.guardCalls)
				} else {
					require.Zero(t, stub.guardCalls)
				}
				require.False(t, stub.locked)
			}
		})
	}
}
