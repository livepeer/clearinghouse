package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
)

// UsageReader is the metering surface needed by user usage routes.
type UsageReader interface {
	QueryUsage(ctx context.Context, q openmeter.UsageQuery) ([]openmeter.UsageRow, error)
}

type selfUsageResponse struct {
	ClientID       string               `json:"clientId"`
	ExternalUserID string               `json:"externalUserId"`
	Meter          string               `json:"meter"`
	Subject        string               `json:"subject"`
	Rows           []openmeter.UsageRow `json:"rows"`
}

// authorizeUsageIdentity verifies a caller's Bearer JWT and returns the
// trusted client and external user ids from the identity-webhook contract.
func (s *Server) authorizeUsageIdentity(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if s.userVerifier == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "JWT verification is not configured")
		return "", "", false
	}
	token := BearerToken(r)
	if token == "" {
		writeBearerUnauthorized(w, "invalid_token", "missing bearer token")
		return "", "", false
	}
	clientID, externalUserID, err := s.userVerifier.VerifyUserAccessToken(r.Context(), token, "")
	if err != nil {
		writeBearerUnauthorized(w, "invalid_token", "invalid bearer token")
		return "", "", false
	}
	clientID = strings.TrimSpace(clientID)
	externalUserID = strings.TrimSpace(externalUserID)
	if clientID == "" || externalUserID == "" || strings.Contains(externalUserID, ":") {
		writeBearerUnauthorized(w, "invalid_token", "invalid bearer token")
		return "", "", false
	}
	return clientID, externalUserID, true
}

// handleUsageSelf serves GET /api/v1/users/me/usage.
//
// This user-scoped route derives tenant and user identity from the Bearer
// signer JWT, so end users do not need a clientId path segment.
func (s *Server) handleUsageSelf(w http.ResponseWriter, r *http.Request) {
	if s.usageReader == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "usage backend is not configured")
		return
	}
	clientID, externalUserID, ok := s.authorizeUsageIdentity(w, r)
	if !ok {
		return
	}
	meter := s.cfg.UsageMeterKey()
	from, to, err := parseWindow(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	subject := openmeter.CustomerKey(clientID, externalUserID)
	rows, err := s.usageReader.QueryUsage(r.Context(), openmeter.UsageQuery{
		MeterSlug: meter,
		Subjects:  []string{subject},
		From:      from,
		To:        to,
		GroupBy:   r.URL.Query()["groupBy"],
	})
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "usage query failed")
		return
	}
	rows = filterRowsToSubject(rows, subject)
	writeJSON(w, http.StatusOK, selfUsageResponse{
		ClientID:       clientID,
		ExternalUserID: externalUserID,
		Meter:          meter,
		Subject:        subject,
		Rows:           rows,
	})
}

func filterRowsToSubject(rows []openmeter.UsageRow, subject string) []openmeter.UsageRow {
	out := make([]openmeter.UsageRow, 0, len(rows))
	for _, row := range rows {
		if row.Subject == subject {
			out = append(out, row)
		}
	}
	return out
}

var errWindowOrder = errors.New("to must not be before from")

func errInvalidTime(field string) error {
	return fmt.Errorf("%s must be an RFC3339 timestamp", field)
}

func parseWindow(r *http.Request) (from, to *time.Time, err error) {
	q := r.URL.Query()
	if raw := strings.TrimSpace(q.Get("from")); raw != "" {
		parsed, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			return nil, nil, errInvalidTime("from")
		}
		from = &parsed
	}
	if raw := strings.TrimSpace(q.Get("to")); raw != "" {
		parsed, perr := time.Parse(time.RFC3339, raw)
		if perr != nil {
			return nil, nil, errInvalidTime("to")
		}
		to = &parsed
	}
	if from != nil && to != nil && to.Before(*from) {
		return nil, nil, errWindowOrder
	}
	return from, to, nil
}
