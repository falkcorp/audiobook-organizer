// file: internal/versions/fingerprint_test.go
// version: 1.0.1
// guid: 9e6f8a5d-0c5a-4a70-b8c5-3d7e0f1b9a99

package versions

import (
	"path/filepath"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

func TestCheckFingerprint_TorrentHashMatch(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Create a purged version with a torrent hash.
	_, _ = store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusInactivePurged,
		Format: "m4b", Source: "deluge", TorrentHash: "abc123",
	})

	match := CheckFingerprint(store, "abc123", nil)
	if !match.Matched {
		t.Fatal("expected match on purged torrent hash")
	}
	if match.MatchType != "torrent_hash" {
		t.Errorf("MatchType = %q, want torrent_hash", match.MatchType)
	}
	if match.BookID != "b1" {
		t.Errorf("BookID = %q, want b1", match.BookID)
	}
	if match.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("Status = %q", match.Status)
	}
}

func TestCheckFingerprint_TorrentHashActiveNotBlocked(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	// Active version should NOT trigger a fingerprint block.
	_, _ = store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusActive,
		Format: "m4b", Source: "deluge", TorrentHash: "active-hash",
	})

	match := CheckFingerprint(store, "active-hash", nil)
	if match.Matched {
		t.Error("active version should not trigger fingerprint match")
	}
}

func TestCheckFingerprint_BlockedForRedownload(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	_, _ = store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: database.BookVersionStatusBlockedForRedownload,
		Format: "m4b", Source: "deluge", TorrentHash: "blocked-hash",
	})

	match := CheckFingerprint(store, "blocked-hash", nil)
	if !match.Matched {
		t.Fatal("blocked_for_redownload should match")
	}
	if match.Status != database.BookVersionStatusBlockedForRedownload {
		t.Errorf("Status = %q", match.Status)
	}
}

func TestCheckFingerprint_NoMatch(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	match := CheckFingerprint(store, "unknown-hash", []string{"unknown-file-hash"})
	if match.Matched {
		t.Error("should not match unknown hashes")
	}
}

func TestCheckFingerprint_EmptyInputs(t *testing.T) {
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	match := CheckFingerprint(store, "", nil)
	if match.Matched {
		t.Error("empty inputs should not match")
	}
}
