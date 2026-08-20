package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteBearerUnauthorizedOmitsDescriptionFromHeader(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	writeBearerUnauthorized(rec, `invalid_token`, `oops "quoted"\r\ninject`)
	got := rec.Header().Get("WWW-Authenticate")
	if strings.Contains(got, "error_description") {
		t.Fatalf("header should omit error_description, got %q", got)
	}
	if strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Fatalf("header must not contain CR/LF, got %q", got)
	}
	if rec.Body.String() == "" || !strings.Contains(rec.Body.String(), "oops") {
		t.Fatalf("JSON body should still carry the description, got %s", rec.Body.String())
	}
}
