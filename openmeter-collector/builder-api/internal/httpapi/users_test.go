package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	auth0mgmt "github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mgmt"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
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
		SignerM2MClientID: "platform-m2m",
		SignerM2MSecret:   "platform-secret",
		APIKeyPrefix:      "sk_",
	}
	return NewServer(cfg, auth0, nil, nil, nil, nil, nil, nil).Handler()
}

func doRotateAPIKey(t *testing.T, h http.Handler, clientID, externalUserID, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	target := "/api/v1/apps/" + clientID + "/users/" + externalUserID + "/api-key"
	req := httptest.NewRequest(http.MethodPost, target, nil)
	if user != "" || pass != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+pass)))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRotateAPIKeyRequiresM2MBasic(t *testing.T) {
	t.Parallel()

	h := newRotateTestServer(&fakeAuth0Users{rotateKey: "sk_new"})
	rec := doRotateAPIKey(t, h, "app-1", "user-1", "", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", rec.Code)
	}
}

func TestRotateAPIKeySuccess(t *testing.T) {
	t.Parallel()

	h := newRotateTestServer(&fakeAuth0Users{rotateKey: "sk_new"})
	rec := doRotateAPIKey(t, h, "app-1", "user-1", "platform-m2m", "platform-secret")
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

func TestRotateAPIKeyUserNotFound(t *testing.T) {
	t.Parallel()

	h := newRotateTestServer(&fakeAuth0Users{rotateErr: auth0mgmt.ErrUserNotFound})
	rec := doRotateAPIKey(t, h, "app-1", "missing-user", "platform-m2m", "platform-secret")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", rec.Code)
	}
}
