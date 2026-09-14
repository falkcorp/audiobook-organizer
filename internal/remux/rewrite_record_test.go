// file: internal/remux/rewrite_record_test.go
// version: 1.0.0
// guid: 7e1c5a39-2f84-4b6d-8a07-c3d9e6b2f415
// last-edited: 2026-09-13

package remux

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/diagnosis"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// Review round 5, item 2: the remux and transcode passes rewrote library files
// in place and never touched book_file, so each rewritten file's row kept the
// old file_hash (and, after a transcode, the old codec, bitrate and sample
// rate) and the next rescan treated the file as replaced. The rewrite is
// injected here so the recording is tested without ffmpeg or a malformed file.

func rowFixture(t *testing.T, codec string, kbps, hz int) (*database.PebbleStore, string, string) {
	t.Helper()
	store, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	path := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(path, bytes.Repeat([]byte("original audio "), 4096), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	before, err := filehash.BookFileHash(path)
	if err != nil {
		t.Fatalf("BookFileHash: %v", err)
	}
	book, err := store.CreateBook(&database.Book{Title: "Remux", FilePath: path})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := store.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: path, FileHash: before,
		Codec: codec, BitrateKbps: kbps, SampleRateHz: hz}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	return store, path, before
}

func rewriteTo(content string) func(string) error {
	return func(p string) error { return os.WriteFile(p, bytes.Repeat([]byte(content), 3000), 0o644) }
}

func rowAt(t *testing.T, store *database.PebbleStore, path string) *database.BookFile {
	t.Helper()
	row, err := store.GetBookFileByPath(path)
	if err != nil || row == nil {
		t.Fatalf("GetBookFileByPath: row=%v err=%v", row, err)
	}
	return row
}

func TestRemuxAndRecord_RecordsRewrittenFileHash(t *testing.T) {
	store, path, before := rowFixture(t, "aac", 128, 44100)
	r := New(&MockStore{})
	r.SetBookFileStore(store)

	if err := r.remuxAndRecord(path, rewriteTo("remuxed container ")); err != nil {
		t.Fatalf("remuxAndRecord: %v", err)
	}
	after, err := filehash.BookFileHash(path)
	if err != nil || after == before {
		t.Fatalf("fixture error: hash after=%q before=%q err=%v", after, before, err)
	}
	row := rowAt(t, store, path)
	if row.FileHash != after {
		t.Errorf("FileHash = %q after the remux, want the new bytes' %q (old %q)", row.FileHash, after, before)
	}
	if row.PostMetadataHash == "" {
		t.Error("PostMetadataHash is empty; want the remuxed file's SHA-256")
	}
}

func TestTranscodeAndRecord_RecordsHashAndRefreshesAudioProperties(t *testing.T) {
	store, path, before := rowFixture(t, "mp3", 128, 44100)
	tr := NewTranscoder(&MockStore{})
	tr.SetBookFileStore(store)
	probe := func(context.Context, string) diagnosis.FileDiagnostic {
		return diagnosis.FileDiagnostic{Codec: "aac", BitrateKbps: 64, SampleRateHz: 22050}
	}

	if err := tr.transcodeAndRecord(context.Background(), path, rewriteTo("transcoded audio "), probe); err != nil {
		t.Fatalf("transcodeAndRecord: %v", err)
	}
	after, err := filehash.BookFileHash(path)
	if err != nil || after == before {
		t.Fatalf("fixture error: hash after=%q before=%q err=%v", after, before, err)
	}
	row := rowAt(t, store, path)
	if row.FileHash != after {
		t.Errorf("FileHash = %q after the transcode, want the new bytes' %q (old %q)", row.FileHash, after, before)
	}
	if row.Codec != "aac" || row.BitrateKbps != 64 || row.SampleRateHz != 22050 {
		t.Errorf("audio properties = %q/%d/%d, want the probe's aac/64/22050", row.Codec, row.BitrateKbps, row.SampleRateHz)
	}
}

// An empty probe (no ffprobe on the host, an unreadable stream) must not zero
// the stored properties; the hash is still recorded.
func TestTranscodeAndRecord_EmptyProbeKeepsStoredProperties(t *testing.T) {
	store, path, _ := rowFixture(t, "mp3", 128, 44100)
	tr := NewTranscoder(&MockStore{})
	tr.SetBookFileStore(store)
	empty := func(context.Context, string) diagnosis.FileDiagnostic { return diagnosis.FileDiagnostic{} }

	if err := tr.transcodeAndRecord(context.Background(), path, rewriteTo("transcoded again "), empty); err != nil {
		t.Fatalf("transcodeAndRecord: %v", err)
	}
	after, _ := filehash.BookFileHash(path)
	row := rowAt(t, store, path)
	if row.FileHash != after {
		t.Errorf("FileHash = %q, want %q", row.FileHash, after)
	}
	if row.Codec != "mp3" || row.BitrateKbps != 128 || row.SampleRateHz != 44100 {
		t.Errorf("audio properties = %q/%d/%d, want the stored mp3/128/44100 kept", row.Codec, row.BitrateKbps, row.SampleRateHz)
	}
}

// Without a store the passes still rewrite, and nothing is recorded.
func TestRemuxAndRecord_NoStoreOnlyRewrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.m4b")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	called := false
	r := New(&MockStore{})
	if err := r.remuxAndRecord(path, func(string) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("remuxAndRecord without a store: called=%v err=%v", called, err)
	}
}
