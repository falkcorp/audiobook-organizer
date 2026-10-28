// file: internal/versions/purge_served_guard_test.go
// version: 1.1.0
// guid: 7d2c9e41-5b8a-4f36-a1d0-c4e83b6f9a25
// last-edited: 2026-10-27

package versions

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The trash purge must never delete a file the version's own live book still
// serves. Trashing the ACTIVE version only flips its status: its files stay
// where the book plays them (book.FilePath and its book_file rows), and 14
// days later the purge deleted them, leaving the book pointing at a missing
// file. These tests run on a real Pebble store with real files and assert on
// the file, the version's status, the result and the journal line.

type servedFixture struct {
	t       *testing.T
	store   *database.PebbleStore
	dir     string // the book's folder
	book    *database.Book
	active  string // path book.FilePath names
	logBuf  *bytes.Buffer
	restore func()
}

func newServedFixture(t *testing.T) *servedFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	dir := filepath.Join(t.TempDir(), "Gotham", "Batman Hush")
	active := filepath.Join(dir, "hush.m4b")
	writeServed(t, active)
	book, err := store.CreateBook(&database.Book{ID: "hush", Title: "Batman: Hush", FilePath: active})
	if err != nil {
		t.Fatalf("create book: %v", err)
	}

	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	return &servedFixture{t: t, store: store, dir: dir, book: book, active: active, logBuf: buf}
}

func writeServed(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func onDisk(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func (f *servedFixture) version(status string, ingestAgo time.Duration) *database.BookVersion {
	f.t.Helper()
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.book.ID, Status: status, Format: "m4b", Source: "imported",
		IngestDate: time.Now().Add(-ingestAgo),
	})
	if err != nil {
		f.t.Fatalf("create version: %v", err)
	}
	return v
}

func (f *servedFixture) row(versionID, path string, track int) {
	f.t.Helper()
	if err := f.store.CreateBookFile(&database.BookFile{
		BookID: f.book.ID, FilePath: path, VersionID: versionID, TrackNumber: track, Format: "m4b",
	}); err != nil {
		f.t.Fatalf("create book file: %v", err)
	}
}

// trashLikeHandler is handleTrashVersion's body: MarkTrashed, persist, and
// when the version was active, promote the newest alt.
func (f *servedFixture) trashLikeHandler(id string) {
	f.t.Helper()
	v, err := f.store.GetBookVersion(id)
	if err != nil || v == nil {
		f.t.Fatalf("get version: %v", err)
	}
	wasActive := v.Status == database.BookVersionStatusActive
	MarkTrashed(v, time.Now())
	if err := f.store.UpdateBookVersion(v); err != nil {
		f.t.Fatalf("trash: %v", err)
	}
	if wasActive {
		if err := AutoPromoteAlt(f.store, f.book.ID); err != nil {
			f.t.Fatalf("promote: %v", err)
		}
	}
}

func (f *servedFixture) status(id string) string {
	f.t.Helper()
	v, err := f.store.GetBookVersion(id)
	if err != nil || v == nil {
		f.t.Fatalf("get version: %v", err)
	}
	return v.Status
}

// afterWindow runs the nightly cleanup 40 days on, well past the 14-day window.
func (f *servedFixture) afterWindow() CleanupResult {
	return cleanupTrashedVersionsAt(f.store, time.Now().Add(40*day))
}

func (f *servedFixture) requireKept(res CleanupResult, v *database.BookVersion, paths int) {
	f.t.Helper()
	if res.Purged != 0 {
		f.t.Fatalf("purged %d, want 0", res.Purged)
	}
	if len(res.Kept) != 1 || res.Kept[0] != (KeptVersion{BookID: f.book.ID, VersionID: v.ID, Paths: paths}) {
		f.t.Fatalf("Kept = %+v, want exactly {%s %s %d}", res.Kept, f.book.ID, v.ID, paths)
	}
	if got := f.status(v.ID); got != database.BookVersionStatusTrash {
		f.t.Fatalf("status = %s, want trash (not purged)", got)
	}
	// One WARN naming the book, the version, the path count and the reason.
	var warns []string
	for _, line := range strings.Split(f.logBuf.String(), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, v.ID) {
			warns = append(warns, line)
		}
	}
	if len(warns) != 1 {
		f.t.Fatalf("got %d WARN lines for the kept version, want 1:\n%s", len(warns), f.logBuf.String())
	}
	for _, want := range []string{f.book.ID, v.ID, "book still serves this file"} {
		if !strings.Contains(warns[0], want) {
			f.t.Errorf("WARN line lacks %q: %s", want, warns[0])
		}
	}
}

func TestPurgeKeepsServedFiles_ActiveVersionTrashedWithAlt(t *testing.T) {
	f := newServedFixture(t)
	v1 := f.version(database.BookVersionStatusActive, 90*day)
	f.row(v1.ID, f.active, 1)
	v2 := f.version(database.BookVersionStatusAlt, 30*day)
	altFile := filepath.Join(VersionSlotDir(f.dir, v2.ID), "hush-alt.m4b")
	writeServed(t, altFile)
	f.row(v2.ID, altFile, 1)

	f.trashLikeHandler(v1.ID)
	if got := f.status(v2.ID); got != database.BookVersionStatusActive {
		t.Fatalf("alt not promoted: %s", got)
	}

	res := f.afterWindow()

	if !onDisk(f.active) {
		t.Fatal("the file book.FilePath names was deleted by the purge")
	}
	if !onDisk(altFile) {
		t.Fatal("the promoted version's file was deleted")
	}
	f.requireKept(res, v1, 1)
}

func TestPurgeKeepsServedFiles_ActiveVersionTrashedAsOnlyVersion(t *testing.T) {
	f := newServedFixture(t)
	v1 := f.version(database.BookVersionStatusActive, 90*day)
	f.row(v1.ID, f.active, 1)

	f.trashLikeHandler(v1.ID)
	res := f.afterWindow()

	if !onDisk(f.active) {
		t.Fatal("the book's only file was deleted by the purge")
	}
	f.requireKept(res, v1, 1)
}

// Rule (b): the path is not book.FilePath, but a book_file row of the book's
// ACTIVE version, or a row with no version, names it.
func TestPurgeKeepsServedFiles_NamedByActiveVersionRowOrUnversionedRow(t *testing.T) {
	for _, tc := range []struct {
		name string
		// servedBy returns the version id the serving row carries.
		servedBy func(activeID string) string
	}{
		{"row of the active version", func(activeID string) string { return activeID }},
		{"row with no version", func(string) string { return "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServedFixture(t)
			active := f.version(database.BookVersionStatusActive, 10*day)
			f.row(active.ID, f.active, 1)

			shared := filepath.Join(f.dir, "hush-part2.m4b")
			writeServed(t, shared)
			f.row(tc.servedBy(active.ID), shared, 2)

			trashed := f.version(database.BookVersionStatusAlt, 60*day)
			f.row(trashed.ID, shared, 1)
			f.trashLikeHandler(trashed.ID)
			res := f.afterWindow()
			if !onDisk(shared) {
				t.Fatal("a file the active version's row names was deleted")
			}
			f.requireKept(res, trashed, 1)
		})
	}
}

// The guard holds on the purge-now path too (handlePurgeVersion calls
// PurgeVersion directly, with no window).
func TestPurgeNowKeepsServedFiles(t *testing.T) {
	f := newServedFixture(t)
	v1 := f.version(database.BookVersionStatusActive, 90*day)
	f.row(v1.ID, f.active, 1)
	f.trashLikeHandler(v1.ID)

	v, _ := f.store.GetBookVersion(v1.ID)
	err := PurgeVersion(f.store, v)
	var served *BookServesFilesError
	if !errors.As(err, &served) {
		t.Fatalf("PurgeVersion err = %v, want *BookServesFilesError", err)
	}
	if served.VersionID != v1.ID || served.BookID != f.book.ID || len(served.Paths) != 1 || served.Paths[0] != f.active {
		t.Fatalf("error = %+v", served)
	}
	if !onDisk(f.active) {
		t.Fatal("purge-now deleted the file the book serves")
	}
	if got := f.status(v1.ID); got != database.BookVersionStatusTrash {
		t.Fatalf("status = %s, want trash", got)
	}
}

// Control: a trashed alt whose files are only its own still purges.
func TestPurgeStillPurgesAltVersionsOwnFiles(t *testing.T) {
	f := newServedFixture(t)
	active := f.version(database.BookVersionStatusActive, 10*day)
	f.row(active.ID, f.active, 1)

	alt := f.version(database.BookVersionStatusAlt, 30*day)
	altFile := filepath.Join(VersionSlotDir(f.dir, alt.ID), "hush-alt.m4b")
	writeServed(t, altFile)
	f.row(alt.ID, altFile, 1)
	f.trashLikeHandler(alt.ID)

	res := f.afterWindow()

	if res.Purged != 1 || len(res.Kept) != 0 {
		t.Fatalf("result = %+v, want 1 purged, 0 kept", res)
	}
	if onDisk(altFile) {
		t.Fatal("the alt version's own file survived its purge")
	}
	if !onDisk(f.active) {
		t.Fatal("the active version's file was deleted by purging the alt")
	}
	if got := f.status(alt.ID); got != database.BookVersionStatusInactivePurged {
		t.Fatalf("status = %s, want inactive_purged", got)
	}
}

// Control: a book that is itself in the trash serves nothing, so the purge
// deletes the version's files as before.
func TestPurgeStillPurgesWhenBookItselfTrashed(t *testing.T) {
	for _, tc := range []struct {
		name string
		mark func(b *database.Book)
	}{
		{"marked for deletion", func(b *database.Book) { yes := true; b.MarkedForDeletion = &yes }},
		{"library state deleted", func(b *database.Book) { s := "deleted"; b.LibraryState = &s }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newServedFixture(t)
			v1 := f.version(database.BookVersionStatusActive, 90*day)
			f.row(v1.ID, f.active, 1)
			f.trashLikeHandler(v1.ID)
			if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error { tc.mark(b); return nil }); err != nil {
				t.Fatalf("trash book: %v", err)
			}

			res := f.afterWindow()

			if res.Purged != 1 || len(res.Kept) != 0 {
				t.Fatalf("result = %+v, want 1 purged, 0 kept", res)
			}
			if onDisk(f.active) {
				t.Fatal("a trashed book's version file survived the purge")
			}
			if got := f.status(v1.ID); got != database.BookVersionStatusInactivePurged {
				t.Fatalf("status = %s, want inactive_purged", got)
			}
		})
	}
}

// failingVersionsStore makes reading the book's versions fail.
type failingVersionsStore struct {
	*database.PebbleStore
}

func (failingVersionsStore) GetBookVersionsByBookID(string) ([]database.BookVersion, error) {
	return nil, errors.New("transient: pebble busy")
}

// Fail closed: when the guard cannot tell what the book serves, nothing is
// deleted and the version is not purged.
func TestPurgeFailsClosedWhenVersionsCannotBeRead(t *testing.T) {
	f := newServedFixture(t)
	v1 := f.version(database.BookVersionStatusActive, 90*day)
	f.row(v1.ID, f.active, 1)
	f.trashLikeHandler(v1.ID)

	res := cleanupTrashedVersionsAt(failingVersionsStore{f.store}, time.Now().Add(40*day))

	if res.Purged != 0 {
		t.Fatalf("purged %d on a read error, want 0", res.Purged)
	}
	if !onDisk(f.active) {
		t.Fatal("a read error let the purge delete the file")
	}
	if got := f.status(v1.ID); got != database.BookVersionStatusTrash {
		t.Fatalf("status = %s, want trash", got)
	}
}

// ---- multi-file books, symlinks, hard links, other books ----

// setBookFilePath re-points the fixture book's FilePath.
func (f *servedFixture) setBookFilePath(p string) {
	f.t.Helper()
	f.book.FilePath = p
	if _, err := f.store.UpdateBook(f.book.ID, f.book); err != nil {
		f.t.Fatalf("update book: %v", err)
	}
}

func (f *servedFixture) requireAllOnDisk(paths ...string) {
	f.t.Helper()
	for _, p := range paths {
		if !onDisk(p) {
			f.t.Fatalf("SERVED FILE DELETED: %s", p)
		}
	}
}

// A multi-file book's FilePath is its containing FOLDER, not a file, so an
// exact match never fires: every track of the trashed active version sits
// inside the folder the book still plays.
func TestPurgeKeepsServedFiles_MultiFileBookFolderFilePath(t *testing.T) {
	for _, withAlt := range []bool{false, true} {
		for _, mode := range []string{"nightly-cleanup", "purge-now"} {
			name := mode + "/only-version"
			if withAlt {
				name = mode + "/with-alt"
			}
			t.Run(name, func(t *testing.T) {
				f := newServedFixture(t)
				f.setBookFilePath(f.dir)
				v1 := f.version(database.BookVersionStatusActive, 90*day)
				p1, p2 := filepath.Join(f.dir, "01.mp3"), filepath.Join(f.dir, "02.mp3")
				writeServed(t, p1)
				writeServed(t, p2)
				f.row(v1.ID, p1, 1)
				f.row(v1.ID, p2, 2)
				var altFile string
				if withAlt {
					v2 := f.version(database.BookVersionStatusAlt, 30*day)
					altFile = filepath.Join(VersionSlotDir(filepath.Dir(f.dir), v2.ID), "alt.m4b")
					writeServed(t, altFile)
					f.row(v2.ID, altFile, 1)
				}
				f.trashLikeHandler(v1.ID)

				if mode == "nightly-cleanup" {
					res := f.afterWindow()
					f.requireAllOnDisk(p1, p2)
					f.requireKept(res, v1, 2)
				} else {
					v, _ := f.store.GetBookVersion(v1.ID)
					var served *BookServesFilesError
					if err := PurgeVersion(f.store, v); !errors.As(err, &served) || len(served.Paths) != 2 {
						t.Fatalf("PurgeVersion err = %v, want *BookServesFilesError with 2 paths", err)
					}
					f.requireAllOnDisk(p1, p2)
					if got := f.status(v1.ID); got != database.BookVersionStatusTrash {
						t.Fatalf("status = %s, want trash", got)
					}
				}
				if withAlt {
					f.requireAllOnDisk(altFile)
				}
			})
		}
	}
}

// Control: with a folder FilePath, a trashed alt whose files sit in its
// .versions/ slot (beside or under the folder) is not served and still purges.
func TestPurgeStillPurgesAltSlotFilesOfFolderBook(t *testing.T) {
	for name, slotBase := range map[string]func(dir string) string{
		"slot beside the folder": func(dir string) string { return filepath.Dir(dir) },
		"slot under the folder":  func(dir string) string { return dir },
	} {
		t.Run(name, func(t *testing.T) {
			f := newServedFixture(t)
			f.setBookFilePath(f.dir)
			active := f.version(database.BookVersionStatusActive, 10*day)
			p1 := filepath.Join(f.dir, "01.mp3")
			writeServed(t, p1)
			f.row(active.ID, p1, 1)
			alt := f.version(database.BookVersionStatusAlt, 30*day)
			altFile := filepath.Join(VersionSlotDir(slotBase(f.dir), alt.ID), "alt.m4b")
			writeServed(t, altFile)
			f.row(alt.ID, altFile, 1)
			f.trashLikeHandler(alt.ID)

			res := f.afterWindow()

			if res.Purged != 1 || len(res.Kept) != 0 {
				t.Fatalf("result = %+v, want 1 purged", res)
			}
			if onDisk(altFile) {
				t.Fatal("the alt's own slot file survived its purge")
			}
			f.requireAllOnDisk(p1)
		})
	}
}

// The folder a book's FilePath names is not the folder of an AUDIO-extension
// FilePath: a single-file book's FilePath is the file, and a sibling file in
// its directory that the version owns is not served by it.
func TestPurgeStillPurgesSiblingOfSingleFileBook(t *testing.T) {
	f := newServedFixture(t)
	active := f.version(database.BookVersionStatusActive, 10*day)
	f.row(active.ID, f.active, 1)
	alt := f.version(database.BookVersionStatusAlt, 30*day)
	sibling := filepath.Join(f.dir, "hush-alt.m4b")
	writeServed(t, sibling)
	f.row(alt.ID, sibling, 1)
	f.trashLikeHandler(alt.ID)

	res := f.afterWindow()

	if res.Purged != 1 || onDisk(sibling) {
		t.Fatalf("result = %+v, sibling on disk = %v: a sibling of a single-file book is not served", res, onDisk(sibling))
	}
	f.requireAllOnDisk(f.active)
}

// book.FilePath recorded through a symlinked root; rows recorded with the
// real path (or the other way round).
func TestPurgeKeepsServedFiles_SymlinkedRoot(t *testing.T) {
	f := newServedFixture(t)
	link := filepath.Join(t.TempDir(), "lib-link")
	if err := os.Symlink(f.dir, link); err != nil {
		t.Fatal(err)
	}
	f.setBookFilePath(filepath.Join(link, "hush.m4b"))
	v1 := f.version(database.BookVersionStatusActive, 90*day)
	f.row(v1.ID, f.active, 1)
	f.trashLikeHandler(v1.ID)

	res := f.afterWindow()

	f.requireAllOnDisk(f.active)
	f.requireKept(res, v1, 1)
}

// The same file under another name (a hard link) is the same file.
func TestPurgeKeepsServedFiles_HardLink(t *testing.T) {
	f := newServedFixture(t)
	link := filepath.Join(f.dir, "hush-link.m4b")
	if err := os.Link(f.active, link); err != nil {
		t.Skipf("hard links unsupported: %v", err)
	}
	active := f.version(database.BookVersionStatusActive, 10*day)
	f.row(active.ID, f.active, 1)
	alt := f.version(database.BookVersionStatusAlt, 30*day)
	f.row(alt.ID, link, 1) // the trashed version's row names the hard link
	f.trashLikeHandler(alt.ID)

	res := f.afterWindow()

	f.requireAllOnDisk(link, f.active)
	f.requireKept(res, alt, 1)
}

// otherBook creates a second live book at path with one active-version row
// (versioned) or one unversioned row.
func (f *servedFixture) otherBook(id, path string, versioned bool, setFilePath bool) *database.Book {
	f.t.Helper()
	fp := ""
	if setFilePath {
		fp = path
	}
	b, err := f.store.CreateBook(&database.Book{ID: id, Title: "Batman: Year One", FilePath: fp})
	if err != nil {
		f.t.Fatalf("create other book: %v", err)
	}
	vid := ""
	if versioned {
		ov, err := f.store.CreateBookVersion(&database.BookVersion{BookID: id, Status: database.BookVersionStatusActive, Format: "m4b", Source: "imported"})
		if err != nil {
			f.t.Fatal(err)
		}
		vid = ov.ID
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: id, FilePath: path, VersionID: vid, TrackNumber: 1, Format: "m4b"}); err != nil {
		f.t.Fatal(err)
	}
	return b
}

// trashedAltNaming trashes an alt of the fixture book whose row names path.
func (f *servedFixture) trashedAltNaming(path string) *database.BookVersion {
	f.t.Helper()
	active := f.version(database.BookVersionStatusActive, 10*day)
	f.row(active.ID, f.active, 1)
	v := f.version(database.BookVersionStatusAlt, 30*day)
	f.row(v.ID, path, 1)
	f.trashLikeHandler(v.ID)
	return v
}

// Deluge and import folders are shared: a file another LIVE book serves is
// kept, whichever way that book names it.
func TestPurgeKeepsServedFiles_ServedByAnotherLiveBook(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(f *servedFixture, shared string)
	}{
		{"FilePath and active-version row", func(f *servedFixture, shared string) { f.otherBook("other", shared, true, true) }},
		{"active-version row only", func(f *servedFixture, shared string) { f.otherBook("other", shared, true, false) }},
		{"unversioned row only", func(f *servedFixture, shared string) { f.otherBook("other", shared, false, false) }},
		{"FilePath is the containing folder", func(f *servedFixture, shared string) {
			if _, err := f.store.CreateBook(&database.Book{ID: "other", Title: "Batman: Year One", FilePath: filepath.Dir(shared)}); err != nil {
				f.t.Fatal(err)
			}
		}},
	} {
		for _, mode := range []string{"nightly-cleanup", "purge-now"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				f := newServedFixture(t)
				shared := filepath.Join(t.TempDir(), "deluge", "shared.m4b")
				writeServed(t, shared)
				tc.make(f, shared)
				v := f.trashedAltNaming(shared)

				if mode == "nightly-cleanup" {
					res := f.afterWindow()
					f.requireAllOnDisk(shared)
					f.requireKept(res, v, 1)
					return
				}
				got, _ := f.store.GetBookVersion(v.ID)
				var served *BookServesFilesError
				if err := PurgeVersion(f.store, got); !errors.As(err, &served) {
					t.Fatalf("PurgeVersion err = %v, want *BookServesFilesError", err)
				}
				f.requireAllOnDisk(shared)
			})
		}
	}
}

// Another book that is itself trashed, or names the file only through a
// non-active version, does not serve it: the version still purges.
func TestPurgeStillPurgesWhenOtherBookDoesNotServe(t *testing.T) {
	for name, setup := range map[string]func(f *servedFixture, shared string){
		"other book trashed": func(f *servedFixture, shared string) {
			f.otherBook("other", shared, true, true)
			if _, err := f.store.ModifyBook("other", func(b *database.Book) error { yes := true; b.MarkedForDeletion = &yes; return nil }); err != nil {
				f.t.Fatal(err)
			}
		},
		"other book names it through an alt version": func(f *servedFixture, shared string) {
			if _, err := f.store.CreateBook(&database.Book{ID: "other", Title: "Batman: Year One"}); err != nil {
				f.t.Fatal(err)
			}
			ov, err := f.store.CreateBookVersion(&database.BookVersion{BookID: "other", Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported"})
			if err != nil {
				f.t.Fatal(err)
			}
			if err := f.store.CreateBookFile(&database.BookFile{BookID: "other", FilePath: shared, VersionID: ov.ID, TrackNumber: 1}); err != nil {
				f.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newServedFixture(t)
			shared := filepath.Join(f.dir, ".versions", "x", "shared.m4b")
			writeServed(t, shared)
			setup(f, shared)
			f.trashedAltNaming(shared)

			res := f.afterWindow()

			if res.Purged != 1 || len(res.Kept) != 0 || onDisk(shared) {
				t.Fatalf("result = %+v, shared on disk = %v, want purged and deleted", res, onDisk(shared))
			}
		})
	}
}

// failingAtPathStore makes the other-books lookup fail.
type failingAtPathStore struct {
	*database.PebbleStore
	err error
}

func (s failingAtPathStore) BookFilesAtPath(string) ([]database.BookFile, error) { return nil, s.err }

type failingByFilePathStore struct{ *database.PebbleStore }

func (failingByFilePathStore) GetBookByFilePath(string) (*database.Book, error) {
	return nil, errors.New("transient: pebble busy")
}

// Fail closed: when the guard cannot tell whether another book serves a file,
// nothing is deleted and the version is not purged.
func TestPurgeFailsClosedWhenOtherBooksCannotBeRead(t *testing.T) {
	for name, wrap := range map[string]func(*database.PebbleStore) TrashedVersionCleaner{
		"index unavailable": func(s *database.PebbleStore) TrashedVersionCleaner {
			return failingAtPathStore{s, database.ErrBookFilesAtPathUnavailable}
		},
		"row lookup error": func(s *database.PebbleStore) TrashedVersionCleaner {
			return failingAtPathStore{s, fmt.Errorf("transient: %w", errors.New("pebble busy"))}
		},
		"book-by-path error": func(s *database.PebbleStore) TrashedVersionCleaner { return failingByFilePathStore{s} },
	} {
		t.Run(name, func(t *testing.T) {
			f := newServedFixture(t)
			own := filepath.Join(VersionSlotDir(f.dir, "slotid"), "alt.m4b")
			writeServed(t, own)
			v := f.trashedAltNaming(own)

			res := cleanupTrashedVersionsAt(wrap(f.store), time.Now().Add(40*day))

			if res.Purged != 0 {
				t.Fatalf("purged %d on a read error, want 0", res.Purged)
			}
			f.requireAllOnDisk(own, f.active)
			if got := f.status(v.ID); got != database.BookVersionStatusTrash {
				t.Fatalf("status = %s, want trash", got)
			}
		})
	}
}
