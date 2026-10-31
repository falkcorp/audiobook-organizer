// file: internal/versions/serve_guard_links_test.go
// version: 1.0.0
// guid: 7a3e8ca0-20ed-4c8d-9eb4-b8bf2efde8c4
// last-edited: 2026-10-30

package versions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// A book organized with the symlink strategy has a book_file row that names the
// LINK. The guard asked the stores which rows name the version's file, an exact
// path match, so it never met the link and a purge deleted the file the link
// plays. These tests purge a trashed alt whose slot file another live book
// reaches through a link, on a real Pebble store with real files and links.

// symlink makes link -> target, creating link's folder.
func (f *servedFixture) symlink(target, link string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		f.t.Fatal(err)
	}
}

// trashedAltWithSlotFile is an active version plus a trashed alt that owns one
// file in its real slot.
func (f *servedFixture) trashedAltWithSlotFile() (*database.BookVersion, string) {
	f.t.Helper()
	v, slotFile := f.altWithSlotFile("hush-alt.m4b")
	f.trashLikeHandler(v.ID)
	return v, slotFile
}

func (f *servedFixture) requirePurgedAndGone(res CleanupResult, v *database.BookVersion, path string) {
	f.t.Helper()
	if res.Purged != 1 || len(res.Kept) != 0 || onDisk(path) {
		f.t.Fatalf("result = %+v, on disk = %v, want the version purged and %s removed", res, onDisk(path), path)
	}
	if got := f.status(v.ID); got != database.BookVersionStatusInactivePurged {
		f.t.Fatalf("status = %s, want %s", got, database.BookVersionStatusInactivePurged)
	}
}

func TestPurgeKeepsFile_AnotherBookLinkResolvesToIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *servedFixture, slotFile string)
	}{
		{"a book_file row that is a link to it", func(f *servedFixture, slotFile string) {
			link := filepath.Join(t.TempDir(), "Gotham", "Batman Year One", "hush-link.m4b")
			f.symlink(slotFile, link)
			f.otherBook("other", link, true, false)
		}},
		{"a Book.FilePath that is a link to it", func(f *servedFixture, slotFile string) {
			link := filepath.Join(t.TempDir(), "Gotham", "Batman Year One", "hush-link.m4b")
			f.symlink(slotFile, link)
			if _, err := f.store.CreateBook(&database.Book{ID: "other", Title: "Batman: Year One", FilePath: link}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link to a link to it", func(f *servedFixture, slotFile string) {
			hop := filepath.Join(t.TempDir(), "hop", "hush-hop.m4b")
			f.symlink(slotFile, hop)
			link := filepath.Join(t.TempDir(), "Gotham", "Batman Year One", "hush-link.m4b")
			f.symlink(hop, link)
			f.otherBook("other", link, true, false)
		}},
		{"a relative link to it", func(f *servedFixture, slotFile string) {
			dir := filepath.Join(t.TempDir(), "Gotham", "Batman Year One")
			rel, err := filepath.Rel(dir, slotFile)
			if err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(dir, "hush-link.m4b")
			f.symlink(rel, link)
			f.otherBook("other", link, true, false)
		}},
		{"a row under a folder that is a link to the slot", func(f *servedFixture, slotFile string) {
			dirLink := filepath.Join(t.TempDir(), "Gotham", "alt-link")
			f.symlink(filepath.Dir(slotFile), dirLink)
			f.otherBook("other", filepath.Join(dirLink, filepath.Base(slotFile)), true, false)
		}},
		{"a FilePath that is a link to the slot folder", func(f *servedFixture, slotFile string) {
			dirLink := filepath.Join(t.TempDir(), "Gotham", "alt-link")
			f.symlink(filepath.Dir(slotFile), dirLink)
			if _, err := f.store.CreateBook(&database.Book{ID: "other", Title: "Batman: Year One", FilePath: dirLink}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link in an unversioned row", func(f *servedFixture, slotFile string) {
			link := filepath.Join(t.TempDir(), "Gotham", "Batman Year One", "hush-link.m4b")
			f.symlink(slotFile, link)
			f.otherBook("other", link, false, false)
		}},
	} {
		for _, mode := range []string{"nightly-cleanup", "purge-now"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				f := newServedFixture(t)
				v, slotFile := f.trashedAltWithSlotFile()
				tc.setup(f, slotFile)

				if mode == "nightly-cleanup" {
					res := f.afterWindow()
					f.requireAllOnDisk(slotFile)
					f.requireKept(res, v, 1)
					return
				}
				got, _ := f.store.GetBookVersion(v.ID)
				var served *BookServesFilesError
				if err := PurgeVersion(f.store, got, testGuard); !errors.As(err, &served) {
					t.Fatalf("PurgeVersion err = %v, want *BookServesFilesError", err)
				}
				f.requireAllOnDisk(slotFile)
			})
		}
	}
}

func TestPurgeStillPurges_WhenOtherBookLinkIsNotItsFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *servedFixture, slotFile string)
	}{
		{"a link to an unrelated file", func(f *servedFixture, slotFile string) {
			unrelated := filepath.Join(t.TempDir(), "elsewhere", "joker.m4b")
			writeServed(t, unrelated)
			link := filepath.Join(t.TempDir(), "Gotham", "Batman Year One", "joker-link.m4b")
			f.symlink(unrelated, link)
			f.otherBook("other", link, true, false)
		}},
		{"a link that points at nothing", func(f *servedFixture, slotFile string) {
			link := filepath.Join(t.TempDir(), "Gotham", "Batman Year One", "dangling.m4b")
			f.symlink(filepath.Join(t.TempDir(), "gone.m4b"), link)
			f.otherBook("other", link, true, false)
		}},
		{"a row whose file is not there", func(f *servedFixture, slotFile string) {
			f.otherBook("other", filepath.Join(t.TempDir(), "never-written.m4b"), true, false)
		}},
		{"a FilePath that is a link to the parent book folder", func(f *servedFixture, slotFile string) {
			// The folder above .versions/ does not serve what is inside it.
			link := filepath.Join(t.TempDir(), "Gotham", "parent-link")
			f.symlink(f.dir, link)
			if _, err := f.store.CreateBook(&database.Book{ID: "other", Title: "Batman: Year One", FilePath: link}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a link held by a book that is trashed", func(f *servedFixture, slotFile string) {
			link := filepath.Join(t.TempDir(), "Gotham", "Batman Year One", "hush-link.m4b")
			f.symlink(slotFile, link)
			f.otherBook("other", link, true, false)
			if _, err := f.store.ModifyBook("other", func(b *database.Book) error { yes := true; b.MarkedForDeletion = &yes; return nil }); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServedFixture(t)
			v, slotFile := f.trashedAltWithSlotFile()
			tc.setup(f, slotFile)

			f.requirePurgedAndGone(f.afterWindow(), v, slotFile)
		})
	}
}

func TestPurgeFailsClosed_WhenAnotherBooksPathCannotBeResolved(t *testing.T) {
	f := newServedFixture(t)
	v, slotFile := f.trashedAltWithSlotFile()

	// a -> b -> a: the link that plays the file, if any, cannot be told.
	dir := filepath.Join(t.TempDir(), "Gotham", "Batman Year One")
	a, b := filepath.Join(dir, "a.m4b"), filepath.Join(dir, "b.m4b")
	f.symlink(b, a)
	f.symlink(a, b)
	f.otherBook("other", a, true, false)

	res, err := servedCleanup(f.store, time.Now().Add(40*day))
	requireFailedClosed(t, res, err, errSymlinkLoop)
	f.requireAllOnDisk(slotFile)
	if got := f.status(v.ID); got != database.BookVersionStatusTrash {
		t.Fatalf("status = %s, want trash: a purge that could not be decided claims nothing", got)
	}
}

// noLibraryReaderStore hides everything but the cleaner's own surface, so the
// complete-library readers are not there.
type noLibraryReaderStore struct{ TrashedVersionCleaner }

func TestPurgeFailsClosed_WhenStoreCannotListTheLibrary(t *testing.T) {
	f := newServedFixture(t)
	v, slotFile := f.trashedAltWithSlotFile()

	res, err := servedCleanup(noLibraryReaderStore{f.store}, time.Now().Add(40*day))
	if res.Failed != 1 || res.Purged != 0 || err == nil {
		t.Fatalf("result = %+v, err = %v, want the purge failed closed", res, err)
	}
	f.requireAllOnDisk(slotFile)
	if got := f.status(v.ID); got != database.BookVersionStatusTrash {
		t.Fatalf("status = %s, want trash", got)
	}
}

// The index is built by a worker pool over chunks of rows: more rows than one
// chunk, so -race has more than one goroutine to look at, and one link among
// them that must be found.
func TestLinkIndex_ManyRowsOneLink(t *testing.T) {
	defer func(old int) { linkChunk = old }(linkChunk)
	linkChunk = 8 // several chunks, so several workers, from a few rows

	f := newServedFixture(t)
	v, slotFile := f.trashedAltWithSlotFile()

	if _, err := f.store.CreateBook(&database.Book{ID: "other", Title: "Batman: Year One"}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rows := 3*linkChunk + 5
	linkAt := 2*linkChunk + 5
	for i := 0; i < rows; i++ {
		p := filepath.Join(root, fmt.Sprintf("dir%d", i%7), fmt.Sprintf("track%03d.m4b", i))
		switch {
		case i == linkAt:
			f.symlink(slotFile, p)
		case i%3 == 0:
			writeServed(t, p)
		case i%3 == 1:
			f.symlink(filepath.Join(root, "nowhere"), p)
		}
		if err := f.store.CreateBookFile(&database.BookFile{BookID: "other", FilePath: p, TrackNumber: i, Format: "m4b"}); err != nil {
			t.Fatal(err)
		}
	}

	own, err := f.store.GetBookFiles(f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	g, err := newServedGuard(f.store, f.book, f.book.ID, own, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	reached, err := g.reachedByOtherBookLink(slotFile)
	if err != nil || !reached {
		t.Fatalf("reachedByOtherBookLink(%s) = %v, %v, want true", slotFile, reached, err)
	}
	unrelated := filepath.Join(root, "unnamed.m4b") // no row names it, no link reaches it
	writeServed(t, unrelated)
	reached, err = g.reachedByOtherBookLink(unrelated)
	if err != nil || reached {
		t.Fatalf("a plain file no link reaches: reachedByOtherBookLink = %v, %v, want false", reached, err)
	}
}
