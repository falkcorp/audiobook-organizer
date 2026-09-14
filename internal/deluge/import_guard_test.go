// file: internal/deluge/import_guard_test.go
// version: 1.0.0
// guid: 8c2d5f37-6a1e-4b94-a7d3-0f9e4b6c2185
// last-edited: 2026-09-14

package deluge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// A static protected prefix (config.ProtectedPaths, the iTunes library) is
// never imported: only a Deluge save_path may be copied out and repointed.
func TestImportToLibrary_StaticProtectedSourceRefused(t *testing.T) {
	itunes := t.TempDir()
	src := filepath.Join(itunes, "iTunes Media", "book.m4b")
	writeFile(t, src)
	root := t.TempDir()

	cases := map[string]*config.Config{
		"config.ProtectedPaths": {RootDir: root, ProtectedPaths: []string{itunes}},
		"iTunes library":        {RootDir: root, ITunes: config.ITunesConfig{LibraryReadPath: filepath.Join(itunes, "iTunes Library.xml")}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			store := &fakeDelugStore{}
			newPath, err := ImportToLibrary(cfg, nil, store, &database.BookFile{ID: "id-it", FilePath: src}, dirProtected{dir: itunes})
			if !errors.Is(err, tagger.ErrProtectedPathWrite) {
				t.Fatalf("err = %v, want one wrapping tagger.ErrProtectedPathWrite", err)
			}
			if newPath != "" || store.updated != nil {
				t.Errorf("newPath=%q updated=%+v, want neither", newPath, store.updated)
			}
			if _, err := os.Stat(filepath.Join(root, "book.m4b")); err == nil {
				t.Error("a copy was made in the library")
			}
		})
	}
}

// The cache's own static list is honoured too (ProtectedBy == static).
func TestImportToLibrary_CacheStaticPathRefused(t *testing.T) {
	keep := t.TempDir()
	src := filepath.Join(keep, "book.m4b")
	writeFile(t, src)
	cache := NewProtectedPathCache(nil, []string{keep})
	_, err := ImportToLibrary(&config.Config{RootDir: t.TempDir()}, nil, &fakeDelugStore{}, &database.BookFile{ID: "x", FilePath: src}, cache)
	if !errors.Is(err, tagger.ErrProtectedPathWrite) {
		t.Fatalf("err = %v, want ErrProtectedPathWrite", err)
	}
}

// The path index holds one row per path. When it names a different row for
// the source, importing would repoint the caller's row and leave that row
// naming the seeding file: refuse.
func TestImportToLibrary_RowAtSourceIsAnotherRow_Refused(t *testing.T) {
	seeding := t.TempDir()
	src := filepath.Join(seeding, "book.m4b")
	writeFile(t, src)
	store := &fakeDelugStore{owners: map[string]*database.BookFile{src: {ID: "other", BookID: "b2", FilePath: src}}}
	_, err := ImportToLibrary(&config.Config{RootDir: t.TempDir()}, nil, store, &database.BookFile{ID: "mine", BookID: "b1", FilePath: src}, dirProtected{dir: seeding})
	if err == nil || store.updated != nil {
		t.Fatalf("err=%v updated=%+v, want a refusal and no row update", err, store.updated)
	}
}

// ExpectedBookFileID that is not bookFile's ID is refused.
func TestImportToLibraryWith_ExpectedRowMismatch_Refused(t *testing.T) {
	seeding := t.TempDir()
	src := filepath.Join(seeding, "book.m4b")
	writeFile(t, src)
	store := &fakeDelugStore{}
	_, err := ImportToLibraryWith(&config.Config{RootDir: t.TempDir()}, nil, store, &database.BookFile{ID: "mine", FilePath: src},
		ImportOptions{Protected: dirProtected{dir: seeding}, ExpectedBookFileID: "caller"})
	if err == nil || store.updated != nil {
		t.Fatalf("err=%v updated=%+v, want a refusal and no row update", err, store.updated)
	}
}

// The write guard's importer passes the caller's row. A row at the path that
// is not the caller's is refused before anything is copied.
func TestLibraryImporterAdapter_CallerRowMismatch_Refused(t *testing.T) {
	seeding := t.TempDir()
	src := filepath.Join(seeding, "book.m4b")
	writeFile(t, src)
	root := t.TempDir()
	store := &fakeDelugStore{owners: map[string]*database.BookFile{src: {ID: "indexed", FilePath: src}}}
	a := NewLibraryImporterAdapter(store, nil, &config.Config{RootDir: root}, dirProtected{dir: seeding})
	if _, err := a.ImportPath(context.Background(), src, "caller-row"); err == nil {
		t.Fatal("want a refusal: the row at the path is not the caller's")
	}
	if store.updated != nil {
		t.Errorf("row updated: %+v", store.updated)
	}
	if _, err := os.Stat(filepath.Join(root, "book.m4b")); err == nil {
		t.Error("a copy was made")
	}
}
