package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// subjectTokenFromRequest reads the end-user credential from Authorization: Bearer,
// JSON {"subject_token":"..."}, or form subject_token.
func subjectTokenFromRequest(r *http.Request) (string, error) {
	if token := BearerToken(r); token != "" {
		return token, nil
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return "", err
	}
	defer r.Body.Close()

	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	switch {
	case strings.HasPrefix(contentType, "application/json"):
		var body subjectTokenJSONBody
		if err := json.Unmarshal(raw, &body); err != nil {
			return "", err
		}
		return strings.TrimSpace(body.SubjectToken), nil
	case strings.HasPrefix(contentType, "application/x-www-form-urlencoded"):
		form, err := url.ParseQuery(string(raw))
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(form.Get("subject_token")), nil
	default:
		return "", nil
	}
}
