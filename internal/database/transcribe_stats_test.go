// file: internal/database/transcribe_stats_test.go
// version: 1.1.0
// guid: 4c1f8a92-7d63-4b05-9e21-8f6a3c0d51e4
// last-edited: 2026-07-01

package database

import (
	"path/filepath"
	"testing"
	"time"
)

// TestTranscribeStats_RoundTrip verifies the aggregate persists and reads back
// through PebbleDB, and that an absent key yields (nil, nil) rather than an error.
func TestTranscribeStats_RoundTrip(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// Absent key → (nil, nil).
	got, err := store.GetTranscribeStats()
	if err != nil {
		t.Fatalf("GetTranscribeStats on empty: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil stats before any write, got %+v", got)
	}

	now := time.Now().Truncate(time.Second)
	want := &TranscribeStats{
		RunOpID:       "run-1",
		StartedAt:     now,
		UpdatedAt:     now,
		TotalBooks:    48763,
		Attempted:     200,
		OK:            5,
		Unparsed:      8,
		SourceMissing: 182,
		FFmpegError:   3,
		WhisperError:  1,
		Empty:         1,
		CacheHits:     2,
	}
	if err := store.PutTranscribeStats(want); err != nil {
		t.Fatalf("PutTranscribeStats: %v", err)
	}

	got, err = store.GetTranscribeStats()
	if err != nil {
		t.Fatalf("GetTranscribeStats: %v", err)
	}
	if got == nil {
		t.Fatal("expected stats after write, got nil")
	}
	if got.OK != 5 || got.Unparsed != 8 || got.SourceMissing != 182 || got.TotalBooks != 48763 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if !got.StartedAt.Equal(now) {
		t.Fatalf("StartedAt mismatch: got %v want %v", got.StartedAt, now)
	}
}

// TestTranscribeStatsStore_Interface confirms *PebbleStore satisfies the narrow
// capability interface the op and handler type-assert to.
func TestTranscribeStatsStore_Interface(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	var _ TranscribeStatsStore = store
}

// TestBook_TranscribeFields_RoundTrip verifies the new per-book outcome fields
// persist through CreateBook/UpdateBook/GetBookByID (Book is JSON-marshaled
// wholesale, so this guards against an accidental memdb strip of the fields).
func TestBook_TranscribeFields_RoundTrip(t *testing.T) {
	store, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	b := &Book{ID: "bk1", Title: "T", Format: "mp3", FilePath: "/x/y.mp3"}
	if _, err := store.CreateBook(b); err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	status := "source_file_missing"
	detail := "source file not found: /x/y.mp3"
	now := time.Now().Truncate(time.Second)
	b.TranscribeStatus = &status
	b.TranscribeError = &detail
	b.TranscribeAttemptedAt = &now
	if _, err := store.UpdateBook(b.ID, b); err != nil {
		t.Fatalf("UpdateBook: %v", err)
	}

	got, err := store.GetBookByID("bk1")
	if err != nil || got == nil {
		t.Fatalf("GetBookByID: %v", err)
	}
	if got.TranscribeStatus == nil || *got.TranscribeStatus != status {
		t.Fatalf("TranscribeStatus not persisted: %+v", got.TranscribeStatus)
	}
	if got.TranscribeError == nil || *got.TranscribeError != detail {
		t.Fatalf("TranscribeError not persisted: %+v", got.TranscribeError)
	}
	if got.TranscribeAttemptedAt == nil || !got.TranscribeAttemptedAt.Equal(now) {
		t.Fatalf("TranscribeAttemptedAt not persisted: %+v", got.TranscribeAttemptedAt)
	}
}
