package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/apikey"
	auth0mint "github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mint"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/httpapi"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

type stubMinter struct{}

func (stubMinter) MintSignerToken(context.Context, string, string) (*auth0mint.TokenResponse, error) {
	return &auth0mint.TokenResponse{
		AccessToken: "minted",
		TokenType:   "Bearer",
		ExpiresIn:   300,
		Scope:       "sign:job",
	}, nil
}

type stubProvisioner struct {
	provision *openmeter.SessionProvision
	err       error
}

func (s stubProvisioner) ProvisionSession(context.Context, openmeter.ProvisionConfig, string, string) (*openmeter.SessionProvision, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.provision != nil {
		return s.provision, nil
	}
	return &openmeter.SessionProvision{
		Customer:    &openmeter.Customer{},
		CustomerKey: "pub-client:demo-user",
	}, nil
}

func newOIDCTokenServer(t *testing.T) http.Handler {
	t.Helper()
	cfg := config.Config{
		Auth0Audience:     "livepeer-clearinghouse",
		SignerM2MClientID: "m2m-client",
		SignerM2MSecret:   "m2m-secret",
		APIKeyPrefix:      "sk_",
	}
	handler := tokenexchange.NewHandler(
		cfg,
		nil,
		&apikey.Store{
			Prefix: "sk_",
			Demo: map[string]apikey.DemoEntry{
				"sk_demo": {ClientID: "pub-client", UserID: "demo-user"},
			},
		},
		stubMinter{},
		stubProvisioner{},
	)
	return httpapi.NewServer(cfg, nil, nil, nil, handler, nil, nil, nil).Handler()
}

func postOIDCToken(t *testing.T, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oidc/token", strings.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	newOIDCTokenServer(t).ServeHTTP(rec, req)
	return rec
}

func TestHandleOIDCTokenRejectsUnsupportedGrantType(t *testing.T) {
	t.Parallel()

	body := "grant_type=client_credentials&subject_token=sk_demo&subject_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Aaccess_token"
	rec := postOIDCToken(t, "application/x-www-form-urlencoded", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid_request") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("expected Cache-Control: no-store")
	}
}

func TestHandleOIDCTokenAPIKeyExchange(t *testing.T) {
	t.Parallel()

	body := strings.Join([]string{
		"grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange",
		"subject_token=sk_demo",
		"subject_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Aaccess_token",
		"audience=livepeer-clearinghouse",
	}, "&")
	rec := postOIDCToken(t, "application/x-www-form-urlencoded", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"access_token":"minted"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"issued_token_type":"urn:ietf:params:oauth:token-type:access_token"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleOIDCTokenAPIKeyExchangeJSON(t *testing.T) {
	t.Parallel()

	body := `{
		"grant_type":"urn:ietf:params:oauth:grant-type:token-exchange",
		"subject_token":"sk_demo",
		"subject_token_type":"urn:ietf:params:oauth:token-type:access_token",
		"audience":"livepeer-clearinghouse"
	}`
	rec := postOIDCToken(t, "application/json", body)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"access_token":"minted"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

func TestHandleOIDCTokenRejectsInvalidJSONAudience(t *testing.T) {
	t.Parallel()

	body := `{
		"grant_type":"urn:ietf:params:oauth:grant-type:token-exchange",
		"subject_token":"sk_demo",
		"subject_token_type":"urn:ietf:params:oauth:token-type:access_token",
		"audience":1
	}`
	rec := postOIDCToken(t, "application/json", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "audience must be a string or array of strings") {
		t.Fatalf("body = %s", rec.Body.String())
	}
}
