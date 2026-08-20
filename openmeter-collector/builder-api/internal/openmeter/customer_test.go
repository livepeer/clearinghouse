package openmeter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeCustomerListAcceptsKonnectItems(t *testing.T) {
	body := []byte(`{"items":[{"id":"01ABC","key":"app:user"}]}`)
	list, err := decodeCustomerList(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Key != "app:user" {
		t.Fatalf("got %#v", list)
	}
}

func TestEnsureCustomerUsesExactKeyMatchOnItems(t *testing.T) {
	var posts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/customers":
			// Partial match noise + exact key (Konnect list behavior).
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]string{
					{"id": "01OTHER", "key": "DK3x:portal-demo"},
					{"id": "01HIT", "key": "DK3x:portal-demo-user"},
				},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/customers":
			posts++
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"status":409,"title":"Conflict"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cust, err := New(srv.URL, "tok").EnsureCustomer(context.Background(), "DK3x", "portal-demo-user", "portal-demo-user")
	if err != nil {
		t.Fatal(err)
	}
	if cust.ID != "01HIT" || cust.Key != "DK3x:portal-demo-user" {
		t.Fatalf("customer = %#v", cust)
	}
	if posts != 0 {
		t.Fatalf("must not create when list returns exact key, posts=%d", posts)
	}
}

func TestEnsureCustomerRecoversFromConflict(t *testing.T) {
	var gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/customers":
			gets++
			if gets == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []map[string]string{{"id": "01RACE", "key": "acme:alice"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/customers":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`conflict error: key overlaps with subject`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cust, err := New(srv.URL, "tok").EnsureCustomer(context.Background(), "acme", "alice", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if cust.ID != "01RACE" {
		t.Fatalf("customer = %#v", cust)
	}
	if gets < 2 {
		t.Fatalf("expected re-fetch after 409, gets=%d", gets)
	}
}

func TestSessionStubStillAcceptsDataShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/customers") {
			_, _ = w.Write([]byte(`{"data":[]}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/customers" {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "cus_1", "key": "t:u"})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cust, err := New(srv.URL, "t").EnsureCustomer(context.Background(), "t", "u", "u")
	if err != nil || cust.ID != "cus_1" {
		t.Fatalf("cust=%v err=%v", cust, err)
	}
}
