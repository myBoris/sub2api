package service

import (
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// Store this extension as a setting so the existing payment tables stay unchanged.
const SettingPaymentVisibleOrderTypes = "CUSTOM_PAYMENT_VISIBLE_ORDER_TYPES"

const accountPaymentOrderType = "account"

func defaultPaymentVisibleOrderTypes() []string {
	return []string{payment.OrderTypeBalance, payment.OrderTypeSubscription}
}

// NormalizePaymentVisibleOrderTypes validates configured payment entries.
func NormalizePaymentVisibleOrderTypes(types []string) ([]string, error) {
	normalized := make([]string, 0, len(types))
	seen := make(map[string]bool, len(types))
	for _, orderType := range types {
		orderType = strings.TrimSpace(orderType)
		switch orderType {
		case payment.OrderTypeBalance, payment.OrderTypeSubscription, accountPaymentOrderType:
		default:
			return nil, infraerrors.BadRequest("INVALID_VISIBLE_ORDER_TYPES", "visible order types must be balance, subscription or account")
		}
		if !seen[orderType] {
			normalized = append(normalized, orderType)
			seen[orderType] = true
		}
	}
	return normalized, nil
}

func parsePaymentVisibleOrderTypes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return defaultPaymentVisibleOrderTypes()
	}
	var types []string
	if json.Unmarshal([]byte(raw), &types) != nil || types == nil {
		// A malformed explicit setting must not expose an unintended payment entry.
		return []string{}
	}
	normalized, err := NormalizePaymentVisibleOrderTypes(types)
	if err != nil {
		return []string{}
	}
	return normalized
}

func encodePaymentVisibleOrderTypes(types *[]string) (string, error) {
	if types == nil {
		return "", nil
	}
	normalized, err := NormalizePaymentVisibleOrderTypes(*types)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	return string(encoded), err
}

// IsOrderTypeVisible preserves the two original entries for older callers that
// construct PaymentConfig without this setting, and honors an explicit empty list.
func (cfg *PaymentConfig) IsOrderTypeVisible(orderType string) bool {
	if cfg == nil {
		return false
	}
	types := cfg.VisibleOrderTypes
	if types == nil {
		types = defaultPaymentVisibleOrderTypes()
	}
	for _, visible := range types {
		if visible == orderType {
			return true
		}
	}
	return false
}
