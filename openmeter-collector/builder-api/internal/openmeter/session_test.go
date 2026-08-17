package openmeter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSessionServiceRequiresAClient(t *testing.T) {
	var nilService *SessionService
	if _, err := nilService.ProvisionSession(context.Background(), ProvisionConfig{}, "acme", "alice"); err == nil {
		t.Error("a nil service must not provision")
	}
	if _, err := (&SessionService{}).ProvisionSession(context.Background(), ProvisionConfig{}, "acme", "alice"); err == nil {
		t.Error("a service with no client must not provision")
	}
}

// TestSessionServiceUsesTheSharedOrganization pins the architecture: every
// tenant is served by the one configured OpenMeter organization, and tenants
// are separated by customer key rather than by separate credentials.
func TestSessionServiceUsesTheSharedOrganization(t *testing.T) {
	var seenAuth []string
	var createdKeys []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = append(seenAuth, r.Header.Get("Authorization"))
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/customers"):
			// No existing customer, so EnsureCustomer creates one.
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/customers":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			key, _ := body["key"].(string)
			createdKeys = append(createdKeys, key)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "cus_" + key, "key": key})
		default:
			// Subscriptions, grants and balance are not under test here.
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	svc := NewSessionService(New(srv.URL, "kpat_shared"))
	for _, tenant := range []struct{ clientID, user string }{
		{"tenant-a", "alice"},
		{"tenant-b", "bob"},
	} {
		// Downstream calls 404 against this stub; the customer step is what
		// this test asserts on.
		_, _ = svc.ProvisionSession(context.Background(), ProvisionConfig{}, tenant.clientID, tenant.user)
	}

	if len(createdKeys) != 2 ||
		createdKeys[0] != "tenant-a:alice" || createdKeys[1] != "tenant-b:bob" {
		t.Fatalf("customer keys = %v, want compound keys per tenant", createdKeys)
	}
	for _, auth := range seenAuth {
		if auth != "Bearer kpat_shared" {
			t.Fatalf("every tenant must use the shared org credential, saw %q", auth)
		}
	}
}
