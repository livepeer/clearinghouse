package openmeter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
)

const microsPerDollar = 1_000_000

// Access is the customer's prepaid / entitlement balance snapshot.
type Access struct {
	HasAccess        bool
	BalanceUSDMicros int64
	Source           string // "credits" | "entitlement" | "none"
}

type creditBalanceResponse struct {
	Balances []creditBalanceRow `json:"balances"`
}

type creditBalanceRow struct {
	Currency string `json:"currency"`
	Live     string `json:"live"`
}

type creditGrantsResponse struct {
	Data []creditGrantRow `json:"data"`
}

type creditGrantRow struct {
	ID       string `json:"id"`
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
	Status   string `json:"status"`
	Key      string `json:"key"`
	Name     string `json:"name"`
}

type entitlementAccessResponse struct {
	Entitlements []entitlementAccess `json:"entitlements"`
	Data         []entitlementAccess `json:"data"`
}

type entitlementAccess struct {
	FeatureKey string          `json:"featureKey"`
	Feature    string          `json:"feature_key"`
	HasAccess  *bool           `json:"hasAccess"`
	HasAccess2 *bool           `json:"has_access"`
	Balance    *float64        `json:"balance"`
	Value      json.RawMessage `json:"value"`
}

// GetAccess reads credits balance first, then entitlement-access as fallback.
func (c *Client) GetAccess(ctx context.Context, customerID, featureKey string) (*Access, error) {
	customerID = strings.TrimSpace(customerID)
	if customerID == "" {
		return nil, fmt.Errorf("customer id is required")
	}

	credits, err := c.getCreditsBalance(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if credits != nil {
		return credits, nil
	}

	return c.getEntitlementAccess(ctx, customerID, featureKey)
}

func (c *Client) getCreditsBalance(ctx context.Context, customerID string) (*Access, error) {
	q := url.Values{}
	q.Set("filter[currency][eq]", "USD")
	path := fmt.Sprintf("/customers/%s/credits/balance?%s", url.PathEscape(customerID), q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter credits balance: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	// A nil, nil return signals "no answer from credits — try entitlements".
	// 404 belongs here: it means this org does not track credits for the
	// customer, not that the customer has no allowance. Answering
	// HasAccess:false directly would deny a customer whose entitlement is
	// perfectly good, and OPENMETER_ENFORCE_ALLOWANCE turns that into a 402.
	switch resp.StatusCode {
	case http.StatusNotFound, http.StatusForbidden,
		http.StatusNotImplemented, http.StatusBadRequest:
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter credits balance %d: %s", resp.StatusCode, string(body))
	}

	var parsed creditBalanceResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	var live string
	for _, row := range parsed.Balances {
		if strings.EqualFold(row.Currency, "USD") {
			live = row.Live
			break
		}
	}
	if live == "" && len(parsed.Balances) > 0 {
		live = parsed.Balances[0].Live
	}
	micros, err := decimalDollarsToUSDMicros(live)
	if err != nil {
		micros = 0
	}
	if micros < 0 {
		micros = 0
	}
	return &Access{
		HasAccess:        micros > 0,
		BalanceUSDMicros: micros,
		Source:           "credits",
	}, nil
}

func (c *Client) getEntitlementAccess(ctx context.Context, customerID, featureKey string) (*Access, error) {
	path := fmt.Sprintf("/customers/%s/entitlement-access", url.PathEscape(customerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter entitlement-access: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return &Access{HasAccess: false, BalanceUSDMicros: 0, Source: "entitlement"}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter entitlement-access %d: %s", resp.StatusCode, string(body))
	}

	var parsed entitlementAccessResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	rows := parsed.Entitlements
	if len(rows) == 0 {
		rows = parsed.Data
	}
	featureKey = strings.TrimSpace(featureKey)
	for _, row := range rows {
		key := row.FeatureKey
		if key == "" {
			key = row.Feature
		}
		if featureKey != "" && key != "" && key != featureKey {
			continue
		}
		has := false
		if row.HasAccess != nil {
			has = *row.HasAccess
		} else if row.HasAccess2 != nil {
			has = *row.HasAccess2
		}
		var balance int64
		if row.Balance != nil {
			balance = int64(*row.Balance)
			if balance < 0 {
				balance = 0
			}
			if balance > 0 {
				has = true
			}
		}
		return &Access{
			HasAccess:        has && (balance > 0 || row.Balance == nil),
			BalanceUSDMicros: balance,
			Source:           "entitlement",
		}, nil
	}
	return &Access{HasAccess: false, BalanceUSDMicros: 0, Source: "entitlement"}, nil
}

// EnsureTrialGrant creates a one-time credit grant when amountMicros > 0 and no matching grant exists.
func (c *Client) EnsureTrialGrant(ctx context.Context, customerID, featureKey, grantKey string, amountMicros int64) error {
	if amountMicros <= 0 {
		return nil
	}
	customerID = strings.TrimSpace(customerID)
	grantKey = strings.TrimSpace(grantKey)
	if customerID == "" || grantKey == "" {
		return fmt.Errorf("customer id and grant key are required")
	}

	existing, err := c.listCreditGrants(ctx, customerID)
	if err != nil {
		return err
	}
	for _, g := range existing {
		if g.Key == grantKey {
			return nil
		}
	}

	payload := map[string]any{
		"name":           "Clearinghouse trial",
		"funding_method": "none",
		"currency":       "USD",
		"amount":         usdMicrosToDecimalDollars(amountMicros),
		"priority":       1,
		"expires_after":  "P1Y",
		"key":            grantKey,
	}
	if fk := strings.TrimSpace(featureKey); fk != "" {
		payload["filters"] = map[string]any{"features": []string{fk}}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/customers/%s/credits/grants", url.PathEscape(customerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openmeter create credit grant: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("openmeter create credit grant %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

func (c *Client) listCreditGrants(ctx context.Context, customerID string) ([]creditGrantRow, error) {
	path := fmt.Sprintf("/customers/%s/credits/grants?page[size]=100", url.PathEscape(customerID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter list credit grants: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter list credit grants %d: %s", resp.StatusCode, string(body))
	}
	var parsed creditGrantsResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	return parsed.Data, nil
}

func decimalDollarsToUSDMicros(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	r := new(big.Rat)
	if _, ok := r.SetString(raw); !ok {
		return 0, fmt.Errorf("invalid decimal amount %q", raw)
	}
	r.Mul(r, big.NewRat(microsPerDollar, 1))
	f := new(big.Float).SetRat(r)
	i, _ := f.Int64()
	return i, nil
}

func usdMicrosToDecimalDollars(micros int64) string {
	if micros < 0 {
		micros = 0
	}
	whole := micros / microsPerDollar
	frac := micros % microsPerDollar
	if frac == 0 {
		return fmt.Sprintf("%d", whole)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%06d", whole, frac), "0"), ".")
}
