// file: internal/deluge/import_test.go
// version: 1.2.0
// guid: b2c3d4e5-f6a7-8901-bcde-f12345678902
// last-edited: 2026-09-13
//
// Tests for ImportToLibrary in internal/deluge/import.go.

package deluge

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeDelugStore is a minimal database.Store for import tests.
type fakeDelugStore struct {
	database.Store
	updated *database.BookFile
	owners  map[string]*database.BookFile // GetBookFileByPath answers
}

func (f *fakeDelugStore) GetBookFileByPath(path string) (*database.BookFile, error) {
	return f.owners[path], nil
}

func (f *fakeDelugStore) UpdateBookFile(id string, file *database.BookFile) error {
	f.updated = file
	return nil
}

func TestImportToLibrary_BasicCopy(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootDir := t.TempDir()
	cfg := &config.Config{RootDir: rootDir, DelugeMoveEnabled: false}
	store := &fakeDelugStore{}
	bf := &database.BookFile{ID: "id-001", FilePath: srcFile}

	newPath, err := ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("ImportToLibrary: %v", err)
	}
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Errorf("destination file does not exist: %v", statErr)
	}
	if store.updated == nil {
		t.Fatal("UpdateBookFile was not called")
	}
	if store.updated.FilePath != newPath {
		t.Errorf("FilePath = %q, want %q", store.updated.FilePath, newPath)
	}
	if store.updated.DelugeOriginalPath != srcFile {
		t.Errorf("DelugeOriginalPath = %q, want %q", store.updated.DelugeOriginalPath, srcFile)
	}
	if store.updated.ImportedFromDelugeAt == nil {
		t.Error("ImportedFromDelugeAt is nil, want non-nil")
	}
}

func TestImportToLibrary_SamePath_NoOp(t *testing.T) {
	rootDir := t.TempDir()
	srcFile := filepath.Join(rootDir, "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{RootDir: rootDir}
	store := &fakeDelugStore{}
	bf := &database.BookFile{ID: "id-002", FilePath: srcFile}

	newPath, err := ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("ImportToLibrary: %v", err)
	}
	if newPath != srcFile {
		t.Errorf("newPath = %q, want %q (same as src)", newPath, srcFile)
	}
	if store.updated != nil {
		t.Error("UpdateBookFile should not be called when src == dest")
	}
}

func TestImportToLibrary_Idempotent(t *testing.T) {
	rootDir := t.TempDir()
	srcFile := filepath.Join(rootDir, "imported.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{RootDir: rootDir}
	store := &fakeDelugStore{}
	importedAt := time.Now().Add(-time.Hour)
	bf := &database.BookFile{ID: "id-003", FilePath: srcFile, ImportedFromDelugeAt: &importedAt}

	newPath, err := ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("ImportToLibrary: %v", err)
	}
	if newPath != srcFile {
		t.Errorf("newPath = %q, want %q (unchanged)", newPath, srcFile)
	}
	if store.updated != nil {
		t.Error("UpdateBookFile should not be called on already-imported file")
	}
}

func TestImportToLibrary_NilBookFile(t *testing.T) {
	cfg := &config.Config{RootDir: t.TempDir()}
	store := &fakeDelugStore{}

	_, err := ImportToLibrary(cfg, nil, store, nil)
	if err == nil {
		t.Error("expected error for nil bookFile")
	}
}

func TestImportToLibrary_MoveStorageNilClient_OK(t *testing.T) {
	srcDir := t.TempDir()
	srcFile := filepath.Join(srcDir, "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}

	rootDir := t.TempDir()
	cfg := &config.Config{RootDir: rootDir, DelugeMoveEnabled: true}
	store := &fakeDelugStore{}
	bf := &database.BookFile{ID: "id-004", FilePath: srcFile, DelugeHash: "abc123"}

	// nil client → MoveStorage skipped, but copy still succeeds.
	newPath, err := ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("ImportToLibrary: %v", err)
	}
	if _, statErr := os.Stat(newPath); statErr != nil {
		t.Errorf("destination file does not exist: %v", statErr)
	}
}

// failingDelugStore fails UpdateBookFile until fail is cleared.
type failingDelugStore struct {
	database.Store
	fail    bool
	updated *database.BookFile
}

// No row claims any destination in these tests.
func (f *failingDelugStore) GetBookFileByPath(string) (*database.BookFile, error) {
	return nil, nil
}

func (f *failingDelugStore) UpdateBookFile(id string, file *database.BookFile) error {
	if f.fail {
		return errors.New("pebble write stall")
	}
	cp := *file
	f.updated = &cp
	return nil
}

// A failed row update must leave no copy behind and restore the struct, so
// the retry succeeds. Before 2026-09-13 the copy stayed in RootDir, the
// struct pointed at it, and every retry failed.
func TestImportToLibrary_UpdateFails_RemovesCopyAndRetrySucceeds(t *testing.T) {
	srcFile := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	cfg := &config.Config{RootDir: rootDir}
	store := &failingDelugStore{fail: true}
	bf := &database.BookFile{ID: "id-fail", FilePath: srcFile}
	dest := filepath.Join(rootDir, "book.m4b")

	newPath, err := ImportToLibrary(cfg, nil, store, bf)
	if err == nil {
		t.Fatal("expected the UpdateBookFile failure to be returned")
	}
	if newPath != "" {
		t.Errorf("newPath = %q, want empty: nothing is left behind", newPath)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("copy at %s must be removed after the failed update (stat err %v)", dest, statErr)
	}
	if bf.FilePath != srcFile || bf.DelugeOriginalPath != "" || bf.ImportedFromDelugeAt != nil {
		t.Errorf("bookFile not restored: FilePath=%q DelugeOriginalPath=%q ImportedFromDelugeAt=%v", bf.FilePath, bf.DelugeOriginalPath, bf.ImportedFromDelugeAt)
	}
	if _, statErr := os.Stat(srcFile); statErr != nil {
		t.Fatalf("source must be untouched: %v", statErr)
	}

	store.fail = false
	newPath, err = ImportToLibrary(cfg, nil, store, bf)
	if err != nil {
		t.Fatalf("retry after a failed update must succeed: %v", err)
	}
	if newPath != dest || store.updated == nil || store.updated.FilePath != dest || store.updated.DelugeOriginalPath != srcFile {
		t.Errorf("retry recorded %+v, newPath %q; want FilePath=%q DelugeOriginalPath=%q", store.updated, newPath, dest, srcFile)
	}
}

// A copy left by an earlier failed attempt (same bytes) is adopted, and a
// failed update never removes a file this call did not create.
func TestImportToLibrary_ExistingIdenticalDestinationIsAdopted(t *testing.T) {
	srcFile := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	dest := filepath.Join(rootDir, "book.m4b")
	if err := os.WriteFile(dest, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{RootDir: rootDir}

	failing := &failingDelugStore{fail: true}
	if _, err := ImportToLibrary(cfg, nil, failing, &database.BookFile{ID: "id-adopt", FilePath: srcFile}); err == nil {
		t.Fatal("expected the UpdateBookFile failure")
	}
	if _, statErr := os.Stat(dest); statErr != nil {
		t.Fatalf("a destination this call did not create must never be removed: %v", statErr)
	}

	store := &failingDelugStore{}
	newPath, err := ImportToLibrary(cfg, nil, store, &database.BookFile{ID: "id-adopt", FilePath: srcFile})
	if err != nil {
		t.Fatalf("an identical existing destination must be adopted: %v", err)
	}
	if newPath != dest || store.updated == nil || store.updated.FilePath != dest {
		t.Errorf("adopt recorded %+v, newPath %q; want %q", store.updated, newPath, dest)
	}
}

func TestImportToLibrary_ExistingDifferentDestinationRefused(t *testing.T) {
	srcFile := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	dest := filepath.Join(rootDir, "book.m4b")
	if err := os.WriteFile(dest, []byte("something else"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &failingDelugStore{}
	if _, err := ImportToLibrary(&config.Config{RootDir: rootDir}, nil, store, &database.BookFile{ID: "id-diff", FilePath: srcFile}); err == nil {
		t.Fatal("a different file at the destination must be refused")
	}
	if store.updated != nil {
		t.Error("no row may be written when the destination is refused")
	}
	if data, _ := os.ReadFile(dest); string(data) != "something else" {
		t.Error("the existing destination must be left as it was")
	}
}

// A destination another book_file row names is that row's file: never
// adopted, even when its bytes match the source.
func TestImportToLibrary_DestinationOwnedByAnotherRowRefused(t *testing.T) {
	srcFile := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	dest := filepath.Join(rootDir, "book.m4b")
	if err := os.WriteFile(dest, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := &fakeDelugStore{owners: map[string]*database.BookFile{
		dest: {ID: "other-row", BookID: "other-book", FilePath: dest},
	}}
	if _, err := ImportToLibrary(&config.Config{RootDir: rootDir}, nil, store, &database.BookFile{ID: "id-owned", FilePath: srcFile}); err == nil {
		t.Fatal("a destination recorded for another book file must be refused")
	}
	if store.updated != nil {
		t.Error("no row may be written when the destination is refused")
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("the other row's file must be left in place: %v", err)
	}
}

// raceDelugStore records rows by path like the real store, and holds one
// book file's UpdateBookFile open, then fails it.
type raceDelugStore struct {
	database.Store
	mu      sync.Mutex
	byPath  map[string]*database.BookFile
	failID  string
	started chan struct{} // closed when failID's update begins
	release chan struct{} // failID's update fails once this closes, or after a timeout
}

func (s *raceDelugStore) GetBookFileByPath(path string) (*database.BookFile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bf := s.byPath[path]; bf != nil {
		cp := *bf
		return &cp, nil
	}
	return nil, nil
}

func (s *raceDelugStore) UpdateBookFile(id string, file *database.BookFile) error {
	if id == s.failID {
		close(s.started)
		select {
		case <-s.release:
		case <-time.After(300 * time.Millisecond):
		}
		return errors.New("pebble write stall")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *file
	s.byPath[file.FilePath] = &cp
	return nil
}

// Import A copies, then its row update fails and it removes the copy. Import
// B, running meanwhile into the same destination, must not adopt that copy:
// B's row would name a file A deletes. B waits for A (destLocks), then copies
// for itself. Without the lock A's update is released by B finishing, which
// is the adopt-then-delete ordering.
func TestImportToLibrary_ConcurrentImportCannotAdoptCopyBeingRemoved(t *testing.T) {
	srcFile := filepath.Join(t.TempDir(), "book.m4b")
	if err := os.WriteFile(srcFile, []byte("audio data"), 0o644); err != nil {
		t.Fatal(err)
	}
	rootDir := t.TempDir()
	dest := filepath.Join(rootDir, "book.m4b")
	cfg := &config.Config{RootDir: rootDir}
	bDone := make(chan struct{})
	store := &raceDelugStore{byPath: map[string]*database.BookFile{}, failID: "id-a", started: make(chan struct{}), release: bDone}

	aErr := make(chan error, 1)
	go func() {
		_, err := ImportToLibrary(cfg, nil, store, &database.BookFile{ID: "id-a", FilePath: srcFile})
		aErr <- err
	}()
	<-store.started // A has copied and is inside its failing row update

	var bPath string
	var bErr error
	go func() {
		defer close(bDone)
		bPath, bErr = ImportToLibrary(cfg, nil, store, &database.BookFile{ID: "id-b", FilePath: srcFile})
	}()

	if err := <-aErr; err == nil {
		t.Fatal("import A's row update was set to fail")
	}
	<-bDone
	if bErr != nil {
		t.Fatalf("import B: %v", bErr)
	}
	if bPath != dest {
		t.Fatalf("import B returned %q, want %q", bPath, dest)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("import B's row names %s, which must exist: %v", dest, err)
	}
	if rec, _ := store.GetBookFileByPath(dest); rec == nil || rec.ID != "id-b" {
		t.Errorf("the destination is recorded for %+v, want id-b", rec)
	}
}
