// file: internal/server/bootstrap_security_test.go
// version: 1.1.0
// guid: 2b9d4e6a-1c3f-4a8b-bd72-5e0a9c4f1d36
// last-edited: 2026-07-03

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeSettingsStore is a minimal in-memory SettingsReadWriter that mirrors the
// real backends' contract: a missing key returns ErrSettingNotFound (wrapped).
type fakeSettingsStore struct {
	m map[string]string
}

func newFakeSettingsStore() *fakeSettingsStore {
	return &fakeSettingsStore{m: map[string]string{}}
}

func (f *fakeSettingsStore) GetSetting(key string) (*database.Setting, error) {
	v, ok := f.m[key]
	if !ok {
		return nil, fmt.Errorf("setting not found: %s: %w", key, database.ErrSettingNotFound)
	}
	return &database.Setting{Key: key, Value: v}, nil
}

func (f *fakeSettingsStore) SetSetting(key, value, _ string, _ bool) error {
	f.m[key] = value
	return nil
}

func (f *fakeSettingsStore) DeleteSetting(key string) error {
	delete(f.m, key)
	return nil
}

// HIGH-4a: once the one-time token is consumed (key deleted), GetSetting returns
// ErrSettingNotFound. ConsumeBootstrapToken must treat that as an invalid token
// — (false, nil) — so the handler returns 401, not 500.
func TestConsumeBootstrapToken_ConsumedTokenIsUnauthorizedNotError(t *testing.T) {
	store := newFakeSettingsStore() // empty — token key absent (already consumed)
	dataDir := t.TempDir()

	valid, err := ConsumeBootstrapToken(store, dataDir, "abbs_anything")
	if err != nil {
		t.Fatalf("expected nil error for a consumed token, got: %v", err)
	}
	if valid {
		t.Fatal("expected valid=false for a consumed token")
	}
}

// A wrong token (key present, hash mismatch) is also (false, nil), not an error.
func TestConsumeBootstrapToken_WrongTokenIsUnauthorized(t *testing.T) {
	store := newFakeSettingsStore()
	_ = store.SetSetting(bootstrapTokenKey, hashBootstrapToken("abbs_correct"), "string", false)
	dataDir := t.TempDir()

	valid, err := ConsumeBootstrapToken(store, dataDir, "abbs_wrong")
	if err != nil {
		t.Fatalf("expected nil error for a wrong token, got: %v", err)
	}
	if valid {
		t.Fatal("expected valid=false for a wrong token")
	}
}

// The happy path still works: a matching token consumes successfully.
func TestConsumeBootstrapToken_CorrectTokenConsumes(t *testing.T) {
	store := newFakeSettingsStore()
	_ = store.SetSetting(bootstrapTokenKey, hashBootstrapToken("abbs_correct"), "string", false)
	dataDir := t.TempDir()

	valid, err := ConsumeBootstrapToken(store, dataDir, "abbs_correct")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !valid {
		t.Fatal("expected valid=true for the correct token")
	}
	// Token must be gone after consumption (single-use).
	if _, err := store.GetSetting(bootstrapTokenKey); err == nil {
		t.Fatal("expected token key to be deleted after consumption")
	}
}

// TestHandleBootstrap_IssuesExpiringKey verifies SEC-1/PROC-6: bootstrap-issued
// keys now carry a non-nil ExpiresAt derived from
// config.AppConfig.BootstrapKeyTTLDays (default 30d when unset/non-positive),
// and the JSON response surfaces expires_at.
func TestHandleBootstrap_IssuesExpiringKey(t *testing.T) {
	origCfg := config.AppConfig
	defer func() { config.AppConfig = origCfg }()
	config.AppConfig.BootstrapKeyTTLDays = 0 // exercise the <=0 -> 30 fallback
	config.AppConfig.DatabasePath = t.TempDir() + "/db"

	settings := newFakeSettingsStore()
	_ = settings.SetSetting(bootstrapTokenKey, hashBootstrapToken("abbs_correct"), "string", false)

	adminUser := &database.User{ID: "admin-1", Username: "admin", Roles: []string{"admin"}}
	adminRole := &database.Role{ID: "admin", Name: "admin", Permissions: []string{string(auth.PermUsersManage)}}

	var createdKey *database.APIKey
	mockStore := &database.MockStore{
		GetSettingFunc:    settings.GetSetting,
		SetSettingFunc:    settings.SetSetting,
		DeleteSettingFunc: settings.DeleteSetting,
		GetRoleByNameFunc: func(name string) (*database.Role, error) {
			if name == "admin" {
				return adminRole, nil
			}
			return nil, nil
		},
		ListUsersFunc: func() ([]database.User, error) {
			return []database.User{*adminUser}, nil
		},
		CreateAPIKeyFunc: func(key *database.APIKey) (*database.APIKey, error) {
			key.ID = "key-bootstrap-1"
			createdKey = key
			return key, nil
		},
	}

	s := &Server{store: mockStore}

	gin.SetMode(gin.TestMode)
	body, _ := json.Marshal(map[string]string{"token": "abbs_correct"})
	req := httptest.NewRequest("POST", "/api/v1/auth/bootstrap", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "203.0.113.77:12345"
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	s.handleBootstrap(c)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	if createdKey == nil {
		t.Fatal("CreateAPIKey was not called")
	}
	if createdKey.ExpiresAt == nil {
		t.Fatal("bootstrap-issued key must have a non-nil ExpiresAt")
	}
	wantMin := time.Now().Add(29 * 24 * time.Hour)
	wantMax := time.Now().Add(31 * 24 * time.Hour)
	if createdKey.ExpiresAt.Before(wantMin) || createdKey.ExpiresAt.After(wantMax) {
		t.Errorf("ExpiresAt = %v, want roughly 30 days from now (between %v and %v)", createdKey.ExpiresAt, wantMin, wantMax)
	}

	var resp struct {
		Data struct {
			ExpiresAt *time.Time `json:"expires_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Data.ExpiresAt == nil {
		t.Fatal("bootstrapResp JSON must include a non-nil expires_at")
	}
}
