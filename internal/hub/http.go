package hub

import (
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Handler builds the hub's complete HTTP surface: the agent ingest endpoint,
// the operator API and the embedded dashboard.
func (h *Hub) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/agent/push", h.handleAgentPush)
	mux.HandleFunc("GET /api/health", h.handleHealth)

	mux.HandleFunc("POST /api/auth/login", h.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", h.handleLogout)
	mux.HandleFunc("GET /api/auth/me", h.handleMe)

	mux.HandleFunc("GET /api/dashboard", h.requireAuth(h.handleDashboard))
	mux.HandleFunc("GET /api/stream", h.requireAuth(h.handleStream))
	mux.HandleFunc("GET /api/servers", h.requireAuth(h.handleListServers))
	mux.HandleFunc("POST /api/servers", h.requireAuth(h.handleCreateServer))
	mux.HandleFunc("PATCH /api/servers/{id}", h.requireAuth(h.handleUpdateServer))
	mux.HandleFunc("DELETE /api/servers/{id}", h.requireAuth(h.handleDeleteServer))
	mux.HandleFunc("POST /api/servers/{id}/token", h.requireAuth(h.handleRotateToken))
	mux.HandleFunc("POST /api/servers/{id}/update", h.requireAuth(h.handleAgentUpdate))
	mux.HandleFunc("GET /api/servers/{id}/history", h.requireAuth(h.handleHistory))
	mux.HandleFunc("GET /api/settings", h.requireAuth(h.handleGetSettings))
	mux.HandleFunc("POST /api/settings", h.requireAuth(h.handleSaveSettings))
	mux.HandleFunc("POST /api/settings/telegram/test", h.requireAuth(h.handleTelegramTest))
	mux.HandleFunc("GET /api/alerts", h.requireAuth(h.handleAlertEvents))
	mux.HandleFunc("POST /api/account", h.requireAuth(h.handleAccount))
	mux.HandleFunc("GET /api/backup", h.requireAuth(h.handleBackup))
	mux.HandleFunc("POST /api/restore", h.requireAuth(h.handleRestore))

	mux.Handle("/", h.staticHandler())

	return securityHeaders(sameOriginWrites(mux))
}

func (h *Hub) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"version": Version,
		"uptime":  int64(time.Since(h.startedAt).Seconds()),
	})
}

// securityHeaders keeps the dashboard out of frames and pins the asset origin;
// every script and style the page needs is served from the binary itself.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

// sameOriginWrites rejects cross-site state changes. Agents push with a bearer
// token and no Origin header, so they are unaffected.
func sameOriginWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(parsed.Host, r.Host) {
				writeJSON(w, http.StatusForbidden, apiError{Error: "cross-origin request rejected"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
