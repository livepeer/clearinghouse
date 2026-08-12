// Package webhookverify delegates end-user JWT verification to the identity-webhook.
//
// The identity-webhook (Node) already verifies Auth0 access tokens against the
// issuer JWKS and returns a UsageIdentity. Rather than duplicate JWKS handling and
// claim extraction in Go, the Builder API forwards the subject token to the
// webhook's POST /authorize contract and reads back {client_id, usage_subject}.
package webhookverify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client verifies user access tokens via the identity-webhook /authorize endpoint.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

// New creates a webhook verification client. baseURL points at the identity-webhook
// service (e.g. http://identity-webhook:8090); secret is the shared WEBHOOK_SECRET.
// A trailing /authorize is tolerated so REMOTE_SIGNER_WEBHOOK_URL can be reused directly.
func New(baseURL, secret string) *Client {
	base := strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	base = strings.TrimSuffix(base, "/authorize")
	return &Client{
		baseURL: strings.TrimSuffix(base, "/"),
		secret:  strings.TrimSpace(secret),
		http: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

type authorizePayload struct {
	Headers map[string][]string `json:"headers"`
}

type authorizeResponse struct {
	Status   int    `json:"status"`
	Reason   string `json:"reason"`
	AuthID   string `json:"auth_id"`
	Identity struct {
		ClientID     string `json:"client_id"`
		UsageSubject string `json:"usage_subject"`
	} `json:"identity"`
}

// VerifyUserAccessToken forwards a user JWT to the identity-webhook and returns the
// resolved tenant client id and external user id. It enforces that the token's
// client id matches expectedClientID (the app's public Auth0 client id).
func (c *Client) VerifyUserAccessToken(ctx context.Context, token, expectedClientID string) (clientID, externalUserID string, err error) {
	body, err := json.Marshal(authorizePayload{
		Headers: map[string][]string{"Authorization": {"Bearer " + token}},
	})
	if err != nil {
		return "", "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/authorize", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("identity-webhook request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("identity-webhook %d: %s", resp.StatusCode, string(raw))
	}

	var parsed authorizeResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", "", fmt.Errorf("decode identity-webhook response: %w", err)
	}
	// Rejects ride back on HTTP 200 with the real status in the body.
	if parsed.Status != http.StatusOK {
		reason := parsed.Reason
		if reason == "" {
			reason = "verification rejected"
		}
		return "", "", fmt.Errorf("identity-webhook rejected token: %s", reason)
	}
	if parsed.Identity.ClientID == "" || parsed.Identity.UsageSubject == "" {
		return "", "", fmt.Errorf("identity-webhook returned incomplete identity")
	}

	expectedClientID = strings.TrimSpace(expectedClientID)
	if expectedClientID != "" && parsed.Identity.ClientID != expectedClientID {
		return "", "", fmt.Errorf("token client does not match clientId")
	}
	return parsed.Identity.ClientID, parsed.Identity.UsageSubject, nil
}
