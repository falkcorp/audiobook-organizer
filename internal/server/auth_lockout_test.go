// file: internal/server/auth_lockout_test.go
// version: 2.1.0
// guid: 8c4e5f3a-9b5a-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-06-04

package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// stubAuthStore is a minimal in-memory AuthStore for lockout tests.
type stubAuthStore struct {
	user *database.User
	err  error
}

func (s *stubAuthStore) CountUsers() (int, error) { return 1, nil }
func (s *stubAuthStore) CreateSession(userID, ip, userAgent string, ttl time.Duration) (*database.Session, error) {
	return &database.Session{ID: "sess-1", UserID: userID, ExpiresAt: time.Now().Add(ttl)}, nil
}
func (s *stubAuthStore) CreateUser(username, email, algo, hash string, roles []string, status string) (*database.User, error) {
	return nil, nil
}
func (s *stubAuthStore) GetRoleByID(id string) (*database.Role, error)     { return nil, nil }
func (s *stubAuthStore) GetRoleByName(name string) (*database.Role, error) { return nil, nil }
func (s *stubAuthStore) GetSession(id string) (*database.Session, error)   { return nil, nil }
func (s *stubAuthStore) GetUserByID(id string) (*database.User, error)     { return s.user, s.err }
func (s *stubAuthStore) GetUserByUsername(username string) (*database.User, error) {
	return s.user, s.err
}
func (s *stubAuthStore) ListUserSessions(userID string) ([]database.Session, error) {
	return nil, nil
}
func (s *stubAuthStore) RevokeSession(id string) error        { return nil }
func (s *stubAuthStore) UpdateUser(user *database.User) error { return nil }

func newTestUser(t *testing.T) *database.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte("correctpassword"), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	return &database.User{
		ID:               "test-lockout-user",
		Username:         "testuser",
		Email:            "test@local",
		PasswordHashAlgo: "bcrypt",
		PasswordHash:     string(hash),
		Roles:            []string{"user"},
		Status:           "active",
	}
}

func loginRequest(t *testing.T, h *handlers.AuthHandler, password string) int {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	body, _ := json.Marshal(map[string]string{
		"username": "testuser",
		"password": password,
	})
	c.Request = httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.Login(c)
	return w.Code
}

// HIGH-3: there is no longer a hard per-account lockout (which a third party
// could weaponize against a known user). Instead a per-IP throttle trips after
// the source IP exhausts its failure budget. Uses an unknown user so the soft
// per-account delay never fires (sleep-free, deterministic), and all attempts
// here share the same source IP.
func TestThrottle_TriggersAfterMaxIPFailures(t *testing.T) {
	store := &stubAuthStore{user: nil}
	h := handlers.NewAuthHandler(store, true)

	const maxFailedPerIP = 15

	for i := 0; i < maxFailedPerIP; i++ {
		code := loginRequest(t, h, "wrongpassword")
		if code == http.StatusTooManyRequests {
			t.Fatalf("throttled after only %d attempts, want %d", i, maxFailedPerIP)
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, code)
		}
	}

	code := loginRequest(t, h, "wrongpassword")
	if code != http.StatusTooManyRequests {
		t.Errorf("after %d failures from one IP: got %d, want 429", maxFailedPerIP, code)
	}
}

func TestLockout_ClearedOnSuccess(t *testing.T) {
	store := &stubAuthStore{user: newTestUser(t)}
	h := handlers.NewAuthHandler(store, true)

	const maxFailedLogins = 10

	for i := 0; i < maxFailedLogins-1; i++ {
		loginRequest(t, h, "wrongpassword")
	}

	// Successful login should clear the counter.
	code := loginRequest(t, h, "correctpassword")
	if code != http.StatusOK {
		t.Fatalf("successful login: got %d, want 200", code)
	}

	// After clearing, wrong password should not immediately lock out.
	code = loginRequest(t, h, "wrongpassword")
	if code == http.StatusTooManyRequests {
		t.Error("should not be locked out after a successful login cleared the counter")
	}
}

func TestLockout_NoLockoutForUnknownUser(t *testing.T) {
	// Unknown user: store returns nil user, no lockout entry exists.
	store := &stubAuthStore{user: nil}
	h := handlers.NewAuthHandler(store, true)

	code := loginRequest(t, h, "anything")
	if code == http.StatusTooManyRequests {
		t.Error("unknown user should not be locked out")
	}
	if code != http.StatusUnauthorized {
		t.Errorf("unknown user: got %d, want 401", code)
	}
}
