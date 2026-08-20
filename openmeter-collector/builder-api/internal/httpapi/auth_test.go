package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestM2MAuthRejectsEmptyExpectedCredentials(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetBasicAuth("", "")
	if M2MAuth(req, "", "") {
		t.Fatal("expected false when expected credentials are empty")
	}
}

func TestM2MAuthRejectsEmptyExpectedSecret(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetBasicAuth("client", "secret")
	if M2MAuth(req, "client", "") {
		t.Fatal("expected false when expected secret is empty")
	}
}

func TestM2MAuthAcceptsValidCredentials(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetBasicAuth("client", "secret")
	if !M2MAuth(req, "client", "secret") {
		t.Fatal("expected true for matching credentials")
	}
}
