package httpapi

import (
	"net/http"
	"net/url"
	"strings"
)

// withCORS wraps h so Kong Dev Portal (and other allowlisted origins) can call
// the API from the browser. OPTIONS preflight is answered without hitting routes.
func withCORS(h http.Handler, allowedOrigins []string, allowKongPortals bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" && corsOriginAllowed(origin, allowedOrigins, allowKongPortals) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Accept")
			w.Header().Set("Access-Control-Max-Age", "86400")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}

func corsOriginAllowed(origin string, allowed []string, allowKongPortals bool) bool {
	for _, a := range allowed {
		if a == "*" || strings.EqualFold(a, origin) {
			return true
		}
	}
	if !allowKongPortals {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Host)
	return strings.HasSuffix(host, ".kongportals.com") || host == "kongportals.com"
}
