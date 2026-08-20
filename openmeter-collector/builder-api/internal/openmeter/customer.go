package openmeter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Customer is an OpenMeter customer record.
type Customer struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

type customerPage struct {
	Data []Customer `json:"data"`
}

type createCustomerRequest struct {
	Key              string           `json:"key"`
	Name             string           `json:"name"`
	UsageAttribution usageAttribution `json:"usage_attribution"`
}

type usageAttribution struct {
	SubjectKeys []string `json:"subject_keys"`
}

// Client upserts OpenMeter customers via Konnect REST API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New creates an OpenMeter HTTP client.
func New(baseURL, apiKey string) *Client {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &Client{
		baseURL: baseURL,
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// EnsureCustomer creates a customer when missing; idempotent on key.
func (c *Client) EnsureCustomer(ctx context.Context, clientID, externalUserID, displayName string) (*Customer, error) {
	key := CustomerKey(clientID, externalUserID)
	if displayName == "" {
		displayName = key
	}

	existing, err := c.findByKey(ctx, key)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	payload := createCustomerRequest{
		Key:  key,
		Name: displayName,
		UsageAttribution: usageAttribution{
			SubjectKeys: []string{key},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/customers", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter create customer: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter create customer %d: %s", resp.StatusCode, string(respBody))
	}

	var created Customer
	if err := json.Unmarshal(respBody, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// LookupCustomerByKey returns the customer for key, or (nil, nil) when missing.
// Konnect credit and entitlement routes require the customer ULID, not this key.
func (c *Client) LookupCustomerByKey(ctx context.Context, key string) (*Customer, error) {
	return c.findByKey(ctx, key)
}

func (c *Client) findByKey(ctx context.Context, key string) (*Customer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/customers", nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Set("filter[key]", key)
	req.URL.RawQuery = q.Encode()
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter list customers: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter list customers %d: %s", resp.StatusCode, string(respBody))
	}

	var page customerPage
	if err := json.Unmarshal(respBody, &page); err == nil && len(page.Data) > 0 {
		for _, cust := range page.Data {
			if cust.Key == key {
				return &cust, nil
			}
		}
	}

	var list []Customer
	if err := json.Unmarshal(respBody, &list); err == nil {
		for _, cust := range list {
			if cust.Key == key {
				return &cust, nil
			}
		}
	}
	return nil, nil
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
}
