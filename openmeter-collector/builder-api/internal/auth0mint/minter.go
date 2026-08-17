package auth0mint

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const signMintUserTokenScope = "sign:mint_user_token"

// TokenResponse is the Auth0 token endpoint response for signer mint.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Scope       string `json:"scope"`
}

// Minter mints short-lived signer JWTs via Auth0 client_credentials.
type Minter struct {
	issuerURL string
	audience  string
	clientID  string
	secret    string
	http      *http.Client
}

// New creates a signer-token minter.
func New(issuerURL, audience, clientID, secret string) *Minter {
	return &Minter{
		issuerURL: strings.TrimSuffix(issuerURL, "/"),
		audience:  audience,
		clientID:  clientID,
		secret:    secret,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// MintSignerToken requests a signer JWT for an app end-user.
// publicClientID is the integrator's public Auth0 client id (path param to Builder API).
func (m *Minter) MintSignerToken(ctx context.Context, publicClientID, externalUserID string) (*TokenResponse, error) {
	tokenURL := m.issuerURL + "/oauth/token"
	form := url.Values{
		"grant_type":       {"client_credentials"},
		"scope":            {signMintUserTokenScope},
		"external_user_id": {externalUserID},
		"client_id":        {publicClientID},
		"audience":         {m.audience},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(m.clientID, m.secret)

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth0 token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("auth0 token endpoint %d: %s", resp.StatusCode, string(body))
	}

	var parsed TokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if parsed.AccessToken == "" {
		return nil, fmt.Errorf("token response missing access_token")
	}
	if parsed.ExpiresIn <= 0 {
		parsed.ExpiresIn = 300
	}
	if parsed.TokenType == "" {
		parsed.TokenType = "Bearer"
	}
	if parsed.Scope == "" {
		parsed.Scope = "sign:job"
	}
	return &parsed, nil
}
