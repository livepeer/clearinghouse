package apikey

import (
	"context"
	"errors"
	"testing"
)

type stubAuth0 struct {
	clientID string
	userID   string
	err      error
}

func (s stubAuth0) ResolveAPIKeyUser(_ context.Context, _, _, _ string) (string, string, error) {
	if s.err != nil {
		return "", "", s.err
	}
	return s.clientID, s.userID, nil
}

func TestStoreResolveDemoKey(t *testing.T) {
	t.Parallel()

	store := &Store{
		Prefix: "sk_",
		Demo: map[string]DemoEntry{
			"sk_demo": {
				ClientID: "demo-client",
				UserID:   "demo-user",
			},
		},
	}

	clientID, userID, err := store.Resolve(context.Background(), "sk_demo", "demo-client")
	if err != nil {
		t.Fatal(err)
	}
	if clientID != "demo-client" || userID != "demo-user" {
		t.Fatalf("got %q %q", clientID, userID)
	}
}

func TestStoreResolveClientMismatch(t *testing.T) {
	t.Parallel()

	store := &Store{
		Prefix: "sk_",
		Demo: map[string]DemoEntry{
			"sk_demo": {ClientID: "demo-client", UserID: "demo-user"},
		},
	}
	_, _, err := store.Resolve(context.Background(), "sk_demo", "other-client")
	if !errors.Is(err, ErrClientMismatch) {
		t.Fatalf("expected ErrClientMismatch, got %v", err)
	}
}

func TestStoreResolveAuth0(t *testing.T) {
	t.Parallel()

	store := &Store{
		Prefix: "sk_",
		Auth0: stubAuth0{
			clientID: "app-1",
			userID:   "auth0|u1",
		},
	}
	clientID, userID, err := store.Resolve(context.Background(), "sk_live", "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if clientID != "app-1" || userID != "auth0|u1" {
		t.Fatalf("got %q %q", clientID, userID)
	}
}
