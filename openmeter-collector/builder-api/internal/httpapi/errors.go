package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// OAuthError is an OAuth 2.0-style error response body.
type OAuthError struct {
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description,omitempty"`
	CorrelationID    string `json:"correlation_id,omitempty"`
}

// APIError is a simple JSON error for non-OAuth routes.
type APIError struct {
	Error string `json:"error"`
}

func newCorrelationID() string {
	return uuid.NewString()
}

func setNoStoreHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
}

func writeOAuthError(w http.ResponseWriter, status int, code, description, correlationID string) {
	w.Header().Set("Content-Type", "application/json")
	setNoStoreHeaders(w)
	if status == http.StatusUnauthorized && code == "invalid_client" {
		w.Header().Set("WWW-Authenticate", `Basic realm="token", charset="UTF-8"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(OAuthError{
		Error:            code,
		ErrorDescription: description,
		CorrelationID:    correlationID,
	})
}

func writeTokenExchangeError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Content-Type", "application/json")
	setNoStoreHeaders(w)
	if status == http.StatusUnauthorized && code == "invalid_client" {
		w.Header().Set("WWW-Authenticate", `Basic realm="token", charset="UTF-8"`)
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(OAuthError{
		Error:            code,
		ErrorDescription: description,
	})
}

func writeBearerUnauthorized(w http.ResponseWriter, code, description string) {
	if code == "" {
		code = "invalid_token"
	}
	// Keep error_description in the JSON body only. Interpolating it into
	// WWW-Authenticate would need RFC 6750 quoted-string escaping.
	w.Header().Set("WWW-Authenticate", `Bearer realm="usage", error="`+rfc6750Quoted(code)+`"`)
	writeOAuthError(w, http.StatusUnauthorized, code, description, "")
}

func rfc6750Quoted(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\r', '\n':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func writeAPIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{Error: message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeTokenJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	setNoStoreHeaders(w)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func oauthDescription(code, description string) string {
	if description != "" {
		return description
	}
	return code
}
