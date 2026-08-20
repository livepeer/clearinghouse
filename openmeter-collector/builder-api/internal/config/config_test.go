package config

import "testing"

func TestUsageMeterKeyFromFeature(t *testing.T) {
	tests := []struct {
		feature string
		want    string
	}{
		{"network_spend", "network_fee_usd_micros"},
		{"billable_spend", "billable_usd_micros"},
		{"", "network_fee_usd_micros"},
	}
	for _, tc := range tests {
		cfg := Config{OpenMeterTrialFeatureKey: tc.feature}
		if got := cfg.UsageMeterKey(); got != tc.want {
			t.Fatalf("feature %q: got %q, want %q", tc.feature, got, tc.want)
		}
	}
}
