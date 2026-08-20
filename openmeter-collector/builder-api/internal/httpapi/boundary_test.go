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
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tenantauth"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

// fakeAdmin records the subjects it was asked for so tests can assert that a
// request never reached the metering layer with another tenant's scope.
type fakeAdmin struct {
	lastQuery     openmeter.UsageQuery
	lastAccessID  string
	lastGrantID   string
	lastLookupKey string
	calls         int
	// rows is returned verbatim, letting a test simulate a backend that
	// ignores the subject filter.
	rows      []openmeter.UsageRow
	keys      map[string][]string
	customers map[string]*openmeter.Customer
}

func (f *fakeAdmin) QueryUsage(_ context.Context, q openmeter.UsageQuery) ([]openmeter.UsageRow, error) {
	f.calls++
	f.lastQuery = q
	if f.rows != nil {
		return f.rows, nil
	}
	rows := make([]openmeter.UsageRow, 0, len(q.Subjects))
	for _, s := range q.Subjects {
		rows = append(rows, openmeter.UsageRow{Subject: s, Value: 1})
	}
	return rows, nil
}

func (f *fakeAdmin) ListCustomerKeysForClient(_ context.Context, clientID string) ([]string, error) {
	f.calls++
	return f.keys[clientID], nil
}

func (f *fakeAdmin) LookupCustomerByKey(_ context.Context, key string) (*openmeter.Customer, error) {
	f.calls++
	f.lastLookupKey = key
	if f.customers != nil {
		return f.customers[key], nil
	}
	// Default: key maps to a deterministic ULID-shaped id so handlers exercise
	// the resolve-then-call path without each test seeding a map.
	return &openmeter.Customer{ID: "01CUSTOMER" + strings.ReplaceAll(key, ":", ""), Key: key}, nil
}

func (f *fakeAdmin) GetAccess(_ context.Context, customerID, _ string) (*openmeter.Access, error) {
	f.calls++
	f.lastAccessID = customerID
	return &openmeter.Access{HasAccess: true, BalanceUSDMicros: 42, Source: "credits"}, nil
}

func (f *fakeAdmin) EnsureTrialGrant(_ context.Context, customerID, _, _ string, _ int64) error {
	f.calls++
	f.lastGrantID = customerID
	return nil
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

const (
	platformID     = "platform-m2m"
	platformSecret = "platform-secret"
)

func newBoundaryServer(admin httpapi.OpenMeterAdmin, verifier tokenexchange.UserTokenVerifier) http.Handler {
	cfg := config.Config{
		SignerM2MClientID:        platformID,
		SignerM2MSecret:          platformSecret,
		OpenMeterTrialFeatureKey: "network_spend",
	}
	auth := tenantauth.New(platformID, platformSecret, map[string]string{
		"tenant-a": "secret-a",
		"tenant-b": "secret-b",
	})
	return httpapi.NewServer(cfg, nil, nil, nil, nil, verifier, nil, auth, admin).Handler()
}

func do(t *testing.T, h http.Handler, method, target, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(`{"amountUsdMicros":5}`))
	req.Header.Set("Content-Type", "application/json")
	if user != "" || pass != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
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

// adminRoutes is every tenant-scoped route, used to assert the boundary holds
// uniformly rather than on whichever route a test happened to pick.
func adminRoutes(clientID string) []struct{ method, path string } {
	return []struct{ method, path string }{
		{http.MethodGet, "/api/v1/apps/" + clientID + "/usage?meter=billable_usd_micros"},
		{http.MethodGet, "/api/v1/apps/" + clientID + "/users/alice/access"},
		{http.MethodPost, "/api/v1/apps/" + clientID + "/users/alice/grants"},
		{http.MethodPost, "/api/v1/apps/" + clientID + "/users"},
	}
}

func TestCrossTenantAccessIsRefusedOnEveryRoute(t *testing.T) {
	admin := &fakeAdmin{keys: map[string][]string{"tenant-b": {"tenant-b:victim"}}}
	h := newBoundaryServer(admin, nil)

	for _, rt := range adminRoutes("tenant-b") {
		// tenant-a authenticates correctly, then asks for tenant-b.
		rec := do(t, h, rt.method, rt.path, "tenant-a", "secret-a")
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: cross-tenant access returned %d, want 404", rt.method, rt.path, rec.Code)
		}
		if body := rec.Body.String(); strings.Contains(body, "victim") {
			t.Errorf("%s %s: response leaked foreign data: %s", rt.method, rt.path, body)
		}
	}
	if admin.calls != 0 {
		t.Errorf("cross-tenant requests reached the metering layer %d times, want 0", admin.calls)
	}
}

func TestUnauthenticatedAndBadCredentialsAreRefused(t *testing.T) {
	admin := &fakeAdmin{}
	h := newBoundaryServer(admin, nil)

	for _, rt := range adminRoutes("tenant-a") {
		for _, c := range []struct{ name, user, pass string }{
			{"no credentials", "", ""},
			{"wrong secret", "tenant-a", "secret-b"},
			{"unknown tenant", "tenant-z", "secret-z"},
		} {
			rec := do(t, h, rt.method, rt.path, c.user, c.pass)
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s [%s]: got %d, want 401", rt.method, rt.path, c.name, rec.Code)
			}
		}
	}
	if admin.calls != 0 {
		t.Errorf("unauthenticated requests reached the metering layer %d times, want 0", admin.calls)
	}
}

func TestTenantReachesOnlyItsOwnSubjects(t *testing.T) {
	admin := &fakeAdmin{keys: map[string][]string{
		"tenant-a": {"tenant-a:alice", "tenant-a:bob"},
		"tenant-b": {"tenant-b:victim"},
	}}
	h := newBoundaryServer(admin, nil)

	rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", "tenant-a", "secret-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("own-tenant usage returned %d: %s", rec.Code, rec.Body.String())
	}
	for _, subject := range admin.lastQuery.Subjects {
		if !strings.HasPrefix(subject, "tenant-a:") {
			t.Errorf("query included foreign subject %q", subject)
		}
	}
	if len(admin.lastQuery.Subjects) != 2 {
		t.Errorf("expected 2 subjects, got %v", admin.lastQuery.Subjects)
	}
}

func TestPlatformAdminMayReachAnyTenant(t *testing.T) {
	admin := &fakeAdmin{keys: map[string][]string{"tenant-b": {"tenant-b:victim"}}}
	h := newBoundaryServer(admin, nil)

	rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-b/usage?meter=m", platformID, platformSecret)
	if rec.Code != http.StatusOK {
		t.Fatalf("platform admin got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSubjectCannotBeSmuggledViaUserID covers the one input that becomes part
// of the customer key. A colon would make "clientId:externalUserId" ambiguous
// about where the tenant ends.
func TestSubjectCannotBeSmuggledViaUserID(t *testing.T) {
	admin := &fakeAdmin{}
	h := newBoundaryServer(admin, nil)

	hostile := []string{
		"tenant-b:victim",
		":tenant-b",
		"alice:",
	}
	for _, id := range hostile {
		rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-a/users/"+id+"/access", "tenant-a", "secret-a")
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusNotFound {
			t.Errorf("externalUserId %q: got %d, want 400 or 404", id, rec.Code)
		}
		if admin.lastAccessID != "" {
			t.Errorf("externalUserId %q reached GetAccess with %q", id, admin.lastAccessID)
		}
		if admin.lastLookupKey != "" && !strings.HasPrefix(admin.lastLookupKey, "tenant-a:") {
			t.Errorf("externalUserId %q looked up out-of-tenant key %q", id, admin.lastLookupKey)
		}
	}

	// Same via the query parameter on the usage route.
	rec := do(t, h, http.MethodGet,
		"/api/v1/apps/tenant-a/usage?meter=m&externalUserId=tenant-b%3Avictim", "tenant-a", "secret-a")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("colon in externalUserId query param: got %d, want 400", rec.Code)
	}
}

func TestAccessAndGrantResolveCustomerID(t *testing.T) {
	admin := &fakeAdmin{
		customers: map[string]*openmeter.Customer{
			"tenant-a:alice": {ID: "01ALICEULID00000000000001", Key: "tenant-a:alice"},
		},
	}
	h := newBoundaryServer(admin, nil)

	rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-a/users/alice/access", "tenant-a", "secret-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("access: %d %s", rec.Code, rec.Body.String())
	}
	if admin.lastLookupKey != "tenant-a:alice" {
		t.Fatalf("lookup key = %q", admin.lastLookupKey)
	}
	if admin.lastAccessID != "01ALICEULID00000000000001" {
		t.Fatalf("GetAccess received %q, want Konnect customer id", admin.lastAccessID)
	}

	rec = do(t, h, http.MethodPost, "/api/v1/apps/tenant-a/users/alice/grants", "tenant-a", "secret-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("grant: %d %s", rec.Code, rec.Body.String())
	}
	if admin.lastGrantID != "01ALICEULID00000000000001" {
		t.Fatalf("EnsureTrialGrant received %q, want Konnect customer id", admin.lastGrantID)
	}
}

func TestAccessMissingCustomerIs404(t *testing.T) {
	admin := &fakeAdmin{customers: map[string]*openmeter.Customer{}}
	h := newBoundaryServer(admin, nil)
	rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-a/users/nobody/access", "tenant-a", "secret-a")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
	if admin.lastAccessID != "" {
		t.Fatalf("GetAccess should not run for a missing customer")
	}
}

// TestForeignRowsAreFilteredOut simulates a metering backend that ignores the
// subject filter and returns the whole meter.
func TestForeignRowsAreFilteredOut(t *testing.T) {
	admin := &fakeAdmin{
		keys: map[string][]string{"tenant-a": {"tenant-a:alice"}},
		rows: []openmeter.UsageRow{
			{Subject: "tenant-a:alice", Value: 1},
			{Subject: "tenant-b:victim", Value: 999},
			{Subject: "tenant-a-corp:eve", Value: 7},
			// An aggregate row carries no tenant evidence and must not pass.
			{Subject: "", Value: 12345},
		},
	}
	h := newBoundaryServer(admin, nil)

	rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=m", "tenant-a", "secret-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Rows []openmeter.UsageRow `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Rows) != 1 || body.Rows[0].Subject != "tenant-a:alice" {
		t.Fatalf("foreign rows survived filtering: %+v", body.Rows)
	}
}

// TestPrefixIsMatchedOnTheSeparator stops client id "tenant-a" from also
// matching customer keys belonging to "tenant-a-corp".
func TestPrefixIsMatchedOnTheSeparator(t *testing.T) {
	admin := &fakeAdmin{}
	h := newBoundaryServer(admin, nil)

	rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-a-corp/usage?meter=m", "tenant-a", "secret-a")
	if rec.Code != http.StatusNotFound {
		t.Errorf("neighbouring client id returned %d, want 404", rec.Code)
	}
}

func TestUsageSelfRequiresBearer(t *testing.T) {
	h := newBoundaryServer(&fakeAdmin{}, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})

	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage?meter=billable_usd_micros", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Bearer") {
		t.Fatalf("expected WWW-Authenticate Bearer, got %q", rec.Header().Get("WWW-Authenticate"))
	}
}

func TestUsageSelfReturnsCallerOnly(t *testing.T) {
	admin := &fakeAdmin{
		rows: []openmeter.UsageRow{
			{Subject: "tenant-a:alice", Value: 1},
			{Subject: "tenant-a:bob", Value: 99},
			{Subject: "tenant-b:victim", Value: 123},
		},
	}
	h := newBoundaryServer(admin, fakeUsageVerifier{clientID: "tenant-a", externalUserID: "alice"})

	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage?meter=billable_usd_micros", "header.payload.signature")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if len(admin.lastQuery.Subjects) != 1 || admin.lastQuery.Subjects[0] != "tenant-a:alice" {
		t.Fatalf("query subjects = %v, want [tenant-a:alice]", admin.lastQuery.Subjects)
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
	h := newBoundaryServer(&fakeAdmin{}, fakeUsageVerifier{err: errors.New("invalid token")})

	rec := doBearer(t, h, http.MethodGet, "/api/v1/users/me/usage?meter=billable_usd_micros", "bad-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestUnconfiguredBoundaryFailsClosed(t *testing.T) {
	// No authenticator wired at all: routes must refuse rather than default open.
	srv := httpapi.NewServer(config.Config{}, nil, nil, nil, nil, nil, nil, nil, &fakeAdmin{})
	rec := do(t, srv.Handler(), http.MethodGet,
		"/api/v1/apps/tenant-a/usage?meter=m", platformID, platformSecret)
	if rec.Code == http.StatusOK {
		t.Fatalf("unconfigured boundary served a request: %d %s", rec.Code, rec.Body.String())
	}
}

// TestBoundaryEndToEndWithRealClient wires the real OpenMeter client through
// the real handler against a fake Konnect backend, so the assertion covers the
// wire request the metering layer actually sends — not just what the handler
// intended.
func TestBoundaryEndToEndWithRealClient(t *testing.T) {
	customers := []openmeter.Customer{
		{Key: "tenant-a:alice"},
		{Key: "tenant-b:victim"},
	}
	var meterQueries []([]string)

	konnect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/meters":
			// Konnect resolves meter key → ULID before POST /meters/{id}/query.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "01METERTEST000000000000001", "key": "billable_usd_micros"}},
			})
		case strings.HasPrefix(r.URL.Path, "/customers"):
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": customers})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/query"):
			var body struct {
				Filters struct {
					Dimensions map[string]struct {
						In []string `json:"in"`
					} `json:"dimensions"`
				} `json:"filters"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode query body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			subjects := body.Filters.Dimensions["subject"].In
			meterQueries = append(meterQueries, subjects)
			rows := []map[string]any{}
			for _, s := range subjects {
				rows = append(rows, map[string]any{
					"value":      "1",
					"from":       "2026-01-01T00:00:00Z",
					"to":         "2026-01-02T00:00:00Z",
					"dimensions": map[string]string{"subject": s},
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer konnect.Close()

	cfg := config.Config{SignerM2MClientID: platformID, SignerM2MSecret: platformSecret}
	auth := tenantauth.New(platformID, platformSecret, map[string]string{"tenant-a": "secret-a"})
	h := httpapi.NewServer(cfg, nil, nil, nil, nil, nil, nil, auth, openmeter.New(konnect.URL, "kpat_test")).Handler()

	// tenant-a reads its own app: only its own subject reaches the backend,
	// even though the shared org contains tenant-b's customer.
	rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", "tenant-a", "secret-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("own-tenant read: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "victim") {
		t.Fatalf("response leaked another tenant's subject: %s", rec.Body.String())
	}
	if len(meterQueries) != 1 || len(meterQueries[0]) != 1 || meterQueries[0][0] != "tenant-a:alice" {
		t.Fatalf("backend received subjects %v, want exactly [tenant-a:alice]", meterQueries)
	}

	// tenant-a reads tenant-b: refused before any backend call.
	before := len(meterQueries)
	rec = do(t, h, http.MethodGet, "/api/v1/apps/tenant-b/usage?meter=billable_usd_micros", "tenant-a", "secret-a")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant read returned %d, want 404", rec.Code)
	}
	if len(meterQueries) != before {
		t.Fatalf("cross-tenant read reached the metering backend: %v", meterQueries)
	}
}

// TestGrantResponseAmountIsANumber pins amountUsdMicros as a JSON number. It
// was serialised with strconv.FormatInt, which contradicted the request schema
// and would break any client decoding it as an integer.
func TestGrantResponseAmountIsANumber(t *testing.T) {
	h := newBoundaryServer(&fakeAdmin{}, nil)
	rec := do(t, h, http.MethodPost,
		"/api/v1/apps/tenant-a/users/alice/grants", "tenant-a", "secret-a")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := string(raw["amountUsdMicros"]); got != "5" {
		t.Fatalf("amountUsdMicros = %s, want the number 5", got)
	}
	var n int64
	if err := json.Unmarshal(raw["amountUsdMicros"], &n); err != nil {
		t.Fatalf("amountUsdMicros is not decodable as an integer: %v", err)
	}
}
