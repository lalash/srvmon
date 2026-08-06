package hub

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionCookie = "srvmon_session"
	sessionTTL    = 14 * 24 * time.Hour
)

// User is a dashboard operator.
type User struct {
	ID           int64
	Username     string
	PasswordHash string
}

// CountUsers reports how many operators exist; zero means first run.
func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CreateUser stores an operator with a bcrypt-hashed password.
func (s *Store) CreateUser(username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO users (username, password_hash, created_at) VALUES (?, ?, ?)`,
		username, string(hash), time.Now().Unix())
	return err
}

// UserByName loads an operator for a login attempt.
func (s *Store) UserByName(username string) (*User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, username, password_hash FROM users WHERE username = ?`, username).
		Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// UserByID loads an operator resolved from a session.
func (s *Store) UserByID(id int64) (*User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, username, password_hash FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

// SetCredentials updates the username and password of an existing operator.
func (s *Store) SetCredentials(id int64, username, password string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE users SET username = ?, password_hash = ? WHERE id = ?`,
		username, string(hash), id)
	return err
}

// CreateSession issues a login cookie value valid for sessionTTL.
func (s *Store) CreateSession(userID int64) (string, error) {
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(sessionTTL).Unix()
	if _, err := s.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, userID, expires); err != nil {
		return "", err
	}
	return token, nil
}

// SessionUser resolves a cookie value to its operator id.
func (s *Store) SessionUser(token string) (int64, error) {
	var userID, expires int64
	err := s.db.QueryRow(`SELECT user_id, expires_at FROM sessions WHERE token = ?`, token).
		Scan(&userID, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if expires < time.Now().Unix() {
		_ = s.DeleteSession(token)
		return 0, ErrNotFound
	}
	return userID, nil
}

// DeleteSession logs one cookie out.
func (s *Store) DeleteSession(token string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
	return err
}

// DeleteUserSessions logs an operator out everywhere, used after a password change.
func (s *Store) DeleteUserSessions(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PruneSessions drops expired cookies.
func (s *Store) PruneSessions() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}

// currentUser returns the operator behind the request, or 0 when signed out.
func (h *Hub) currentUser(r *http.Request) int64 {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return 0
	}
	userID, err := h.store.SessionUser(cookie.Value)
	if err != nil {
		return 0
	}
	return userID
}

func (h *Hub) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(sessionTTL),
	})
}

func (h *Hub) clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   isTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func isTLS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// requireAuth guards every operator-facing endpoint.
func (h *Hub) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := h.currentUser(r)
		if userID == 0 {
			writeJSON(w, http.StatusUnauthorized, apiError{Error: "unauthorized"})
			return
		}
		next(w, r.WithContext(withUser(r.Context(), userID)))
	}
}

// checkPassword reports whether the plaintext matches the stored hash.
func checkPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
