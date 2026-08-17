// Package tenantauth binds an authenticated caller to the tenant it may act on.
//
// Before this package the admin surface authenticated against a single global
// M2M credential and then read the tenant from the request path, so any holder
// of the platform secret could address any tenant. Authorization here is always
// a comparison between the authenticated principal and the tenant in the path;
// callers must never derive a tenant from user input alone.
package tenantauth

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"strings"
)

// Kind distinguishes the platform operator from a single tenant.
type Kind int

const (
	// KindNone is an unauthenticated caller.
	KindNone Kind = iota
	// KindPlatformAdmin may act on any tenant.
	KindPlatformAdmin
	// KindTenant may act only on its own ClientID.
	KindTenant
)

// Principal is the authenticated caller.
type Principal struct {
	Kind     Kind
	ClientID string
}

// Authenticated reports whether the principal proved an identity.
func (p Principal) Authenticated() bool { return p.Kind != KindNone }

// CanAccess reports whether the principal may act on clientID.
//
// A tenant principal is confined to an exact match on its own client id. The
// empty client id never matches, so a zero Principal cannot reach anything.
func (p Principal) CanAccess(clientID string) bool {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return false
	}
	switch p.Kind {
	case KindPlatformAdmin:
		return true
	case KindTenant:
		return p.ClientID != "" && p.ClientID == clientID
	default:
		return false
	}
}

// Authenticator resolves client credentials to a Principal.
type Authenticator struct {
	platformClientID string
	platformSecret   string
	tenantSecrets    map[string]string
}

// New builds an Authenticator. Platform credentials are optional; when either
// half is empty the platform-admin path is disabled rather than matching on
// empty input. tenantSecrets maps client id to that tenant's admin secret.
func New(platformClientID, platformSecret string, tenantSecrets map[string]string) *Authenticator {
	cleaned := make(map[string]string, len(tenantSecrets))
	for clientID, secret := range tenantSecrets {
		clientID = strings.TrimSpace(clientID)
		secret = strings.TrimSpace(secret)
		if clientID == "" || secret == "" {
			continue
		}
		cleaned[clientID] = secret
	}
	return &Authenticator{
		platformClientID: strings.TrimSpace(platformClientID),
		platformSecret:   strings.TrimSpace(platformSecret),
		tenantSecrets:    cleaned,
	}
}

// Authenticate resolves credentials to a Principal, returning a zero Principal
// when they match nothing. Both branches use constant-time comparison.
func (a *Authenticator) Authenticate(clientID, secret string) Principal {
	clientID = strings.TrimSpace(clientID)
	secret = strings.TrimSpace(secret)
	if clientID == "" || secret == "" {
		return Principal{}
	}

	if a.platformClientID != "" && a.platformSecret != "" &&
		constantTimeEqual(clientID, a.platformClientID) &&
		constantTimeEqual(secret, a.platformSecret) {
		return Principal{Kind: KindPlatformAdmin}
	}

	if expected, ok := a.tenantSecrets[clientID]; ok && constantTimeEqual(secret, expected) {
		return Principal{Kind: KindTenant, ClientID: clientID}
	}

	return Principal{}
}

// ParseTenantSecrets reads the TENANT_ADMIN_KEYS JSON object mapping client id
// to admin secret. An empty string yields an empty map, which disables
// tenant-scoped admin credentials without disabling the platform principal.
func ParseTenantSecrets(raw string) (map[string]string, error) {
	out := map[string]string{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return out, nil
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("TENANT_ADMIN_KEYS must be a JSON object of clientId to secret: %w", err)
	}
	for clientID, secret := range parsed {
		clientID = strings.TrimSpace(clientID)
		secret = strings.TrimSpace(secret)
		if clientID == "" || secret == "" {
			continue
		}
		out[clientID] = secret
	}
	return out, nil
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
