package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const secretBytes = 24

// StoredKey is persisted in Auth0 app_metadata.builder_api_key.
type StoredKey struct {
	Salt string `json:"salt"`
	Hash string `json:"hash"`
}

// Generate creates a new API key and its stored hash record.
// Format: {prefix}{encodedUserID}_{secret}
func Generate(prefix, auth0UserID string) (plaintext string, stored StoredKey, err error) {
	secret := make([]byte, secretBytes)
	if _, err = rand.Read(secret); err != nil {
		return "", StoredKey{}, err
	}
	secretHex := hex.EncodeToString(secret)

	salt := make([]byte, 16)
	if _, err = rand.Read(salt); err != nil {
		return "", StoredKey{}, err
	}

	encodedUserID := base64.RawURLEncoding.EncodeToString([]byte(auth0UserID))
	plaintext = fmt.Sprintf("%s%s_%s", prefix, encodedUserID, secretHex)
	stored = StoredKey{
		Salt: hex.EncodeToString(salt),
		Hash: hashSecret(salt, secret),
	}
	return plaintext, stored, nil
}

// ParseUserID extracts the Auth0 user id embedded in an API key.
func ParseUserID(prefix, apiKey string) (string, error) {
	if !strings.HasPrefix(apiKey, prefix) {
		return "", fmt.Errorf("invalid api key prefix")
	}
	rest := strings.TrimPrefix(apiKey, prefix)
	parts := strings.SplitN(rest, "_", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("invalid api key format")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", fmt.Errorf("invalid api key encoding")
	}
	return string(raw), nil
}

// Verify checks a plaintext API key against stored salt/hash.
func Verify(apiKey string, stored StoredKey) bool {
	secretHex := apiKey[strings.LastIndex(apiKey, "_")+1:]
	secret, err := hex.DecodeString(secretHex)
	if err != nil {
		return false
	}
	salt, err := hex.DecodeString(stored.Salt)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(stored.Hash), []byte(hashSecret(salt, secret))) == 1
}

func hashSecret(salt, secret []byte) string {
	h := sha256.New()
	h.Write(salt)
	h.Write(secret)
	return hex.EncodeToString(h.Sum(nil))
}

// DemoEntry is one env-backed demo API key mapping.
type DemoEntry struct {
	ClientID         string `json:"clientId"`
	UserID           string `json:"userId"`
	UsageSubjectType string `json:"usageSubjectType"`
}

// LoadDemoStore parses DEMO_API_KEYS JSON into a map of apiKey -> entry.
func LoadDemoStore(raw string) (map[string]DemoEntry, error) {
	store := make(map[string]DemoEntry)
	if raw == "" {
		return store, nil
	}
	var parsed map[string]DemoEntry
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("DEMO_API_KEYS must be valid JSON: %w", err)
	}
	for key, entry := range parsed {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		userID := strings.TrimSpace(entry.UserID)
		if userID == "" {
			continue
		}
		clientID := strings.TrimSpace(entry.ClientID)
		if clientID == "" {
			clientID = "demo-client"
		}
		usageType := strings.TrimSpace(entry.UsageSubjectType)
		if usageType == "" {
			usageType = "api_key_user"
		}
		store[key] = DemoEntry{
			ClientID:         clientID,
			UserID:           userID,
			UsageSubjectType: usageType,
		}
	}
	return store, nil
}

// IsM2MSecret returns true when the bearer token looks like an M2M client secret.
func IsM2MSecret(token string) bool {
	return strings.HasPrefix(token, "pmth_cs_") || strings.Contains(token, "secret")
}
