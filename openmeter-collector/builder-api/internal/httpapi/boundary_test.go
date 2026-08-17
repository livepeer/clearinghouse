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

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/apikey"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/httpapi"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tenantauth"
)

type fakeAdmin struct {
	lastQuery openmeter.UsageQuery
	calls     int
	rows      []openmeter.UsageRow
	err       error
}

func (f *fakeAdmin) QueryUsage(_ context.Context, q openmeter.UsageQuery) ([]openmeter.UsageRow, error) {
	f.calls++
	f.lastQuery = q
	if f.err != nil {
		return nil, f.err
	}
	if f.rows != nil {
		return f.rows, nil
	}
	return []openmeter.UsageRow{}, nil
}

type fakeVerifier struct {
	clientID string
	userID   string
	err      error
}

func (f *fakeVerifier) VerifyUserAccessToken(_ context.Context, _, expectedClientID string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	if expectedClientID != "" && f.clientID != "" && expectedClientID != f.clientID {
		return "", "", errors.New("client mismatch")
	}
	return f.clientID, f.userID, nil
}

const (
	platformID     = "platform-m2m"
	platformSecret = "platform-secret"
	apiKeyPrefix   = "sk_"
)

func newUsageServer(admin httpapi.OpenMeterAdmin, verifier *fakeVerifier, keys *apikey.Store) http.Handler {
	cfg := config.Config{
		SignerM2MClientID: platformID,
		SignerM2MSecret:   platformSecret,
		APIKeyPrefix:      apiKeyPrefix,
	}
	auth := tenantauth.New(platformID, platformSecret, map[string]string{
		"tenant-a": "secret-a",
		"tenant-b": "secret-b",
	})
	var v interface {
		VerifyUserAccessToken(context.Context, string, string) (string, string, error)
	}
	if verifier != nil {
		v = verifier
	}
	return httpapi.NewServer(cfg, nil, nil, nil, nil, nil, auth, admin, v, keys).Handler()
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
	req := httptest.NewRequest(method, target, strings.NewReader(`{"externalUserId":"alice"}`))
	req.Header.Set("Content-Type", "application/json")
	if user != "" || pass != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// jwtShape is three base64url-ish segments so resolveUsageSubject treats it as a JWT.
const jwtShape = "aaa.bbb.ccc"

func TestUsageRejectsBasicAndMissingBearer(t *testing.T) {
	admin := &fakeAdmin{}
	h := newUsageServer(admin, &fakeVerifier{clientID: "tenant-a", userID: "alice"}, nil)

	for _, c := range []struct {
		name string
		rec  *httptest.ResponseRecorder
	}{
		{"no auth", doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", "")},
		{"basic tenant", doBasic(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", "tenant-a", "secret-a")},
		{"basic platform", doBasic(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", platformID, platformSecret)},
	} {
		if c.rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: got %d, want 401", c.name, c.rec.Code)
		}
	}
	if admin.calls != 0 {
		t.Errorf("unauthenticated usage reached metering %d times", admin.calls)
	}
}

func TestUsageJWTHappyPath(t *testing.T) {
	admin := &fakeAdmin{
		rows: []openmeter.UsageRow{
			{Value: 1, GroupBy: map[string]string{"client_id": "tenant-a", "external_user_id": "alice"}},
			{Value: 9, GroupBy: map[string]string{"client_id": "tenant-a", "external_user_id": "bob"}},
			{Value: 8, GroupBy: map[string]string{"client_id": "tenant-b", "external_user_id": "alice"}},
		},
	}
	h := newUsageServer(admin, &fakeVerifier{clientID: "tenant-a", userID: "alice"}, nil)

	rec := doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", jwtShape)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if admin.lastQuery.ClientID != "tenant-a" {
		t.Fatalf("query client = %q", admin.lastQuery.ClientID)
	}
	var body struct {
		Actor string               `json:"actor"`
		Rows  []openmeter.UsageRow `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Actor != "alice" {
		t.Fatalf("actor = %q", body.Actor)
	}
	if len(body.Rows) != 1 || body.Rows[0].Value != 1 {
		t.Fatalf("rows = %+v", body.Rows)
	}
}

func TestUsageCrossAppPathIs404(t *testing.T) {
	admin := &fakeAdmin{}
	h := newUsageServer(admin, &fakeVerifier{clientID: "tenant-a", userID: "alice"}, nil)

	rec := doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-b/usage?meter=billable_usd_micros", jwtShape)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
	if admin.calls != 0 {
		t.Fatalf("cross-app usage reached metering")
	}
}

func TestUsageExternalUserIdMustMatchActor(t *testing.T) {
	admin := &fakeAdmin{}
	h := newUsageServer(admin, &fakeVerifier{clientID: "tenant-a", userID: "alice"}, nil)

	rec := doBearer(t, h, http.MethodGet,
		"/api/v1/apps/tenant-a/usage?meter=billable_usd_micros&externalUserId=bob", jwtShape)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
	if admin.calls != 0 {
		t.Fatalf("mismatched externalUserId reached metering")
	}
}

func TestUsageRejectsUnknownMeter(t *testing.T) {
	admin := &fakeAdmin{}
	h := newUsageServer(admin, &fakeVerifier{clientID: "tenant-a", userID: "alice"}, nil)

	rec := doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=not_a_meter", jwtShape)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400", rec.Code)
	}
}

func TestUsageAPIKeyHappyPath(t *testing.T) {
	admin := &fakeAdmin{
		rows: []openmeter.UsageRow{
			{Value: 3, GroupBy: map[string]string{"client_id": "tenant-a", "external_user_id": "alice"}},
		},
	}
	keys, err := apikey.LoadDemoStore(`{"sk_test_alice":{"clientId":"tenant-a","userId":"alice"}}`)
	if err != nil {
		t.Fatal(err)
	}
	store := &apikey.Store{Prefix: apiKeyPrefix, Demo: keys}
	h := newUsageServer(admin, nil, store)

	rec := doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", "sk_test_alice")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
	if len(admin.lastQuery.ClientID) == 0 {
		t.Fatal("expected client filter")
	}
}

func TestCreateUserStillUsesTenantBasic(t *testing.T) {
	h := newUsageServer(&fakeAdmin{}, nil, nil)
	rec := doBasic(t, h, http.MethodPost, "/api/v1/apps/tenant-b/users", "tenant-a", "secret-a")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant create user got %d, want 404", rec.Code)
	}
	rec = doBasic(t, h, http.MethodPost, "/api/v1/apps/tenant-a/users", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("create user without Basic got %d, want 401", rec.Code)
	}
}

func TestAccessAndGrantsRoutesRemoved(t *testing.T) {
	h := newUsageServer(&fakeAdmin{}, &fakeVerifier{clientID: "tenant-a", userID: "alice"}, nil)
	for _, path := range []string{
		"/api/v1/apps/tenant-a/users/alice/access",
		"/api/v1/apps/tenant-a/users/alice/grants",
	} {
		rec := doBearer(t, h, http.MethodGet, path, jwtShape)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			// POST grants
			rec = doBasic(t, h, http.MethodPost, path, "tenant-a", "secret-a")
		}
		if rec.Code == http.StatusOK {
			t.Fatalf("%s still served OK", path)
		}
	}
}

func TestBoundaryEndToEndWithRealClient(t *testing.T) {
	var meterQueries []string

	konnect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/meters":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "01METERTEST000000000000001", "key": "billable_usd_micros"}},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/query"):
			var body struct {
				Filters struct {
					Dimensions map[string]struct {
						Eq string `json:"eq"`
					} `json:"dimensions"`
				} `json:"filters"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode query body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			clientID := body.Filters.Dimensions["client_id"].Eq
			meterQueries = append(meterQueries, clientID)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"value": "1",
						"from":  "2026-01-01T00:00:00Z",
						"to":    "2026-01-02T00:00:00Z",
						"dimensions": map[string]string{
							"client_id":        "tenant-a",
							"external_user_id": "alice",
						},
					},
					{
						"value": "999",
						"from":  "2026-01-01T00:00:00Z",
						"to":    "2026-01-02T00:00:00Z",
						"dimensions": map[string]string{
							"client_id":        "tenant-a",
							"external_user_id": "victim",
						},
					},
				},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer konnect.Close()

	cfg := config.Config{APIKeyPrefix: apiKeyPrefix}
	h := httpapi.NewServer(
		cfg, nil, nil, nil, nil, nil, nil,
		openmeter.New(konnect.URL, "kpat_test"),
		&fakeVerifier{clientID: "tenant-a", userID: "alice"},
		nil,
	).Handler()

	rec := doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-a/usage?meter=billable_usd_micros", jwtShape)
	if rec.Code != http.StatusOK {
		t.Fatalf("own usage: %d %s", rec.Code, rec.Body.String())
	}
	if len(meterQueries) != 1 || meterQueries[0] != "tenant-a" {
		t.Fatalf("backend client filter = %v", meterQueries)
	}
	if strings.Contains(rec.Body.String(), "victim") {
		t.Fatalf("response leaked another actor: %s", rec.Body.String())
	}

	before := len(meterQueries)
	rec = doBearer(t, h, http.MethodGet, "/api/v1/apps/tenant-b/usage?meter=billable_usd_micros", jwtShape)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-app got %d, want 404", rec.Code)
	}
	if len(meterQueries) != before {
		t.Fatalf("cross-app reached metering: %v", meterQueries)
	}
}
