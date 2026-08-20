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
	lastQuery   openmeter.UsageQuery
	rows        []openmeter.UsageRow
	customer    *openmeter.Customer
	access      *openmeter.Access
	lookupErr   error
	accessErr   error
	lastSubject string
}

func (f *fakeUsageReader) QueryUsage(_ context.Context, q openmeter.UsageQuery) ([]openmeter.UsageRow, error) {
	f.lastQuery = q
	if f.rows != nil {
		return f.rows, nil
	}
	return []openmeter.UsageRow{{Subject: q.Subjects[0], Value: 1}}, nil
}

func (f *fakeUsageReader) LookupCustomerByKey(_ context.Context, key string) (*openmeter.Customer, error) {
	f.lastSubject = key
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	if f.customer != nil {
		return f.customer, nil
	}
	return &openmeter.Customer{ID: "cust-1", Key: key}, nil
}

func (f *fakeUsageReader) GetAccess(_ context.Context, _, _ string) (*openmeter.Access, error) {
	if f.accessErr != nil {
		return nil, f.accessErr
	}
	if f.access != nil {
		return f.access, nil
	}
	return &openmeter.Access{HasAccess: true, BalanceUSDMicros: 5_000_000, Source: "credits"}, nil
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
		SignerM2MClientID:        platformID,
		SignerM2MSecret:          platformSecret,
		OpenMeterTrialFeatureKey: "billable_spend",
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
	rec := doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage", "jwt")
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

func TestDeprecatedRotateAPIKeyRouteNotFound(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodPost, "/api/v1/apps/tenant-a/users/alice/api-key", "sk_demo")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestUsageSelfRequiresBearer(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage", "")
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
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage", "header.payload.signature")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if usageReader.lastQuery.MeterSlug != "billable_usd_micros" {
		t.Fatalf("meter slug = %q, want billable_usd_micros", usageReader.lastQuery.MeterSlug)
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

func TestBalanceSelfRequiresBearer(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/balance", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestBalanceSelfReturnsCallerLiveBalance(t *testing.T) {
	usageReader := &fakeUsageReader{
		access: &openmeter.Access{HasAccess: true, BalanceUSDMicros: 5_000_000, Source: "credits"},
	}
	h := newServer(usageReader, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/balance", "header.payload.signature")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if usageReader.lastSubject != "tenant-a:alice" {
		t.Fatalf("lookup key = %q, want tenant-a:alice", usageReader.lastSubject)
	}
	var body struct {
		Subject          string `json:"subject"`
		Feature          string `json:"feature"`
		HasAccess        bool   `json:"hasAccess"`
		BalanceUSDMicros int64  `json:"balanceUsdMicros"`
		Source           string `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Subject != "tenant-a:alice" || body.Feature != "billable_spend" {
		t.Fatalf("subject/feature = %q %q", body.Subject, body.Feature)
	}
	if !body.HasAccess || body.BalanceUSDMicros != 5_000_000 || body.Source != "credits" {
		t.Fatalf("body = %+v", body)
	}
}

func TestBalanceSelfNotFoundWithoutCustomer(t *testing.T) {
	h := newServer(&nilCustomerReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/balance", "header.payload.signature")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

type nilCustomerReader struct {
	fakeUsageReader
}

func (n *nilCustomerReader) LookupCustomerByKey(context.Context, string) (*openmeter.Customer, error) {
	return nil, nil
}

func TestUsageSelfRejectsInvalidVerifierResult(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{err: errors.New("invalid token")})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage", "bad-token")
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

func TestCreateUserUnavailableWithoutDeps(t *testing.T) {
	h := newServer(&fakeUsageReader{}, nil)
	rec := doBasic(t, h, http.MethodPost, "/api/v1/apps/app-1/users", platformID, platformSecret)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503", rec.Code)
	}
}

func TestOIDCTokenUnavailableWithoutHandler(t *testing.T) {
	h := newServer(&fakeUsageReader{}, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/oidc/token", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503: %s", rec.Code, rec.Body.String())
	}
}
