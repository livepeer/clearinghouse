package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/apikey"
	auth0mgmt "github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mgmt"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

type fakeAuth0Users struct {
	rotateKey string
	rotateErr error
}

func (f *fakeAuth0Users) UpsertUser(context.Context, string, string, string, string, bool, string) (*auth0mgmt.UserRecord, error) {
	return nil, errors.New("not implemented")
}

func (f *fakeAuth0Users) RotateAPIKey(context.Context, string, string, string) (string, error) {
	if f.rotateErr != nil {
		return "", f.rotateErr
	}
	return f.rotateKey, nil
}

func newRotateTestServer(auth0 auth0UserAdmin) http.Handler {
	cfg := config.Config{
		Auth0Audience:     "livepeer-clearinghouse",
		SignerM2MClientID: "platform-m2m",
		SignerM2MSecret:   "platform-secret",
		APIKeyPrefix:      "sk_",
	}
	keyStore := &apikey.Store{
		Prefix: "sk_",
		Demo: map[string]apikey.DemoEntry{
			"sk_demo": {ClientID: "app-1", UserID: "user-1"},
		},
	}
	tokenHandler := tokenexchange.NewHandler(cfg, nil, keyStore, nil, nil)
	return NewServer(cfg, auth0, nil, nil, tokenHandler, nil, nil, nil).Handler()
}

func doRotateAPIKeySelf(t *testing.T, h http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/api-key", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func doRotateAPIKeySelfForm(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/api-key", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRotateAPIKeySelfRequiresSubjectToken(t *testing.T) {
	t.Parallel()

	h := newRotateTestServer(&fakeAuth0Users{rotateKey: "sk_new"})
	rec := doRotateAPIKeySelf(t, h, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestRotateAPIKeySelfBearerAPIKey(t *testing.T) {
	t.Parallel()

	h := newRotateTestServer(&fakeAuth0Users{rotateKey: "sk_new"})
	rec := doRotateAPIKeySelf(t, h, "sk_demo")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}

	var body rotateAPIKeyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ClientID != "app-1" || body.ExternalUserID != "user-1" || body.APIKey != "sk_new" || body.Status != "active" {
		t.Fatalf("body = %+v", body)
	}
}

func TestRotateAPIKeySelfFormSubjectToken(t *testing.T) {
	t.Parallel()

	h := newRotateTestServer(&fakeAuth0Users{rotateKey: "sk_new"})
	rec := doRotateAPIKeySelfForm(t, h, "subject_token=sk_demo")
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestRotateAPIKeySelfUserNotFound(t *testing.T) {
	t.Parallel()

	h := newRotateTestServer(&fakeAuth0Users{rotateErr: auth0mgmt.ErrUserNotFound})
	rec := doRotateAPIKeySelf(t, h, "sk_demo")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}

func TestRotateAPIKeySelfRejectsInvalidSubjectToken(t *testing.T) {
	t.Parallel()

	h := newRotateTestServer(&fakeAuth0Users{rotateKey: "sk_new"})
	rec := doRotateAPIKeySelf(t, h, "not-a-valid-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}
