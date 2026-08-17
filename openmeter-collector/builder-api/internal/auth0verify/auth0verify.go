// Package auth0verify verifies Auth0 end-user access tokens in-process via JWKS.
//
// Used when REMOTE_SIGNER_WEBHOOK_URL is unset. When the webhook URL is set,
// builder-api still delegates to identity-webhook (Node-compatible /authorize).
package auth0verify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

const clockSkew = 60 * time.Second

// Verifier validates Auth0 access tokens against the configured issuer JWKS.
type Verifier struct {
	issuer   string // normalized, no trailing slash
	audience string
	jwksURI  string
	http     *http.Client

	mu     sync.Mutex
	cache  *jwk.Cache
	keySet jwk.Set // optional injected set for tests; skips remote fetch
}

// New builds a verifier for issuer (Auth0 domain URL) and API audience.
// issuer may include a trailing slash; both forms are accepted at verify time.
func New(issuer, audience string) *Verifier {
	return &Verifier{
		issuer:   normalizeIssuer(issuer),
		audience: strings.TrimSpace(audience),
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// NewWithKeySet is for unit tests: verify against an injected JWKS, no HTTP.
func NewWithKeySet(issuer, audience string, set jwk.Set) *Verifier {
	v := New(issuer, audience)
	v.keySet = set
	return v
}

// VerifyUserAccessToken validates the JWT and returns (clientID, externalUserID).
// Client is azp, falling back to app_client_id. Subject is sub, falling back to
// external_user_id. When expectedClientID is non-empty it must match.
func (v *Verifier) VerifyUserAccessToken(ctx context.Context, token, expectedClientID string) (clientID, externalUserID string, err error) {
	if v.issuer == "" || v.audience == "" {
		return "", "", fmt.Errorf("auth0verify: issuer and audience are required")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", "", fmt.Errorf("auth0verify: empty token")
	}

	keySet, err := v.resolveKeySet(ctx)
	if err != nil {
		return "", "", err
	}

	parsed, err := jwt.Parse(
		[]byte(token),
		jwt.WithKeySet(keySet),
		jwt.WithValidate(true),
		jwt.WithAudience(v.audience),
		jwt.WithAcceptableSkew(clockSkew),
	)
	if err != nil {
		return "", "", fmt.Errorf("auth0verify: token invalid: %w", err)
	}

	iss := strings.TrimSpace(parsed.Issuer())
	if !issuerMatches(v.issuer, iss) {
		return "", "", fmt.Errorf("auth0verify: issuer mismatch")
	}

	clientID = claimString(parsed, "azp")
	if clientID == "" {
		clientID = claimString(parsed, "app_client_id")
	}
	externalUserID = strings.TrimSpace(parsed.Subject())
	if externalUserID == "" {
		externalUserID = claimString(parsed, "external_user_id")
	}
	if clientID == "" || externalUserID == "" {
		return "", "", fmt.Errorf("auth0verify: token missing client or subject claims")
	}

	expectedClientID = strings.TrimSpace(expectedClientID)
	if expectedClientID != "" && clientID != expectedClientID {
		return "", "", fmt.Errorf("token client does not match clientId")
	}
	return clientID, externalUserID, nil
}

func (v *Verifier) resolveKeySet(ctx context.Context) (jwk.Set, error) {
	if v.keySet != nil {
		return v.keySet, nil
	}

	v.mu.Lock()
	defer v.mu.Unlock()

	if v.jwksURI == "" {
		uri, err := discoverJWKSURI(ctx, v.http, v.issuer)
		if err != nil {
			return nil, err
		}
		v.jwksURI = uri
	}

	if v.cache == nil {
		cache := jwk.NewCache(ctx)
		if err := cache.Register(v.jwksURI); err != nil {
			return nil, fmt.Errorf("auth0verify: register jwks: %w", err)
		}
		v.cache = cache
	}

	set, err := v.cache.Get(ctx, v.jwksURI)
	if err != nil {
		return nil, fmt.Errorf("auth0verify: fetch jwks: %w", err)
	}
	return set, nil
}

type openIDConfig struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

func discoverJWKSURI(ctx context.Context, client *http.Client, issuer string) (string, error) {
	url := issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("auth0verify: discovery request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("auth0verify: discovery HTTP %d", resp.StatusCode)
	}
	var doc openIDConfig
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", fmt.Errorf("auth0verify: discovery decode: %w", err)
	}
	if doc.JWKSURI == "" {
		return "", fmt.Errorf("auth0verify: discovery missing jwks_uri")
	}
	if doc.Issuer != "" && !issuerMatches(issuer, doc.Issuer) {
		return "", fmt.Errorf("auth0verify: discovery issuer mismatch")
	}
	return strings.TrimSpace(doc.JWKSURI), nil
}

func normalizeIssuer(issuer string) string {
	return strings.TrimSuffix(strings.TrimSpace(issuer), "/")
}

func issuerMatches(expected, got string) bool {
	return normalizeIssuer(expected) == normalizeIssuer(got)
}

func claimString(tok jwt.Token, name string) string {
	raw, ok := tok.Get(name)
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}
