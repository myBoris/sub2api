package service

import (
	"context"
	"reflect"
	"testing"
)

func TestPaymentVisibleOrderTypesDefaultsAndExplicitSelection(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{name: "legacy defaults", want: []string{"balance", "subscription"}},
		{name: "account only", raw: `["account"]`, want: []string{"account"}},
		{name: "explicit empty", raw: `[]`, want: []string{}},
		{name: "deduplicate", raw: `["balance","account","balance"]`, want: []string{"balance", "account"}},
		{name: "invalid setting hides entries", raw: `["unknown"]`, want: []string{}},
		{name: "malformed setting hides entries", raw: `{}`, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := (&PaymentConfigService{}).parsePaymentConfig(map[string]string{SettingPaymentVisibleOrderTypes: tc.raw})
			if !reflect.DeepEqual(cfg.VisibleOrderTypes, tc.want) {
				t.Fatalf("visible order types = %v, want %v", cfg.VisibleOrderTypes, tc.want)
			}
			for _, orderType := range []string{"balance", "subscription", "account", "unknown"} {
				wantVisible := false
				for _, visible := range tc.want {
					wantVisible = wantVisible || visible == orderType
				}
				if got := cfg.IsOrderTypeVisible(orderType); got != wantVisible {
					t.Fatalf("IsOrderTypeVisible(%q) = %v, want %v", orderType, got, wantVisible)
				}
			}
		})
	}
}

func TestPaymentVisibleOrderTypesConfigRoundTripAndOmission(t *testing.T) {
	repo := &paymentConfigSettingRepoStub{}
	svc := &PaymentConfigService{settingRepo: repo}
	for _, types := range [][]string{{"balance", "subscription", "account"}, {}, {"account"}} {
		if err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{VisibleOrderTypes: &types}); err != nil {
			t.Fatal(err)
		}
		cfg, err := svc.GetPaymentConfig(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cfg.VisibleOrderTypes, types) {
			t.Fatalf("round trip = %v, want %v", cfg.VisibleOrderTypes, types)
		}
	}
	enabled := true
	if err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	if _, written := repo.updates[SettingPaymentVisibleOrderTypes]; written {
		t.Fatal("omitted visible order types were overwritten")
	}
	if repo.values[SettingPaymentVisibleOrderTypes] != `["account"]` {
		t.Fatal("omitted visible order types were not preserved")
	}
}

func TestPaymentVisibleOrderTypesInvalidUpdateDoesNotWrite(t *testing.T) {
	repo := &paymentConfigSettingRepoStub{}
	svc := &PaymentConfigService{settingRepo: repo}
	types := []string{"account", "unknown"}
	enabled := true
	if err := svc.UpdatePaymentConfig(context.Background(), UpdatePaymentConfigRequest{Enabled: &enabled, VisibleOrderTypes: &types}); err == nil {
		t.Fatal("invalid order type was accepted")
	}
	if repo.updates != nil {
		t.Fatalf("invalid update wrote settings: %v", repo.updates)
	}
}
