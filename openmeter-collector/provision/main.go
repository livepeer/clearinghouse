package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

type ensureRequest struct {
	ClientID       string         `json:"client_id"`
	ClientIDAlt    string         `json:"clientId"`
	ExternalUserID string         `json:"external_user_id"`
	ExternalUserAlt  string         `json:"externalUserId"`
	AuthID         string         `json:"auth_id"`
	AuthIDAlt        string         `json:"authId"`
	Subject        string         `json:"subject"`
	Data           map[string]any `json:"data"`
}

func requiredEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func stringField(body map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := body[key]; ok {
			if value, ok := raw.(string); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	return ""
}

func parseIdentity(body ensureRequest) (clientID, externalUserID string, ok bool) {
	data := body.Data
	if data == nil {
		data = map[string]any{}
	}

	clientID = firstNonEmpty(
		body.ClientID,
		body.ClientIDAlt,
		stringField(data, "client_id", "clientId"),
	)
	externalUserID = firstNonEmpty(
		body.ExternalUserID,
		body.ExternalUserAlt,
		stringField(data, "external_user_id", "externalUserId"),
	)
	if clientID != "" && externalUserID != "" {
		return clientID, externalUserID, true
	}

	authID := firstNonEmpty(
		body.AuthID,
		body.AuthIDAlt,
		stringField(data, "auth_id", "authId"),
		body.Subject,
	)
	colon := strings.Index(authID, ":")
	if colon > 0 && colon < len(authID)-1 {
		return authID[:colon], authID[colon+1:], true
	}
	return "", "", false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func main() {
	port := strings.TrimSpace(os.Getenv("PROVISION_PORT"))
	if port == "" {
		port = "8091"
	}
	planKey := strings.TrimSpace(os.Getenv("OPENMETER_DEFAULT_PLAN_KEY"))
	if planKey == "" {
		planKey = "clearinghouse_default_ppu"
	}

	baseURL, err := requiredEnv("OPENMETER_URL")
	if err != nil {
		log.Fatal(err)
	}
	apiKey, err := requiredEnv("OPENMETER_API_KEY")
	if err != nil {
		log.Fatal(err)
	}

	provisioner := NewProvisioner(baseURL, apiKey, planKey)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/ensure", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}

		var body ensureRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request json"})
				return
			}
		}

		clientID, externalUserID, ok := parseIdentity(body)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "client_id and external_user_id (or auth_id) are required",
			})
			return
		}

		result, err := provisioner.Ensure(r.Context(), ProvisionInput{
			ClientID:       clientID,
			ExternalUserID: externalUserID,
			DisplayName:    fmt.Sprintf("%s:%s", clientID, externalUserID),
		})
		if err != nil {
			log.Printf("provision-server ensure failed: %v", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})

	addr := "127.0.0.1:" + port
	log.Printf("provision-server listening on %s plan=%s", addr, planKey)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}
