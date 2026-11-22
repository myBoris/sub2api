//go:build unit

package handler

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/accountbilling"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type renewalDiagnoser struct{ fakeDiagnoser }

func (d *renewalDiagnoser) CheckAccountRenewalAvailability(context.Context, *int64, string) error {
	return service.ErrAccountRenewalExpired
}

func TestAccountRenewalClassificationKeeps402InsteadOfNoModelOrCapacity(t *testing.T) {
	classification := classifySelectionFailureError(service.ErrAccountRenewalExpired, noAccountErrorClassification{Status: 404, ModelNotFound: true})
	require.Equal(t, http.StatusPaymentRequired, classification.Status)
	require.Equal(t, service.AccountRenewalExpiredCode, classification.ErrType)
	classification = classifyNoAccountError(context.Background(), &renewalDiagnoser{}, &service.APIKey{}, "model", "model", service.PlatformOpenAI)
	require.Equal(t, http.StatusPaymentRequired, classification.Status)
	require.Equal(t, service.AccountRenewalExpiredMessage, classification.Message)
}

type websocketRenewalAccessStore struct{ expired atomic.Bool }

func (s *websocketRenewalAccessStore) AccessStates(_ context.Context, ids []int64) (map[int64]accountbilling.AccessState, error) {
	states := make(map[int64]accountbilling.AccessState, len(ids))
	for _, id := range ids {
		states[id] = accountbilling.AccessState{Expired: s.expired.Load()}
	}
	return states, nil
}

func TestAccountRenewalWebSocketRejectsNewTurnAfterExpiry(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			store := &websocketRenewalAccessStore{}
			runOpenAIResponsesWebSocketUsageLogCase(t, openAIResponsesWSUsageLogCase{
				firstPayload:              `{"type":"response.create","model":"gpt-5.4","input":"hello"}`,
				secondPayload:             `{"type":"response.create","model":"gpt-5.4","input":"again"}`,
				ingressMode:               mode,
				accountRenewalGuard:       service.NewAccountRenewalGuard(store),
				afterFirstUpstreamRequest: func(*service.ChannelService) error { store.expired.Store(true); return nil },
				secondTurnCloseExpected:   true,
				closeReason:               service.AccountRenewalExpiredWSReason,
			})
			// The helper asserts the first completion, 1008 close reason, and
			// exactly one upstream frame before returning from a rejected turn.
			require.True(t, store.expired.Load())
		})
	}
}
