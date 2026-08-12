package httpapi_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/httpapi"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tenantauth"
)

// fakeAdmin records the subjects it was asked for so tests can assert that a
// request never reached the metering layer with another tenant's scope.
type fakeAdmin struct {
	lastQuery    openmeter.UsageQuery
	lastAccessID string
	lastGrantID  string
	calls        int
	// rows is returned verbatim, letting a test simulate a backend that
	// ignores the subject filter.
	rows []openmeter.UsageRow
	keys map[string][]string
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

const (
	platformID     = "platform-m2m"
	platformSecret = "platform-secret"
)

func newBoundaryServer(admin httpapi.OpenMeterAdmin) http.Handler {
	cfg := config.Config{
		SignerM2MClientID:        platformID,
		SignerM2MSecret:          platformSecret,
		OpenMeterTrialFeatureKey: "network_spend",
	}
	auth := tenantauth.New(platformID, platformSecret, map[string]string{
		"tenant-a": "secret-a",
		"tenant-b": "secret-b",
	})
	return httpapi.NewServer(cfg, nil, nil, nil, nil, nil, auth, admin).Handler()
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
	h := newBoundaryServer(admin)

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
	h := newBoundaryServer(admin)

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
	h := newBoundaryServer(admin)

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
	h := newBoundaryServer(admin)

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
	h := newBoundaryServer(admin)

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
		if admin.lastAccessID != "" && !strings.HasPrefix(admin.lastAccessID, "tenant-a:") {
			t.Errorf("externalUserId %q produced out-of-tenant customer key %q", id, admin.lastAccessID)
		}
	}

	// Same via the query parameter on the usage route.
	rec := do(t, h, http.MethodGet,
		"/api/v1/apps/tenant-a/usage?meter=m&externalUserId=tenant-b%3Avictim", "tenant-a", "secret-a")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("colon in externalUserId query param: got %d, want 400", rec.Code)
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
	h := newBoundaryServer(admin)

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
	h := newBoundaryServer(admin)

	rec := do(t, h, http.MethodGet, "/api/v1/apps/tenant-a-corp/usage?meter=m", "tenant-a", "secret-a")
	if rec.Code != http.StatusNotFound {
		t.Errorf("neighbouring client id returned %d, want 404", rec.Code)
	}
}

func TestUnconfiguredBoundaryFailsClosed(t *testing.T) {
	// No authenticator wired at all: routes must refuse rather than default open.
	srv := httpapi.NewServer(config.Config{}, nil, nil, nil, nil, nil, nil, &fakeAdmin{})
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
		case strings.HasPrefix(r.URL.Path, "/customers"):
			if r.URL.Query().Get("page") != "1" {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": customers})
		case strings.Contains(r.URL.Path, "/query"):
			subjects := r.URL.Query()["subject"]
			meterQueries = append(meterQueries, subjects)
			rows := []map[string]any{}
			for _, s := range subjects {
				rows = append(rows, map[string]any{"subject": s, "value": 1})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": rows})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer konnect.Close()

	cfg := config.Config{SignerM2MClientID: platformID, SignerM2MSecret: platformSecret}
	auth := tenantauth.New(platformID, platformSecret, map[string]string{"tenant-a": "secret-a"})
	h := httpapi.NewServer(cfg, nil, nil, nil, nil, nil, auth, openmeter.New(konnect.URL, "kpat_test")).Handler()

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
	h := newBoundaryServer(&fakeAdmin{})
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
