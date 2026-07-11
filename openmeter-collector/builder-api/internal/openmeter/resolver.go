package openmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TenantCreds are OpenMeter management credentials for one platform client_id.
type TenantCreds struct {
	BaseURL string
	Token   string
	Region  string
	OrgID   string
}

// Resolver loads per-tenant OpenMeter credentials from konnect-credentials,
// falling back to a process-wide URL/key for single-org local stacks.
type Resolver struct {
	CredentialsURL string
	PlatformSecret string
	FallbackURL    string
	FallbackToken  string
	HTTP           *http.Client
}

// NewResolver constructs a credential resolver.
func NewResolver(credentialsURL, platformSecret, fallbackURL, fallbackToken string) *Resolver {
	return &Resolver{
		CredentialsURL: strings.TrimRight(strings.TrimSpace(credentialsURL), "/"),
		PlatformSecret: strings.TrimSpace(platformSecret),
		FallbackURL:    strings.TrimRight(strings.TrimSpace(fallbackURL), "/"),
		FallbackToken:  strings.TrimSpace(fallbackToken),
		HTTP: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// Resolve returns OpenMeter base URL + token for clientID.
func (r *Resolver) Resolve(ctx context.Context, clientID string) (*TenantCreds, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, fmt.Errorf("client id is required")
	}

	if r.CredentialsURL != "" && r.PlatformSecret != "" {
		creds, err := r.lookup(ctx, clientID)
		if err == nil {
			return creds, nil
		}
		// Unbound tenant: fall through to global fallback when configured.
		if !isNotFound(err) {
			return nil, err
		}
	}

	if r.FallbackURL == "" || r.FallbackToken == "" {
		return nil, fmt.Errorf("openmeter credentials unavailable for client %q (bind tenant or set OPENMETER_URL/OPENMETER_API_KEY)", clientID)
	}
	return &TenantCreds{
		BaseURL: r.FallbackURL,
		Token:   r.FallbackToken,
	}, nil
}

type openmeterLookupResponse struct {
	ClientID      string `json:"client_id"`
	Region        string `json:"region"`
	OrgID         string `json:"org_id"`
	OpenMeterBase string `json:"openmeter_base"`
	Token         string `json:"token"`
	Error         string `json:"error"`
}

func (r *Resolver) lookup(ctx context.Context, clientID string) (*TenantCreds, error) {
	url := fmt.Sprintf("%s/v1/internal/tenants/%s/openmeter", r.CredentialsURL, urlPathEscape(clientID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+r.PlatformSecret)
	req.Header.Set("Accept", "application/json")

	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("konnect-credentials lookup: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("konnect-credentials lookup %d: %s", resp.StatusCode, string(body))
	}

	var parsed openmeterLookupResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	base := strings.TrimRight(strings.TrimSpace(parsed.OpenMeterBase), "/")
	token := strings.TrimSpace(parsed.Token)
	if base == "" || token == "" {
		return nil, fmt.Errorf("konnect-credentials lookup returned empty openmeter_base/token")
	}
	return &TenantCreds{
		BaseURL: base,
		Token:   token,
		Region:  parsed.Region,
		OrgID:   parsed.OrgID,
	}, nil
}

var errNotFound = fmt.Errorf("tenant not bound")

func isNotFound(err error) bool {
	return err == errNotFound
}

func urlPathEscape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "/", "%2F"), " ", "%20")
}

// ClientFor builds an OpenMeter client from resolved credentials.
func (r *Resolver) ClientFor(ctx context.Context, clientID string) (*Client, error) {
	creds, err := r.Resolve(ctx, clientID)
	if err != nil {
		return nil, err
	}
	return New(creds.BaseURL, creds.Token), nil
}
