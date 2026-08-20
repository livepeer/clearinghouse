package openmeter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStripeCheckoutURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"https://checkout.stripe.com/c/pay/cs_test_1#fid=abc", "https://checkout.stripe.com/c/pay/cs_test_1#fid=abc"},
		{"https://pay.checkout.stripe.com/c/pay/cs_1", "https://pay.checkout.stripe.com/c/pay/cs_1"},
		{"http://checkout.stripe.com/c/pay/cs_1", ""},
		{"https://evil.com/c/pay/cs_1", ""},
		{"https://checkout.stripe.com.evil.com/c/pay/cs_1", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := StripeCheckoutURL(tc.in); got != tc.want {
			t.Fatalf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestHTTPSRedirectURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"https://app.example.com/ok", "https://app.example.com/ok"},
		{"https://app.example.com/ok?session={CHECKOUT_SESSION_ID}", "https://app.example.com/ok?session={CHECKOUT_SESSION_ID}"},
		{"https://localhost:3000/billing/ok", "https://localhost:3000/billing/ok"},
		{"http://app.example.com/ok", ""},
		{"javascript:alert(1)", ""},
		{"https://evil.com@app.example.com/ok", ""},
		{"https://user:pass@app.example.com/ok", ""},
	}
	for _, tc := range cases {
		if got := HTTPSRedirectURL(tc.in); got != tc.want {
			t.Fatalf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestGetStripeBillingRefs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/customers/cust-1/billing" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"app_data": map[string]any{
				"stripe": map[string]any{
					"customer_id":               "cus_123",
					"default_payment_method_id": "pm_abc",
				},
			},
		})
	}))
	t.Cleanup(srv.Close)

	refs, err := New(srv.URL, "token").GetStripeBillingRefs(context.Background(), "cust-1")
	if err != nil {
		t.Fatal(err)
	}
	if refs.StripeCustomerID != "cus_123" || refs.DefaultPaymentMethodID != "pm_abc" || !refs.HasDefaultPaymentMethod {
		t.Fatalf("%+v", refs)
	}
}

func TestGetStripeBillingRefsDecodeError(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	t.Cleanup(srv.Close)

	_, err := New(srv.URL, "token").GetStripeBillingRefs(context.Background(), "cust-1")
	if err == nil {
		t.Fatal("expected decode error")
	}
}

func TestGetStripeBillingRefsEmptyOn404(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	t.Cleanup(srv.Close)

	refs, err := New(srv.URL, "token").GetStripeBillingRefs(context.Background(), "cust-1")
	if err != nil {
		t.Fatal(err)
	}
	if refs.HasDefaultPaymentMethod || refs.StripeCustomerID != "" {
		t.Fatalf("%+v", refs)
	}
}

func TestCreateStripeCheckoutSession(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/customers/cust-1/billing/stripe/checkout-sessions" {
			t.Fatalf("%s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		opts := body["stripe_options"].(map[string]any)
		if opts["success_url"] != "https://app.example.com/ok" || opts["cancel_url"] != "https://app.example.com/cancel" {
			t.Fatalf("options = %#v", opts)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"url":        "https://checkout.stripe.com/c/pay/cs_test_1#fid=x",
			"session_id": "cs_test_1",
		})
	}))
	t.Cleanup(srv.Close)

	session, err := New(srv.URL, "token").CreateStripeCheckoutSession(
		context.Background(),
		"cust-1",
		"https://app.example.com/ok",
		"https://app.example.com/cancel",
	)
	if err != nil {
		t.Fatal(err)
	}
	if session.CheckoutURL != "https://checkout.stripe.com/c/pay/cs_test_1#fid=x" || session.SessionID != "cs_test_1" {
		t.Fatalf("%+v", session)
	}
}

func TestCreateStripeCheckoutSessionRejectsNonStripeHost(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"url": "https://evil.example/pay"})
	}))
	t.Cleanup(srv.Close)

	_, err := New(srv.URL, "token").CreateStripeCheckoutSession(
		context.Background(),
		"cust-1",
		"https://app.example.com/ok",
		"https://app.example.com/cancel",
	)
	if err == nil {
		t.Fatal("expected error")
	}
}
