package tokenexchange

import (
	"context"
	"strings"

	auth0mint "github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/auth0mint"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/apikey"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/config"
	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/openmeter"
)

// Request is a parsed RFC 8693 token exchange request.
type Request struct {
	PublicClientID     string
	ClientID           string
	ClientSecret       string
	GrantType          string
	SubjectToken       string
	SubjectTokenType   string
	RequestedTokenType string
	Resource           string
	Audiences          []string
}

// Result is a signer-session token exchange response.
type Result struct {
	AccessToken      string
	TokenType        string
	ExpiresIn        int
	Scope            string
	SignerURL        string
	DiscoveryURL     string
	IssuedTokenType  string
	CorrelationID    string
	HasAccess        bool
	BalanceUSDMicros int64
}

// SignerMinter mints short-lived signer JWTs.
type SignerMinter interface {
	MintSignerToken(ctx context.Context, publicClientID, externalUserID string) (*auth0mint.TokenResponse, error)
}

// SessionProvisioner upserts the OpenMeter customer and default-plan subscription.
type SessionProvisioner interface {
	ProvisionSession(ctx context.Context, cfg openmeter.ProvisionConfig, clientID, externalUserID string) (*openmeter.SessionProvision, error)
}

// UserTokenVerifier verifies an end-user access token (JWT) and returns the tenant
// client id and external user id. Implemented by the identity-webhook client.
type UserTokenVerifier interface {
	VerifyUserAccessToken(ctx context.Context, token, expectedClientID string) (clientID, externalUserID string, err error)
}

// Handler performs RFC 8693 signer JWT token exchange.
type Handler struct {
	cfg       config.Config
	verifier  UserTokenVerifier
	apiKeys   *apikey.Store
	minter    SignerMinter
	openmeter SessionProvisioner
}

// NewHandler constructs a token exchange handler.
func NewHandler(
	cfg config.Config,
	verifier UserTokenVerifier,
	apiKeys *apikey.Store,
	minter SignerMinter,
	om SessionProvisioner,
) *Handler {
	return &Handler{
		cfg:       cfg,
		verifier:  verifier,
		apiKeys:   apiKeys,
		minter:    minter,
		openmeter: om,
	}
}

// Exchange validates the request and mints a signer session.
func (h *Handler) Exchange(ctx context.Context, req Request, correlationID string) (*Result, error) {
	if strings.TrimSpace(req.GrantType) != GrantType {
		return nil, invalidRequest("grant_type must be " + GrantType)
	}
	if strings.TrimSpace(req.SubjectToken) == "" {
		return nil, invalidRequest("subject_token is required")
	}
	if strings.TrimSpace(req.SubjectTokenType) != SubjectAccessTokenType {
		return nil, unsupportedTokenType("subject_token_type must be " + SubjectAccessTokenType)
	}

	if err := h.validateClient(req.ClientID, req.ClientSecret); err != nil {
		return nil, err
	}
	if err := h.validateRequestedTokenType(req.RequestedTokenType); err != nil {
		return nil, err
	}
	if err := h.validateTarget(req.Resource, req.Audiences); err != nil {
		return nil, err
	}

	publicClientID := strings.TrimSpace(req.PublicClientID)
	if publicClientID == "" {
		return nil, invalidRequest("clientId is required")
	}

	clientID, externalUserID, err := h.resolveSubject(ctx, req.SubjectToken, publicClientID)
	if err != nil {
		return nil, err
	}

	session, err := h.openmeter.ProvisionSession(ctx, h.provisionConfig(), clientID, externalUserID)
	if err != nil {
		return nil, wrapServerError(err)
	}
	if h.cfg.OpenMeterEnforceAllowance && (session == nil || !session.HasAccess) {
		return nil, insufficientAllowance("trial credits exhausted; top up allowance before minting a signer session")
	}

	minted, err := h.minter.MintSignerToken(ctx, clientID, externalUserID)
	if err != nil {
		return nil, wrapServerError(err)
	}

	scope := strings.TrimSpace(minted.Scope)
	if scope == "" {
		scope = DefaultScope
	}

	result := &Result{
		AccessToken:      minted.AccessToken,
		TokenType:        "Bearer",
		ExpiresIn:        minted.ExpiresIn,
		Scope:            scope,
		IssuedTokenType:  IssuedAccessTokenType,
		CorrelationID:    correlationID,
		HasAccess:        session != nil && session.HasAccess,
		BalanceUSDMicros: 0,
	}
	if session != nil {
		result.BalanceUSDMicros = session.BalanceUSDMicros
		result.HasAccess = session.HasAccess
	}
	if h.cfg.SignerURL != "" {
		result.SignerURL = h.cfg.SignerURL
	}
	if h.cfg.DiscoveryURL != "" {
		result.DiscoveryURL = h.cfg.DiscoveryURL
	}
	return result, nil
}

func (h *Handler) provisionConfig() openmeter.ProvisionConfig {
	return openmeter.ProvisionConfig{
		DefaultPlanKey:      h.cfg.OpenMeterDefaultPlanKey,
		TrialFeatureKey:     h.cfg.OpenMeterTrialFeatureKey,
		TrialGrantUSDMicros: h.cfg.OpenMeterTrialGrantUSDMicros,
		EnforceAllowance:    h.cfg.OpenMeterEnforceAllowance,
	}
}

func (h *Handler) validateClient(clientID, clientSecret string) error {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)
	if clientID == "" && clientSecret == "" {
		return nil
	}
	if clientID == "" || clientSecret == "" {
		return invalidClient("client authentication requires both client id and secret")
	}
	if clientID != h.cfg.SignerM2MClientID || clientSecret != h.cfg.SignerM2MSecret {
		return invalidClient("invalid client credentials")
	}
	return nil
}

func (h *Handler) validateRequestedTokenType(requested string) error {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == IssuedAccessTokenType {
		return nil
	}
	return invalidRequest("requested_token_type must be " + IssuedAccessTokenType + " or omitted")
}

func (h *Handler) validateTarget(resource string, audiences []string) error {
	expected := normalizeURI(h.cfg.Auth0Audience)
	if expected == "" {
		return serverError("audience is not configured")
	}

	resource = strings.TrimSpace(resource)
	if resource != "" {
		if normalizeURI(resource) != expected {
			return invalidTarget("resource must be omitted or name the signer audience")
		}
		return nil
	}

	nonEmpty := make([]string, 0, len(audiences))
	for _, aud := range audiences {
		aud = strings.TrimSpace(aud)
		if aud != "" {
			nonEmpty = append(nonEmpty, aud)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
	}
	for _, aud := range nonEmpty {
		if normalizeURI(aud) != expected {
			return invalidTarget("audience must be omitted or name the signer audience")
		}
	}
	return nil
}

func (h *Handler) resolveSubject(ctx context.Context, subjectToken, publicClientID string) (clientID, externalUserID string, err error) {
	subjectToken = strings.TrimSpace(subjectToken)
	if strings.Count(subjectToken, ".") == 2 {
		if h.verifier == nil {
			return "", "", invalidGrant("subject_token is not a valid access token for this issuer")
		}
		verifiedClientID, verifiedUserID, verifyErr := h.verifier.VerifyUserAccessToken(ctx, subjectToken, publicClientID)
		if verifyErr != nil {
			return "", "", invalidGrant("subject_token is not a valid access token for this issuer")
		}
		return verifiedClientID, verifiedUserID, nil
	}

	if !strings.HasPrefix(subjectToken, h.cfg.APIKeyPrefix) {
		return "", "", invalidGrant("subject_token is not a valid access token for this issuer")
	}
	if h.apiKeys == nil {
		return "", "", invalidGrant("subject_token is not a valid access token for this issuer")
	}
	clientID, externalUserID, resolveErr := h.apiKeys.Resolve(ctx, subjectToken, publicClientID)
	if resolveErr != nil {
		if resolveErr == apikey.ErrClientMismatch {
			return "", "", invalidGrant("subject_token client does not match this app")
		}
		return "", "", invalidGrant("subject_token is not a valid access token for this issuer")
	}
	return clientID, externalUserID, nil
}

func normalizeURI(value string) string {
	return strings.TrimSuffix(strings.TrimSpace(value), "/")
}
