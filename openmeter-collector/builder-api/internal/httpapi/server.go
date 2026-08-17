package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	auth0mgmt "github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mgmt"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mint"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tenantauth"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

// Server wires Builder API routes and dependencies.
type Server struct {
	cfg           config.Config
	auth0         *auth0mgmt.Client
	minter        *auth0mint.Minter
	openmeter     openmeterSession
	tokenExchange *tokenexchange.Handler
	openAPISpec   []byte
	tenantAuth    *tenantauth.Authenticator
	admin         OpenMeterAdmin
}

type openmeterSession interface {
	ProvisionSession(ctx context.Context, cfg openmeter.ProvisionConfig, clientID, externalUserID string) (*openmeter.SessionProvision, error)
}

// NewServer constructs the HTTP API server.
func NewServer(cfg config.Config, auth0 *auth0mgmt.Client, minter *auth0mint.Minter, om openmeterSession, tokenExchange *tokenexchange.Handler, openAPISpec []byte, tenantAuth *tenantauth.Authenticator, admin OpenMeterAdmin) *Server {
	return &Server{
		cfg:           cfg,
		auth0:         auth0,
		minter:        minter,
		openmeter:     om,
		tokenExchange: tokenExchange,
		openAPISpec:   openAPISpec,
		tenantAuth:    tenantAuth,
		admin:         admin,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /api/v1/docs", s.handleDocs)
	mux.HandleFunc("POST /api/v1/apps/{clientId}/users", s.handleCreateUser)
	mux.HandleFunc("POST /api/v1/apps/{clientId}/oidc/token", s.handleOIDCToken)
	mux.HandleFunc("GET /api/v1/apps/{clientId}/usage", s.handleUsage)
	mux.HandleFunc("GET /api/v1/apps/{clientId}/users/{externalUserId}/access", s.handleUserAccess)
	mux.HandleFunc("POST /api/v1/apps/{clientId}/users/{externalUserId}/grants", s.handleGrantAllowance)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.openAPISpec)
}

func (s *Server) handleDocs(w http.ResponseWriter, _ *http.Request) {
	html := `<!doctype html>
<html>
  <head>
    <title>Clearinghouse Builder API</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="/api/v1/openapi.json" src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.61.0"></script>
  </body>
</html>`
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(html))
}

type createUserRequest struct {
	ExternalUserID string `json:"externalUserId"`
	Email          string `json:"email"`
	Connection     string `json:"connection"`
	IssueAPIKey    *bool  `json:"issueApiKey"`
}

type createUserResponse struct {
	ID             string `json:"id"`
	ClientID       string `json:"clientId"`
	ExternalUserID string `json:"externalUserId"`
	Email          string `json:"email,omitempty"`
	Status         string `json:"status"`
	APIKey         string `json:"apiKey,omitempty"`
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	clientID, ok := s.authorizeTenant(w, r)
	if !ok {
		return
	}

	body, err := readJSONBody[createUserRequest](r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	externalUserID := strings.TrimSpace(body.ExternalUserID)
	if externalUserID == "" {
		writeAPIError(w, http.StatusBadRequest, "externalUserId is required")
		return
	}

	issueKey := true
	if body.IssueAPIKey != nil {
		issueKey = *body.IssueAPIKey
	}

	ctx := r.Context()
	user, err := s.auth0.UpsertUser(ctx, clientID, externalUserID, strings.TrimSpace(body.Email), strings.TrimSpace(body.Connection), issueKey, s.cfg.APIKeyPrefix)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	if _, err := s.openmeter.ProvisionSession(ctx, openmeter.ProvisionConfig{
		DefaultPlanKey:      s.cfg.OpenMeterDefaultPlanKey,
		TrialFeatureKey:     s.cfg.OpenMeterTrialFeatureKey,
		TrialGrantUSDMicros: s.cfg.OpenMeterTrialGrantUSDMicros,
	}, clientID, externalUserID); err != nil {
		writeAPIError(w, http.StatusBadGateway, "openmeter customer provisioning failed")
		return
	}

	status := http.StatusOK
	if user.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, createUserResponse{
		ID:             user.ID,
		ClientID:       clientID,
		ExternalUserID: externalUserID,
		Email:          user.Email,
		Status:         "active",
		APIKey:         user.APIKey,
	})
}

func readJSONBody[T any](r *http.Request) (T, error) {
	var zero T
	defer r.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return zero, err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		return zero, nil
	}
	var out T
	if err := json.Unmarshal(raw, &out); err != nil {
		return zero, err
	}
	return out, nil
}
