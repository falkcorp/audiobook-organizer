// file: internal/versions/purge_served_guard_test.go
// version: 1.0.0
// guid: 7d2c9e41-5b8a-4f36-a1d0-c4e83b6f9a25
// last-edited: 2026-10-27

package versions

import (
	"bytes"
	"errors"
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
