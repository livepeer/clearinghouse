package tokenexchange_test

import (
	"context"
	"errors"
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/apikey"
	auth0mint "github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mint"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

type stubMinter struct {
	response *auth0mint.TokenResponse
	err      error
}

func (s stubMinter) MintSignerToken(_ context.Context, _, _ string) (*auth0mint.TokenResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

func (s *stubProvisioner) ProvisionSession(context.Context, openmeter.ProvisionConfig, string, string) (*openmeter.SessionProvision, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	hasAccess := true
	balance := int64(1_000_000)
	if s.hasAccess != nil {
		hasAccess = *s.hasAccess
	}
	if s.balance != nil {
		balance = *s.balance
	}
	return &openmeter.SessionProvision{
		Customer:         &openmeter.Customer{ID: "cust-1", Key: "pub-client:demo-user"},
		CustomerKey:      "pub-client:demo-user",
		HasAccess:        hasAccess,
		BalanceUSDMicros: balance,
		BalanceSource:    "credits",
	}, nil
}

type stubProvisioner struct {
	err       error
	calls     int
	hasAccess *bool
	balance   *int64
}

// stubVerifier stands in for the identity-webhook client.
type stubVerifier struct {
	clientID       string
	externalUserID string
	err            error
}

func (s stubVerifier) VerifyUserAccessToken(_ context.Context, _, expectedClientID string) (string, string, error) {
	if s.err != nil {
		return "", "", s.err
	}
	if expectedClientID != "" && s.clientID != expectedClientID {
		return "", "", errors.New("token client does not match clientId")
	}
	return s.clientID, s.externalUserID, nil
}

func testHandler(t *testing.T, verifier tokenexchange.UserTokenVerifier) *tokenexchange.Handler {
	t.Helper()
	return testHandlerWith(t, verifier, &stubProvisioner{}, config.Config{
		Auth0Audience:             "livepeer-clearinghouse",
		SignerM2MClientID:         "m2m-client",
		SignerM2MSecret:           "m2m-secret",
		APIKeyPrefix:              "sk_",
		SignerURL:                 "http://localhost:8081",
		DiscoveryURL:              "http://localhost/discovery",
		OpenMeterEnforceAllowance: true,
	})
}

func testHandlerWith(t *testing.T, verifier tokenexchange.UserTokenVerifier, provisioner tokenexchange.SessionProvisioner, cfg config.Config) *tokenexchange.Handler {
	t.Helper()
	if cfg.APIKeyPrefix == "" {
		cfg.APIKeyPrefix = "sk_"
	}
	return tokenexchange.NewHandler(
		cfg,
		verifier,
		&apikey.Store{
			Prefix: cfg.APIKeyPrefix,
			Demo: map[string]apikey.DemoEntry{
				"sk_demo": {ClientID: "pub-client", UserID: "demo-user"},
			},
		},
		stubMinter{response: &auth0mint.TokenResponse{
			AccessToken: "minted-jwt",
			TokenType:   "Bearer",
			ExpiresIn:   300,
			Scope:       "sign:job",
		}},
		provisioner,
	)
}

func TestExchangeDerivesPublicClientIDWhenOmitted(t *testing.T) {
	t.Parallel()
	h := testHandler(t, nil)
	result, err := h.Exchange(context.Background(), tokenexchange.Request{
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "sk_demo",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
	}, "corr")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if result.AccessToken != "minted-jwt" {
		t.Fatalf("access_token = %q", result.AccessToken)
	}
}

func TestExchangeRejectsInvalidGrantType(t *testing.T) {
	t.Parallel()
	h := testHandler(t, nil)
	_, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		ClientID:         "m2m-client",
		ClientSecret:     "m2m-secret",
		GrantType:        "client_credentials",
		SubjectToken:     "token",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
	}, "corr")
	if err == nil || err.(*tokenexchange.Error).Code != "invalid_request" {
		t.Fatalf("expected invalid_request, got %v", err)
	}
}

func TestExchangeRejectsInvalidClient(t *testing.T) {
	t.Parallel()
	h := testHandler(t, nil)
	_, err := h.Exchange(context.Background(), tokenexchange.Request{
		ClientID:         "wrong",
		ClientSecret:     "secret",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "sk_demo",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
	}, "corr")
	if err == nil || err.(*tokenexchange.Error).Code != "invalid_client" {
		t.Fatalf("expected invalid_client, got %v", err)
	}
}

func TestExchangeRejectsUnsupportedSubjectTokenType(t *testing.T) {
	t.Parallel()
	h := testHandler(t, nil)
	_, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		ClientID:         "m2m-client",
		ClientSecret:     "m2m-secret",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "token",
		SubjectTokenType: "urn:ietf:params:oauth:token-type:id_token",
	}, "corr")
	if err == nil || err.(*tokenexchange.Error).Code != "unsupported_token_type" {
		t.Fatalf("expected unsupported_token_type, got %v", err)
	}
}

func TestExchangeRejectsInvalidTargetAudience(t *testing.T) {
	t.Parallel()
	h := testHandler(t, nil)
	_, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		ClientID:         "m2m-client",
		ClientSecret:     "m2m-secret",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "sk_demo",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
		Audiences:        []string{"other-audience"},
	}, "corr")
	if err == nil || err.(*tokenexchange.Error).Code != "invalid_target" {
		t.Fatalf("expected invalid_target, got %v", err)
	}
}

func TestExchangeAPIKeyHappyPathWithoutM2M(t *testing.T) {
	t.Parallel()
	h := testHandler(t, nil)
	result, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "sk_demo",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
		Audiences:        []string{"livepeer-clearinghouse"},
	}, "corr-0")
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessToken != "minted-jwt" {
		t.Fatalf("access_token = %q", result.AccessToken)
	}
}

func TestExchangeRejectsClientMismatch(t *testing.T) {
	t.Parallel()
	h := testHandler(t, nil)
	_, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "other-client",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "sk_demo",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
	}, "corr")
	if err == nil || err.(*tokenexchange.Error).Code != "invalid_grant" {
		t.Fatalf("expected invalid_grant, got %v", err)
	}
}

func TestExchangeRepeatMintReusesProvisioner(t *testing.T) {
	t.Parallel()
	provisioner := &stubProvisioner{}
	h := tokenexchange.NewHandler(
		config.Config{
			Auth0Audience:     "livepeer-clearinghouse",
			SignerM2MClientID: "m2m-client",
			SignerM2MSecret:   "m2m-secret",
			APIKeyPrefix:      "sk_",
		},
		nil,
		&apikey.Store{
			Prefix: "sk_",
			Demo: map[string]apikey.DemoEntry{
				"sk_demo": {ClientID: "pub-client", UserID: "demo-user"},
			},
		},
		stubMinter{response: &auth0mint.TokenResponse{AccessToken: "minted-jwt", ExpiresIn: 300, Scope: "sign:job"}},
		provisioner,
	)
	for i := 0; i < 2; i++ {
		result, err := h.Exchange(context.Background(), tokenexchange.Request{
			PublicClientID:   "pub-client",
			GrantType:        tokenexchange.GrantType,
			SubjectToken:     "sk_demo",
			SubjectTokenType: tokenexchange.SubjectAccessTokenType,
		}, "corr")
		if err != nil {
			t.Fatal(err)
		}
		if result.AccessToken != "minted-jwt" {
			t.Fatalf("access_token = %q", result.AccessToken)
		}
	}
	if provisioner.calls != 2 {
		t.Fatalf("provision calls = %d", provisioner.calls)
	}
}

func TestExchangeAPIKeyHappyPath(t *testing.T) {
	t.Parallel()
	h := testHandler(t, nil)
	result, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		ClientID:         "m2m-client",
		ClientSecret:     "m2m-secret",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "sk_demo",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
		Audiences:        []string{"livepeer-clearinghouse"},
	}, "corr-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessToken != "minted-jwt" {
		t.Fatalf("access_token = %q", result.AccessToken)
	}
	if result.Scope != "sign:job" {
		t.Fatalf("scope = %q", result.Scope)
	}
	if result.IssuedTokenType != tokenexchange.IssuedAccessTokenType {
		t.Fatalf("issued_token_type = %q", result.IssuedTokenType)
	}
	if !result.HasAccess || result.BalanceUSDMicros != 1_000_000 {
		t.Fatalf("balance fields = has_access=%v balance=%d", result.HasAccess, result.BalanceUSDMicros)
	}
}

func TestExchangeBlocksWhenAllowanceExhausted(t *testing.T) {
	t.Parallel()
	falseVal := false
	zero := int64(0)
	provisioner := &stubProvisioner{hasAccess: &falseVal, balance: &zero}
	minterCalls := 0
	h := tokenexchange.NewHandler(
		config.Config{
			Auth0Audience:             "livepeer-clearinghouse",
			SignerM2MClientID:         "m2m-client",
			SignerM2MSecret:           "m2m-secret",
			APIKeyPrefix:              "sk_",
			OpenMeterEnforceAllowance: true,
		},
		nil,
		&apikey.Store{
			Prefix: "sk_",
			Demo: map[string]apikey.DemoEntry{
				"sk_demo": {ClientID: "pub-client", UserID: "demo-user"},
			},
		},
		countingMinter{fn: func() { minterCalls++ }, response: &auth0mint.TokenResponse{AccessToken: "x", ExpiresIn: 1}},
		provisioner,
	)
	_, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "sk_demo",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
	}, "corr")
	if err == nil || err.(*tokenexchange.Error).Code != "insufficient_allowance" {
		t.Fatalf("expected insufficient_allowance, got %v", err)
	}
	if err.(*tokenexchange.Error).Status != 402 {
		t.Fatalf("expected status 402, got %d", err.(*tokenexchange.Error).Status)
	}
	if minterCalls != 0 {
		t.Fatalf("minter should not be called, got %d", minterCalls)
	}
	if provisioner.calls != 1 {
		t.Fatalf("provisioner calls = %d", provisioner.calls)
	}
}

func TestExchangeSkipsGateWhenEnforceDisabled(t *testing.T) {
	t.Parallel()
	falseVal := false
	provisioner := &stubProvisioner{hasAccess: &falseVal}
	h := testHandlerWith(t, nil, provisioner, config.Config{
		Auth0Audience:             "livepeer-clearinghouse",
		SignerM2MClientID:         "m2m-client",
		SignerM2MSecret:           "m2m-secret",
		APIKeyPrefix:              "sk_",
		OpenMeterEnforceAllowance: false,
	})
	result, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "sk_demo",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
	}, "corr")
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessToken != "minted-jwt" {
		t.Fatalf("access_token = %q", result.AccessToken)
	}
}

type countingMinter struct {
	fn       func()
	response *auth0mint.TokenResponse
}

func (c countingMinter) MintSignerToken(context.Context, string, string) (*auth0mint.TokenResponse, error) {
	if c.fn != nil {
		c.fn()
	}
	return c.response, nil
}

func TestExchangeJWTHappyPath(t *testing.T) {
	t.Parallel()
	verifier := stubVerifier{clientID: "pub-client", externalUserID: "user-1"}
	h := testHandler(t, verifier)
	result, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		ClientID:         "m2m-client",
		ClientSecret:     "m2m-secret",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "header.payload.signature",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
		Resource:         "livepeer-clearinghouse",
	}, "corr-2")
	if err != nil {
		t.Fatal(err)
	}
	if result.AccessToken != "minted-jwt" {
		t.Fatalf("access_token = %q", result.AccessToken)
	}
}

func TestExchangeJWTRejectedByVerifier(t *testing.T) {
	t.Parallel()
	verifier := stubVerifier{err: errors.New("invalid token")}
	h := testHandler(t, verifier)
	_, err := h.Exchange(context.Background(), tokenexchange.Request{
		PublicClientID:   "pub-client",
		GrantType:        tokenexchange.GrantType,
		SubjectToken:     "header.payload.signature",
		SubjectTokenType: tokenexchange.SubjectAccessTokenType,
	}, "corr")
	if err == nil || err.(*tokenexchange.Error).Code != "invalid_grant" {
		t.Fatalf("expected invalid_grant, got %v", err)
	}
}
