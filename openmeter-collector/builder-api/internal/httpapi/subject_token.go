package httpapi

import (
	"io"
	"net/http"
	"net/url"
	"strings"
)

// subjectTokenFromRequest reads the end-user credential from Authorization: Bearer
// or, for POST form requests, subject_token (same tokens accepted as RFC 8693 exchange).
func subjectTokenFromRequest(r *http.Request) (string, error) {
	if token := BearerToken(r); token != "" {
		return token, nil
	}

	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		return "", nil
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return "", err
	}
	defer r.Body.Close()

	form, err := url.ParseQuery(string(raw))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(form.Get("subject_token")), nil
}
