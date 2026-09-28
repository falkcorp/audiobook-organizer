// file: internal/scanner/file_ownership_test.go
// version: 1.0.0
// guid: 6c2a43e1-d6ec-4eae-8570-41c987062ce6
// last-edited: 2026-09-28

package scanner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ownershipErrStore fails the path lookup, to prove the check fails closed.
type ownershipErrStore struct{ *database.PebbleStore }

func (ownershipErrStore) BookFilesAtPath(string) ([]database.BookFile, error) {
	return nil, errors.New("injected: path index unavailable")
}

// ownershipDanglingStore reports one book as gone while its book_file rows
// remain, the dangling-row shape.
type ownershipDanglingStore struct {
	*database.PebbleStore
	goneID string
}

func (s ownershipDanglingStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.goneID {
		return nil, nil
	}
	return s.PebbleStore.GetBookByID(id)
}

// ownershipFixture is a multi-file parent book normalized to its directory,
// owning three chapter files, plus two files nobody owns.
type ownershipFixture struct {
	store    *database.PebbleStore
	parent   *database.Book
	dir      string
	chapters []string
	loose    []string
}

func newOwnershipFixture(t *testing.T) ownershipFixture {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)

	prevConfig := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prevConfig })
	root := t.TempDir()
	config.AppConfig.RootDir = root
	config.AppConfig.SupportedExtensions = []string{".mp3"}

	dir := filepath.Join(root, "Christopher Paolini", "Eldest")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	chapters := []string{write("97.mp3", "a"), write("98.mp3", "b"), write("99.mp3", "c")}
	loose := []string{write("new-1.mp3", "d"), write("new-2.mp3", "e")}

	parent, err := store.CreateBook(&database.Book{Title: "Eldest", FilePath: dir})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	for i, p := range chapters {
		if err := store.CreateBookFile(&database.BookFile{BookID: parent.ID, FilePath: p, TrackNumber: i + 1}); err != nil {
			t.Fatalf("create book_file %s: %v", p, err)
		}
	}
	return ownershipFixture{store: store, parent: parent, dir: dir, chapters: chapters, loose: loose}
}

func useScannerStore(t *testing.T, s scannerStore) {
	t.Helper()
	SetStore(s)
	t.Cleanup(func() { SetStore(nil) })
}

func TestCheckFileOwnership(t *testing.T) {
	cases := []struct {
		name     string
		book     func(f ownershipFixture) *Book
		wrap     func(f ownershipFixture) scannerStore
		wantSkip bool
		wantErr  bool
	}{
		{
			name: "unowned single file is a new book",
			book: func(f ownershipFixture) *Book { return &Book{FilePath: f.loose[0]} },
		},
		{
			name:     "chapter owned by a multi-file parent is a fragment",
			book:     func(f ownershipFixture) *Book { return &Book{FilePath: f.chapters[1]} },
			wantSkip: true,
		},
		{
			name: "rescan of the normalized multi-file book is the same book",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.chapters[0], SegmentFiles: f.chapters}
			},
		},
		{
			name: "group mixing an owned chapter with unowned files is a fragment",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.loose[0], SegmentFiles: []string{f.loose[0], f.chapters[2], f.loose[1]}}
			},
			wantSkip: true,
		},
		{
			name: "two of three chapters is a piece of the parent",
			book: func(f ownershipFixture) *Book {
				return &Book{FilePath: f.chapters[0], SegmentFiles: f.chapters[:2]}
			},
			wantSkip: true,
		},
		{
			name: "a book already at the path is an update",
			book: func(f ownershipFixture) *Book { return &Book{FilePath: f.dir} },
		},
		{
			// A directory-shaped book with no row at the folder: its files are
			// the folder's audio files, and the parent owns three of them while
			// two are loose -- a new directory book there would duplicate the
			// parent's rows.
			name: "new directory book over owned files is a fragment",
			book: func(f ownershipFixture) *Book { return &Book{FilePath: f.dir} },
			wrap: func(f ownershipFixture) scannerStore {
				_, err := f.store.ModifyBook(f.parent.ID, func(b *database.Book) error {
					b.FilePath = f.chapters[0] // not normalized: no row at the folder
					return nil
				})
				if err != nil {
					panic(err)
				}
				return f.store
			},
			wantSkip: true,
		},
		{
			name: "dangling row whose book is gone owns nothing",
			book: func(f ownershipFixture) *Book { return &Book{FilePath: f.chapters[1]} },
			wrap: func(f ownershipFixture) scannerStore {
				return ownershipDanglingStore{PebbleStore: f.store, goneID: f.parent.ID}
			},
		},
		{
			name:    "store error fails closed",
			book:    func(f ownershipFixture) *Book { return &Book{FilePath: f.loose[0]} },
			wrap:    func(f ownershipFixture) scannerStore { return ownershipErrStore{f.store} },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newOwnershipFixture(t)
			var s scannerStore = f.store
			if tc.wrap != nil {
				s = tc.wrap(f)
			}
			useScannerStore(t, s)

			v, err := checkFileOwnership(tc.book(f))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if v.skip != tc.wantSkip {
				t.Fatalf("skip = %v (%s), want %v", v.skip, v.reason, tc.wantSkip)
			}
			if tc.wantSkip && (len(v.owners) != 1 || v.owners[0] != f.parent.ID) {
				t.Fatalf("owners = %v, want [%s]", v.owners, f.parent.ID)
			}
		})
	}
}

// TestSaveBookToDatabase_ChapterOfOwnedBookIsNotImported is the end-to-end
// shape of the Eldest incident: a chapter file already owned by its parent's
// book_file row is scanned on its own, carrying the parent's organizer-ID tag.
// No book may be created for it, and the parent's FilePath must not be
// repointed at the chapter by the organizer-ID relink.
func TestSaveBookToDatabase_ChapterOfOwnedBookIsNotImported(t *testing.T) {
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)

	before := fragmentSkipCount.Load()
	chapter := &Book{
		FilePath:        f.chapters[1],
		Title:           "98",
		Author:          "Eldest",
		Format:          ".mp3",
		BookOrganizerID: f.parent.ID,
	}
	if err := saveBookToDatabase(context.Background(), chapter); err != nil {
		t.Fatalf("saveBookToDatabase: %v", err)
	}
	if got, _ := f.store.GetBookByFilePath(f.chapters[1]); got != nil {
		t.Fatalf("a book %s was created at the chapter path", got.ID)
	}
	parent, err := f.store.GetBookByID(f.parent.ID)
	if err != nil || parent == nil {
		t.Fatalf("parent lookup: %v", err)
	}
	if parent.FilePath != f.dir {
		t.Fatalf("parent FilePath = %q, want %q (relinked to a chapter)", parent.FilePath, f.dir)
	}
	if a, _ := f.store.GetAuthorByName("Eldest"); a != nil {
		t.Fatalf("author %q was created for a skipped fragment", a.Name)
	}
	if fragmentSkipCount.Load() != before+1 {
		t.Fatalf("fragmentSkipCount did not advance")
	}

	// And an unowned file in the same folder still imports.
	loose := &Book{FilePath: f.loose[0], Title: "New", Author: "Someone", Format: ".mp3"}
	if err := saveBookToDatabase(context.Background(), loose); err != nil {
		t.Fatalf("save unowned: %v", err)
	}
	if got, _ := f.store.GetBookByFilePath(f.loose[0]); got == nil {
		t.Fatalf("unowned file was not imported")
	}
}

// TestSaveBookToDatabase_RegroupedFragmentFolderIsNotOverlaid is the first
// rescan after the wider chapter grouping deploys: a folder whose chapters were
// ALREADY imported as one single-file book each is now emitted as one
// multi-file group whose FilePath is fragment #1's path. The path hit must not
// be treated as "update fragment #1 with the group's values" -- the files
// belong to several books, so the group is a fragment set and is skipped.
func TestSaveBookToDatabase_RegroupedFragmentFolderIsNotOverlaid(t *testing.T) {
	f := newOwnershipFixture(t)
	useScannerStore(t, f.store)

	var frags []*database.Book
	for i, p := range f.loose {
		b, err := f.store.CreateBook(&database.Book{Title: fmt.Sprintf("%02d", i+1), FilePath: p})
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: p}); err != nil {
			t.Fatal(err)
		}
		frags = append(frags, b)
	}

	group := &Book{FilePath: f.loose[0], SegmentFiles: f.loose, Title: "The Whole Book", Author: "Someone", Duration: 36000}
	v, err := checkFileOwnership(group)
	if err != nil || !v.skip || len(v.owners) != len(frags) {
		t.Fatalf("verdict = %+v, err %v; want a skip naming %d owners", v, err, len(frags))
	}
	if err := saveBookToDatabase(context.Background(), group); err != nil {
		t.Fatalf("saveBookToDatabase: %v", err)
	}
	got, err := f.store.GetBookByID(frags[0].ID)
	if err != nil || got == nil {
		t.Fatalf("fragment #1 lookup: %v", err)
	}
	if got.Title != "01" {
		t.Fatalf("fragment #1 was overlaid with the group's values: title %q", got.Title)
	}
}
