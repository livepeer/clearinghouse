package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/httpapi"
)

func TestCORSKongPortalPreflight(t *testing.T) {
	h := httpapi.NewServer(config.Config{CORSAllowKongPortals: true}, nil, nil, nil, nil, nil, nil, nil, nil, nil).Handler()
	req := httptest.NewRequest(http.MethodOptions, "/health", nil)
	req.Header.Set("Origin", "https://d915d332f644.us.kongportals.com")
	req.Header.Set("Access-Control-Request-Method", "GET")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status=%d want %d", rec.Code, http.StatusNoContent)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://d915d332f644.us.kongportals.com" {
		t.Fatalf("Allow-Origin=%q", got)
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("Allow-Headers=%q missing Authorization", rec.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestCORSRejectUnknownOrigin(t *testing.T) {
	h := httpapi.NewServer(config.Config{CORSAllowKongPortals: true}, nil, nil, nil, nil, nil, nil, nil, nil, nil).Handler()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unexpected Allow-Origin=%q", got)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestCORSExplicitAllowlist(t *testing.T) {
	h := httpapi.NewServer(config.Config{
		CORSAllowedOrigins:   []string{"https://docs.example.com"},
		CORSAllowKongPortals: false,
	}, nil, nil, nil, nil, nil, nil, nil, nil, nil).Handler()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://docs.example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://docs.example.com" {
		t.Fatalf("Allow-Origin=%q", got)
	}
}
