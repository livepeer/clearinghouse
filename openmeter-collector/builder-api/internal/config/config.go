package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds runtime configuration for the Builder API.
type Config struct {
	Port                         string
	Auth0Domain                  string
	Auth0Issuer                  string
	Auth0Audience                string
	MgmtClientID                 string
	MgmtClientSecret             string
	SignerM2MClientID            string
	SignerM2MSecret              string
	DBConnection                 string
	OpenMeterURL                 string
	OpenMeterAPIKey              string
	OpenMeterDefaultPlanKey      string
	OpenMeterTrialFeatureKey     string
	OpenMeterTrialGrantUSDMicros int64
	OpenMeterEnforceAllowance    bool
	SignerURL                    string
	DiscoveryURL                 string
	APIKeyPrefix                 string
	DemoAPIKeys                  string
	// IdentityWebhookURL and WebhookSecret delegate end-user JWT verification to the
	// identity-webhook service (POST /authorize). Same URL as go-livepeer
	// -remoteSignerWebhookUrl (REMOTE_SIGNER_WEBHOOK_URL). JWT subject tokens require both.
	IdentityWebhookURL string
	WebhookSecret      string
}

// Load reads configuration from environment variables.
func Load() (Config, error) {
	cfg := Config{
		Port:                      envOr("BUILDER_API_PORT", "8095"),
		Auth0Domain:               firstEnv("AUTH0_DOMAIN"),
		Auth0Issuer:               firstEnv("AUTH0_ISSUER"),
		Auth0Audience:             envOr(firstEnv("AUTH0_AUDIENCE", "DEMO_APP_AUTH0_AUDIENCE"), "livepeer-clearinghouse"),
		MgmtClientID:              firstEnv("AUTH0_MGMT_CLIENT_ID"),
		MgmtClientSecret:          firstEnv("AUTH0_MGMT_CLIENT_SECRET"),
		SignerM2MClientID:         firstEnv("AUTH0_SIGNER_M2M_CLIENT_ID", "DEMO_APP_AUTH0_M2M_CLIENT_ID"),
		SignerM2MSecret:           firstEnv("AUTH0_SIGNER_M2M_CLIENT_SECRET", "DEMO_APP_AUTH0_M2M_CLIENT_SECRET"),
		DBConnection:              envOr("AUTH0_DB_CONNECTION", "Username-Password-Authentication"),
		OpenMeterURL:              envOr("OPENMETER_URL", "https://us.api.konghq.com/v3/openmeter"),
		OpenMeterAPIKey:           strings.TrimSpace(os.Getenv("OPENMETER_API_KEY")),
		OpenMeterDefaultPlanKey:   envOr("OPENMETER_DEFAULT_PLAN_KEY", "clearinghouse_default_ppu"),
		OpenMeterTrialFeatureKey:  envOr("OPENMETER_TRIAL_FEATURE_KEY", "network_spend"),
		OpenMeterEnforceAllowance: envBool("OPENMETER_ENFORCE_ALLOWANCE", true),
		SignerURL:                 strings.TrimSpace(os.Getenv("SIGNER_URL")),
		DiscoveryURL: envOr(
			"DISCOVERY_URL",
			"https://discovery-service-production-8955.up.railway.app/v1/discovery/raw?serviceType=legacy",
		),
		APIKeyPrefix:       envOr("API_KEY_PREFIX", "sk_"),
		DemoAPIKeys:        strings.TrimSpace(os.Getenv("DEMO_API_KEYS")),
		IdentityWebhookURL: strings.TrimSpace(os.Getenv("REMOTE_SIGNER_WEBHOOK_URL")),
		WebhookSecret:      strings.TrimSpace(os.Getenv("WEBHOOK_SECRET")),
	}

	if v := strings.TrimSpace(os.Getenv("OPENMETER_TRIAL_GRANT_USD_MICROS")); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return cfg, fmt.Errorf("OPENMETER_TRIAL_GRANT_USD_MICROS must be an integer: %w", err)
		}
		cfg.OpenMeterTrialGrantUSDMicros = n
	}

	if cfg.Auth0Issuer == "" && cfg.Auth0Domain != "" {
		cfg.Auth0Issuer = "https://" + strings.TrimSuffix(cfg.Auth0Domain, "/") + "/"
	}
	cfg.Auth0Issuer = strings.TrimSuffix(cfg.Auth0Issuer, "/")

	missing := make([]string, 0)
	if cfg.Auth0Domain == "" {
		missing = append(missing, "AUTH0_DOMAIN")
	}
	if cfg.MgmtClientID == "" {
		missing = append(missing, "AUTH0_MGMT_CLIENT_ID")
	}
	if cfg.MgmtClientSecret == "" {
		missing = append(missing, "AUTH0_MGMT_CLIENT_SECRET")
	}
	if cfg.SignerM2MClientID == "" {
		missing = append(missing, "AUTH0_SIGNER_M2M_CLIENT_ID")
	}
	if cfg.SignerM2MSecret == "" {
		missing = append(missing, "AUTH0_SIGNER_M2M_CLIENT_SECRET")
	}
	if cfg.OpenMeterAPIKey == "" {
		missing = append(missing, "OPENMETER_API_KEY")
	}
	if cfg.OpenMeterURL == "" {
		missing = append(missing, "OPENMETER_URL")
	}
	if len(missing) > 0 {
		return cfg, fmt.Errorf("missing required env: %s", strings.Join(missing, ", "))
	}

	if _, err := strconv.Atoi(cfg.Port); err != nil {
		return cfg, fmt.Errorf("BUILDER_API_PORT must be numeric: %w", err)
	}

	return cfg, nil
}

// UsageMeterKey returns the OpenMeter meter slug for user usage queries.
// It is derived from OPENMETER_TRIAL_FEATURE_KEY using the feature→meter
// mapping in catalog.json (network_spend → network_fee_usd_micros).
func (c Config) UsageMeterKey() string {
	switch strings.TrimSpace(c.OpenMeterTrialFeatureKey) {
	case "billable_spend":
		return "billable_usd_micros"
	default:
		return "network_fee_usd_micros"
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func firstEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}
