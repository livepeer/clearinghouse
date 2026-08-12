package auth0mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/auth0/go-auth0/v2/management"
	auth0client "github.com/auth0/go-auth0/v2/management/client"
	"github.com/auth0/go-auth0/v2/management/option"

	"github.com/livepeer/clearinghouse/openmeter-collector/builder-api/internal/apikey"
)

// Client wraps Auth0 Management API user operations.
type Client struct {
	api          *auth0client.Management
	dbConnection string
}

// UserRecord is a provisioned Auth0 end-user.
type UserRecord struct {
	ID             string
	Email          string
	Connection     string
	ClientID       string
	ExternalUserID string
	Created        bool
	APIKey         string
}

// New creates a Management API client.
func New(domain, clientID, clientSecret, dbConnection string) (*Client, error) {
	api, err := auth0client.New(
		domain,
		option.WithClientCredentialsAndAudience(
			context.Background(),
			clientID,
			clientSecret,
			"https://"+strings.TrimSuffix(domain, "/")+"/api/v2/",
		),
	)
	if err != nil {
		return nil, fmt.Errorf("auth0 management client: %w", err)
	}
	return &Client{
		api:          api,
		dbConnection: dbConnection,
	}, nil
}

// UpsertUser creates or updates an Auth0 Database user for an integrator end-user.
func (c *Client) UpsertUser(ctx context.Context, publicClientID, externalUserID, email, connection string, issueAPIKey bool, keyPrefix string) (*UserRecord, error) {
	if connection == "" {
		connection = c.dbConnection
	}

	existing, err := c.findByMetadata(ctx, publicClientID, externalUserID)
	if err != nil {
		return nil, err
	}

	if existing != nil {
		rec := userRecordFromAuth0(existing, publicClientID, externalUserID, false)
		if email != "" && existing.GetEmail() != email {
			appMeta := cloneAppMetadata(existing.GetAppMetadata())
			_, err = c.api.Users.Update(ctx, existing.GetUserID(), &management.UpdateUserRequestContent{
				Email:       management.String(email),
				AppMetadata: &appMeta,
			})
			if err != nil {
				return nil, fmt.Errorf("update auth0 user email: %w", err)
			}
			rec.Email = email
		}
		return rec, nil
	}

	username := sanitizeUsername(externalUserID)
	password, err := randomPassword()
	if err != nil {
		return nil, err
	}

	appMeta := management.AppMetadata{
		"clientId":       publicClientID,
		"externalUserId": externalUserID,
	}

	create := &management.CreateUserRequestContent{
		Connection:  connection,
		Password:    management.String(password),
		AppMetadata: &appMeta,
		VerifyEmail: management.Bool(false),
	}
	if email != "" {
		create.Email = management.String(email)
	} else {
		create.Username = management.String(username)
	}

	created, err := c.api.Users.Create(ctx, create)
	if err != nil {
		return nil, fmt.Errorf("create auth0 user: %w", err)
	}

	rec := userRecordFromAuth0(created, publicClientID, externalUserID, true)
	if issueAPIKey {
		plaintext, stored, err := apikey.Generate(keyPrefix, created.GetUserID())
		if err != nil {
			return nil, err
		}
		appMeta["builder_api_key"] = stored
		meta := appMeta
		_, err = c.api.Users.Update(ctx, created.GetUserID(), &management.UpdateUserRequestContent{
			AppMetadata: &meta,
		})
		if err != nil {
			return nil, fmt.Errorf("store api key metadata: %w", err)
		}
		rec.APIKey = plaintext
	}
	return rec, nil
}

// ResolveAPIKeyUser validates an API key and returns client/external user ids.
func (c *Client) ResolveAPIKeyUser(ctx context.Context, apiKey, keyPrefix, expectedClientID string) (clientID, externalUserID string, err error) {
	userID, err := apikey.ParseUserID(keyPrefix, apiKey)
	if err != nil {
		return "", "", err
	}

	u, err := c.api.Users.Get(ctx, userID, &management.GetUserRequestParameters{})
	if err != nil {
		return "", "", fmt.Errorf("load auth0 user: %w", err)
	}

	meta, err := parseAppMetadata(u.GetAppMetadata())
	if err != nil {
		return "", "", err
	}
	if meta.BuilderAPIKey == nil || !apikey.Verify(apiKey, *meta.BuilderAPIKey) {
		return "", "", fmt.Errorf("invalid api key")
	}
	if expectedClientID != "" && meta.ClientID != expectedClientID {
		return "", "", fmt.Errorf("api key client mismatch")
	}
	return meta.ClientID, meta.ExternalUserID, nil
}

func (c *Client) findByMetadata(ctx context.Context, clientID, externalUserID string) (*management.UserResponseSchema, error) {
	query := fmt.Sprintf(`app_metadata.clientId:"%s" AND app_metadata.externalUserId:"%s"`,
		escapeQuery(clientID), escapeQuery(externalUserID))
	page, err := c.api.Users.List(ctx, &management.ListUsersRequestParameters{
		Q:       management.String(query),
		PerPage: management.Int(5),
	})
	if err != nil {
		return nil, fmt.Errorf("search auth0 users: %w", err)
	}
	if page == nil || len(page.Results) == 0 {
		return nil, nil
	}
	return page.Results[0], nil
}

func userRecordFromAuth0(u interface {
	GetUserID() string
	GetEmail() string
	GetIdentities() []*management.UserIdentitySchema
}, clientID, externalUserID string, created bool) *UserRecord {
	connection := ""
	if ids := u.GetIdentities(); len(ids) > 0 && ids[0] != nil {
		connection = ids[0].GetConnection()
	}
	return &UserRecord{
		ID:             u.GetUserID(),
		Email:          u.GetEmail(),
		Connection:     connection,
		ClientID:       clientID,
		ExternalUserID: externalUserID,
		Created:        created,
	}
}

type appMetadata struct {
	ClientID       string            `json:"clientId"`
	ExternalUserID string            `json:"externalUserId"`
	BuilderAPIKey  *apikey.StoredKey `json:"builder_api_key,omitempty"`
}

func parseAppMetadata(raw management.UserAppMetadataSchema) (appMetadata, error) {
	if raw == nil {
		return appMetadata{}, fmt.Errorf("missing app_metadata")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return appMetadata{}, err
	}
	var meta appMetadata
	if err := json.Unmarshal(b, &meta); err != nil {
		return appMetadata{}, err
	}
	if meta.ClientID == "" || meta.ExternalUserID == "" {
		return appMetadata{}, fmt.Errorf("incomplete app_metadata")
	}
	return meta, nil
}

func sanitizeUsername(externalUserID string) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			return r
		default:
			return '_'
		}
	}, externalUserID)
	if s == "" {
		return "user"
	}
	if len(s) > 60 {
		return s[:60]
	}
	return s
}

func escapeQuery(value string) string {
	return strings.ReplaceAll(value, `"`, `\"`)
}

func cloneAppMetadata(raw management.UserAppMetadataSchema) management.AppMetadata {
	out := management.AppMetadata{}
	for k, v := range raw {
		out[k] = v
	}
	return out
}
