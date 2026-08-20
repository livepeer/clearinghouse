package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/httpapi"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

const (
	platformID     = "platform-m2m"
	platformSecret = "platform-secret"
)

type fakeUsageReader struct {
	lastQuery openmeter.UsageQuery
	rows      []openmeter.UsageRow
}

func (f *fakeUsageReader) QueryUsage(_ context.Context, q openmeter.UsageQuery) ([]openmeter.UsageRow, error) {
	f.lastQuery = q
	if f.rows != nil {
		return f.rows, nil
	}
	return []openmeter.UsageRow{{Subject: q.Subjects[0], Value: 1}}, nil
}

type fakeUsageVerifier struct {
	clientID       string
	externalUserID string
	err            error
}

func (f fakeUsageVerifier) VerifyUserAccessToken(context.Context, string, string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	return f.clientID, f.externalUserID, nil
}

func newServer(usageReader httpapi.UsageReader, verifier tokenexchange.UserTokenVerifier) http.Handler {
	cfg := config.Config{
		SignerM2MClientID: platformID,
		SignerM2MSecret:   platformSecret,
	}
	return httpapi.NewServer(cfg, nil, nil, nil, nil, verifier, nil, usageReader).Handler()
}

func doBearer(t *testing.T, h http.Handler, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doBasic(t *testing.T, h http.Handler, method, target, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(`{"externalUserId":"u1"}`))
	req.Header.Set("Content-Type", "application/json")
	if user != "" || pass != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestDeprecatedAppUsageRouteNotFound(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", "jwt")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestDeprecatedAppOIDCTokenRouteNotFound(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodPost, "/api/v1/apps/tenant-a/oidc/token", "jwt")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestUsageSelfRequiresBearer(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage?meter=billable_usd_micros", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("expected WWW-Authenticate Bearer, got %q", rec.Header().Get("WWW-Authenticate"))
	}
}

func TestUsageSelfReturnsCallerOnly(t *testing.T) {
	usageReader := &fakeUsageReader{
		rows: []openmeter.UsageRow{
			{Subject: "tenant-a:alice", Value: 1},
			{Subject: "tenant-a:bob", Value: 99},
			{Subject: "tenant-b:victim", Value: 123},
		},
	}
	h := newServer(usageReader, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage?meter=billable_usd_micros", "header.payload.signature")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if len(usageReader.lastQuery.Subjects) != 1 || usageReader.lastQuery.Subjects[0] != "tenant-a:alice" {
		t.Fatalf("query subjects = %v, want [tenant-a:alice]", usageReader.lastQuery.Subjects)
	}
	var body struct {
		Subject string               `json:"subject"`
		Rows    []openmeter.UsageRow `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Subject != "tenant-a:alice" {
		t.Fatalf("subject = %q", body.Subject)
	}
	if len(body.Rows) != 1 || body.Rows[0].Subject != "tenant-a:alice" {
		t.Fatalf("rows = %+v", body.Rows)
	}
}

func TestUsageSelfRejectsInvalidVerifierResult(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{err: errors.New("invalid token")})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage?meter=billable_usd_micros", "bad-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestCreateUserRequiresM2MBasic(t *testing.T) {
	h := newServer(&fakeUsageReader{}, nil)
	rec := doBasic(t, h, http.MethodPost, "/api/v1/apps/app-1/users", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}
