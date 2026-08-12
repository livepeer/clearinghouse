package apikey

import (
	"context"
	"errors"
)

// ErrClientMismatch is returned when an API key belongs to a different client.
var ErrClientMismatch = errors.New("api key client mismatch")

// Auth0Resolver validates API keys stored in Auth0 app_metadata.
type Auth0Resolver interface {
	ResolveAPIKeyUser(ctx context.Context, apiKey, keyPrefix, expectedClientID string) (clientID, externalUserID string, err error)
}

// Store resolves bearer API keys from demo env mappings or Auth0.
type Store struct {
	Prefix string
	Demo   map[string]DemoEntry
	Auth0  Auth0Resolver
}

// Resolve returns client and external user ids for a plaintext API key.
func (s *Store) Resolve(ctx context.Context, token, expectedClientID string) (clientID, externalUserID string, err error) {
	if entry, ok := s.Demo[token]; ok {
		if expectedClientID != "" && entry.ClientID != expectedClientID {
			return "", "", ErrClientMismatch
		}
		return entry.ClientID, entry.UserID, nil
	}
	if s.Auth0 == nil {
		return "", "", errors.New("invalid api key")
	}
	return s.Auth0.ResolveAPIKeyUser(ctx, token, s.Prefix, expectedClientID)
}
