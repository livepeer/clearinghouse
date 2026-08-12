package openmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

type meterQueryResponse struct {
	Data []meterQueryRow `json:"data"`
}

type meterQueryRow struct {
	Subject     string            `json:"subject"`
	WindowStart time.Time         `json:"windowStart"`
	WindowEnd   time.Time         `json:"windowEnd"`
	Value       float64           `json:"value"`
	GroupBy     map[string]string `json:"groupBy"`
}

// QueryUsage reads a meter for an explicit subject list.
//
// An empty subject list returns no rows rather than querying every subject —
// an unscoped meter query in a shared tenant would return other tenants' usage.
func (c *Client) QueryUsage(ctx context.Context, q UsageQuery) ([]UsageRow, error) {
	slug := strings.TrimSpace(q.MeterSlug)
	if slug == "" {
		return nil, fmt.Errorf("meter slug is required")
	}
	if len(q.Subjects) == 0 {
		return []UsageRow{}, nil
	}

	params := url.Values{}
	for _, subject := range q.Subjects {
		subject = strings.TrimSpace(subject)
		if subject != "" {
			params.Add("subject", subject)
		}
	}
	if len(params["subject"]) == 0 {
		return []UsageRow{}, nil
	}
	if q.From != nil {
		params.Set("from", q.From.UTC().Format(time.RFC3339))
	}
	if q.To != nil {
		params.Set("to", q.To.UTC().Format(time.RFC3339))
	}
	for _, g := range q.GroupBy {
		if g = strings.TrimSpace(g); g != "" {
			params.Add("groupBy", g)
		}
	}

	path := fmt.Sprintf("/meters/%s/query?%s", url.PathEscape(slug), params.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openmeter query meter: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("meter %q not found", slug)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openmeter query meter %d: %s", resp.StatusCode, string(body))
	}

	var parsed meterQueryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	rows := make([]UsageRow, 0, len(parsed.Data))
	for _, row := range parsed.Data {
		rows = append(rows, UsageRow{
			Subject:     row.Subject,
			WindowStart: row.WindowStart,
			WindowEnd:   row.WindowEnd,
			Value:       row.Value,
			GroupBy:     row.GroupBy,
		})
	}
	return rows, nil
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

		batch := decodeCustomerList(body)
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

func decodeCustomerList(body []byte) []Customer {
	var page customerPage
	if err := json.Unmarshal(body, &page); err == nil && len(page.Data) > 0 {
		return page.Data
	}
	var list []Customer
	if err := json.Unmarshal(body, &list); err == nil {
		return list
	}
	return nil
}
