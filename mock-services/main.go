package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

func main() {
	webhookSecret := os.Getenv("WEBHOOK_SECRET")
	authID := envOr("MOCK_AUTH_ID", "demo-client:demo-user")
	ethUSD := envOr("MOCK_ETH_USD", "3500.00")
	host := envOr("MOCK_HOST", "0.0.0.0")
	port := envOr("MOCK_PORT", "8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", authorizeHandler(webhookSecret, authID))
	mux.HandleFunc("/events", eventsHandler())
	mux.HandleFunc("/prices", pricesHandler(ethUSD))

	addr := fmt.Sprintf("%s:%s", host, port)
	log.Printf("mock-services: listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, requestLogger(mux)))
}

func authorizeHandler(secret, authID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if secret != "" {
			expected := "Bearer " + secret
			if r.Header.Get("Authorization") != expected {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		if len(body) > 0 {
			log.Printf("mock-services: authorize payload=%s", body)
		}

		// Mirror the identity webhook contract: go-livepeer stamps the returned
		// auth_id ("client_id:usage_subject") onto the Kafka event it emits.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"status":200,"auth_id":%q}`, authID)
	}
}

func eventsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "failed to read body", http.StatusInternalServerError)
			return
		}
		if len(body) > 0 {
			log.Printf("mock-services: ingest payload=%s", body)
		}

		w.WriteHeader(http.StatusNoContent)
	}
}

func pricesHandler(ethUSD string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		// Coinbase spot shape — collector parses data.amount (also accepts
		// {"price":…} and {"ethereum":{"usd":…}}). Set PRICE_ORACLE_URL to this
		// endpoint to warm the collector's ETH/USD cache offline.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"data":{"base":"ETH","currency":"USD","amount":%q}}`, ethUSD)
	}
}

func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("mock-services: %s %s %s", r.Method, r.URL.Path, r.Proto)
		next.ServeHTTP(w, r)
	})
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
