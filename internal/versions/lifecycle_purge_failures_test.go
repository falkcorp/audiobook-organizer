// file: internal/versions/lifecycle_purge_failures_test.go
// version: 1.2.0
// guid: 3d8a1f64-27c9-4b05-9e73-a1c5e0b84f29
// last-edited: 2026-10-27

package versions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// bookRowLookupStore makes the all-rows book path lookup
// (database.BookRowPathReader, which PurgeVersion resolves with AsCapability)
// fail.
type bookRowLookupStore struct{ *database.PebbleStore }

func (bookRowLookupStore) BookIDsAtPath(string) ([]string, error) {
	return nil, errors.New("injected book lookup failure")
}

// noBookRowLookupStore hides the all-rows book path lookup: it embeds the
// database.Store INTERFACE, which does not carry BookIDsAtPath (it is a
// capability, not a Store method), and has no Unwrap, so AsCapability cannot
// reach the concrete store behind it. This is a store that cannot answer "does
// any book, a trashed one included, name this path?".
type noBookRowLookupStore struct{ database.Store }

// The lifecycle refusal of a retired version must not send the user to a
// control that does not work yet.
func TestRefuseRetired_DoesNotPointAtTheDisabledRestore(t *testing.T) {
	err := refuseRetired(&database.BookVersion{ID: "v1", Status: database.BookVersionStatusRetired, RetiredFromBookID: "b9"}, "purge")
	if !errors.Is(err, ErrRetiredVersion) {
		t.Fatalf("refuseRetired = %v, want ErrRetiredVersion", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "separate book") {
		t.Errorf("message %q points at the separate-book restore, which is not available yet", err)
	}
}

func requireTrashed(t *testing.T, f *sharedPathFixture) {
	t.Helper()
	got, err := f.store.GetBookVersion(f.trashed.ID)
	if err != nil || got == nil {
		t.Fatalf("reread version: %v", err)
	}
	if got.Status != database.BookVersionStatusTrash || got.PurgedDate != nil {
		t.Errorf("status=%s PurgedDate=%v, want the version left in the trash", got.Status, got.PurgedDate)
	}
}

// requirePurging asserts the version holds the purging claim: a purge that
// started and did not finish.
func requirePurging(t *testing.T, f *sharedPathFixture) {
	t.Helper()
	got, err := f.store.GetBookVersion(f.trashed.ID)
	if err != nil || got == nil {
		t.Fatalf("reread version: %v", err)
	}
	if got.Status != database.BookVersionStatusPurging || got.PurgedDate != nil {
		t.Errorf("status=%s PurgedDate=%v, want the version left purging", got.Status, got.PurgedDate)
	}
}

// A live book can name a file with no book_file row at all (its own FilePath).
// PurgeVersion keeps such a file, like the book purge does, and still removes
// the files nothing names: ownOnly is the control that the call deletes.
func TestPurgeVersion_KeepsAFileALiveBookNames(t *testing.T) {
	f := newSharedPathFixture(t)
	if _, err := f.store.CreateBook(&database.Book{Title: "Names The File", FilePath: f.unrowed}); err != nil {
		t.Fatal(err)
	}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, f.unrowed)
	requireGone(t, f.ownOnly)
}

// The version's own book naming its file is not another owner: the file is the
// version's, and the version is what is being purged (the live-book check
// would otherwise keep every file the book's FilePath happens to be).
func TestPurgeVersion_TheVersionsOwnBookIsNotAnotherOwner(t *testing.T) {
	f := newSharedPathFixture(t)
	holder, err := f.store.GetBookByID(f.holderID)
	if err != nil || holder == nil {
		t.Fatalf("holder: %v", err)
	}
	holder.FilePath = f.ownOnly
	if _, err := f.store.UpdateBook(f.holderID, holder); err != nil {
		t.Fatal(err)
	}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireGone(t, f.ownOnly)
}

// A TRASHED book (soft-deleted, restorable) that names a file keeps it, exactly
// as a live one does: the live-only lookup this replaced let the purge delete
// the audio a restore would have wanted. The file nothing names is the control.
func TestPurgeVersion_KeepsAFileATrashedBookNames(t *testing.T) {
	f := newSharedPathFixture(t)
	yes := true
	if _, err := f.store.CreateBook(&database.Book{
		Title: "Batman: Hush", FilePath: f.unrowed, MarkedForDeletion: &yes,
	}); err != nil {
		t.Fatal(err)
	}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, f.unrowed)
	requireGone(t, f.ownOnly)
}

// The same holds when the trashed book names a path the version's OWN row names
// (the version's file, shared with a trashed book): own row plus a trashed
// book's FilePath keeps it, and the nightly cleanup agrees.
func TestCleanupTrashedVersions_KeepsAFileATrashedBookNames(t *testing.T) {
	f := newSharedPathFixture(t)
	yes := true
	if _, err := f.store.CreateBook(&database.Book{
		Title: "Batman: The Long Halloween", FilePath: f.ownOnly, MarkedForDeletion: &yes,
	}); err != nil {
		t.Fatal(err)
	}
	if n, _, _ := cleanupTrashedVersionsAt(f.store, time.Now()); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	requireOnDisk(t, f.ownOnly)
	requireGone(t, f.unrowed)
}

// A retired row is a holder too (BookFilesAtPath keeps retired rows): a path
// only a retired row names, outside the version's own rows and slot, is kept.
// Pinned here next to the trashed-book case so the three holders (live book,
// trashed book, retired row) each have a test that fails if its lookup is
// dropped. The version's own row names the path so it is a purge target.
func TestPurgeVersion_KeepsAFileOnlyARetiredRowOutsideTheSlotNames(t *testing.T) {
	f := newSharedPathFixture(t)
	extra := filepath.Join(t.TempDir(), "gotham_extra.m4b")
	if err := os.WriteFile(extra, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	shell, err := f.store.CreateBook(&database.Book{Title: "Shell Of Dick Grayson", FilePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{
		BookID: f.holderID, FilePath: extra, VersionID: f.trashed.ID, TrackNumber: 20,
	}); err != nil {
		t.Fatal(err)
	}
	row := &database.BookFile{BookID: shell.ID, FilePath: extra, TrackNumber: 1}
	if err := f.store.CreateBookFile(row); err != nil {
		t.Fatal(err)
	}
	retiredAt := time.Now()
	retiredVer, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holderID, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-2", RetiredAt: &retiredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, f.holderID, retiredVer.ID); err != nil {
		t.Fatal(err)
	}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, extra)
	requireGone(t, f.ownOnly)
}

// When the book lookup cannot answer, the purge fails closed like the row
// lookup does: nothing deleted, version still in the trash.
func TestPurgeVersion_FailsClosedWhenBookLookupFails(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(bookRowLookupStore{f.store}, v)
	if err == nil || !strings.Contains(err.Error(), "injected book lookup failure") {
		t.Fatalf("PurgeVersion = %v, want the book lookup failure", err)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
	requireTrashed(t, f)
}

// A store that does not provide the all-rows book lookup at all (capability
// absent) deletes nothing, on the direct purge and on the nightly cleanup, and
// leaves the version in the trash: absent is never read as "no book names it".
func TestPurgeVersion_FailsClosedWhenBookLookupCapabilityAbsent(t *testing.T) {
	f := newSharedPathFixture(t)
	store := noBookRowLookupStore{f.store}
	if _, ok := database.AsCapability[database.BookRowPathReader](store); ok {
		t.Fatal("test store unexpectedly provides BookRowPathReader")
	}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(store, v)
	if err == nil || !strings.Contains(err.Error(), "BookRowPathReader") {
		t.Fatalf("PurgeVersion = %v, want the missing-capability error", err)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
	requireTrashed(t, f)

	if n, failed, cerr := cleanupTrashedVersionsAt(store, time.Now()); n != 0 || failed != 1 || cerr == nil {
		t.Errorf("cleanup = purged %d failed %d err %v, want 0 purged, 1 failed, an error", n, failed, cerr)
	}
	for _, p := range []string{f.ownOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
	requireTrashed(t, f)
}

// stuckRow gives the trashed version one more own row, naming a directory that
// still holds a file: os.Remove refuses it (not-empty, not not-exist) whether or
// not the test runs as root.
func (f *sharedPathFixture) stuckRow(t *testing.T) (dir, inner string) {
	t.Helper()
	dir = filepath.Join(t.TempDir(), "stuck")
	inner = filepath.Join(dir, "inner.m4b")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inner, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{
		BookID: f.holderID, FilePath: dir, VersionID: f.trashed.ID, TrackNumber: 9,
	}); err != nil {
		t.Fatal(err)
	}
	return dir, inner
}

// A file that cannot be removed leaves the version purging, so the next run
// retries it; marking it purged would orphan the file for good, and leaving it
// in the trash would let it be restored with files missing. The files that could
// be removed are gone (partial progress is fine and idempotent), the error says
// what failed, and once the obstacle is gone the retry completes.
func TestPurgeVersion_UnremovableFileLeavesTheVersionPurging(t *testing.T) {
	f := newSharedPathFixture(t)
	dir, inner := f.stuckRow(t)

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v)
	if err == nil || !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("PurgeVersion = %v, want a could-not-be-removed error", err)
	}
	requirePurging(t, f)
	requireGone(t, f.ownOnly)
	requireOnDisk(t, inner)

	if err := os.Remove(inner); err != nil {
		t.Fatal(err)
	}
	v, _ = f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err != nil {
		t.Fatalf("retry after the obstacle is gone: %v", err)
	}
	requireGone(t, dir)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("status = %s after the retry, want inactive_purged", got.Status)
	}
}

// A file that is already gone counts as removed: a version whose file vanished
// (a crashed earlier attempt, someone's cleanup) must still become purged, not
// fail on every nightly run.
func TestPurgeVersion_AMissingFileCountsAsRemoved(t *testing.T) {
	f := newSharedPathFixture(t)
	if err := os.Remove(f.ownOnly); err != nil {
		t.Fatal(err)
	}
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err != nil {
		t.Fatalf("PurgeVersion with a file already gone: %v", err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("status = %s, want inactive_purged", got.Status)
	}
}

// The nightly cleanup reports a run whose due purge failed: the failed count,
// and an error that wraps the cause so the op can fail on it. The same cleanup
// on a healthy store reports a clean run, and a run that fails and is retried
// after the obstacle clears purges the version.
func TestCleanupTrashedVersions_ReportsFailedPurges(t *testing.T) {
	f := newSharedPathFixture(t)

	purged, failed, err := cleanupTrashedVersionsAt(pathLookupStore{f.store}, time.Now())
	if purged != 0 || failed != 1 {
		t.Fatalf("purged=%d failed=%d, want 0 and 1", purged, failed)
	}
	if !errors.Is(err, database.ErrBookFilesAtPathUnavailable) {
		t.Fatalf("err = %v, want it to wrap ErrBookFilesAtPathUnavailable", err)
	}
	if !strings.Contains(err.Error(), "1 of 1") {
		t.Errorf("err = %q, want the failed count and the total", err)
	}
	requireTrashed(t, f)

	purged, failed, err = cleanupTrashedVersionsAt(f.store, time.Now())
	if purged != 1 || failed != 0 || err != nil {
		t.Fatalf("healthy run: purged=%d failed=%d err=%v, want 1, 0, nil", purged, failed, err)
	}
}

// A file that cannot be removed fails the cleanup run the same way, and the
// version, now purging, is retried by every later run until it finishes: it
// cannot stay stuck, and it cannot be restored meanwhile.
func TestCleanupTrashedVersions_UnremovableFileIsAFailedRun(t *testing.T) {
	f := newSharedPathFixture(t)
	_, inner := f.stuckRow(t)

	purged, failed, err := cleanupTrashedVersionsAt(f.store, time.Now())
	if purged != 0 || failed != 1 || err == nil {
		t.Fatalf("purged=%d failed=%d err=%v, want 0, 1, an error", purged, failed, err)
	}
	requirePurging(t, f)

	// The next run retries it (the version is purging, not trash) and fails the
	// same way while the obstacle stands.
	purged, failed, err = cleanupTrashedVersionsAt(f.store, time.Now())
	if purged != 0 || failed != 1 || err == nil {
		t.Fatalf("second run: purged=%d failed=%d err=%v, want 0, 1, an error", purged, failed, err)
	}
	requirePurging(t, f)

	if err := os.Remove(inner); err != nil {
		t.Fatal(err)
	}
	purged, failed, err = cleanupTrashedVersionsAt(f.store, time.Now())
	if purged != 1 || failed != 0 || err != nil {
		t.Fatalf("retry: purged=%d failed=%d err=%v, want 1, 0, nil", purged, failed, err)
	}
}

// A stamp that cannot be written is a failure too: the legacy row is neither
// purged nor silently ignored by the run.
func TestCleanupTrashedVersions_StampFailureIsAFailedRun(t *testing.T) {
	f := newTrashFixture(t, time.Now().Add(-90*day), database.BookVersionStatusTrash)
	purged, failed, err := cleanupTrashedVersionsAt(failingUpdateStore{f.store}, time.Now().Add(365*day))
	if purged != 0 || failed != 1 || err == nil {
		t.Fatalf("purged=%d failed=%d err=%v, want 0, 1, an error", purged, failed, err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("stamp version %s", f.ver.ID)) {
		t.Errorf("err = %q, want it to name the version whose stamp failed", err)
	}
}

// A purging version is retried at once, whatever its age: a purge-now of a
// version trashed minutes ago that failed part way is finished by the nightly
// run, not left until a 14-day window nobody started counting.
func TestCleanupTrashedVersions_RetriesAPurgingVersionWithoutAWindow(t *testing.T) {
	f := newSharedPathFixture(t)
	_, inner := f.stuckRow(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err == nil {
		t.Fatal("PurgeVersion succeeded with an unremovable file")
	}
	requirePurging(t, f)
	if err := os.Remove(inner); err != nil {
		t.Fatal(err)
	}

	// A clock far BEFORE the original window: only the purging status can make
	// the run pick it up.
	purged, failed, err := cleanupTrashedVersionsAt(f.store, time.Now().Add(-365*day))
	if purged != 1 || failed != 0 || err != nil {
		t.Fatalf("purged=%d failed=%d err=%v, want 1, 0, nil", purged, failed, err)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged || got.PurgedDate == nil {
		t.Errorf("status=%s PurgedDate=%v, want inactive_purged", got.Status, got.PurgedDate)
	}
}

// A version whose purge started cannot be restored: some of its files are gone,
// and an alt made of it would name them. MarkRestored refuses it, and so does
// the store for a caller that read the version before the claim (the stale
// write is the race the cleanup's comment describes).
func TestPurgingVersion_CannotBeRestored(t *testing.T) {
	f := newSharedPathFixture(t)
	f.stuckRow(t)
	stale, _ := f.store.GetBookVersion(f.trashed.ID) // read before the claim
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err == nil {
		t.Fatal("PurgeVersion succeeded with an unremovable file")
	}

	reread, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := MarkRestored(reread); !errors.Is(err, database.ErrVersionPurging) {
		t.Fatalf("MarkRestored(purging) = %v, want ErrVersionPurging", err)
	}
	if reread.Status != database.BookVersionStatusPurging {
		t.Errorf("MarkRestored changed the caller's copy to %s", reread.Status)
	}
	if err := MarkTrashed(reread, time.Now()); !errors.Is(err, database.ErrVersionPurging) {
		t.Fatalf("MarkTrashed(purging) = %v, want ErrVersionPurging", err)
	}

	// The stale copy still says trash: restoring it writes alt over purging.
	if err := MarkRestored(stale); err != nil {
		t.Fatalf("MarkRestored(stale trash copy): %v", err)
	}
	if err := f.store.UpdateBookVersion(stale); !errors.Is(err, database.ErrVersionPurging) {
		t.Fatalf("stale restore write = %v, want ErrVersionPurging", err)
	}
	requirePurging(t, f)
}

// The race the cleanup's comment names: a restore that lands after the
// cleanup's re-read and before PurgeVersion. The claim is refused, nothing is
// deleted, the run does not count it as a failure, and the restore stands.
type restoreAfterReadStore struct {
	*database.PebbleStore
	t *testing.T
}

func (s restoreAfterReadStore) GetBookVersion(id string) (*database.BookVersion, error) {
	v, err := s.PebbleStore.GetBookVersion(id)
	if err != nil || v == nil || v.Status != database.BookVersionStatusTrash {
		return v, err
	}
	// The user restores it right after the cleanup read it as trash.
	restored := *v
	if err := MarkRestored(&restored); err != nil {
		s.t.Fatal(err)
	}
	if err := s.PebbleStore.UpdateBookVersion(&restored); err != nil {
		s.t.Fatal(err)
	}
	return v, nil
}

func TestCleanupTrashedVersions_RestoreBetweenReReadAndClaimWins(t *testing.T) {
	f := newSharedPathFixture(t)

	purged, failed, err := cleanupTrashedVersionsAt(restoreAfterReadStore{f.store, t}, time.Now())
	if purged != 0 || failed != 0 || err != nil {
		t.Fatalf("purged=%d failed=%d err=%v, want 0, 0, nil: a restore that won is not a failure", purged, failed, err)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusAlt {
		t.Errorf("status = %s, want alt (the restore stands)", got.Status)
	}
}

// PurgeVersion called directly with a copy that went stale reports
// ErrVersionNotInTrash, the error purge-now maps to 409.
func TestPurgeVersion_RestoredSinceReadIsNotInTrash(t *testing.T) {
	f := newSharedPathFixture(t)
	stale, _ := f.store.GetBookVersion(f.trashed.ID)
	restored, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := MarkRestored(restored); err != nil {
		t.Fatal(err)
	}
	if err := f.store.UpdateBookVersion(restored); err != nil {
		t.Fatal(err)
	}

	err := PurgeVersion(f.store, stale)
	if !errors.Is(err, ErrVersionNotInTrash) {
		t.Fatalf("PurgeVersion(stale) = %v, want ErrVersionNotInTrash", err)
	}
	if stale.Status != database.BookVersionStatusTrash {
		t.Errorf("the caller's copy was left as %s, want it put back to trash", stale.Status)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
}

// orphanedVersion is a trashed version, due for purge, whose book has been
// deleted. The book never had book_file rows: DeleteBook refuses while it owns
// any, which is why a version of a deleted book has nothing on disk to remove.
func orphanedVersion(t *testing.T) (*database.PebbleStore, *database.BookVersion) {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.WaitForWarmup()
	book, err := store.CreateBook(&database.Book{Title: "Gone Book", FilePath: filepath.Join(t.TempDir(), "gone.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-40 * day)
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: book.ID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &stamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteBook(book.ID); err != nil {
		t.Fatalf("DeleteBook: %v", err)
	}
	if gone, _ := store.GetBookByID(book.ID); gone != nil {
		t.Fatal("the book is still there")
	}
	return store, ver
}

// A version whose book is gone: nothing of it can be on disk, so it becomes
// purged and the call succeeds instead of failing "book not found" every night.
func TestPurgeVersion_BookGoneMarksThePurgedWithoutFailing(t *testing.T) {
	store, ver := orphanedVersion(t)
	if err := PurgeVersion(store, ver); err != nil {
		t.Fatalf("PurgeVersion of a version whose book is gone: %v", err)
	}
	got, _ := store.GetBookVersion(ver.ID)
	if got.Status != database.BookVersionStatusInactivePurged || got.PurgedDate == nil {
		t.Errorf("status=%s PurgedDate=%v, want inactive_purged", got.Status, got.PurgedDate)
	}
}

func TestCleanupTrashedVersions_BookGoneDoesNotFailTheRun(t *testing.T) {
	store, ver := orphanedVersion(t)
	purged, failed, err := cleanupTrashedVersionsAt(store, time.Now())
	if purged != 1 || failed != 0 || err != nil {
		t.Fatalf("purged=%d failed=%d err=%v, want 1, 0, nil", purged, failed, err)
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("status = %s, want inactive_purged", got.Status)
	}
}

// A version of a deleted book that was RESTORED since the caller read it is not
// marked purged: the orphan path claims the version first, like any purge.
func TestPurgeVersion_BookGoneButRestoredIsNotMarkedPurged(t *testing.T) {
	store, ver := orphanedVersion(t)
	restored, _ := store.GetBookVersion(ver.ID)
	if err := MarkRestored(restored); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateBookVersion(restored); err != nil {
		t.Fatal(err)
	}
	if err := PurgeVersion(store, ver); !errors.Is(err, ErrVersionNotInTrash) {
		t.Fatalf("PurgeVersion = %v, want ErrVersionNotInTrash", err)
	}
	if got, _ := store.GetBookVersion(ver.ID); got.Status != database.BookVersionStatusAlt {
		t.Errorf("status = %s, want alt", got.Status)
	}
}

// bookReadFailsStore makes the version's book unreadable.
type bookReadFailsStore struct{ *database.PebbleStore }

func (bookReadFailsStore) GetBookByID(string) (*database.Book, error) {
	return nil, errors.New("injected book read failure")
}

// A book that cannot be READ is not a book that is gone: the purge fails with
// the cause (not "not found"), and the version is left untouched.
func TestPurgeVersion_BookReadErrorIsAFailureNotAnOrphan(t *testing.T) {
	stamp := time.Now().Add(-40 * day)
	f := newTrashFixtureStamped(t, time.Now().Add(-90*day), database.BookVersionStatusTrash, &stamp)
	err := PurgeVersion(bookReadFailsStore{f.store}, f.reread(t))
	if err == nil || !strings.Contains(err.Error(), "injected book read failure") {
		t.Fatalf("PurgeVersion = %v, want the read failure named", err)
	}
	if strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q calls a read failure \"not found\"", err)
	}
	if got := f.reread(t); got.Status != database.BookVersionStatusTrash || got.PurgedDate != nil {
		t.Errorf("status=%s PurgedDate=%v, want the version untouched in the trash", got.Status, got.PurgedDate)
	}
}

// Every failing version is named, and every cause is reachable with errors.Is,
// so one stuck version cannot hide a new failure behind it.
func TestCleanupTrashedVersions_NamesEveryFailedVersion(t *testing.T) {
	now := time.Now()
	stamp1 := now.Add(-40 * day)
	f1 := newTrashFixtureStamped(t, now.Add(-90*day), database.BookVersionStatusTrash, &stamp1)
	// A second due version in the same store, whose book is unreadable.
	other, err := f1.store.CreateBook(&database.Book{Title: "Second Holder", FilePath: filepath.Join(t.TempDir(), "x.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	stamp := now.Add(-40 * day)
	v2, err := f1.store.CreateBookVersion(&database.BookVersion{
		BookID: other.ID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &stamp,
	})
	if err != nil {
		t.Fatal(err)
	}
	st := failingBooksStore{PebbleStore: f1.store, failIDs: map[string]bool{f1.ver.BookID: true, other.ID: true}}

	purged, failed, err := cleanupTrashedVersionsAt(st, now)
	if purged != 0 || failed != 2 || err == nil {
		t.Fatalf("purged=%d failed=%d err=%v, want 0, 2, an error", purged, failed, err)
	}
	for _, id := range []string{f1.ver.ID, v2.ID} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("err %q does not name failed version %s", err, id)
		}
	}
	if !errors.Is(err, errBookRead) {
		t.Errorf("err does not wrap the cause: %v", err)
	}
}

var errBookRead = errors.New("injected book read failure")

// failingBooksStore fails GetBookByID for the listed book ids only.
type failingBooksStore struct {
	*database.PebbleStore
	failIDs map[string]bool
}

func (s failingBooksStore) GetBookByID(id string) (*database.Book, error) {
	if s.failIDs[id] {
		return nil, errBookRead
	}
	return s.PebbleStore.GetBookByID(id)
}

// The version's own book naming a file that ANOTHER version of the same book
// also has a row for: the file is kept. otherRowNamingPath is what keeps it
// (the other version's row is not "an own row of the version being purged"); the
// own-book exclusion on the live-book check must not let it through. This is
// the control that fails if that exclusion is ever widened to the whole book.
func TestPurgeVersion_BookFilePathAndAnotherVersionRowBothNameTheFile(t *testing.T) {
	f := newSharedPathFixture(t)
	shared := filepath.Join(t.TempDir(), "shared_with_other_version.m4b")
	if err := os.WriteFile(shared, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	holder, err := f.store.GetBookByID(f.holderID)
	if err != nil || holder == nil {
		t.Fatalf("holder: %v", err)
	}
	holder.FilePath = shared // the book's own FilePath names it ...
	if _, err := f.store.UpdateBook(f.holderID, holder); err != nil {
		t.Fatal(err)
	}
	other, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holderID, Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ version string }{{other.ID}, {f.trashed.ID}} { // ... and so do both versions' rows
		if err := f.store.CreateBookFile(&database.BookFile{
			BookID: f.holderID, FilePath: shared, VersionID: row.version, TrackNumber: 7,
		}); err != nil {
			t.Fatal(err)
		}
	}

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	if err := PurgeVersion(f.store, v); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, shared)
	requireGone(t, f.ownOnly)
}
