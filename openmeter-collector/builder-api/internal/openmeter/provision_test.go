package openmeter_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
)

func TestCustomerKey(t *testing.T) {
	t.Parallel()
	got := openmeter.CustomerKey(" pub-client ", " demo-user ")
	if got != "pub-client:demo-user" {
		t.Fatalf("CustomerKey() = %q", got)
	}
}

func TestProvisionSessionCreatesCustomerAndSubscription(t *testing.T) {
	t.Parallel()

	var (
		customerCreated     bool
		subscriptionCreated bool
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/customers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/customers":
			customerCreated = true
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"cust-1","key":"pub-client:demo-user"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/subscriptions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/subscriptions":
			subscriptionCreated = true
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"sub-1"}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	client := openmeter.New(server.URL, "test-key")
	result, err := client.ProvisionSession(context.Background(), openmeter.ProvisionConfig{
		DefaultPlanKey: "clearinghouse_default_ppu",
	}, "pub-client", "demo-user")
	if err != nil {
		t.Fatal(err)
	}
	if result.CustomerKey != "pub-client:demo-user" {
		t.Fatalf("customer key = %q", result.CustomerKey)
	}
	if !customerCreated || !subscriptionCreated {
		t.Fatalf("customer=%v subscription=%v", customerCreated, subscriptionCreated)
	}
}

func TestProvisionSessionReusesExistingCustomer(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/customers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"cust-1","key":"pub-client:demo-user"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/subscriptions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"sub-1","customer_id":"cust-1","status":"active"}]}`))
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	client := openmeter.New(server.URL, "test-key")
	result, err := client.ProvisionSession(context.Background(), openmeter.ProvisionConfig{
		DefaultPlanKey: "clearinghouse_default_ppu",
	}, "pub-client", "demo-user")
	if err != nil {
		t.Fatal(err)
	}
	if result.Customer.ID != "cust-1" {
		t.Fatalf("customer id = %q", result.Customer.ID)
	}
}
