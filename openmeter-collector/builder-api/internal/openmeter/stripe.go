package openmeter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// StripeBillingRefs are Konnect Stripe ids for an OpenMeter customer.
// Empty fields mean no Stripe customer / default payment method is on file.
type StripeBillingRefs struct {
	StripeCustomerID        string
	DefaultPaymentMethodID  string
	HasDefaultPaymentMethod bool
}

// StripeCheckoutSession is a setup-mode Checkout session created via Konnect.
type StripeCheckoutSession struct {
	CheckoutURL string
	SessionID   string
}

type customerBillingResponse struct {
	AppData *struct {
		Stripe *struct {
			CustomerID                   string `json:"customer_id"`
			DefaultPaymentMethodID       string `json:"default_payment_method_id"`
			StripeCustomerID             string `json:"stripeCustomerId"`
			StripeDefaultPaymentMethodID string `json:"stripeDefaultPaymentMethodId"`
		} `json:"stripe"`
	} `json:"app_data"`
}

type stripeCheckoutRequest struct {
	StripeOptions stripeCheckoutOptions `json:"stripe_options"`
}

type stripeCheckoutOptions struct {
	SuccessURL string `json:"success_url"`
	CancelURL  string `json:"cancel_url"`
	Currency   string `json:"currency"`
}

type stripeCheckoutResponse struct {
	URL       string `json:"url"`
	SessionID string `json:"session_id"`
	SessionId string `json:"sessionId"`
}

// GetStripeBillingRefs reads Konnect customer billing app_data. Missing Stripe
// app or empty document returns zero refs (fail-open). Transport / HTTP errors
// are returned so callers can decide.
func (c *Client) GetStripeBillingRefs(ctx context.Context, customerID string) (StripeBillingRefs, error) {
	customerID = strings.TrimSpace(customerID)
	if customerID == "" {
		return StripeBillingRefs{}, fmt.Errorf("customer id is required")
	}
	path := fmt.Sprintf("/customers/%s/billing", url.PathEscape(customerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return StripeBillingRefs{}, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return StripeBillingRefs{}, fmt.Errorf("openmeter customer billing: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return StripeBillingRefs{}, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return StripeBillingRefs{}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return StripeBillingRefs{}, fmt.Errorf("openmeter customer billing %d: %s", resp.StatusCode, string(body))
	}

	var parsed customerBillingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return StripeBillingRefs{}, nil
	}
	if parsed.AppData == nil || parsed.AppData.Stripe == nil {
		return StripeBillingRefs{}, nil
	}
	stripe := parsed.AppData.Stripe
	customer := strings.TrimSpace(stripe.CustomerID)
	if customer == "" {
		customer = strings.TrimSpace(stripe.StripeCustomerID)
	}
	pm := strings.TrimSpace(stripe.DefaultPaymentMethodID)
	if pm == "" {
		pm = strings.TrimSpace(stripe.StripeDefaultPaymentMethodID)
	}
	return StripeBillingRefs{
		StripeCustomerID:        customer,
		DefaultPaymentMethodID:  pm,
		HasDefaultPaymentMethod: pm != "",
	}, nil
}

// CreateStripeCheckoutSession starts a Konnect Stripe Checkout session in
// setup mode for an existing OpenMeter customer.
func (c *Client) CreateStripeCheckoutSession(ctx context.Context, customerID, successURL, cancelURL string) (*StripeCheckoutSession, error) {
	customerID = strings.TrimSpace(customerID)
	if customerID == "" {
		return nil, fmt.Errorf("customer id is required")
	}
	payload, err := json.Marshal(stripeCheckoutRequest{
		StripeOptions: stripeCheckoutOptions{
			SuccessURL: successURL,
			CancelURL:  cancelURL,
			Currency:   "USD",
		},
	})
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/customers/%s/billing/stripe/checkout-sessions", url.PathEscape(customerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter stripe checkout: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter stripe checkout %d: %s", resp.StatusCode, string(body))
	}

	var parsed stripeCheckoutResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("openmeter stripe checkout: decode: %w", err)
	}
	checkoutURL := StripeCheckoutURL(parsed.URL)
	if checkoutURL == "" {
		return nil, fmt.Errorf("openmeter stripe checkout: url unavailable")
	}
	sessionID := strings.TrimSpace(parsed.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(parsed.SessionId)
	}
	return &StripeCheckoutSession{
		CheckoutURL: checkoutURL,
		SessionID:   sessionID,
	}, nil
}

// StripeCheckoutURL returns a same-origin-safe https checkout.stripe.com URL,
// or empty if the input is not an allowlisted Stripe Checkout host.
func StripeCheckoutURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "checkout.stripe.com" && !strings.HasSuffix(host, ".checkout.stripe.com") {
		return ""
	}
	return "https://" + host + parsed.EscapedPath() + queryOrEmpty(parsed) + fragmentOrEmpty(parsed)
}

func queryOrEmpty(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	return "?" + u.RawQuery
}

func fragmentOrEmpty(u *url.URL) string {
	if u.Fragment == "" && u.RawFragment == "" {
		return ""
	}
	if u.RawFragment != "" {
		return "#" + u.RawFragment
	}
	return "#" + u.EscapedFragment()
}

// HTTPSRedirectURL returns the trimmed URL when it is https with a host, else "".
func HTTPSRedirectURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return strings.TrimSpace(raw)
}
