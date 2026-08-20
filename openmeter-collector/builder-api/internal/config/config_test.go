package config

import "testing"

func TestUsageMeterKeyExplicit(t *testing.T) {
	cfg := Config{OpenMeterUsageMeterKey: "signed_ticket_count"}
	if got := cfg.UsageMeterKey(); got != "signed_ticket_count" {
		t.Fatalf("got %q, want signed_ticket_count", got)
	}
}

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
