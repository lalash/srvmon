package hub

import (
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Setting keys. Thresholds are percentages; offline and sustain are seconds
// and consecutive-evaluation counts.
const (
	keyAlertsEnabled = "alert.enabled"
	keyAlertCPU      = "alert.cpu"
	keyAlertMem      = "alert.mem"
	keyAlertDisk     = "alert.disk"
	keyAlertOffline  = "alert.offline"
	keyAlertSustain  = "alert.sustain"
	keyTelegramToken = "telegram.token"
	keyTelegramChat  = "telegram.chat"
	keyBaseURL       = "hub.baseUrl"
)

type settingsPayload struct {
	AlertsEnabled      bool    `json:"alertsEnabled"`
	CpuThreshold       float64 `json:"cpuThreshold"`
	MemThreshold       float64 `json:"memThreshold"`
	DiskThreshold      float64 `json:"diskThreshold"`
	OfflineAfter       int     `json:"offlineAfter"`
	Sustain            int     `json:"sustain"`
	TelegramChatID     string  `json:"telegramChatId"`
	TelegramConfigured bool    `json:"telegramConfigured"`
	TelegramToken      string  `json:"telegramToken,omitempty"`
	BaseURL            string  `json:"baseUrl"`
}

func (h *Hub) handleGetSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, settingsPayload{
		AlertsEnabled:      h.store.Setting(keyAlertsEnabled, "1") == "1",
		CpuThreshold:       h.settingFloat(keyAlertCPU, 90),
		MemThreshold:       h.settingFloat(keyAlertMem, 85),
		DiskThreshold:      h.settingFloat(keyAlertDisk, 90),
		OfflineAfter:       int(h.offlineAfter()),
		Sustain:            h.settingInt(keyAlertSustain, 3),
		TelegramChatID:     h.store.Setting(keyTelegramChat, ""),
		TelegramConfigured: h.store.Setting(keyTelegramToken, "") != "",
		BaseURL:            h.store.Setting(keyBaseURL, ""),
	})
}

// handleSaveSettings persists the alert configuration. An empty Telegram token
// means "leave the stored one alone", so the secret never has to round-trip to
// the browser; the literal "-" clears it.
func (h *Hub) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsPayload
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}

	if in.OfflineAfter < 10 {
		in.OfflineAfter = 10
	}
	if in.Sustain < 1 {
		in.Sustain = 1
	}

	updates := map[string]string{
		keyAlertsEnabled: boolSetting(in.AlertsEnabled),
		keyAlertCPU:      formatFloat(clampPercent(in.CpuThreshold, 90)),
		keyAlertMem:      formatFloat(clampPercent(in.MemThreshold, 85)),
		keyAlertDisk:     formatFloat(clampPercent(in.DiskThreshold, 90)),
		keyAlertOffline:  strconv.Itoa(in.OfflineAfter),
		keyAlertSustain:  strconv.Itoa(in.Sustain),
		keyTelegramChat:  strings.TrimSpace(in.TelegramChatID),
		keyBaseURL:       strings.TrimRight(strings.TrimSpace(in.BaseURL), "/"),
	}
	if token := strings.TrimSpace(in.TelegramToken); token != "" {
		if token == "-" {
			token = ""
		}
		updates[keyTelegramToken] = token
	}

	for key, value := range updates {
		if err := h.store.SetSetting(key, value); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
			return
		}
	}
	h.handleGetSettings(w, r)
}

func (h *Hub) handleTelegramTest(w http.ResponseWriter, _ *http.Request) {
	if err := h.sendTelegram("✅ srvmon test message — alerts are wired up correctly."); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Hub) handleAlertEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := h.store.AlertEvents(limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

type accountInput struct {
	Username        string `json:"username"`
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

func (h *Hub) handleAccount(w http.ResponseWriter, r *http.Request) {
	var in accountInput
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}

	userID := userFrom(r.Context())
	user, err := h.store.UserByID(userID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
		return
	}
	if !checkPassword(user.PasswordHash, in.CurrentPassword) {
		writeJSON(w, http.StatusForbidden, apiError{Error: "current password is wrong"})
		return
	}

	username := strings.TrimSpace(in.Username)
	if username == "" {
		username = user.Username
	}
	password := in.NewPassword
	if password == "" {
		password = in.CurrentPassword
	}
	if len(password) < 8 {
		writeJSON(w, http.StatusBadRequest, apiError{Error: "password must be at least 8 characters"})
		return
	}

	if err := h.store.SetCredentials(user.ID, username, password); err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	// Every other cookie for this operator dies with the old password.
	_ = h.store.DeleteUserSessions(user.ID)
	h.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type loginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// loginLimiter throttles password guessing per client address.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

const (
	loginWindow      = 15 * time.Minute
	loginMaxAttempts = 10
)

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{attempts: map[string][]time.Time{}}
}

func (l *loginLimiter) blocked(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) >= loginMaxAttempts
}

func (l *loginLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attempts[key] = append(l.recent(key), time.Now())
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

// recent must be called with the mutex held.
func (l *loginLimiter) recent(key string) []time.Time {
	cutoff := time.Now().Add(-loginWindow)
	kept := l.attempts[key][:0]
	for _, at := range l.attempts[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	l.attempts[key] = kept
	return kept
}

func (h *Hub) handleLogin(w http.ResponseWriter, r *http.Request) {
	client := clientIP(r)
	if h.logins.blocked(client) {
		writeJSON(w, http.StatusTooManyRequests, apiError{Error: "too many attempts, try again later"})
		return
	}

	var in loginInput
	if err := decodeJSON(r, &in); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{Error: err.Error()})
		return
	}

	user, err := h.store.UserByName(strings.TrimSpace(in.Username))
	if err != nil || !checkPassword(user.PasswordHash, in.Password) {
		h.logins.fail(client)
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "wrong username or password"})
		return
	}

	token, err := h.store.CreateSession(user.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiError{Error: err.Error()})
		return
	}
	h.logins.reset(client)
	h.setSessionCookie(w, r, token)
	writeJSON(w, http.StatusOK, map[string]string{"username": user.Username})
}

func (h *Hub) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		_ = h.store.DeleteSession(cookie.Value)
	}
	h.clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *Hub) handleMe(w http.ResponseWriter, r *http.Request) {
	userID := h.currentUser(r)
	if userID == 0 {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
		return
	}
	user, err := h.store.UserByID(userID)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"username": user.Username, "version": Version})
}

func clientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if first, _, ok := strings.Cut(forwarded, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(forwarded)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (h *Hub) settingFloat(key string, fallback float64) float64 {
	value, err := strconv.ParseFloat(h.store.Setting(key, ""), 64)
	if err != nil {
		return fallback
	}
	return value
}

func (h *Hub) settingInt(key string, fallback int) int {
	value, err := strconv.Atoi(h.store.Setting(key, ""))
	if err != nil {
		return fallback
	}
	return value
}

func clampPercent(value, fallback float64) float64 {
	if value <= 0 || value > 100 {
		return fallback
	}
	return value
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func boolSetting(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
