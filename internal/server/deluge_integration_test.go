// file: internal/server/deluge_integration_test.go
// version: 2.3.0
// guid: 7a8b9c0d-1e2f-3a4b-5c6d-7e8f9a0b1c2d
// last-edited: 2026-09-12
//
// Integration tests for Deluge notification helpers and HTTP handlers.
// Service logic moved to internal/deluge/integration.go; tests updated to
// use deluge.SetGlobalClientForTest for client injection.

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/gin-gonic/gin"
)

func TestNotifyDelugeMoveStorage_EmptyHash(t *testing.T) {
	// Should silently no-op with empty hash.
	deluge.NotifyDelugeMoveStorage("", "/some/path")
}

func TestNotifyDelugeMoveStorage_NoClient(t *testing.T) {
	// Inject nil client and clear config so GetClient returns nil.
	restore := deluge.SetGlobalClientForTest(nil)
	origURL := config.AppConfig.DelugeWebURL
	config.AppConfig.DelugeWebURL = ""
	origHost := config.AppConfig.DownloadClient.Torrent.Deluge.Host
	config.AppConfig.DownloadClient.Torrent.Deluge.Host = ""
	defer func() {
		restore()
		config.AppConfig.DelugeWebURL = origURL
		config.AppConfig.DownloadClient.Torrent.Deluge.Host = origHost
	}()

	// Should silently no-op when Deluge is not configured.
	deluge.NotifyDelugeMoveStorage("abc123", "/new/path")
}

func TestNotifyDelugeMoveStorage_WithMockServer(t *testing.T) {
	var calledMoveStorage bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
			Params []any  `json:"params"`
			ID     int64  `json:"id"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": true})
		case "core.move_storage":
			calledMoveStorage = true
			json.NewEncoder(w).Encode(map[string]any{"id": req.ID, "result": nil})
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	// Create a real deluge client pointing to our mock.
	client, err := deluge.New(srv.URL, "deluge")
	if err != nil {
		t.Fatalf("create client: %v", err)
	}

	restore := deluge.SetGlobalClientForTest(client)
	origMove := config.AppConfig.DelugeMoveEnabled
	config.AppConfig.DelugeMoveEnabled = true
	defer func() {
		restore()
		config.AppConfig.DelugeMoveEnabled = origMove
	}()

	deluge.NotifyDelugeMoveStorage("abc123", "/new/path/to/book.m4b")

	if !calledMoveStorage {
		t.Error("expected MoveStorage to be called")
	}
}

func TestHandleDelugeStatus_NotConfigured(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStoreInMemory(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	origURL := config.AppConfig.DelugeWebURL
	origHost := config.AppConfig.DownloadClient.Torrent.Deluge.Host
	config.AppConfig.DelugeWebURL = ""
	config.AppConfig.DownloadClient.Torrent.Deluge.Host = ""
	defer func() {
		config.AppConfig.DelugeWebURL = origURL
		config.AppConfig.DownloadClient.Torrent.Deluge.Host = origHost
	}()

	srv := NewServer(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deluge/status", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Configured bool   `json:"configured"`
		URL        string `json:"url"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Configured {
		t.Error("expected configured=false when not set")
	}
}

func TestHandleDelugeStatus_Configured(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStoreInMemory(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	origStore := database.GetGlobalStore()
	database.SetGlobalStore(store)
	t.Cleanup(func() {
		database.SetGlobalStore(origStore)
		store.Close()
	})

	origURL := config.AppConfig.DelugeWebURL
	config.AppConfig.DelugeWebURL = "http://localhost:8112"
	defer func() {
		config.AppConfig.DelugeWebURL = origURL
		// NewServer's deluge-plugin Build calls deluge.GetClient() while the
		// URL above is set, which CACHES a live client in the package-level
		// singleton. Restoring the URL string alone leaks that client into
		// every later test in the process (and across -count=N iterations),
		// where it flips the deluge plugin live inside MockStore-based server
		// tests → unexpected UpsertOpDefinitionV2 mock failures.
		deluge.ResetGlobalClientForTest()
	}()

	srv := NewServer(store)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/deluge/status", nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var envelope struct {
		Data struct {
			Configured bool   `json:"configured"`
			URL        string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &envelope)
	if !envelope.Data.Configured {
		t.Error("expected configured=true")
	}
	if envelope.Data.URL != "http://localhost:8112" {
		t.Errorf("expected url=http://localhost:8112, got %s", envelope.Data.URL)
	}
}

func TestNotifyDelugeAfterVersionSwap(t *testing.T) {
	gin.SetMode(gin.TestMode)

	pebblePath := filepath.Join(t.TempDir(), "pebble")
	store, err := database.NewPebbleStoreInMemory(pebblePath)
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBook(&database.Book{
		ID: "b1", Title: "Test Book", FilePath: "/lib/books/b1/book.m4b",
	})

	// With no Deluge client configured, should not panic.
	restore := deluge.SetGlobalClientForTest(nil)
	origURL := config.AppConfig.DelugeWebURL
	config.AppConfig.DelugeWebURL = ""
	origHost := config.AppConfig.DownloadClient.Torrent.Deluge.Host
	config.AppConfig.DownloadClient.Torrent.Deluge.Host = ""
	defer func() {
		restore()
		config.AppConfig.DelugeWebURL = origURL
		config.AppConfig.DownloadClient.Torrent.Deluge.Host = origHost
	}()

	fromVer := &database.BookVersion{ID: "v1", BookID: "b1", TorrentHash: "abc123"}
	toVer := &database.BookVersion{ID: "v2", BookID: "b1", TorrentHash: "def456"}

	// Should not panic even without Deluge configured.
	deluge.NotifyDelugeAfterVersionSwap(store, fromVer, toVer, "/lib/books/b1/book.m4b")
}
