package httpapi

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
)

// M2MAuth validates HTTP Basic auth against the configured signer M2M client.
func M2MAuth(r *http.Request, expectedClientID, expectedSecret string) bool {
	clientID, secret, ok := ClientCredentialsFromRequest(r, nil)
	if !ok {
		return false
	}
	return clientID == expectedClientID && secret == expectedSecret
}

// ClientCredentialsFromRequest extracts OAuth client credentials from Basic auth
// or application/x-www-form-urlencoded body fields.
func ClientCredentialsFromRequest(r *http.Request, form url.Values) (clientID, clientSecret string, ok bool) {
	if username, password, basicOK := r.BasicAuth(); basicOK {
		return username, password, true
	}

	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "basic ") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(auth[6:]))
		if err == nil {
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) == 2 {
				return parts[0], parts[1], true
			}
		}
	}

	if form != nil {
		clientID = strings.TrimSpace(form.Get("client_id"))
		clientSecret = strings.TrimSpace(form.Get("client_secret"))
		if clientID != "" && clientSecret != "" {
			return clientID, clientSecret, true
		}
	}
	return "", "", false
}

// BearerToken extracts the bearer token from Authorization header.
func BearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) < 7 || !strings.EqualFold(auth[:6], "bearer") {
		return ""
	}
	if len(auth) == 6 {
		return ""
	}
	if auth[6] != ' ' {
		return ""
	}
	return strings.TrimSpace(auth[7:])
}
