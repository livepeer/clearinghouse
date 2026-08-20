package openmeter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const customerPageSize = 100

// maxCustomerPages bounds the prefix scan so a large shared tenant cannot turn
// one admin request into an unbounded walk of the customer list.
const maxCustomerPages = 50

// UsageRow is one metered window for one subject.
type UsageRow struct {
	Subject     string            `json:"subject"`
	WindowStart time.Time         `json:"windowStart"`
	WindowEnd   time.Time         `json:"windowEnd"`
	Value       float64           `json:"value"`
	GroupBy     map[string]string `json:"groupBy,omitempty"`
}

// UsageQuery selects a slice of usage. Subjects must already be constrained to
// the caller's tenant; this type does no authorization of its own.
type UsageQuery struct {
	MeterSlug string
	Subjects  []string
	From      *time.Time
	To        *time.Time
	GroupBy   []string
}

type meterQueryRequest struct {
	From              string            `json:"from,omitempty"`
	To                string            `json:"to,omitempty"`
	GroupByDimensions []string          `json:"group_by_dimensions,omitempty"`
	Filters           *meterQueryFilter `json:"filters,omitempty"`
}

type meterQueryFilter struct {
	Dimensions map[string]meterDimFilter `json:"dimensions,omitempty"`
}

type meterDimFilter struct {
	In []string `json:"in,omitempty"`
	Eq string   `json:"eq,omitempty"`
}

type meterQueryResponse struct {
	Data []meterQueryRow `json:"data"`
}

// Konnect Metering & Billing returns rows shaped like MeterQueryRow: value is a
// decimal string, windows are from/to, and subject lives under dimensions.
type meterQueryRow struct {
	Subject     string            `json:"subject"`
	WindowStart time.Time         `json:"windowStart"`
	WindowEnd   time.Time         `json:"windowEnd"`
	From        time.Time         `json:"from"`
	To          time.Time         `json:"to"`
	Value       json.RawMessage   `json:"value"`
	GroupBy     map[string]string `json:"groupBy"`
	Dimensions  map[string]string `json:"dimensions"`
}

type meterListResponse struct {
	Data []meterListItem `json:"data"`
}

type meterListItem struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

// QueryUsage reads a meter for an explicit subject list.
//
// An empty subject list returns no rows rather than querying every subject —
// an unscoped meter query in a shared tenant would return other tenants' usage.
//
// Against Kong Konnect Metering & Billing this uses POST /meters/{meterId}/query
// (ULID path + JSON body). The legacy OpenMeter Cloud GET /meters/{slug}/query
// returns 405 on Konnect.
func (c *Client) QueryUsage(ctx context.Context, q UsageQuery) ([]UsageRow, error) {
	slug := strings.TrimSpace(q.MeterSlug)
	if slug == "" {
		return nil, fmt.Errorf("meter slug is required")
	}
	subjects := make([]string, 0, len(q.Subjects))
	for _, subject := range q.Subjects {
		subject = strings.TrimSpace(subject)
		if subject != "" {
			subjects = append(subjects, subject)
		}
	}
	if len(subjects) == 0 {
		return []UsageRow{}, nil
	}

	meterID, err := c.resolveMeterID(ctx, slug)
	if err != nil {
		return nil, err
	}

	body := meterQueryRequest{
		GroupByDimensions: []string{"subject"},
		Filters: &meterQueryFilter{
			Dimensions: map[string]meterDimFilter{
				"subject": {In: subjects},
			},
		},
	}
	if q.From != nil {
		body.From = q.From.UTC().Format(time.RFC3339)
	}
	if q.To != nil {
		body.To = q.To.UTC().Format(time.RFC3339)
	}
	for _, g := range q.GroupBy {
		if g = strings.TrimSpace(g); g != "" && g != "subject" {
			body.GroupByDimensions = append(body.GroupByDimensions, g)
		}
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/meters/%s/query", meterID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter query meter: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("meter %q not found", slug)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter query meter %d: %s", resp.StatusCode, string(respBody))
	}

	var parsed meterQueryResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, err
	}
	rows := make([]UsageRow, 0, len(parsed.Data))
	for _, row := range parsed.Data {
		value, err := parseMeterValue(row.Value)
		if err != nil {
			return nil, fmt.Errorf("openmeter query meter value: %w", err)
		}
		subject := row.Subject
		groupBy := row.GroupBy
		if subject == "" && row.Dimensions != nil {
			subject = row.Dimensions["subject"]
			groupBy = row.Dimensions
		}
		start, end := row.WindowStart, row.WindowEnd
		if start.IsZero() {
			start = row.From
		}
		if end.IsZero() {
			end = row.To
		}
		rows = append(rows, UsageRow{
			Subject:     subject,
			WindowStart: start,
			WindowEnd:   end,
			Value:       value,
			GroupBy:     groupBy,
		})
	}
	return rows, nil
}

func parseMeterValue(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var asFloat float64
	if err := json.Unmarshal(raw, &asFloat); err == nil {
		return asFloat, nil
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err != nil {
		return 0, fmt.Errorf("unsupported value %s", string(raw))
	}
	return strconv.ParseFloat(asString, 64)
}

func (c *Client) resolveMeterID(ctx context.Context, key string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/meters", nil)
	if err != nil {
		return "", err
	}
	q := req.URL.Query()
	q.Set("pageSize", "100")
	req.URL.RawQuery = q.Encode()
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("openmeter list meters: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("openmeter list meters %d: %s", resp.StatusCode, string(body))
	}

	var listed meterListResponse
	if err := json.Unmarshal(body, &listed); err != nil {
		return "", err
	}
	for _, m := range listed.Data {
		if m.Key == key {
			if m.ID == "" {
				return "", fmt.Errorf("meter %q has no id", key)
			}
			return m.ID, nil
		}
	}
	// Konnect paths require a ULID; if the caller already passed one, use it.
	if looksLikeULID(key) {
		return key, nil
	}
	return "", fmt.Errorf("meter %q not found", key)
}

func looksLikeULID(s string) bool {
	if len(s) != 26 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

// ListCustomerKeysForClient returns the customer keys belonging to clientID,
// up to maxCustomerPages pages of results. It is deliberately bounded rather
// than exhaustive: callers must not assume a complete scan of a large tenant.
//
// Customer keys are CustomerKey(clientID, externalUserID), so a tenant's
// customers are exactly those prefixed "clientID:". The prefix is matched
// against the separator to stop client id "acme" from also matching
// "acme-corp:user".
func (c *Client) ListCustomerKeysForClient(ctx context.Context, clientID string) ([]string, error) {
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, fmt.Errorf("client id is required")
	}
	prefix := clientID + ":"

	var keys []string
	for page := 1; page <= maxCustomerPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/customers", nil)
		if err != nil {
			return nil, err
		}
		q := req.URL.Query()
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(customerPageSize))
		req.URL.RawQuery = q.Encode()
		c.setHeaders(req)

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("openmeter list customers: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("openmeter list customers %d: %s", resp.StatusCode, string(body))
		}

		batch, err := decodeCustomerList(body)
		if err != nil {
			return nil, err
		}
		for _, cust := range batch {
			if strings.HasPrefix(cust.Key, prefix) {
				keys = append(keys, cust.Key)
			}
		}
		if len(batch) < customerPageSize {
			break
		}
	}
	return keys, nil
}

func decodeCustomerList(body []byte) ([]Customer, error) {
	var page customerPage
	if err := json.Unmarshal(body, &page); err == nil {
		if page.Items != nil {
			return page.Items, nil
		}
		if page.Data != nil {
			return page.Data, nil
		}
	}
	var list []Customer
	if err := json.Unmarshal(body, &list); err == nil {
		return list, nil
	}
	return nil, fmt.Errorf("openmeter customers response has unexpected shape: %s", string(body))
}
