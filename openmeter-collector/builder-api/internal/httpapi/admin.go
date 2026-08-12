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

// OpenMeterAdmin is the metering surface the admin routes need.
type OpenMeterAdmin interface {
	QueryUsage(ctx context.Context, q openmeter.UsageQuery) ([]openmeter.UsageRow, error)
	ListCustomerKeysForClient(ctx context.Context, clientID string) ([]string, error)
	GetAccess(ctx context.Context, customerID, featureKey string) (*openmeter.Access, error)
	EnsureTrialGrant(ctx context.Context, customerID, featureKey, grantKey string, amountMicros int64) error
}

// maxUsageSubjects bounds a tenant-wide query.
const maxUsageSubjects = 500

// authorizeTenant authenticates the caller and confirms it may act on the
// tenant named in the path. It returns the trusted client id — callers must use
// this return value rather than re-reading the path, so an authorized tenant id
// is the only thing that can ever reach the metering layer.
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

// externalUserIDFromPath validates the user segment. A colon is rejected
// because customer keys are "clientId:externalUserId" — allowing one would make
// the key ambiguous about where the tenant ends.
func externalUserIDFromPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("externalUserId"))
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, "externalUserId is required")
		return "", false
	}
	if strings.Contains(id, ":") {
		writeAPIError(w, http.StatusBadRequest, "externalUserId must not contain ':'")
		return "", false
	}
	return id, true
}

type usageResponse struct {
	ClientID string               `json:"clientId"`
	Meter    string               `json:"meter"`
	Subjects []string             `json:"subjects"`
	Rows     []openmeter.UsageRow `json:"rows"`
}

// handleUsage serves GET /api/v1/apps/{clientId}/usage.
//
// Without externalUserId it reports every customer belonging to the tenant;
// with one it reports that single subject. Either way the subject list is built
// from the authorized client id, never from a caller-supplied subject.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authorizeTenant(w, r)
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
	from, to, err := parseWindow(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	var subjects []string
	if externalUserID := strings.TrimSpace(r.URL.Query().Get("externalUserId")); externalUserID != "" {
		if strings.Contains(externalUserID, ":") {
			writeAPIError(w, http.StatusBadRequest, "externalUserId must not contain ':'")
			return
		}
		subjects = []string{openmeter.CustomerKey(clientID, externalUserID)}
	} else {
		keys, err := s.admin.ListCustomerKeysForClient(ctx, clientID)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, "usage lookup failed")
			return
		}
		if len(keys) > maxUsageSubjects {
			writeAPIError(w, http.StatusBadRequest, "too many customers for a single query; filter by externalUserId")
			return
		}
		subjects = keys
	}

	rows, err := s.admin.QueryUsage(ctx, openmeter.UsageQuery{
		MeterSlug: meter,
		Subjects:  subjects,
		From:      from,
		To:        to,
		GroupBy:   r.URL.Query()["groupBy"],
	})
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "usage query failed")
		return
	}

	// Defence in depth: the backend must never widen the requested scope.
	rows = filterRowsToTenant(rows, clientID)

	if subjects == nil {
		subjects = []string{}
	}
	writeJSON(w, http.StatusOK, usageResponse{
		ClientID: clientID,
		Meter:    meter,
		Subjects: subjects,
		Rows:     rows,
	})
}

// filterRowsToTenant drops any row whose subject is outside the tenant. The
// query is already scoped; this guards against a backend that ignores the
// subject filter and returns the whole meter.
//
// A row with an empty subject is dropped rather than passed through. An
// aggregate row carries no evidence of which tenant it belongs to, so letting
// one through would defeat the filter for exactly the case it exists to catch.
func filterRowsToTenant(rows []openmeter.UsageRow, clientID string) []openmeter.UsageRow {
	prefix := clientID + ":"
	out := make([]openmeter.UsageRow, 0, len(rows))
	for _, row := range rows {
		if strings.HasPrefix(row.Subject, prefix) {
			out = append(out, row)
		}
	}
	return out
}

type accessResponse struct {
	ClientID         string `json:"clientId"`
	ExternalUserID   string `json:"externalUserId"`
	CustomerKey      string `json:"customerKey"`
	HasAccess        bool   `json:"hasAccess"`
	BalanceUSDMicros int64  `json:"balanceUsdMicros"`
	Source           string `json:"source"`
}

// handleUserAccess serves GET /api/v1/apps/{clientId}/users/{externalUserId}/access.
func (s *Server) handleUserAccess(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authorizeTenant(w, r)
	if !ok {
		return
	}
	externalUserID, ok := externalUserIDFromPath(w, r)
	if !ok {
		return
	}
	if s.admin == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "metering backend is not configured")
		return
	}

	featureKey := strings.TrimSpace(r.URL.Query().Get("feature"))
	if featureKey == "" {
		featureKey = s.cfg.OpenMeterTrialFeatureKey
	}

	customerKey := openmeter.CustomerKey(clientID, externalUserID)
	access, err := s.admin.GetAccess(r.Context(), customerKey, featureKey)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, "access lookup failed")
		return
	}
	if access == nil {
		access = &openmeter.Access{Source: "none"}
	}
	writeJSON(w, http.StatusOK, accessResponse{
		ClientID:         clientID,
		ExternalUserID:   externalUserID,
		CustomerKey:      customerKey,
		HasAccess:        access.HasAccess,
		BalanceUSDMicros: access.BalanceUSDMicros,
		Source:           access.Source,
	})
}

type grantRequest struct {
	FeatureKey   string `json:"featureKey"`
	GrantKey     string `json:"grantKey"`
	AmountMicros int64  `json:"amountUsdMicros"`
}

// handleGrantAllowance serves POST /api/v1/apps/{clientId}/users/{externalUserId}/grants.
func (s *Server) handleGrantAllowance(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authorizeTenant(w, r)
	if !ok {
		return
	}
	externalUserID, ok := externalUserIDFromPath(w, r)
	if !ok {
		return
	}
	if s.admin == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "metering backend is not configured")
		return
	}

	body, err := readJSONBody[grantRequest](r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.AmountMicros <= 0 {
		writeAPIError(w, http.StatusBadRequest, "amountUsdMicros must be positive")
		return
	}
	featureKey := strings.TrimSpace(body.FeatureKey)
	if featureKey == "" {
		featureKey = s.cfg.OpenMeterTrialFeatureKey
	}
	if featureKey == "" {
		writeAPIError(w, http.StatusBadRequest, "featureKey is required")
		return
	}
	grantKey := strings.TrimSpace(body.GrantKey)
	if grantKey == "" {
		grantKey = "admin-grant"
	}

	customerKey := openmeter.CustomerKey(clientID, externalUserID)
	if err := s.admin.EnsureTrialGrant(r.Context(), customerKey, featureKey, grantKey, body.AmountMicros); err != nil {
		writeAPIError(w, http.StatusBadGateway, "grant failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"clientId":        clientID,
		"externalUserId":  externalUserID,
		"customerKey":     customerKey,
		"featureKey":      featureKey,
		"grantKey":        grantKey,
		"amountUsdMicros": body.AmountMicros,
	})
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
