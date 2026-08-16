// file: internal/server/deluge_centralization_test.go
// version: 1.1.0
// guid: 3b7c4d2a-1e5f-4870-b8c5-9f0e1d2c3a4b
// last-edited: 2026-08-15
//
// Tests for NotifyDelugeAfterOrganize: the integration between the
// library centralization (organize) pipeline and Deluge's move_storage.
//
// These tests verify the four spec-required scenarios:
//  1. Enabled + TorrentHash → MoveStorage is called
//  2. Disabled + TorrentHash → MoveStorage is NOT called
//  3. Enabled + empty TorrentHash → MoveStorage is NOT called
//  4. MoveStorage error → centralization still succeeds (best-effort)

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/deluge"
)

// mockDelugeServer spins up a minimal JSON-RPC stub for Deluge.
// It counts core.move_storage calls and can be configured to return
// an error response for that method.
type mockDelugeServer struct {
	moveStorageCalls atomic.Int32
	returnError      bool
	server           *httptest.Server
}

func newMockDelugeServer(t *testing.T, returnError bool) *mockDelugeServer {
	t.Helper()
	m := &mockDelugeServer{returnError: returnError}
	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string        `json:"method"`
			Params []interface{} `json:"params"`
			ID     int64         `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		switch req.Method {
		case "auth.login":
			json.NewEncoder(w).Encode(map[string]interface{}{"id": req.ID, "result": true})
		case "core.move_storage":
			m.moveStorageCalls.Add(1)
			if m.returnError {
				json.NewEncoder(w).Encode(map[string]interface{}{
					"id":    req.ID,
					"error": map[string]interface{}{"code": -1, "message": "deluge offline"},
				})
			} else {
				json.NewEncoder(w).Encode(map[string]interface{}{"id": req.ID, "result": nil})
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(m.server.Close)
	return m
}

// wireMockDeluge points the global Deluge client at mock and enables
// DelugeMoveEnabled. Returns a cleanup function.
func wireMockDeluge(t *testing.T, mock *mockDelugeServer) {
	t.Helper()
	client, err := deluge.New(mock.server.URL, "deluge")
	if err != nil {
		t.Fatalf("create mock deluge client: %v", err)
	}

	restoreClient := deluge.SetGlobalClientForTest(client)
	origURL := config.AppConfig.DelugeWebURL
	origMove := config.AppConfig.DelugeMoveEnabled
	config.AppConfig.DelugeWebURL = mock.server.URL
	config.AppConfig.DelugeMoveEnabled = true
	t.Cleanup(func() {
		restoreClient()
		config.AppConfig.DelugeWebURL = origURL
		config.AppConfig.DelugeMoveEnabled = origMove
	})
}

// newTestStoreWithVersion creates a PebbleStore, inserts a book and an
// active BookVersion with the given torrentHash, and returns the store.
func newTestStoreWithVersion(t *testing.T, bookID, torrentHash string) *database.PebbleStore {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, err = store.CreateBook(&database.Book{
		ID:       bookID,
		Title:    "Test Book",
		FilePath: "/old/path/book.m4b",
	})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}
	_, err = store.CreateBookVersion(&database.BookVersion{
		BookID:      bookID,
		Status:      database.BookVersionStatusActive,
		Format:      "m4b",
		Source:      "deluge",
		TorrentHash: torrentHash,
	})
	if err != nil {
		t.Fatalf("create book version: %v", err)
	}
	return store
}

// TestNotifyDelugeAfterOrganize_CallsMoveStorage verifies that when
// DelugeMoveEnabled=true and the active BookVersion has a TorrentHash,
// NotifyDelugeAfterOrganize calls core.move_storage exactly once.
func TestNotifyDelugeAfterOrganize_CallsMoveStorage(t *testing.T) {
	mock := newMockDelugeServer(t, false)
	wireMockDeluge(t, mock)

	store := newTestStoreWithVersion(t, "book1", "abc123deadbeef")
	NotifyDelugeAfterOrganize(store, "book1", "/new/library/path/book.m4b")

	if got := mock.moveStorageCalls.Load(); got != 1 {
		t.Errorf("expected 1 move_storage call, got %d", got)
	}
}

// TestNotifyDelugeAfterOrganize_SkipsWhenDisabled verifies that when
// DelugeMoveEnabled=false, MoveStorage is never called even if a
// TorrentHash is present.
func TestNotifyDelugeAfterOrganize_SkipsWhenDisabled(t *testing.T) {
	mock := newMockDelugeServer(t, false)
	// Wire the client but override DelugeMoveEnabled to false.
	client, err := deluge.New(mock.server.URL, "deluge")
	if err != nil {
		t.Fatalf("create mock deluge client: %v", err)
	}
	restoreClient := deluge.SetGlobalClientForTest(client)
	origMove := config.AppConfig.DelugeMoveEnabled
	config.AppConfig.DelugeMoveEnabled = false
	t.Cleanup(func() {
		restoreClient()
		config.AppConfig.DelugeMoveEnabled = origMove
	})

	store := newTestStoreWithVersion(t, "book2", "abc123deadbeef")
	NotifyDelugeAfterOrganize(store, "book2", "/new/library/path/book.m4b")

	if got := mock.moveStorageCalls.Load(); got != 0 {
		t.Errorf("expected 0 move_storage calls when disabled, got %d", got)
	}
}

// TestNotifyDelugeAfterOrganize_SkipsWhenNoTorrentHash verifies that
// when the active BookVersion has an empty TorrentHash, MoveStorage is
// not called (the book was not torrent-sourced).
func TestNotifyDelugeAfterOrganize_SkipsWhenNoTorrentHash(t *testing.T) {
	mock := newMockDelugeServer(t, false)
	wireMockDeluge(t, mock)

	store := newTestStoreWithVersion(t, "book3", "") // empty hash
	NotifyDelugeAfterOrganize(store, "book3", "/new/library/path/book.m4b")

	if got := mock.moveStorageCalls.Load(); got != 0 {
		t.Errorf("expected 0 move_storage calls for non-torrent book, got %d", got)
	}
}

// TestNotifyDelugeAfterOrganize_DelugeErrorIsBestEffort verifies that
// a MoveStorage error from Deluge does NOT cause NotifyDelugeAfterOrganize
// to return an error — the call is best-effort and the organize already
// succeeded when this function is invoked.
func TestNotifyDelugeAfterOrganize_DelugeErrorIsBestEffort(t *testing.T) {
	mock := newMockDelugeServer(t, true) // configured to return error
	wireMockDeluge(t, mock)

	store := newTestStoreWithVersion(t, "book4", "abc123deadbeef")

	// Must not panic. NotifyDelugeAfterOrganize has no return value by
	// design — errors are logged but never surfaced to the caller.
	NotifyDelugeAfterOrganize(store, "book4", "/new/library/path/book.m4b")

	// The call was still attempted (error came from Deluge, not a skip).
	if got := mock.moveStorageCalls.Load(); got != 1 {
		t.Errorf("expected 1 move_storage attempt even on error, got %d", got)
	}
}
