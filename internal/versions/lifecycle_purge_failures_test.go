// file: internal/versions/lifecycle_purge_failures_test.go
// version: 1.0.0
// guid: 3d8a1f64-27c9-4b05-9e73-a1c5e0b84f29
// last-edited: 2026-10-24

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

// liveLookupStore makes the live-book path lookup fail.
type liveLookupStore struct{ *database.PebbleStore }

func (liveLookupStore) LiveBookIDsAtPath(string) ([]string, error) {
	return nil, errors.New("injected live lookup failure")
}

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

// When the live-book lookup cannot answer, the purge fails closed like the
// row lookup does: nothing deleted, version still in the trash.
func TestPurgeVersion_FailsClosedWhenLiveLookupFails(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(liveLookupStore{f.store}, v)
	if err == nil || !strings.Contains(err.Error(), "injected live lookup failure") {
		t.Fatalf("PurgeVersion = %v, want the live lookup failure", err)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
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

// A file that cannot be removed leaves the version in the trash, so the next run
// retries it; marking it purged would orphan the file for good. The files that
// could be removed are gone (partial progress is fine and idempotent), the error
// says what failed, and once the obstacle is gone the retry completes.
func TestPurgeVersion_UnremovableFileKeepsTheVersionInTheTrash(t *testing.T) {
	f := newSharedPathFixture(t)
	dir, inner := f.stuckRow(t)

	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(f.store, v)
	if err == nil || !strings.Contains(err.Error(), "could not be removed") {
		t.Fatalf("PurgeVersion = %v, want a could-not-be-removed error", err)
	}
	requireTrashed(t, f)
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
// version is retried by the next one.
func TestCleanupTrashedVersions_UnremovableFileIsAFailedRun(t *testing.T) {
	f := newSharedPathFixture(t)
	_, inner := f.stuckRow(t)

	purged, failed, err := cleanupTrashedVersionsAt(f.store, time.Now())
	if purged != 0 || failed != 1 || err == nil {
		t.Fatalf("purged=%d failed=%d err=%v, want 0, 1, an error", purged, failed, err)
	}
	requireTrashed(t, f)

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
