package main

import (
	"context"
	_ "embed"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/apikey"
	auth0mgmt "github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mgmt"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mint"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/httpapi"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tenantauth"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/webhookverify"
)

//go:embed openapi.json
var openAPISpec []byte

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	auth0Client, err := auth0mgmt.New(cfg.Auth0Domain, cfg.MgmtClientID, cfg.MgmtClientSecret, cfg.DBConnection)
	if err != nil {
		log.Fatalf("auth0: %v", err)
	}

	minter := auth0mint.New(cfg.Auth0Issuer, cfg.Auth0Audience, cfg.SignerM2MClientID, cfg.SignerM2MSecret)
	// One Kong OpenMeter organization, shared by all tenants. Isolation is by
	// customer key and is enforced in the admin API.
	omClient := openmeter.New(cfg.OpenMeterURL, cfg.OpenMeterAPIKey)
	session := openmeter.NewSessionService(omClient)

	// End-user JWT verification is delegated to the identity-webhook (POST /authorize).
	var verifier tokenexchange.UserTokenVerifier
	if cfg.IdentityWebhookURL != "" && cfg.WebhookSecret != "" {
		verifier = webhookverify.New(cfg.IdentityWebhookURL, cfg.WebhookSecret)
	} else {
		log.Printf("identity-webhook not configured; JWT subject tokens will be rejected (set REMOTE_SIGNER_WEBHOOK_URL + WEBHOOK_SECRET)")
	}

	demoKeys, err := apikey.LoadDemoStore(cfg.DemoAPIKeys)
	if err != nil {
		log.Fatalf("demo api keys: %v", err)
	}

	keyStore := &apikey.Store{
		Prefix: cfg.APIKeyPrefix,
		Demo:   demoKeys,
		Auth0:  auth0Client,
	}
	tokenHandler := tokenexchange.NewHandler(cfg, verifier, keyStore, minter, session)

	tenantSecrets, err := tenantauth.ParseTenantSecrets(cfg.TenantAdminKeys)
	if err != nil {
		log.Fatalf("tenant admin keys: %v", err)
	}
	if len(tenantSecrets) == 0 {
		log.Printf("no TENANT_ADMIN_KEYS configured; admin routes accept the platform M2M credential only")
	}
	tenantAuth := tenantauth.New(cfg.SignerM2MClientID, cfg.SignerM2MSecret, tenantSecrets)

	// Admin routes read the shared OpenMeter tenant with one platform
	// credential and enforce the per-tenant boundary themselves. Left nil when
	// unconfigured so the routes answer 503 instead of panicking.
	var adminAPI httpapi.OpenMeterAdmin
	if cfg.OpenMeterURL != "" && cfg.OpenMeterAPIKey != "" {
		adminAPI = openmeter.New(cfg.OpenMeterURL, cfg.OpenMeterAPIKey)
	} else {
		log.Printf("OPENMETER_URL/OPENMETER_API_KEY unset; admin usage routes disabled")
	}

	srv := httpapi.NewServer(cfg, auth0Client, minter, session, tokenHandler, verifier, openAPISpec, tenantAuth, adminAPI)
	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("builder-api listening on :%s", cfg.Port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
