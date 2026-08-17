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

// OpenMeterAdmin is the metering surface the usage route needs.
type OpenMeterAdmin interface {
	QueryUsage(ctx context.Context, q openmeter.UsageQuery) ([]openmeter.UsageRow, error)
}

// authorizeTenant authenticates the caller with HTTP Basic and confirms it may
// act on the tenant named in the path. It returns the trusted client id —
// callers must use this return value rather than re-reading the path.
//
// A caller that authenticates but does not own the tenant gets 404, not 403:
// on a shared OpenMeter tenant a 403 would confirm that another tenant's client
// id exists.
func (s *Server) authorizeTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	pathClientID := strings.TrimSpace(r.PathValue("clientId"))
	if pathClientID == "" {
		writeAPIError(w, http.StatusBadRequest, "clientId is required")
		return "", false
	}
	if s.tenantAuth == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "tenant authorization is not configured")
		return "", false
	}

	credClientID, secret, ok := ClientCredentialsFromRequest(r, nil)
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "Unauthorized")
		return "", false
	}
	principal := s.tenantAuth.Authenticate(credClientID, secret)
	if !principal.Authenticated() {
		writeAPIError(w, http.StatusUnauthorized, "Unauthorized")
		return "", false
	}
	if !principal.CanAccess(pathClientID) {
		writeAPIError(w, http.StatusNotFound, "app not found")
		return "", false
	}
	return pathClientID, true
}

// authorizeUsageActor authenticates Bearer JWT or sk_* and returns the path
// client id plus the authenticated external user id (actor). M2M Basic is
// rejected — usage is self-serve only.
func (s *Server) authorizeUsageActor(w http.ResponseWriter, r *http.Request) (clientID, actor string, ok bool) {
	pathClientID := strings.TrimSpace(r.PathValue("clientId"))
	if pathClientID == "" {
		writeAPIError(w, http.StatusBadRequest, "clientId is required")
		return "", "", false
	}

	token := BearerToken(r)
	if token == "" {
		writeAPIError(w, http.StatusUnauthorized, "Unauthorized")
		return "", "", false
	}

	resolvedClientID, externalUserID, err := s.resolveUsageSubject(r.Context(), token, "")
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, "Unauthorized")
		return "", "", false
	}
	if strings.TrimSpace(resolvedClientID) != pathClientID {
		writeAPIError(w, http.StatusNotFound, "app not found")
		return "", "", false
	}
	externalUserID = strings.TrimSpace(externalUserID)
	if externalUserID == "" {
		writeAPIError(w, http.StatusUnauthorized, "Unauthorized")
		return "", "", false
	}
	return pathClientID, externalUserID, true
}

func (s *Server) resolveUsageSubject(ctx context.Context, token, expectedClientID string) (clientID, externalUserID string, err error) {
	token = strings.TrimSpace(token)
	if strings.Count(token, ".") == 2 {
		if s.userVerifier == nil {
			return "", "", errUnauthorized
		}
		return s.userVerifier.VerifyUserAccessToken(ctx, token, expectedClientID)
	}
	prefix := s.cfg.APIKeyPrefix
	if prefix == "" {
		prefix = "sk_"
	}
	if !strings.HasPrefix(token, prefix) {
		return "", "", errUnauthorized
	}
	if s.apiKeys == nil {
		return "", "", errUnauthorized
	}
	clientID, externalUserID, resolveErr := s.apiKeys.Resolve(ctx, token, expectedClientID)
	if resolveErr != nil {
		return "", "", resolveErr
	}
	return clientID, externalUserID, nil
}

var errUnauthorized = errors.New("unauthorized")

type usageResponse struct {
	ClientID string               `json:"clientId"`
	Meter    string               `json:"meter"`
	Actor    string               `json:"actor"`
	Subjects []string             `json:"subjects"`
	Rows     []openmeter.UsageRow `json:"rows"`
}

// handleUsage serves GET /api/v1/apps/{clientId}/usage.
//
// Auth is Bearer Auth0 user JWT or sk_* (Railway direct). The path clientId must
// match the credential's app. Optional externalUserId must equal the actor.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	clientID, actor, ok := s.authorizeUsageActor(w, r)
	if !ok {
		return
	}
	if s.admin == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "metering backend is not configured")
		return
	}

	meter := strings.TrimSpace(r.URL.Query().Get("meter"))
	if meter == "" {
		writeAPIError(w, http.StatusBadRequest, "meter is required")
		return
	}
	if !openmeter.CatalogMeterKnown(meter) {
		writeAPIError(w, http.StatusBadRequest, "meter is not a catalog meter")
		return
	}

	if filter := strings.TrimSpace(r.URL.Query().Get("externalUserId")); filter != "" {
		if strings.Contains(filter, ":") {
			writeAPIError(w, http.StatusBadRequest, "externalUserId must not contain ':'")
			return
		}
		if filter != actor {
			writeAPIError(w, http.StatusBadRequest, "externalUserId must match the authenticated actor")
			return
		}
	}

	groupBy := r.URL.Query()["groupBy"]
	if msg := openmeter.ValidateUsageGroupBy(meter, groupBy); msg != "" {
		writeAPIError(w, http.StatusBadRequest, msg)
		return
	}

	from, to, err := parseWindow(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	rows, err := s.admin.QueryUsage(r.Context(), openmeter.UsageQuery{
		MeterSlug: meter,
		ClientID:  clientID,
		From:      from,
		To:        to,
		GroupBy:   groupBy,
	})
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "usage query failed")
		return
	}

	rows = filterRowsToActor(rows, clientID, actor)
	subjects := []string{openmeter.CustomerKey(clientID, actor)}

	writeJSON(w, http.StatusOK, usageResponse{
		ClientID: clientID,
		Meter:    meter,
		Actor:    actor,
		Subjects: subjects,
		Rows:     rows,
	})
}

// filterRowsToActor drops rows outside the authenticated actor. The query is
// already scoped by client_id; this guards against a backend that ignores
// filters or returns other users under the same app.
func filterRowsToActor(rows []openmeter.UsageRow, clientID, actor string) []openmeter.UsageRow {
	out := make([]openmeter.UsageRow, 0, len(rows))
	for _, row := range rows {
		if openmeter.RowMatchesActor(row, clientID, actor) {
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
