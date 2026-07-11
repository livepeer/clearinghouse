package openmeter

// SessionProvision is the result of no-database OpenMeter provisioning for exchange.
type SessionProvision struct {
	Customer         *Customer
	CustomerKey      string
	HasAccess        bool
	BalanceUSDMicros int64
	BalanceSource    string
}

// ProvisionConfig controls default-plan subscription and trial grant provisioning.
type ProvisionConfig struct {
	DefaultPlanKey       string
	TrialFeatureKey      string
	TrialGrantUSDMicros  int64
	EnforceAllowance     bool
}
