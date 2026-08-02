// file: internal/server/reset_handler_test.go
// version: 1.4.0
// guid: 9a0b1c2d-3e4f-5a6b-7c8d-9e0f1a2b3c4d
// last-edited: 2026-07-16

package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
)

// TestResetSystem_Success tests the reset endpoint with successful reset
func TestResetSystem_Success(t *testing.T) {
	origCfg := config.AppConfig
	defer func() { config.AppConfig = origCfg }()

	server := setupHandlerTestServer(t)

	// Mock the Reset function
	if store, ok := database.GetGlobalStore().(*database.MockStore); ok {
		store.ResetFunc = func() error {
			return nil
		}
	}

	// Simulate a modified config state before reset
	originalSetupComplete := config.AppConfig.SetupComplete
	originalAutoOrganize := config.AppConfig.AutoOrganize
	config.AppConfig.SetupComplete = true
	config.AppConfig.AutoOrganize = false

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/system/reset", bytes.NewReader([]byte(`{"confirm":"RESET"}`)))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	newSystemHandler(server).ResetSystem(c)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp struct {
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Data.Message != "System reset successfully" {
		t.Errorf("expected 'System reset successfully', got %q", resp.Data.Message)
	}

	// Verify that config was actually reset to defaults
	if config.AppConfig.SetupComplete != false {
		t.Errorf("expected SetupComplete to be reset to false, got %v", config.AppConfig.SetupComplete)
	}
	if config.AppConfig.AutoOrganize != true {
		t.Errorf("expected AutoOrganize to be reset to true, got %v", config.AppConfig.AutoOrganize)
	}

	// Restore original values for test cleanup
	config.AppConfig.SetupComplete = originalSetupComplete
	config.AppConfig.AutoOrganize = originalAutoOrganize
}

// TestResetSystem_Error tests reset when database reset fails
func TestResetSystem_Error(t *testing.T) {
	server := setupHandlerTestServer(t)

	// Mock the Reset function to return an error
	if store, ok := database.GetGlobalStore().(*database.MockStore); ok {
		store.ResetFunc = func() error {
			return errors.New("test error")
		}
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/system/reset", bytes.NewReader([]byte(`{"confirm":"RESET"}`)))
	req.Header.Set("Content-Type", "application/json")
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	newSystemHandler(server).ResetSystem(c)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", w.Code)
	}
}

// TestResetSystem_MultipleResets tests that multiple consecutive resets work correctly
func TestResetSystem_MultipleResets(t *testing.T) {
	origCfg := config.AppConfig
	defer func() { config.AppConfig = origCfg }()

	server := setupHandlerTestServer(t)

	// Mock the Reset function
	if store, ok := database.GetGlobalStore().(*database.MockStore); ok {
		store.ResetFunc = func() error {
			return nil
		}
	}

	// Perform first reset
	originalLogLevel := config.AppConfig.LogLevel
	config.AppConfig.LogLevel = "debug"

	w1 := httptest.NewRecorder()
	req1 := httptest.NewRequest("POST", "/api/v1/system/reset", bytes.NewReader([]byte(`{"confirm":"RESET"}`)))
	req1.Header.Set("Content-Type", "application/json")
	c1, _ := gin.CreateTestContext(w1)
	c1.Request = req1

	newSystemHandler(server).ResetSystem(c1)

	if w1.Code != http.StatusOK {
		t.Errorf("first reset: expected status 200, got %d", w1.Code)
	}

	if config.AppConfig.LogLevel != "info" {
		t.Errorf("first reset: expected LogLevel to be reset to 'info', got %q", config.AppConfig.LogLevel)
	}

	// Perform second reset with modified config
	config.AppConfig.LogLevel = "debug"

	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/system/reset", bytes.NewReader([]byte(`{"confirm":"RESET"}`)))
	req2.Header.Set("Content-Type", "application/json")
	c2, _ := gin.CreateTestContext(w2)
	c2.Request = req2

	newSystemHandler(server).ResetSystem(c2)

	if w2.Code != http.StatusOK {
		t.Errorf("second reset: expected status 200, got %d", w2.Code)
	}

	if config.AppConfig.LogLevel != "info" {
		t.Errorf("second reset: expected LogLevel to be reset to 'info', got %q", config.AppConfig.LogLevel)
	}

	// Restore original value for test cleanup
	config.AppConfig.LogLevel = originalLogLevel
}
