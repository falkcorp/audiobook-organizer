// file: internal/deluge/import_retired_test.go
// version: 1.0.0
// guid: 5a9d1c36-7e42-4b08-8f63-d2c0b4e91a57
// last-edited: 2026-10-24

package deluge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// retiredPathStore answers the way the production store does for a path only a
// retired version's row holds: the plain (live-only) lookup says nil, and the
// retired-aware read names the row. It implements RetiredBookFileReader itself
// (the embedded interface is nil; only the path read is called) so the
// retired-aware helper reaches it instead of the plain getter.
type retiredPathStore struct {
	*fakeDelugStore
	database.RetiredBookFileReader
	retired map[string]*database.BookFile
}

func (s *retiredPathStore) GetBookFileByPath(string) (*database.BookFile, error) { return nil, nil }

func (s *retiredPathStore) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	return s.retired[path], nil
}

func newRetiredPathStore(path string) *retiredPathStore {
	return &retiredPathStore{
		fakeDelugStore: &fakeDelugStore{},
		retired: map[string]*database.BookFile{
			path: {ID: "retired-row", BookID: "holder", FilePath: path, Retired: true},
		},
	}
}

// The source's path index entry belongs to a retired version's row. The
// live-only getter said nil, so the import copied the retired version's bytes
// into the library for a live row that merely shares the path.
func TestImportToLibrary_SourceHeldByRetiredRow_Refused(t *testing.T) {
	seeding := t.TempDir()
	src := filepath.Join(seeding, "book.m4b")
	writeFile(t, src)
	root := t.TempDir()
	store := newRetiredPathStore(src)

	_, err := ImportToLibrary(&config.Config{RootDir: root}, nil, store,
		&database.BookFile{ID: "mine", BookID: "b1", FilePath: src}, dirProtected{dir: seeding})
	if err == nil || !strings.Contains(err.Error(), "retired version's book file retired-row") {
		t.Fatalf("err=%v, want a refusal naming the retired row", err)
	}
	if store.updated != nil {
		t.Errorf("row updated: %+v", store.updated)
	}
	if _, statErr := os.Stat(filepath.Join(root, "book.m4b")); statErr == nil {
		t.Error("a copy was made")
	}
}

// A destination only a retired row holds is claimed: adopting or copying over it
// would leave two rows on one file.
func TestImportToLibrary_DestinationHeldByRetiredRow_Refused(t *testing.T) {
	seeding := t.TempDir()
	src := filepath.Join(seeding, "book.m4b")
	writeFile(t, src)
	root := t.TempDir()
	dest := filepath.Join(root, "book.m4b")
	store := newRetiredPathStore(dest)

	_, err := ImportToLibrary(&config.Config{RootDir: root}, nil, store,
		&database.BookFile{ID: "mine", BookID: "b1", FilePath: src}, dirProtected{dir: seeding})
	if err == nil || !strings.Contains(err.Error(), "already recorded for book file retired-row") {
		t.Fatalf("err=%v, want a refusal naming the retired row", err)
	}
	if store.updated != nil {
		t.Errorf("row updated: %+v", store.updated)
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("a copy was made onto the retired row's path")
	}
}

// The write guard's importer must not read a path only a retired version
// holds as "no record" (nor repoint the retired row): it refuses, as a skip,
// and says why.
func TestLibraryImporterAdapter_ProtectedPathHeldByRetiredRow_RefusedAsSkip(t *testing.T) {
	seeding := t.TempDir()
	src := filepath.Join(seeding, "book.m4b")
	writeFile(t, src)
	root := t.TempDir()
	store := newRetiredPathStore(src)
	a := NewLibraryImporterAdapter(store, nil, &config.Config{RootDir: root}, dirProtected{dir: seeding})

	_, err := a.ImportPath(context.Background(), src, "")
	if !errors.Is(err, tagger.ErrProtectedPathWrite) {
		t.Fatalf("err=%v, want ErrProtectedPathWrite (counted as a skip)", err)
	}
	if !strings.Contains(err.Error(), "retired version row retired-row") {
		t.Errorf("err=%v, want the retired row named", err)
	}
	if store.updated != nil {
		t.Errorf("row updated: %+v", store.updated)
	}
	if _, statErr := os.Stat(filepath.Join(root, "book.m4b")); statErr == nil {
		t.Error("a copy was made")
	}
}
