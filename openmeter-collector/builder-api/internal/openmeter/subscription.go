package openmeter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type subscriptionPage struct {
	Data []subscription `json:"data"`
}

type subscription struct {
	ID         string `json:"id"`
	CustomerID string `json:"customer_id"`
	Status     string `json:"status"`
}

type createSubscriptionRequest struct {
	Customer customerRef `json:"customer"`
	Plan     planRef     `json:"plan"`
}

type customerRef struct {
	Key string `json:"key"`
}

type planRef struct {
	Key string `json:"key"`
}

// EnsureDefaultSubscription ensures the customer has a subscription on the default plan.
func (c *Client) EnsureDefaultSubscription(ctx context.Context, customerID, customerKey, planKey string) error {
	planKey = strings.TrimSpace(planKey)
	if planKey == "" {
		return nil
	}
	customerKey = strings.TrimSpace(customerKey)
	if customerKey == "" {
		return fmt.Errorf("customer key is required for subscription ensure")
	}

	existing, err := c.listSubscriptions(ctx, customerID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil
	}

	payload := createSubscriptionRequest{
		Customer: customerRef{Key: customerKey},
		Plan:     planRef{Key: planKey},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/subscriptions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openmeter create subscription: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("openmeter create subscription %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *Client) listSubscriptions(ctx context.Context, customerID string) ([]subscription, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/subscriptions", nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	// Konnect only honors the bracketed filter form; a bare `customer_id` param is
	// ignored and returns unrelated subscriptions (false-positive idempotency skip).
	q.Set("filter[customer_id]", customerID)
	req.URL.RawQuery = q.Encode()
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter list subscriptions: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter list subscriptions %d: %s", resp.StatusCode, string(respBody))
	}

	var page subscriptionPage
	if err := json.Unmarshal(respBody, &page); err == nil && len(page.Data) > 0 {
		return page.Data, nil
	}

	var list []subscription
	if err := json.Unmarshal(respBody, &list); err == nil {
		return list, nil
	}
	return nil, nil
}
