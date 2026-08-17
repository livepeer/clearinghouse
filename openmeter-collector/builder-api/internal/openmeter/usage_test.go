package openmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCatalogMetersMatchProvisionFile(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	catalogPath := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "provision", "catalog.json"))
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	var catalog struct {
		Meters []struct {
			Key        string            `json:"key"`
			Dimensions map[string]string `json:"dimensions"`
		} `json:"meters"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("parse catalog: %v", err)
	}
	if len(catalog.Meters) != len(catalogMeters) {
		t.Fatalf("catalog.json has %d meters, go map has %d", len(catalog.Meters), len(catalogMeters))
	}
	for _, m := range catalog.Meters {
		dims, ok := catalogMeters[m.Key]
		if !ok {
			t.Errorf("missing meter %q in catalogMeters", m.Key)
			continue
		}
		if len(dims) != len(m.Dimensions) {
			t.Errorf("meter %q: go has %d dims, json has %d", m.Key, len(dims), len(m.Dimensions))
		}
		for dim := range m.Dimensions {
			if _, ok := dims[dim]; !ok {
				t.Errorf("meter %q missing dimension %q", m.Key, dim)
			}
		}
	}
}

func TestNormalizeAndMatchKeys(t *testing.T) {
	if got := NormalizePlatformUserID("owner:uuid-1"); got != "uuid-1" {
		t.Fatalf("normalize owner = %q", got)
	}
	if got := NormalizePlatformUserID("user:uuid-1"); got != "uuid-1" {
		t.Fatalf("normalize user = %q", got)
	}
	keys := ExternalUserIDMatchKeys("uuid-1")
	for _, want := range []string{"uuid-1", "owner:uuid-1", "user:uuid-1"} {
		if _, ok := keys[want]; !ok {
			t.Errorf("missing match key %q", want)
		}
	}
}

func TestRowMatchesActor(t *testing.T) {
	row := UsageRow{
		GroupBy: map[string]string{
			"client_id":        "app-a",
			"external_user_id": "owner:alice",
		},
		Value: 1,
	}
	if !RowMatchesActor(row, "app-a", "alice") {
		t.Fatal("expected actor match on owner:alice")
	}
	if RowMatchesActor(row, "app-a", "bob") {
		t.Fatal("bob must not match alice row")
	}
	if RowMatchesActor(row, "app-b", "alice") {
		t.Fatal("foreign client_id must not match")
	}
}

// TestQueryUsageRefusesEmptyClientID is the important one: an unscoped meter
// query against a shared OpenMeter tenant would return every tenant's usage.
func TestQueryUsageRefusesEmptyClientID(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"value":"999","dimensions":{"client_id":"other"}}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "token")

	for _, clientID := range []string{"", "  "} {
		rows, err := c.QueryUsage(context.Background(), UsageQuery{MeterSlug: "m", ClientID: clientID})
		if err != nil {
			t.Fatalf("clientID %q: %v", clientID, err)
		}
		if len(rows) != 0 {
			t.Errorf("clientID %q: returned %d rows, want 0", clientID, len(rows))
		}
	}
	if called {
		t.Error("an empty client id must not reach the metering backend at all")
	}
}

func TestQueryUsageFiltersByClientID(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody meterQueryRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/meters") && !strings.Contains(r.URL.Path, "/query"):
			_, _ = w.Write([]byte(`{"data":[{"id":"01METERTEST000000000000001","key":"billable_usd_micros"}]}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/query"):
			gotMethod = r.Method
			gotPath = r.URL.Path
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode body: %v", err)
			}
			_, _ = w.Write([]byte(`{"data":[{"value":"2","from":"2026-01-01T00:00:00Z","to":"2026-01-02T00:00:00Z","dimensions":{"client_id":"a","external_user_id":"1"}}]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rows, err := New(srv.URL, "token").QueryUsage(context.Background(), UsageQuery{
		MeterSlug: "billable_usd_micros",
		ClientID:  "a",
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/meters/01METERTEST000000000000001/query" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotBody.Filters.Dimensions["client_id"].Eq != "a" {
		t.Fatalf("client_id filter = %+v", gotBody.Filters.Dimensions["client_id"])
	}
	hasClient, hasUser := false, false
	for _, d := range gotBody.GroupByDimensions {
		if d == "client_id" {
			hasClient = true
		}
		if d == "external_user_id" {
			hasUser = true
		}
	}
	if !hasClient || !hasUser {
		t.Fatalf("group_by_dimensions = %v", gotBody.GroupByDimensions)
	}
	if len(rows) != 1 || rows[0].Value != 2 || rows[0].GroupBy["client_id"] != "a" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestQueryUsageRequiresMeter(t *testing.T) {
	if _, err := New("http://unused", "t").QueryUsage(context.Background(), UsageQuery{ClientID: "a"}); err == nil {
		t.Fatal("expected an error for a missing meter slug")
	}
}

// TestListCustomerKeysMatchesOnTheSeparator guards against client id "acme"
// also claiming customers belonging to "acme-corp".
func TestListCustomerKeysMatchesOnTheSeparator(t *testing.T) {
	all := []Customer{
		{Key: "acme:alice"},
		{Key: "acme:bob"},
		{Key: "acme-corp:eve"},
		{Key: "acmex:mallory"},
		{Key: "other:carol"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": all})
	}))
	defer srv.Close()

	keys, err := New(srv.URL, "token").ListCustomerKeysForClient(context.Background(), "acme")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected only acme's 2 customers, got %v", keys)
	}
	for _, k := range keys {
		if k != "acme:alice" && k != "acme:bob" {
			t.Errorf("unexpected key %q", k)
		}
	}
}

func TestListCustomerKeysRequiresClientID(t *testing.T) {
	if _, err := New("http://unused", "t").ListCustomerKeysForClient(context.Background(), "  "); err == nil {
		t.Fatal("expected an error for an empty client id")
	}
}

// TestListCustomerKeysIsBounded stops one admin request from walking an
// unbounded customer list on a large shared tenant.
func TestListCustomerKeysIsBounded(t *testing.T) {
	var pages int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		pages++
		full := make([]Customer, customerPageSize)
		for i := range full {
			full[i] = Customer{Key: fmt.Sprintf("acme:user-%d-%d", pages, i)}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": full})
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "token").ListCustomerKeysForClient(context.Background(), "acme"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if pages != maxCustomerPages {
		t.Fatalf("walked %d pages, want the %d-page cap", pages, maxCustomerPages)
	}
}
