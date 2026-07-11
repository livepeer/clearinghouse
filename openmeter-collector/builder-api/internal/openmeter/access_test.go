package openmeter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDecimalDollarsToUSDMicros(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"1", 1_000_000},
		{"5.00", 5_000_000},
		{"0.000001", 1},
		{"1.5", 1_500_000},
	}
	for _, tc := range cases {
		got, err := decimalDollarsToUSDMicros(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %d want %d", tc.in, got, tc.want)
		}
	}
}

func TestUSDMicrosToDecimalDollars(t *testing.T) {
	t.Parallel()
	if got := usdMicrosToDecimalDollars(5_000_000); got != "5" {
		t.Fatalf("got %q", got)
	}
	if got := usdMicrosToDecimalDollars(1_500_000); got != "1.5" {
		t.Fatalf("got %q", got)
	}
}

func TestGetAccessCreditsBalance(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/customers/cust-1/credits/balance" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(creditBalanceResponse{
			Balances: []creditBalanceRow{{Currency: "USD", Live: "2.5"}},
		})
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "token")
	access, err := client.GetAccess(context.Background(), "cust-1", "billable_spend")
	if err != nil {
		t.Fatal(err)
	}
	if !access.HasAccess || access.BalanceUSDMicros != 2_500_000 || access.Source != "credits" {
		t.Fatalf("%+v", access)
	}
}

func TestGetAccessFallsBackToEntitlement(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/customers/cust-1/credits/balance":
			http.Error(w, "forbidden", http.StatusForbidden)
		case "/customers/cust-1/entitlement-access":
			has := true
			_ = json.NewEncoder(w).Encode(entitlementAccessResponse{
				Entitlements: []entitlementAccess{{
					FeatureKey: "billable_spend",
					HasAccess:  &has,
					Balance:    floatPtr(42),
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "token")
	access, err := client.GetAccess(context.Background(), "cust-1", "billable_spend")
	if err != nil {
		t.Fatal(err)
	}
	if !access.HasAccess || access.BalanceUSDMicros != 42 || access.Source != "entitlement" {
		t.Fatalf("%+v", access)
	}
}

func TestEnsureTrialGrantIdempotent(t *testing.T) {
	t.Parallel()
	posts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/customers/cust-1/credits/grants":
			_ = json.NewEncoder(w).Encode(creditGrantsResponse{Data: []creditGrantRow{}})
		case r.Method == http.MethodPost && r.URL.Path == "/customers/cust-1/credits/grants":
			posts++
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"g1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client := New(srv.URL, "token")
	if err := client.EnsureTrialGrant(context.Background(), "cust-1", "billable_spend", "trial:a:b", 1_000_000); err != nil {
		t.Fatal(err)
	}
	if posts != 1 {
		t.Fatalf("posts = %d", posts)
	}
}

func floatPtr(v float64) *float64 { return &v }
