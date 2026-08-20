package httpapi

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

type tokenExchangeResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int    `json:"expires_in"`
	Scope            string `json:"scope"`
	SignerURL        string `json:"signer_url,omitempty"`
	DiscoveryURL     string `json:"discovery_url,omitempty"`
	IssuedTokenType  string `json:"issued_token_type,omitempty"`
	CorrelationID    string `json:"correlation_id,omitempty"`
	HasAccess        bool   `json:"has_access"`
	BalanceUSDMicros int64  `json:"balance_usd_micros"`
}

func (s *Server) handleOIDCToken(w http.ResponseWriter, r *http.Request) {
	correlationID := newCorrelationID()
	if r.Method != http.MethodPost {
		writeTokenExchangeError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
		return
	}

	contentType := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_request", "content-type must be application/x-www-form-urlencoded")
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_request", "unable to read request body")
		return
	}
	defer r.Body.Close()

	form, err := url.ParseQuery(string(raw))
	if err != nil {
		writeTokenExchangeError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}

	clientID, clientSecret, _ := ClientCredentialsFromRequest(r, form)
	req := tokenexchange.Request{
		ClientID:           clientID,
		ClientSecret:       clientSecret,
		GrantType:          form.Get("grant_type"),
		SubjectToken:       form.Get("subject_token"),
		SubjectTokenType:   form.Get("subject_token_type"),
		RequestedTokenType: form.Get("requested_token_type"),
		Resource:           form.Get("resource"),
		Audiences:          form["audience"],
	}

	result, err := s.tokenExchange.Exchange(r.Context(), req, correlationID)
	if err != nil {
		var te *tokenexchange.Error
		if errors.As(err, &te) {
			writeTokenExchangeError(w, te.Status, te.Code, oauthDescription(te.Code, te.Error()))
			return
		}
		writeTokenExchangeError(w, http.StatusInternalServerError, "server_error", "token exchange failed")
		return
	}

	writeTokenJSON(w, http.StatusOK, tokenExchangeResponse{
		AccessToken:      result.AccessToken,
		TokenType:        result.TokenType,
		ExpiresIn:        result.ExpiresIn,
		Scope:            result.Scope,
		SignerURL:        result.SignerURL,
		DiscoveryURL:     result.DiscoveryURL,
		IssuedTokenType:  result.IssuedTokenType,
		CorrelationID:    result.CorrelationID,
		HasAccess:        result.HasAccess,
		BalanceUSDMicros: result.BalanceUSDMicros,
	})
}
