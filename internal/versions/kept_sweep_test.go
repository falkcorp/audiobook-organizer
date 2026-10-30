// file: internal/versions/kept_sweep_test.go
// version: 1.0.4
// guid: 7d2c9e41-6b0a-4f58-a3d7-1e4b8c5f0926
// last-edited: 2026-10-29

package versions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// keptFixture is a live book (Batman: Year One) with a purged version whose
// files a purge kept because something else named them. Each addKept makes a
// file in the version's slot and notes it on the version, as PurgeVersion does.
type keptFixture struct {
	store   *database.PebbleStore
	book    *database.Book
	ver     *database.BookVersion
	bookDir string
	slot    string
	n       int
}

func newKeptFixture(t *testing.T) *keptFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.WaitForWarmup()

	bookDir := t.TempDir()
	// The file the book plays: a removal is allowed only while the book is known to
	// serve a file that exists (servedGuard.requireServedFileOnDisk).
	writeTestFile(t, filepath.Join(bookDir, "year-one.m4b"), "audio")
	book, err := store.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: filepath.Join(bookDir, "year-one.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	purgedAt := time.Now().Add(-3 * day)
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusInactivePurged, Format: "m4b", Source: "imported", PurgedDate: &purgedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &keptFixture{store: store, book: book, ver: ver, bookDir: bookDir, slot: VersionSlotDir(bookDir, ver.ID)}
}

// addKept writes a file in the slot, notes it as kept for why, and returns its path.
func (f *keptFixture) addKept(t *testing.T, why string) string {
	t.Helper()
	f.n++
	p := filepath.Join(f.slot, fmt.Sprintf("part%d.m4b", f.n))
	writeTestFile(t, p, "audio")
	f.note(t, p, why)
	return p
}

func (f *keptFixture) note(t *testing.T, p, why string) {
	t.Helper()
	cur, err := f.store.GetBookVersion(f.ver.ID)
	if err != nil || cur == nil {
		t.Fatalf("reread version: %v (nil=%v)", err, cur == nil)
	}
	cur.PurgeKept = append(cur.PurgeKept, database.PurgeFileNote{Path: p, Reason: why})
	if err := f.store.UpdateBookVersion(cur); err != nil {
		t.Fatalf("note %s: %v", p, err)
	}
}

func (f *keptFixture) kept(t *testing.T) []string {
	t.Helper()
	cur, err := f.store.GetBookVersion(f.ver.ID)
	if err != nil || cur == nil {
		t.Fatalf("reread version: %v (nil=%v)", err, cur == nil)
	}
	var out []string
	for _, n := range cur.PurgeKept {
		out = append(out, n.Path)
	}
	return out
}

// rowOwner makes a trashed-or-live other book own p through a book_file row.
func (f *keptFixture) rowOwner(t *testing.T, p string) (bookID, fileID string) {
	t.Helper()
	other, err := f.store.CreateBook(&database.Book{Title: "Batman: The Long Halloween", FilePath: filepath.Join(t.TempDir(), "lh.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	row := &database.BookFile{BookID: other.ID, FilePath: p, TrackNumber: 1}
	if err := f.store.CreateBookFile(row); err != nil {
		t.Fatal(err)
	}
	return other.ID, row.ID
}

// bookOwner makes a TRASHED book (restorable, so it still holds the file) whose
// own FilePath is p.
func (f *keptFixture) bookOwner(t *testing.T, p string) string {
	t.Helper()
	b, err := f.store.CreateBook(&database.Book{Title: "Batman: Hush", FilePath: p})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ModifyBook(b.ID, func(b *database.Book) error {
		yes := true
		b.MarkedForDeletion = &yes
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return b.ID
}

// testNight numbers the nights the test clock has been moved through, and
// testClockBase is where the first one starts: today, so a version trashed a
// while ago is old enough to purge on any night the clock reaches.
var (
	testNight     int
	testClockBase = time.Now()
	testClock     = testClockBase
)

// nextNight moves the sweep's clock to the next calendar day: the sweep counts
// one night a day (BookVersion.PurgeUndecidedNightOn), and the helpers below are
// one nightly run each. A test that wants two runs on the same day uses
// sweepSameNight. The clock is passed to the sweep (KeptSweepOptions.Now) and to
// the cleanup (cleanupTrashedVersionsAt), as the server wiring does.
func nextNight(t *testing.T) {
	t.Helper()
	testNight++
	testClock = testClockBase.AddDate(0, 0, testNight)
}

// sweep is one nightly run with the switch on, on a new day.
func sweep(t *testing.T, store KeptSweepStore, guard PurgeGuard) KeptSweepResult {
	t.Helper()
	nextNight(t)
	return sweepSameNight(t, store, guard, true)
}

// sweepReport is one nightly run with the switch off (the shipped default), on a
// new day.
func sweepReport(t *testing.T, store KeptSweepStore, guard PurgeGuard) KeptSweepResult {
	t.Helper()
	nextNight(t)
	return sweepSameNight(t, store, guard, false)
}

// sweepSameNight runs the sweep again on the day the clock is at.
func sweepSameNight(t *testing.T, store KeptSweepStore, guard PurgeGuard, apply bool) KeptSweepResult {
	t.Helper()
	res, err := SweepKeptFiles(store, guard, KeptSweepOptions{Apply: apply, Now: testClock})
	if err != nil {
		t.Fatalf("SweepKeptFiles (apply=%v): %v", apply, err)
	}
	return res
}

func requireCounts(t *testing.T, res KeptSweepResult, removed, gone, still, onErr int) {
	t.Helper()
	if res.Removed != removed || res.Gone != gone || res.StillKept != still || res.KeptOnError != onErr {
		t.Fatalf("removed=%d gone=%d still_kept=%d kept_on_error=%d, want %d %d %d %d (%+v)",
			res.Removed, res.Gone, res.StillKept, res.KeptOnError, removed, gone, still, onErr, res)
	}
}

// The whole point. A file kept because a trashed book named it stays while that
// book is there, is removed (and its note dropped, and the slot tidied) once the
// book is hard-deleted, and a third run finds nothing to do.
func TestSweepKeptFiles_FileWhoseTrashedBookIsGoneIsRemoved(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "it is the file path of book(s) [x] (live or in the trash)")
	owner := f.bookOwner(t, p)

	res := sweep(t, f.store, testGuard)
	requireCounts(t, res, 0, 0, 1, 0)
	requireOnDisk(t, p)
	if got := f.kept(t); !slices.Equal(got, []string{p}) {
		t.Fatalf("PurgeKept = %v, want %s still noted", got, p)
	}

	if err := f.store.DeleteBook(owner); err != nil {
		t.Fatalf("hard-delete the trashed book: %v", err)
	}
	res = sweep(t, f.store, testGuard)
	requireCounts(t, res, 1, 0, 0, 0)
	requireGone(t, p)
	if got := f.kept(t); len(got) != 0 {
		t.Fatalf("PurgeKept = %v, want it emptied", got)
	}
	if len(res.RemovedFiles) != 1 || res.RemovedFiles[0].VersionID != f.ver.ID || res.RemovedFiles[0].Files[0].Path != p {
		t.Errorf("RemovedFiles = %+v, want %s of version %s", res.RemovedFiles, p, f.ver.ID)
	}
	if _, err := os.Stat(f.slot); !os.IsNotExist(err) {
		t.Errorf("the emptied slot %s is still there (stat err %v)", f.slot, err)
	}
	requireOnDisk(t, filepath.Join(f.bookDir, "year-one.m4b"))

	// A re-run does nothing: no count, and no write to the version.
	before, _ := f.store.GetBookVersion(f.ver.ID)
	res = sweep(t, f.store, testGuard)
	requireCounts(t, res, 0, 0, 0, 0)
	after, _ := f.store.GetBookVersion(f.ver.ID)
	if after.Version != before.Version {
		t.Errorf("a re-run wrote the version (revision %d -> %d)", before.Version, after.Version)
	}
}

// The same with a book_file row as the other owner, and the row going away.
func TestSweepKeptFiles_FileWhoseOtherRowIsGoneIsRemoved(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "book_file x of book y still names it")
	_, rowID := f.rowOwner(t, p)

	requireCounts(t, sweep(t, f.store, testGuard), 0, 0, 1, 0)
	requireOnDisk(t, p)

	if err := f.store.DeleteBookFile(rowID); err != nil {
		t.Fatal(err)
	}
	requireCounts(t, sweep(t, f.store, testGuard), 1, 0, 0, 0)
	requireGone(t, p)
}

// Only the file whose owner is gone is removed; its neighbour that is still owned
// stays, with its note.
func TestSweepKeptFiles_OnlyTheUnownedFileGoes(t *testing.T) {
	f := newKeptFixture(t)
	free := f.addKept(t, "kept once")
	owned := f.addKept(t, "kept once")
	f.rowOwner(t, owned)

	res := sweep(t, f.store, testGuard)
	requireCounts(t, res, 1, 0, 1, 0)
	requireGone(t, free)
	requireOnDisk(t, owned)
	if got := f.kept(t); !slices.Equal(got, []string{owned}) {
		t.Fatalf("PurgeKept = %v, want only %s", got, owned)
	}
	// The slot still holds the owned file, so it is not tidied.
	if _, err := os.Stat(f.slot); err != nil {
		t.Errorf("the slot went although it still holds a kept file: %v", err)
	}
}

// A read that fails is not "no owner": the file and the note stay.
func TestSweepKeptFiles_ReadErrorKeepsTheFileAndTheNote(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &failingOwnerStore{PebbleStore: f.store, path: p}

	res := sweep(t, s, testGuard)
	requireCounts(t, res, 0, 0, 0, 1)
	requireOnDisk(t, p)
	if got := f.kept(t); !slices.Equal(got, []string{p}) {
		t.Fatalf("PurgeKept = %v, want %s still noted", got, p)
	}
	if len(res.ErrorFiles) != 1 || !strings.Contains(res.ErrorFiles[0].Files[0].Reason, "owner lookup down") {
		t.Errorf("ErrorFiles = %+v, want the lookup error as the reason", res.ErrorFiles)
	}
}

// The book-row side of the owner check fails closed too: a store that cannot
// answer "which books name this path" (no database.BookRowPathReader) removes
// nothing.
func TestSweepKeptFiles_BookRowLookupErrorKeepsTheFile(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &failingBookRowStore{PebbleStore: f.store}

	requireCounts(t, sweep(t, s, testGuard), 0, 0, 0, 1)
	requireOnDisk(t, p)
	if got := f.kept(t); len(got) != 1 {
		t.Fatalf("PurgeKept = %v, want the note kept", got)
	}
}

// The version's own book cannot be read: no served-file answer, nothing removed.
func TestSweepKeptFiles_UnreadableBookKeepsTheFile(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &failingBookStore{PebbleStore: f.store, id: f.book.ID}

	requireCounts(t, sweep(t, s, testGuard), 0, 0, 0, 1)
	requireOnDisk(t, p)
}

// A path under the iTunes library is never touched, whatever the owners say.
func TestSweepKeptFiles_ITunesPathIsNeverRemoved(t *testing.T) {
	f := newKeptFixture(t)
	itunes := filepath.Join(f.bookDir, "iTunes Media", "Audiobooks", "Hush", "hush.m4b")
	writeTestFile(t, itunes, "audio")
	f.note(t, itunes, "kept once")
	guard := &fsGuard{protected: func(p string) bool { return strings.Contains(p, "iTunes Media") }}

	res := sweep(t, f.store, guard)
	requireCounts(t, res, 0, 0, 1, 0)
	requireOnDisk(t, itunes)
	if got := f.kept(t); !slices.Equal(got, []string{itunes}) {
		t.Fatalf("PurgeKept = %v, want the iTunes note kept", got)
	}
}

// A file a live book serves is not removed: here a note for a file outside the
// version's slot, which the version's own live book still plays.
func TestSweepKeptFiles_FileALiveBookServesIsNotRemoved(t *testing.T) {
	f := newKeptFixture(t)
	served := filepath.Join(f.bookDir, "year-one-part2.m4b")
	writeTestFile(t, served, "audio")
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, FilePath: served, TrackNumber: 2}); err != nil {
		t.Fatal(err)
	}
	f.note(t, served, KeptReason)

	res := sweep(t, f.store, testGuard)
	requireCounts(t, res, 0, 0, 1, 0)
	requireOnDisk(t, served)
	if got := f.kept(t); !slices.Equal(got, []string{served}) {
		t.Fatalf("PurgeKept = %v, want %s still noted", got, served)
	}
}

// And a file ANOTHER live book plays (an active version's row of that book).
func TestSweepKeptFiles_FileAnotherLiveBookServesIsNotRemoved(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	f.rowOwner(t, p) // a live book's row, no version: served

	requireCounts(t, sweep(t, f.store, testGuard), 0, 0, 1, 0)
	requireOnDisk(t, p)
}

// A removal is allowed only while the version's own live book serves something
// that exists, as a trashed version's purge requires: with none, "nothing is
// served" is a guess.
func TestSweepKeptFiles_BookThatServesNothingOnDiskKeepsTheFile(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	if err := os.Remove(filepath.Join(f.bookDir, "year-one.m4b")); err != nil {
		t.Fatal(err)
	}

	res := sweep(t, f.store, testGuard)
	requireCounts(t, res, 0, 0, 0, 1)
	requireOnDisk(t, p)
}

// A removal that fails keeps the note and counts as kept on error.
func TestSweepKeptFiles_FailedRemovalKeepsTheNote(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	failRemovals(t, func(string) error { return syscall.EROFS })

	res := sweep(t, f.store, testGuard)
	requireCounts(t, res, 0, 0, 0, 1)
	requireOnDisk(t, p)
	if got := f.kept(t); !slices.Equal(got, []string{p}) {
		t.Fatalf("PurgeKept = %v, want the note kept", got)
	}
}

// The note is already stale (the file is gone) and its folder vouches for that:
// the note is dropped without an unlink.
func TestSweepKeptFiles_NoteForAFileThatIsAlreadyGoneIsDropped(t *testing.T) {
	f := newKeptFixture(t)
	gone := f.addKept(t, "kept once")
	sibling := f.addKept(t, "kept once")
	f.rowOwner(t, sibling) // keeps the slot (the folder) populated
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	attempted := failRemovals(t, func(string) error { return nil })

	requireCounts(t, sweep(t, f.store, testGuard), 0, 1, 1, 0)
	if len(*attempted) != 0 {
		t.Errorf("the sweep unlinked %v for a file that was already gone", *attempted)
	}
	if got := f.kept(t); !slices.Equal(got, []string{sibling}) {
		t.Fatalf("PurgeKept = %v, want only %s", got, sibling)
	}
}

// A file that is not there under a folder that is not there cannot be told apart
// from an unmounted dataset: kept on error, note stays.
func TestSweepKeptFiles_MissingFileUnderEmptyFolderCannotBeToldApart(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	// The slot is now an empty folder: which is also how an unmounted dataset looks.
	guard := &fsGuard{undecidable: func(path string) error {
		if entries, err := os.ReadDir(filepath.Dir(path)); err == nil && len(entries) == 0 {
			return errors.New("its folder is there but empty")
		}
		return nil
	}}

	requireCounts(t, sweep(t, f.store, guard), 0, 0, 0, 1)
	if got := f.kept(t); !slices.Equal(got, []string{p}) {
		t.Fatalf("PurgeKept = %v, want the note kept", got)
	}
}

// A library that is not ready removes nothing, and the sweep says so per file.
func TestSweepKeptFiles_LibraryNotReadyKeepsTheFile(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")

	res := sweep(t, f.store, &fsGuard{rootErr: errors.New("library root is not mounted")})
	requireCounts(t, res, 0, 0, 0, 1)
	requireOnDisk(t, p)
}

// A record that cannot be written stops the sweep there: the first file is gone
// (and counted), the rest is untouched.
func TestSweepKeptFiles_FailedRecordStopsTheRest(t *testing.T) {
	f := newKeptFixture(t)
	a := f.addKept(t, "kept once")
	b := f.addKept(t, "kept once")
	s := &keptWriteFailStore{PebbleStore: f.store}
	s.fail = true

	res := sweep(t, s, testGuard)
	if res.Removed != 1 || res.KeptOnError != 1 {
		t.Fatalf("removed=%d kept_on_error=%d, want 1 and 1 (%+v)", res.Removed, res.KeptOnError, res)
	}
	requireGone(t, a)
	requireOnDisk(t, b)
}

// The owners are asked again for each path right before its unlink, as the
// purge does: a row that starts naming the second file while the first is being
// removed (a writer that does not take the merge lock, the library scan) keeps it.
func TestSweepKeptFiles_RowAddedAfterTheDecisionKeepsTheFile(t *testing.T) {
	f := newKeptFixture(t)
	first := f.addKept(t, "kept once")
	second := f.addKept(t, "kept once")
	orig := removeFile
	removeFile = func(p string) error {
		err := orig(p)
		if p == first {
			f.rowOwner(t, second) // the scan imports the second file as a new book
		}
		return err
	}
	t.Cleanup(func() { removeFile = orig })

	res := sweep(t, f.store, testGuard)

	requireCounts(t, res, 1, 0, 1, 0)
	requireGone(t, first)
	requireOnDisk(t, second)
	if got := f.kept(t); !slices.Equal(got, []string{second}) {
		t.Fatalf("PurgeKept = %v, want only %s", got, second)
	}
}

// Versions the caller has just purged are not asked again.
func TestSweepKeptFiles_SkipsVersionsJustPurged(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")

	res, err := SweepKeptFiles(f.store, testGuard, KeptSweepOptions{Apply: true, Skip: map[string]struct{}{f.ver.ID: {}}})
	if err != nil {
		t.Fatal(err)
	}
	requireCounts(t, res, 0, 0, 0, 0)
	requireOnDisk(t, p)
}

// Only a purged version is swept: a version in the trash, with notes of its own
// kind, is not this sweep's.
func TestSweepKeptFiles_OnlyPurgedVersions(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	cur, _ := f.store.GetBookVersion(f.ver.ID)
	cur.Status = database.BookVersionStatusBlockedForRedownload
	if err := f.store.UpdateBookVersion(cur); err != nil {
		t.Fatal(err)
	}

	requireCounts(t, sweep(t, f.store, testGuard), 0, 0, 0, 0)
	requireOnDisk(t, p)
}

// The listing fails: that is the one error, and nothing is touched.
func TestSweepKeptFiles_ListErrorIsReturned(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	s := &failingListStore{PebbleStore: f.store}

	if _, err := SweepKeptFiles(s, testGuard, KeptSweepOptions{Apply: true}); err == nil {
		t.Fatal("SweepKeptFiles succeeded although the purged versions could not be listed")
	}
	requireOnDisk(t, p)
}

func TestSweepKeptFiles_NoGuardRemovesNothing(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")
	if _, err := SweepKeptFiles(f.store, nil, KeptSweepOptions{Apply: true}); err == nil {
		t.Fatal("SweepKeptFiles succeeded with no guard")
	}
	requireOnDisk(t, p)
}

// Many purged versions, a pool of workers, -race: every free file goes, every
// owned one stays, and no path is handled twice (the removal hook sees each once).
func TestSweepKeptFiles_ManyVersionsRunInAPool(t *testing.T) {
	old := keptSweepWorkers
	keptSweepWorkers = 4
	t.Cleanup(func() { keptSweepWorkers = old })

	base := newKeptFixture(t)
	var free, owned []string
	for i := 0; i < 24; i++ {
		ver, err := base.store.CreateBookVersion(&database.BookVersion{
			BookID: base.book.ID, Status: database.BookVersionStatusInactivePurged, Format: "m4b", Source: "imported",
		})
		if err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(VersionSlotDir(base.bookDir, ver.ID), "part.m4b")
		writeTestFile(t, p, "audio")
		ver.PurgeKept = []database.PurgeFileNote{{Path: p, Reason: "kept once"}}
		if err := base.store.UpdateBookVersion(ver); err != nil {
			t.Fatal(err)
		}
		if i%3 == 0 {
			base.rowOwner(t, p)
			owned = append(owned, p)
		} else {
			free = append(free, p)
		}
	}
	seen := map[string]int{}
	var mu sync.Mutex
	orig := removeFile
	removeFile = func(p string) error {
		mu.Lock()
		seen[p]++
		mu.Unlock()
		return orig(p)
	}
	t.Cleanup(func() { removeFile = orig })

	// The test guard counts its RootReady calls with a bare increment, which the
	// pool's workers would race on; the real guard keeps no such state.
	res := sweep(t, base.store, &lockedGuard{PurgeGuard: &fsGuard{}})
	requireCounts(t, res, len(free), 0, len(owned), 0)
	for _, p := range free {
		requireGone(t, p)
		if seen[p] != 1 {
			t.Errorf("%s was removed %d times, want once", p, seen[p])
		}
	}
	for _, p := range owned {
		requireOnDisk(t, p)
	}
}

// Two versions that note the same file are one group, so no two workers handle
// the same path; unrelated versions are separate groups.
func TestGroupBySharedPath(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared.m4b")
	mk := func(id string, paths ...string) database.BookVersion {
		v := database.BookVersion{ID: id}
		for _, p := range paths {
			v.PurgeKept = append(v.PurgeKept, database.PurgeFileNote{Path: p})
		}
		return v
	}
	cands := []database.BookVersion{
		mk("a", shared, filepath.Join(dir, "a.m4b")),
		mk("b", filepath.Join(dir, "b.m4b")),
		mk("c", filepath.Join(dir, "c.m4b"), shared),
		mk("d", filepath.Join(dir, "d.m4b")),
	}
	groups := groupBySharedPath(cands)
	if len(groups) != 3 {
		t.Fatalf("groups = %v, want 3 (a and c together)", groups)
	}
	for _, g := range groups {
		if slices.Contains(g, 0) != slices.Contains(g, 2) {
			t.Errorf("group %v splits the versions that share %s", g, shared)
		}
	}
}

// The nightly cleanup runs the sweep, and a version it purged in the same run is
// not swept (the purge has just asked).
func TestCleanup_RunsTheKeptFileSweepAfterThePurges(t *testing.T) {
	f := newKeptFixture(t)
	p := f.addKept(t, "kept once")

	res, err := cleanupTrashedVersionsAt(f.store, time.Now(), testGuard, true)
	if err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if res.KeptSweepErr != nil || res.KeptSweep.Removed != 1 || res.KeptSweep.Versions != 1 {
		t.Fatalf("KeptSweep = %+v err=%v, want one file removed", res.KeptSweep, res.KeptSweepErr)
	}
	requireGone(t, p)
}

// lockedGuard serialises RootReady, for the tests that run the pool.
type lockedGuard struct {
	PurgeGuard
	mu sync.Mutex
}

func (g *lockedGuard) RootReady() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.PurgeGuard.RootReady()
}

// ---- stores that fail one question ----

type failingOwnerStore struct {
	*database.PebbleStore
	path string
}

func (s *failingOwnerStore) BookFilesAtPath(p string) ([]database.BookFile, error) {
	if p == s.path {
		return nil, errors.New("owner lookup down")
	}
	return s.PebbleStore.BookFilesAtPath(p)
}

// failingBookRowStore hides database.BookRowPathReader: the capability lookup
// finds nothing, as on a store that does not provide it.
type failingBookRowStore struct{ *database.PebbleStore }

func (s *failingBookRowStore) BookIDsAtPath(string) ([]string, error) {
	return nil, errors.New("book row lookup down")
}

type failingBookStore struct {
	*database.PebbleStore
	id string
}

func (s *failingBookStore) GetBookByID(id string) (*database.Book, error) {
	if id == s.id {
		return nil, errors.New("book read down")
	}
	return s.PebbleStore.GetBookByID(id)
}

type failingListStore struct{ *database.PebbleStore }

func (s *failingListStore) ListPurgedBookVersions() ([]database.BookVersion, error) {
	return nil, errors.New("list down")
}

type keptWriteFailStore struct {
	*database.PebbleStore
	fail bool
}

func (s *keptWriteFailStore) UpdateBookVersion(v *database.BookVersion) error {
	if s.fail {
		return errors.New("store write down")
	}
	return s.PebbleStore.UpdateBookVersion(v)
}
