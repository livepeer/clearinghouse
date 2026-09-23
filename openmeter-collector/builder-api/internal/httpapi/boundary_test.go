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
	lastQuery      openmeter.UsageQuery
	rows           []openmeter.UsageRow
	customer       *openmeter.Customer
	access         *openmeter.Access
	lookupErr      error
	accessErr      error
	lastSubject    string
	billingRefs    openmeter.StripeBillingRefs
	billingErr     error
	checkout       *openmeter.StripeCheckoutSession
	checkoutErr    error
	lastCustomerID string
	lastSuccessURL string
	lastCancelURL  string
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

func (f *fakeUsageReader) GetStripeBillingRefs(_ context.Context, customerID string) (openmeter.StripeBillingRefs, error) {
	f.lastCustomerID = customerID
	if f.billingErr != nil {
		return openmeter.StripeBillingRefs{}, f.billingErr
	}
	return f.billingRefs, nil
}

func (f *fakeUsageReader) CreateStripeCheckoutSession(_ context.Context, customerID, successURL, cancelURL string) (*openmeter.StripeCheckoutSession, error) {
	f.lastCustomerID = customerID
	f.lastSuccessURL = successURL
	f.lastCancelURL = cancelURL
	if f.checkoutErr != nil {
		return nil, f.checkoutErr
	}
	if f.checkout != nil {
		return f.checkout, nil
	}
	return &openmeter.StripeCheckoutSession{
		CheckoutURL: "https://checkout.stripe.com/c/pay/cs_test_1",
		SessionID:   "cs_test_1",
	}, nil
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

func TestPaymentMethodSelfRequiresBearer(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/payment-method", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestPaymentMethodSelfNotFoundWithoutCustomer(t *testing.T) {
	h := newServer(&nilCustomerReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/payment-method", "header.payload.signature")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404: %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentMethodSelfReturnsBillingRefs(t *testing.T) {
	usageReader := &fakeUsageReader{
		billingRefs: openmeter.StripeBillingRefs{
			StripeCustomerID:        "cus_123",
			DefaultPaymentMethodID:  "pm_abc",
			HasDefaultPaymentMethod: true,
		},
	}
	h := newServer(usageReader, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/payment-method", "header.payload.signature")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if usageReader.lastSubject != "tenant-a:alice" || usageReader.lastCustomerID != "cust-1" {
		t.Fatalf("lookup subject=%q customer=%q", usageReader.lastSubject, usageReader.lastCustomerID)
	}
	var body struct {
		Subject                 string `json:"subject"`
		HasDefaultPaymentMethod bool   `json:"hasDefaultPaymentMethod"`
		StripeCustomerID        string `json:"stripeCustomerId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Subject != "tenant-a:alice" || !body.HasDefaultPaymentMethod || body.StripeCustomerID != "cus_123" {
		t.Fatalf("%+v", body)
	}
}

func TestPaymentMethodSelfGETEmptyBilling(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/payment-method", "header.payload.signature")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		HasDefaultPaymentMethod bool   `json:"hasDefaultPaymentMethod"`
		StripeCustomerID        string `json:"stripeCustomerId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.HasDefaultPaymentMethod || body.StripeCustomerID != "" {
		t.Fatalf("%+v", body)
	}
}

func TestPaymentMethodSelfGETBadGatewayOnBillingError(t *testing.T) {
	h := newServer(&fakeUsageReader{billingErr: errors.New("konnect down")}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/payment-method", "header.payload.signature")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d, want 502: %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentMethodSelfPOSTCreatesCheckout(t *testing.T) {
	usageReader := &fakeUsageReader{}
	h := newServer(usageReader, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/payment-method", strings.NewReader(`{
		"successUrl":"https://app.example.com/ok",
		"cancelUrl":"https://app.example.com/cancel"
	}`))
	req.Header.Set("Authorization", "Bearer header.payload.signature")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if usageReader.lastSuccessURL != "https://app.example.com/ok" || usageReader.lastCancelURL != "https://app.example.com/cancel" {
		t.Fatalf("urls = %q %q", usageReader.lastSuccessURL, usageReader.lastCancelURL)
	}
	var body struct {
		CheckoutURL string `json:"checkoutUrl"`
		SessionID   string `json:"sessionId"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.CheckoutURL != "https://checkout.stripe.com/c/pay/cs_test_1" || body.SessionID != "cs_test_1" {
		t.Fatalf("%+v", body)
	}
}

func TestPaymentMethodSelfPOSTRejectsUserinfo(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/payment-method", strings.NewReader(`{
		"successUrl":"https://evil.com@app.example.com/ok",
		"cancelUrl":"https://app.example.com/cancel"
	}`))
	req.Header.Set("Authorization", "Bearer header.payload.signature")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentMethodSelfPOSTRejectsNonHTTPS(t *testing.T) {
	h := newServer(&fakeUsageReader{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/payment-method", strings.NewReader(`{
		"successUrl":"http://app.example.com/ok",
		"cancelUrl":"https://app.example.com/cancel"
	}`))
	req.Header.Set("Authorization", "Bearer header.payload.signature")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPaymentMethodSelfPOSTCheckoutFailure(t *testing.T) {
	h := newServer(&fakeUsageReader{checkoutErr: errors.New("no stripe app")}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/payment-method", strings.NewReader(`{
		"successUrl":"https://app.example.com/ok",
		"cancelUrl":"https://app.example.com/cancel"
	}`))
	req.Header.Set("Authorization", "Bearer header.payload.signature")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
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
