package openmeter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestQueryUsageRefusesEmptySubjectList is the important one: an unscoped meter
// query against a shared OpenMeter tenant would return every tenant's usage, so
// an empty subject list must return nothing rather than querying broadly.
func TestQueryUsageRefusesEmptySubjectList(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"subject":"other:user","value":999}]}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "token")

	for _, subjects := range [][]string{nil, {}, {"", "  "}} {
		rows, err := c.QueryUsage(context.Background(), UsageQuery{MeterSlug: "m", Subjects: subjects})
		if err != nil {
			t.Fatalf("subjects %v: %v", subjects, err)
		}
		if len(rows) != 0 {
			t.Errorf("subjects %v: returned %d rows, want 0", subjects, len(rows))
		}
	}
	if called {
		t.Error("an empty subject list must not reach the metering backend at all")
	}
}

func TestQueryUsageSendsEverySubject(t *testing.T) {
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
			_, _ = w.Write([]byte(`{"data":[{"value":"2","from":"2026-01-01T00:00:00Z","to":"2026-01-02T00:00:00Z","dimensions":{"subject":"a:1"}}]}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	rows, err := New(srv.URL, "token").QueryUsage(context.Background(), UsageQuery{
		MeterSlug: "billable_usd_micros",
		Subjects:  []string{"a:1", "a:2"},
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/meters/01METERTEST000000000000001/query" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	in := gotBody.Filters.Dimensions["subject"].In
	if len(in) != 2 || in[0] != "a:1" || in[1] != "a:2" {
		t.Fatalf("subject filter = %v", in)
	}
	if len(rows) != 1 || rows[0].Value != 2 || rows[0].Subject != "a:1" {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestQueryUsageRequiresMeter(t *testing.T) {
	if _, err := New("http://unused", "t").QueryUsage(context.Background(), UsageQuery{Subjects: []string{"a:1"}}); err == nil {
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
