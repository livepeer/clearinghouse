package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	auth0mgmt "github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mgmt"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mint"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/tokenexchange"
)

type auth0UserAdmin interface {
	UpsertUser(ctx context.Context, publicClientID, externalUserID, email, connection string, issueAPIKey bool, keyPrefix string) (*auth0mgmt.UserRecord, error)
	RotateAPIKey(ctx context.Context, publicClientID, externalUserID, keyPrefix string) (string, error)
}

// Server wires Builder API routes and dependencies.
type Server struct {
	cfg           config.Config
	auth0         auth0UserAdmin
	minter        *auth0mint.Minter
	openmeter     openmeterSession
	tokenExchange *tokenexchange.Handler
	userVerifier  tokenexchange.UserTokenVerifier
	openAPISpec   []byte
	usageReader   UsageReader
}

type openmeterSession interface {
	ProvisionSession(ctx context.Context, cfg openmeter.ProvisionConfig, clientID, externalUserID string) (*openmeter.SessionProvision, error)
}

// NewServer constructs the HTTP API server.
func NewServer(
	cfg config.Config,
	auth0 auth0UserAdmin,
	minter *auth0mint.Minter,
	om openmeterSession,
	tokenExchange *tokenexchange.Handler,
	userVerifier tokenexchange.UserTokenVerifier,
	openAPISpec []byte,
	usageReader UsageReader,
) *Server {
	return &Server{
		cfg:           cfg,
		auth0:         auth0,
		minter:        minter,
		openmeter:     om,
		tokenExchange: tokenExchange,
		userVerifier:  userVerifier,
		openAPISpec:   openAPISpec,
		usageReader:   usageReader,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("GET /api/v1/docs", s.handleDocs)
	mux.HandleFunc("POST /api/v1/apps/{clientId}/users", s.handleCreateUser)
	mux.HandleFunc("POST /api/v1/users/me/api-key", s.handleRotateAPIKeySelf)
	mux.HandleFunc("POST /api/v1/oidc/token", s.handleOIDCToken)
	mux.HandleFunc("GET /api/v1/users/me/usage", s.handleUsageSelf)
	mux.HandleFunc("GET /api/v1/users/me/balance", s.handleBalanceSelf)
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
	clientID := strings.TrimSpace(r.PathValue("clientId"))
	if clientID == "" {
		writeAPIError(w, http.StatusBadRequest, "clientId is required")
		return
	}
	if !M2MAuth(r, s.cfg.SignerM2MClientID, s.cfg.SignerM2MSecret) {
		writeAPIError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if s.auth0 == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "auth0 is not configured")
		return
	}
	if s.openmeter == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "openmeter is not configured")
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

type rotateAPIKeyResponse struct {
	ClientID       string `json:"clientId"`
	ExternalUserID string `json:"externalUserId"`
	APIKey         string `json:"apiKey"`
	Status         string `json:"status"`
}

// handleRotateAPIKeySelf serves POST /api/v1/users/me/api-key.
//
// Identity is derived from a subject token (Bearer header or form subject_token):
// an Auth0 user JWT or end-user API key (sk_*). The app client id is inferred
// from the token, matching the RFC 8693 token-exchange subject resolution path.
func (s *Server) handleRotateAPIKeySelf(w http.ResponseWriter, r *http.Request) {
	if s.tokenExchange == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "subject token resolution is not configured")
		return
	}
	if s.auth0 == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "auth0 is not configured")
		return
	}

	subjectToken, err := subjectTokenFromRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "malformed subject token request")
		return
	}
	if subjectToken == "" {
		writeBearerUnauthorized(w, "invalid_token", "missing subject token")
		return
	}

	clientID, externalUserID, err := s.tokenExchange.ResolveSubject(r.Context(), subjectToken, "")
	if err != nil {
		writeBearerUnauthorized(w, "invalid_token", "invalid subject token")
		return
	}
	clientID = strings.TrimSpace(clientID)
	externalUserID = strings.TrimSpace(externalUserID)
	if clientID == "" || externalUserID == "" || strings.Contains(externalUserID, ":") {
		writeBearerUnauthorized(w, "invalid_token", "invalid subject token")
		return
	}

	plaintext, err := s.auth0.RotateAPIKey(r.Context(), clientID, externalUserID, s.cfg.APIKeyPrefix)
	if err != nil {
		if errors.Is(err, auth0mgmt.ErrUserNotFound) {
			writeAPIError(w, http.StatusNotFound, "user not found")
			return
		}
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, rotateAPIKeyResponse{
		ClientID:       clientID,
		ExternalUserID: externalUserID,
		APIKey:         plaintext,
		Status:         "active",
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
