// file: internal/plugins/maintenance/build_folder_book_files_test.go
// version: 1.0.1
// guid: b1896d34-1538-4a3f-a3f0-469871f9ca8c
// last-edited: 2026-09-25

package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

// fbSpyStore counts the op's only write method, so the dry-run test can assert
// that nothing was written rather than only that no row is visible afterwards.
type fbSpyStore struct {
	*database.PebbleStore
	mu     sync.Mutex
	writes int
}

func (s *fbSpyStore) BatchCreateBookFiles(files []*database.BookFile) error {
	s.mu.Lock()
	s.writes++
	s.mu.Unlock()
	return s.PebbleStore.BatchCreateBookFiles(files)
}

func newFolderBuildStore(t *testing.T) *fbSpyStore {
	t.Helper()
	s, err := database.NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	if _, err := s.BackfillBookAtPathIndex(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &fbSpyStore{PebbleStore: s}
}

// stubProbe answers each file's duration by basename and records every probe.
func stubProbe(t *testing.T, byName map[string]int) *[]string {
	t.Helper()
	var mu sync.Mutex
	probed := &[]string{}
	restore := bookfileaudio.SetProbeForTesting(func(path string) (*mediainfo.MediaInfo, error) {
		mu.Lock()
		*probed = append(*probed, path)
		mu.Unlock()
		d, ok := byName[filepath.Base(path)]
		if !ok {
			return nil, errors.New("no stub duration")
		}
		return &mediainfo.MediaInfo{Duration: d}, nil
	})
	t.Cleanup(restore)
	return probed
}

func absBook(t *testing.T, s *fbSpyStore, title, path string) *database.Book {
	t.Helper()
	organized, primary := "organized", true
	b, err := s.CreateBook(&database.Book{Title: title, FilePath: path, LibraryState: &organized, IsPrimaryVersion: &primary})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func onlyEntry(t *testing.T, r *folderBuildReport, id string) folderBuildBook {
	t.Helper()
	for _, e := range r.Books {
		if e.BookID == id {
			return e
		}
	}
	t.Fatalf("no result entry for %s in %+v", id, r.Books)
	return folderBuildBook{}
}

func TestBuildFolderBookFiles_BuildsRowsWithDurationsInNaturalOrder(t *testing.T) {
	s := newFolderBuildStore(t)
	stubProbe(t, map[string]int{"1.mp3": 100, "2.mp3": 200, "10.mp3": 300})
	dir := makeFolder(t, "1.mp3", "2.mp3", "10.mp3", "cover.jpg")
	b := absBook(t, s, "Folder Book", dir)

	r, err := buildFolderBookFiles(context.Background(), s, folderBuildParams{Apply: true, BookIDs: []string{b.ID}}, &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	e := onlyEntry(t, r, b.ID)
	if e.Decision != fbBuilt || e.FileCount != 3 || e.TotalDurationSec != 600 {
		t.Fatalf("entry = %+v, want built, 3 files, 600s", e)
	}

	rows, err := s.GetBookFiles(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	want := map[string][2]int{"1.mp3": {1, 100}, "2.mp3": {2, 200}, "10.mp3": {3, 300}}
	for _, row := range rows {
		w := want[filepath.Base(row.FilePath)]
		if row.TrackNumber != w[0] || row.Duration != w[1] || row.FileSize <= 0 {
			t.Errorf("%s: track=%d dur=%d size=%d, want track=%d dur=%d size>0",
				filepath.Base(row.FilePath), row.TrackNumber, row.Duration, row.FileSize, w[0], w[1])
		}
	}
	got, err := s.GetBookByID(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Duration == nil || *got.Duration != 600 {
		t.Fatalf("book Duration = %v, want the recomputed 600", got.Duration)
	}
}

func TestBuildFolderBookFiles_SkipsFilesAnotherLiveBookOwns(t *testing.T) {
	s := newFolderBuildStore(t)
	stubProbe(t, map[string]int{"01.mp3": 100, "02.mp3": 200, "03.mp3": 300})
	dir := makeFolder(t, "01.mp3", "02.mp3", "03.mp3")
	target := absBook(t, s, "Target", dir)
	owner, err := s.CreateBook(&database.Book{Title: "Owner", FilePath: filepath.Join(t.TempDir(), "elsewhere")})
	if err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(dir, "02.mp3")
	if err := s.CreateBookFile(&database.BookFile{BookID: owner.ID, FilePath: owned, Duration: 200}); err != nil {
		t.Fatal(err)
	}

	r, err := buildFolderBookFiles(context.Background(), s, folderBuildParams{Apply: true, BookIDs: []string{target.ID}}, &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	e := onlyEntry(t, r, target.ID)
	if e.Decision != fbBuilt || e.FileCount != 2 {
		t.Fatalf("entry = %+v, want built with 2 files", e)
	}
	if e.OwnedSkippedCount != 1 || len(e.OwnedSkipped) != 1 || e.OwnedSkipped[0].Path != owned ||
		len(e.OwnedSkipped[0].OwnerBookIDs) != 1 || e.OwnedSkipped[0].OwnerBookIDs[0] != owner.ID {
		t.Fatalf("owned_skipped = %+v, want %s owned by %s", e.OwnedSkipped, owned, owner.ID)
	}
	if r.OwnedFilesSkipped != 1 || r.BooksWithOwnedFiles != 1 {
		t.Fatalf("summary owned=%d books=%d, want 1/1", r.OwnedFilesSkipped, r.BooksWithOwnedFiles)
	}
	rows, err := s.GetBookFiles(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.FilePath == owned {
			t.Fatalf("target claimed %s, which %s already owns", owned, owner.ID)
		}
	}
	if len(rows) != 2 {
		t.Fatalf("target rows = %d, want 2", len(rows))
	}
}

func TestBuildFolderBookFiles_DryRunWritesNothing(t *testing.T) {
	s := newFolderBuildStore(t)
	stubProbe(t, map[string]int{"a.mp3": 60, "b.mp3": 90})
	dir := makeFolder(t, "a.mp3", "b.mp3")
	b := absBook(t, s, "Dry", dir)
	// A book ABS does not list: in the dry run's per-book entries as out of
	// scope, because it was named explicitly.
	imported := "imported"
	other, err := s.CreateBook(&database.Book{Title: "Imported", FilePath: makeFolder(t, "x.mp3"), LibraryState: &imported})
	if err != nil {
		t.Fatal(err)
	}

	r, err := buildFolderBookFiles(context.Background(), s, folderBuildParams{BookIDs: []string{b.ID, other.ID}}, &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if !r.DryRun {
		t.Fatal("report.DryRun = false for a run without apply")
	}
	if s.writes != 0 {
		t.Fatalf("BatchCreateBookFiles called %d times in a dry run", s.writes)
	}
	rows, err := s.GetBookFiles(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("dry run left %d rows", len(rows))
	}
	e := onlyEntry(t, r, b.ID)
	if e.Decision != fbWouldBuild || e.FileCount != 2 || e.TotalDurationSec != 150 {
		t.Fatalf("entry = %+v, want would_build, 2 files, 150s", e)
	}
	if o := onlyEntry(t, r, other.ID); o.Decision != fbOutOfScope {
		t.Fatalf("imported book decision = %q, want %q", o.Decision, fbOutOfScope)
	}
	if r.Counts[fbWouldBuild] != 1 || r.Counts[fbOutOfScope] != 1 {
		t.Fatalf("counts = %v", r.Counts)
	}
}

func TestBuildFolderBookFiles_SkipsITunesRoot(t *testing.T) {
	s := newFolderBuildStore(t)
	probed := stubProbe(t, map[string]int{"01.m4b": 100})
	dir := filepath.Join(t.TempDir(), "books", "itunes", "Author", "Book")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "01.m4b"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := absBook(t, s, "iTunes Book", dir)

	r, err := buildFolderBookFiles(context.Background(), s, folderBuildParams{Apply: true}, &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	if e := onlyEntry(t, r, b.ID); e.Decision != fbITunesRoot {
		t.Fatalf("decision = %q, want %q", e.Decision, fbITunesRoot)
	}
	if s.writes != 0 || len(*probed) != 0 {
		t.Fatalf("iTunes book: writes=%d probes=%d, want 0/0", s.writes, len(*probed))
	}
}

func TestBuildFolderBookFiles_TwoBooksOneFolderAreBothSkipped(t *testing.T) {
	s := newFolderBuildStore(t)
	stubProbe(t, map[string]int{"01.mp3": 100})
	dir := makeFolder(t, "01.mp3")
	a := absBook(t, s, "A", dir)
	b := absBook(t, s, "B", dir+string(filepath.Separator))

	r, err := buildFolderBookFiles(context.Background(), s, folderBuildParams{Apply: true}, &fakeReporter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if e := onlyEntry(t, r, id); e.Decision != fbSharedFolder {
			t.Fatalf("%s decision = %q, want %q", id, e.Decision, fbSharedFolder)
		}
	}
	if s.writes != 0 {
		t.Fatalf("writes = %d, want 0", s.writes)
	}
}
