// file: internal/database/book_first_audio_file_test.go
// version: 1.0.0
// guid: 8d3a6e21-4c75-4f08-b9a2-71e5c0d96b34
// last-edited: 2026-10-10

package database

import (
	"fmt"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audioext"
)

func fafMakeBook(t *testing.T, s Store, id, bookPath string) {
	t.Helper()
	if _, err := s.CreateBook(&Book{ID: id, Title: "T " + id, FilePath: bookPath}); err != nil {
		t.Fatalf("CreateBook %s: %v", id, err)
	}
}

func fafAddFile(t *testing.T, s Store, bookID string, disc, track int, path string, missing bool) {
	t.Helper()
	f := &BookFile{BookID: bookID, FilePath: path, DiscNumber: disc, TrackNumber: track, Missing: missing}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile %s: %v", path, err)
	}
}

func TestFirstAudioFile_OrdersByDiscTrackPath(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	exts := audioext.DefaultSet()
	fafMakeBook(t, s, "b1", "")
	fafAddFile(t, s, "b1", 2, 1, "/x/a-d2t1.mp3", false)
	fafAddFile(t, s, "b1", 1, 5, "/x/a-d1t5.mp3", false)
	fafAddFile(t, s, "b1", 1, 2, "/x/z-d1t2.mp3", false)
	fafAddFile(t, s, "b1", 1, 2, "/x/b-d1t2.mp3", false)

	got, ok, err := FirstAudioFile(s, "b1", exts)
	if err != nil || !ok {
		t.Fatalf("FirstAudioFile ok=%v err=%v", ok, err)
	}
	if got.FilePath != "/x/b-d1t2.mp3" {
		t.Errorf("first = %q, want /x/b-d1t2.mp3 (disc, then track, then path)", got.FilePath)
	}
	all, _ := s.GetBookFiles("b1")
	if len(all) == 0 || all[0].FilePath != got.FilePath {
		t.Errorf("helper disagrees with GetBookFiles order: %q vs %v", got.FilePath, all)
	}
}

func TestFirstAudioFile_SkipsMissingEmptyAndNonAudio(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	exts := audioext.DefaultSet()
	fafMakeBook(t, s, "b1", "")
	fafAddFile(t, s, "b1", 1, 1, "/x/gone.mp3", true)
	fafAddFile(t, s, "b1", 1, 2, "/x/cover.jpg", false)
	fafAddFile(t, s, "b1", 1, 3, "/x/notes.txt", false)
	fafAddFile(t, s, "b1", 1, 4, "/x/real.m4b", false)

	got, ok, err := FirstAudioFile(s, "b1", exts)
	if err != nil || !ok || got.FilePath != "/x/real.m4b" {
		t.Fatalf("got %q ok=%v err=%v, want /x/real.m4b", got.FilePath, ok, err)
	}

	fafMakeBook(t, s, "b2", "")
	fafAddFile(t, s, "b2", 1, 1, "/y/gone.mp3", true)
	fafAddFile(t, s, "b2", 1, 2, "/y/cover.jpg", false)
	if _, ok, err := FirstAudioFile(s, "b2", exts); err != nil || ok {
		t.Errorf("only missing/non-audio rows: ok=%v err=%v, want ok=false", ok, err)
	}
}

// TestFirstAudioFile_NoRowsIgnoresBookFilePath is the regression this helper
// exists for: Book.FilePath is stale and must never be consulted.
func TestFirstAudioFile_NoRowsIgnoresBookFilePath(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	exts := audioext.DefaultSet()
	fafMakeBook(t, s, "b1", "/lib/stale/book.m4b")

	if f, ok, err := FirstAudioFile(s, "b1", exts); err != nil || ok {
		t.Errorf("single: file=%q ok=%v err=%v, want ok=false", f.FilePath, ok, err)
	}
	m, err := FirstAudioFiles(s, []string{"b1"}, exts)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := m["b1"]; present {
		t.Errorf("batch returned an entry for a book with no file rows: %v", m)
	}
}

func TestFirstAudioFile_RespectsConfiguredExtensions(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	fafMakeBook(t, s, "b1", "")
	fafAddFile(t, s, "b1", 1, 1, "/x/a.mp3", false)
	fafAddFile(t, s, "b1", 1, 2, "/x/b.m4b", false)
	got, ok, _ := FirstAudioFile(s, "b1", audioext.NewSet([]string{"m4b"}))
	if !ok || got.FilePath != "/x/b.m4b" {
		t.Errorf("got %q ok=%v, want /x/b.m4b under an m4b-only set", got.FilePath, ok)
	}
}

func TestFirstAudioFiles_AgreesWithSingleFormFor50Books(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	exts := audioext.DefaultSet()
	var ids []string
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("book%02d", i)
		ids = append(ids, id)
		fafMakeBook(t, s, id, fmt.Sprintf("/lib/stale/%s.m4b", id))
		switch i % 5 {
		case 0: // no rows at all
		case 1: // only missing
			fafAddFile(t, s, id, 1, 1, fmt.Sprintf("/lib/%s/a.mp3", id), true)
		case 2: // only non-audio
			fafAddFile(t, s, id, 1, 1, fmt.Sprintf("/lib/%s/cover.jpg", id), false)
		default: // several, shuffled insertion
			fafAddFile(t, s, id, 2, 1, fmt.Sprintf("/lib/%s/d2.mp3", id), false)
			fafAddFile(t, s, id, 1, 9, fmt.Sprintf("/lib/%s/d1t9.mp3", id), false)
			fafAddFile(t, s, id, 1, 3, fmt.Sprintf("/lib/%s/d1t3.m4b", id), false)
			fafAddFile(t, s, id, 1, 1, fmt.Sprintf("/lib/%s/gone.mp3", id), true)
		}
	}
	batch, err := FirstAudioFiles(s, ids, exts)
	if err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, id := range ids {
		single, ok, err := FirstAudioFile(s, id, exts)
		if err != nil {
			t.Fatal(err)
		}
		b, inBatch := batch[id]
		if ok != inBatch {
			t.Fatalf("%s: single ok=%v batch present=%v", id, ok, inBatch)
		}
		if ok {
			want++
			if b.ID != single.ID || b.FilePath != single.FilePath {
				t.Errorf("%s: batch %q (%s) != single %q (%s)", id, b.FilePath, b.ID, single.FilePath, single.ID)
			}
		}
	}
	if want != 20 || len(batch) != 20 {
		t.Errorf("books with a usable file: single=%d batch=%d, want 20", want, len(batch))
	}
}
