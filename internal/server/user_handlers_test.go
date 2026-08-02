// file: internal/server/user_handlers_test.go
// version: 1.0.1

package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

func setupUserHandlerServer(t *testing.T) (*Server, database.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStore(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	srv := NewServer(store)
	return srv, store
}

func TestHandleListUsers_Empty(t *testing.T) {
	srv, _ := setupUserHandlerServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Users []map[string]interface{} `json:"users"`
			Count int                      `json:"count"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Count != 0 {
		t.Errorf("expected 0 users, got %d", resp.Data.Count)
	}
}

func TestHandleListUsers_WithUsers(t *testing.T) {
	srv, store := setupUserHandlerServer(t)

	_, _ = store.CreateUser("alice", "alice@example.com", "bcrypt", "hash123", []string{"admin"}, "active")
	_, _ = store.CreateUser("bob", "bob@example.com", "bcrypt", "hash456", []string{"viewer"}, "active")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Users []map[string]interface{} `json:"users"`
			Count int                      `json:"count"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Count != 2 {
		t.Errorf("expected 2 users, got %d", resp.Data.Count)
	}

	// Verify password hash is NOT exposed.
	for _, u := range resp.Data.Users {
		if _, ok := u["password_hash"]; ok {
			t.Error("password_hash should not be exposed in list response")
		}
	}
}

func TestHandleCreateInvite(t *testing.T) {
	srv, store := setupUserHandlerServer(t)

	// Create a role first since the invite references a role_id.
	_, _ = store.CreateRole(&database.Role{
		ID: "editor", Name: "editor", Permissions: []string{"library.view"},
	})

	body, _ := json.Marshal(map[string]interface{}{
		"username": "newuser",
		"role_id":  "editor",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Token  string           `json:"token"`
			Invite *database.Invite `json:"invite"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Token == "" {
		t.Error("expected non-empty token")
	}
	if resp.Data.Invite == nil {
		t.Fatal("expected invite in response")
	}
	if resp.Data.Invite.Username != "newuser" {
		t.Errorf("expected username=newuser, got %s", resp.Data.Invite.Username)
	}
}

func TestHandleCreateInvite_MissingFields(t *testing.T) {
	srv, _ := setupUserHandlerServer(t)

	body, _ := json.Marshal(map[string]string{
		"username": "testuser",
		// missing role_id
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/invite", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing role_id, got %d", w.Code)
	}
}

func TestHandleListInvites(t *testing.T) {
	srv, _ := setupUserHandlerServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/invites", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Data struct {
			Invites []database.Invite `json:"invites"`
			Count   int               `json:"count"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Data.Count != 0 {
		t.Errorf("expected 0 invites, got %d", resp.Data.Count)
	}
}

// TestGenerateToken was removed in Phase 2: generateToken moved to
// internal/server/handlers/user.go (unexported). It is covered by
// the handler-level invite tests in that package.
