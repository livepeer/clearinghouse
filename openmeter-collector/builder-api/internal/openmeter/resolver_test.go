package openmeter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolverUsesCredentialsService(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/internal/tenants/app-1/openmeter" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer plat-secret" {
			t.Fatalf("auth = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"client_id":      "app-1",
			"region":         "us",
			"org_id":         "org-1",
			"openmeter_base": "https://us.api.konghq.com/v3/openmeter",
			"token":          "kpat_tenant",
		})
	}))
	t.Cleanup(srv.Close)

	r := NewResolver(srv.URL, "plat-secret", "https://fallback.example/v3/openmeter", "fallback-key")
	creds, err := r.Resolve(context.Background(), "app-1")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Token != "kpat_tenant" || creds.BaseURL != "https://us.api.konghq.com/v3/openmeter" {
		t.Fatalf("%+v", creds)
	}
}

func TestResolverFallsBackWhenUnbound(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"tenant_not_bound"}`, http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	r := NewResolver(srv.URL, "plat-secret", "https://fallback.example/v3/openmeter", "fallback-key")
	creds, err := r.Resolve(context.Background(), "missing")
	if err != nil {
		t.Fatal(err)
	}
	if creds.Token != "fallback-key" {
		t.Fatalf("%+v", creds)
	}
}
