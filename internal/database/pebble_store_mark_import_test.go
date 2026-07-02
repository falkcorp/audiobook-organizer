// file: internal/database/pebble_store_mark_import_test.go
// version: 1.0.0
// guid: 7c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f
// last-edited: 2026-07-01

package database

import (
	"context"
	"path/filepath"
	"testing"
)

func newPebbleStoreForMarkImport(t *testing.T) *PebbleStore {
	t.Helper()
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "mark-import-db"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestMarkFileImportedFromDeluge_ByPath_PopulatesDownloadHashWhenEmpty covers
// the "match by original path" branch: DownloadHash should be set from
// torrentHash when it started empty.
func TestMarkFileImportedFromDeluge_ByPath_PopulatesDownloadHashWhenEmpty(t *testing.T) {
	store := newPebbleStoreForMarkImport(t)

	bf := &BookFile{
		BookID:   "book-1",
		FilePath: "/downloads/original.mp3",
	}
	if err := store.CreateBookFile(bf); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	torrentHash := "abc123deadbeef"
	if err := store.MarkFileImportedFromDeluge(context.Background(), "/downloads/original.mp3", "/library/final.mp3", torrentHash); err != nil {
		t.Fatalf("MarkFileImportedFromDeluge: %v", err)
	}

	got, err := store.GetBookFileByID(bf.BookID, bf.ID)
	if err != nil {
		t.Fatalf("GetBookFileByID: %v", err)
	}
	if got == nil {
		t.Fatalf("expected book file, got nil")
	}
	if got.DelugeHash != torrentHash {
		t.Fatalf("DelugeHash = %q, want %q", got.DelugeHash, torrentHash)
	}
	if got.DownloadHash != torrentHash {
		t.Fatalf("DownloadHash = %q, want %q (should auto-populate from empty)", got.DownloadHash, torrentHash)
	}
}

// TestMarkFileImportedFromDeluge_ByPath_DoesNotClobberManualDownloadHash
// ensures a pre-set (manual) DownloadHash is left untouched by the Deluge
// import path.
func TestMarkFileImportedFromDeluge_ByPath_DoesNotClobberManualDownloadHash(t *testing.T) {
	store := newPebbleStoreForMarkImport(t)

	manualHash := "manually-set-hash"
	bf := &BookFile{
		BookID:       "book-2",
		FilePath:     "/downloads/original2.mp3",
		DownloadHash: manualHash,
	}
	if err := store.CreateBookFile(bf); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	torrentHash := "different-torrent-hash"
	if err := store.MarkFileImportedFromDeluge(context.Background(), "/downloads/original2.mp3", "/library/final2.mp3", torrentHash); err != nil {
		t.Fatalf("MarkFileImportedFromDeluge: %v", err)
	}

	got, err := store.GetBookFileByID(bf.BookID, bf.ID)
	if err != nil {
		t.Fatalf("GetBookFileByID: %v", err)
	}
	if got == nil {
		t.Fatalf("expected book file, got nil")
	}
	if got.DelugeHash != torrentHash {
		t.Fatalf("DelugeHash = %q, want %q", got.DelugeHash, torrentHash)
	}
	if got.DownloadHash != manualHash {
		t.Fatalf("DownloadHash = %q, want unchanged %q (manual value must not be clobbered)", got.DownloadHash, manualHash)
	}
}

// TestMarkFileImportedFromDeluge_ByTorrentHash_PopulatesDownloadHash covers
// the fallback "match by torrent hash via BookVersion" branch.
func TestMarkFileImportedFromDeluge_ByTorrentHash_PopulatesDownloadHash(t *testing.T) {
	store := newPebbleStoreForMarkImport(t)

	torrentHash := "fallback-torrent-hash"
	bv := &BookVersion{
		BookID:      "book-3",
		Status:      "active",
		Format:      "mp3",
		Source:      "deluge",
		TorrentHash: torrentHash,
	}
	created, err := store.CreateBookVersion(bv)
	if err != nil {
		t.Fatalf("CreateBookVersion: %v", err)
	}

	bf := &BookFile{
		BookID:    "book-3",
		FilePath:  "/library/original-name.mp3",
		VersionID: created.ID,
	}
	if err := store.CreateBookFile(bf); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	if err := store.MarkFileImportedFromDeluge(context.Background(), "", "/library/original-name.mp3", torrentHash); err != nil {
		t.Fatalf("MarkFileImportedFromDeluge: %v", err)
	}

	got, err := store.GetBookFileByID(bf.BookID, bf.ID)
	if err != nil {
		t.Fatalf("GetBookFileByID: %v", err)
	}
	if got == nil {
		t.Fatalf("expected book file, got nil")
	}
	if got.DelugeHash != torrentHash {
		t.Fatalf("DelugeHash = %q, want %q", got.DelugeHash, torrentHash)
	}
	if got.DownloadHash != torrentHash {
		t.Fatalf("DownloadHash = %q, want %q (should auto-populate from empty)", got.DownloadHash, torrentHash)
	}
}
