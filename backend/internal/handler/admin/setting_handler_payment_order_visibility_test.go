package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestSettingsPaymentVisibleOrderTypesRoundTrip(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{})
	h.paymentConfigService = service.NewPaymentConfigService(nil, repo, nil)
	for _, types := range [][]string{{"balance", "subscription", "account"}, {}, {"account"}} {
		rec := doUpdateSettings(t, h, map[string]any{"payment_visible_order_types": types}, nil)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var result struct {
			Data struct {
				Types []string `json:"payment_visible_order_types"`
			} `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.Equal(t, types, result.Data.Types)

		getRec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(getRec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/settings", nil)
		h.GetSettings(ctx)
		require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
		require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &result))
		require.Equal(t, types, result.Data.Types)
	}
	stored := repo.values[service.SettingPaymentVisibleOrderTypes]
	rec := doUpdateSettings(t, h, map[string]any{"site_name": "Preserve payment entries"}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, stored, repo.values[service.SettingPaymentVisibleOrderTypes])
}

func TestSettingsPaymentVisibleOrderTypesRejectsUnknownBeforeWriting(t *testing.T) {
	h, repo := newStepUpSwitchTestHandler(t, map[string]string{service.SettingKeySiteName: "Original"})
	h.paymentConfigService = service.NewPaymentConfigService(nil, repo, nil)
	rec := doUpdateSettings(t, h, map[string]any{
		"site_name": "Changed", "payment_visible_order_types": []string{"unknown"},
	}, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "Original", repo.values[service.SettingKeySiteName])
	require.NotContains(t, repo.values, service.SettingPaymentVisibleOrderTypes)
}
