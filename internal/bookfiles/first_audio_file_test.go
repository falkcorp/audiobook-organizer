// file: internal/bookfiles/first_audio_file_test.go
// version: 2.0.0
// guid: 8d3a6e21-4c75-4f08-b9a2-71e5c0d96b34
// last-edited: 2026-10-10

package bookfiles

import (
	"fmt"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audioext"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/oklog/ulid/v2"
	"github.com/stretchr/testify/require"
)

func setupTestDB(t *testing.T) (database.Store, func()) {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory("db")
	if err != nil {
		t.Fatalf("NewPebbleStoreInMemory: %v", err)
	}
	return s, func() { _ = s.Close() }
}

func fafMakeBook(t *testing.T, s database.Store, id, bookPath string) {
	t.Helper()
	if _, err := s.CreateBook(&database.Book{ID: id, Title: "T " + id, FilePath: bookPath}); err != nil {
		t.Fatalf("CreateBook %s: %v", id, err)
	}
}

func fafAddFile(t *testing.T, s database.Store, bookID string, disc, track int, path string, missing bool) {
	t.Helper()
	f := &database.BookFile{BookID: bookID, FilePath: path, DiscNumber: disc, TrackNumber: track, Missing: missing}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile %s: %v", path, err)
	}
}

func TestFirstAudioFile_OrdersByDiscTrackPath(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	var exts []string // nil: Resolve falls back to the default set
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
	var exts []string // nil: Resolve falls back to the default set
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
	var exts []string // nil: Resolve falls back to the default set
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
	got, ok, _ := FirstAudioFile(s, "b1", []string{"m4b"})
	if !ok || got.FilePath != "/x/b.m4b" {
		t.Errorf("got %q ok=%v, want /x/b.m4b under an m4b-only set", got.FilePath, ok)
	}
}

func TestFirstAudioFiles_AgreesWithSingleFormFor50Books(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	var exts []string // nil: Resolve falls back to the default set
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

// TestFirstAudioFile_EmptyConfiguredFallsBackToDefaults pins SF-1: a nil or
// empty configured list must resolve to the default set, not to "no audio".
func TestFirstAudioFile_EmptyConfiguredFallsBackToDefaults(t *testing.T) {
	s, cleanup := setupTestDB(t)
	defer cleanup()
	fafMakeBook(t, s, "b1", "")
	fafAddFile(t, s, "b1", 1, 1, "/x/book.m4b", false)
	for name, configured := range map[string][]string{"nil": nil, "empty": {}} {
		got, ok, err := FirstAudioFile(s, "b1", configured)
		if err != nil || !ok || got.FilePath != "/x/book.m4b" {
			t.Errorf("single/%s: got %q ok=%v err=%v, want /x/book.m4b", name, got.FilePath, ok, err)
		}
		m, err := FirstAudioFiles(s, []string{"b1"}, configured)
		if err != nil || m["b1"].FilePath != "/x/book.m4b" {
			t.Errorf("batch/%s: got %v err=%v, want /x/book.m4b", name, m, err)
		}
	}
}

// TestFirstAudioFiles_MemdbAgreesWithSingleAndGetBookFiles runs the agreement
// check with memdb enabled, so the batch form reads the memdb path while the
// single form and the expectation read Pebble through GetBookFiles.
func TestFirstAudioFiles_MemdbAgreesWithSingleAndGetBookFiles(t *testing.T) {
	p, err := database.NewPebbleStoreInMemory("faf_memdb_" + ulid.Make().String())
	require.NoError(t, err)
	defer p.Close()
	var store database.Store = p
	p.WaitForWarmup()
	require.True(t, p.IsMemReady(), "memdb must be published so the batch read takes the memdb path")
	p.UseMemDB = true

	def := audioext.DefaultSet()
	var ids []string
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("mbook%02d", i)
		ids = append(ids, id)
		fafMakeBook(t, store, id, fmt.Sprintf("/lib/stale/%s.m4b", id))
		switch i % 5 {
		case 0: // no rows at all
		case 1: // only missing
			fafAddFile(t, store, id, 1, 1, fmt.Sprintf("/lib/%s/a.mp3", id), true)
		case 2: // only non-audio, including a partial download
			fafAddFile(t, store, id, 1, 1, fmt.Sprintf("/lib/%s/cover.jpg", id), false)
			fafAddFile(t, store, id, 1, 2, fmt.Sprintf("/lib/%s/a.mp3.part", id), false)
		default: // several, inserted out of order
			fafAddFile(t, store, id, 2, 1, fmt.Sprintf("/lib/%s/d2.mp3", id), false)
			fafAddFile(t, store, id, 1, 9, fmt.Sprintf("/lib/%s/d1t9.mp3", id), false)
			fafAddFile(t, store, id, 1, 4, fmt.Sprintf("/lib/%s/d1t4.mp3.part", id), false)
			fafAddFile(t, store, id, 0, 0, fmt.Sprintf("/lib/%s/lead.mp3", id), true)
			fafAddFile(t, store, id, 1, 3, fmt.Sprintf("/lib/%s/D1T3.M4B", id), false)
			fafAddFile(t, store, id, 1, 1, fmt.Sprintf("/lib/%s/gone.mp3", id), true)
		}
	}

	batch, err := FirstAudioFiles(p, ids, nil)
	require.NoError(t, err)
	want := 0
	for _, id := range ids {
		// Expectation derived independently from GetBookFiles (already sorted
		// by disc, track, path).
		all, err := store.GetBookFiles(id)
		require.NoError(t, err)
		sort.SliceStable(all, func(a, b int) bool {
			x, y := all[a], all[b]
			if x.DiscNumber != y.DiscNumber {
				return x.DiscNumber < y.DiscNumber
			}
			if x.TrackNumber != y.TrackNumber {
				return x.TrackNumber < y.TrackNumber
			}
			return x.FilePath < y.FilePath
		})
		var expect *database.BookFile
		for i := range all {
			if !all[i].Missing && def.MatchPath(all[i].FilePath) {
				expect = &all[i]
				break
			}
		}
		single, ok, err := FirstAudioFile(p, id, nil)
		require.NoError(t, err)
		b, inBatch := batch[id]
		require.Equal(t, expect != nil, ok, "%s: single vs GetBookFiles expectation", id)
		require.Equal(t, ok, inBatch, "%s: batch presence vs single", id)
		if expect != nil {
			want++
			require.Equal(t, expect.ID, single.ID, "%s: single vs GetBookFiles", id)
			require.Equal(t, single.ID, b.ID, "%s: batch vs single", id)
			require.Equal(t, single.FilePath, b.FilePath, "%s: batch vs single path", id)
		}
	}
	require.Equal(t, 20, want)
	require.Len(t, batch, 20)
}
