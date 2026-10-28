// file: internal/versions/trash_window_test.go
// version: 1.3.4
// guid: 3e6f1b8a-52c4-4d97-a0e3-9b7c2f4d81a6
// last-edited: 2026-10-28

package versions

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The 14-day trash window counts from when a version entered the trash
// (TrashedAt). Until 2026-10-11 CleanupTrashedVersions counted it from
// CreatedAt, so a version older than 14 days was purged -- its files deleted
// from disk -- at the first daily run after it was trashed. These tests run
// on a real Pebble store with real files and assert on the file, not just the
// purge count.

const day = 24 * time.Hour

type trashFixture struct {
	store    *database.PebbleStore
	bookDir  string
	versionF string // the version's audio file inside .versions/{vid}/
	ver      *database.BookVersion
}

// newTrashFixture builds a book with one alt version whose single file lives
// in its .versions/{vid}/ slot and is linked by a book_file row.
func newTrashFixture(t *testing.T, createdAt time.Time, status string) *trashFixture {
	t.Helper()
	return newTrashFixtureStamped(t, createdAt, status, nil)
}

// newTrashFixtureStamped is newTrashFixture with a caller-chosen TrashedAt.
// The store never moves the stamp of a row that stays in the trash, so a past
// stamp can only arrive the way a real one does: on a row created in the
// trash (CreateBookVersion keeps a trash-status caller stamp). trashedAt is
// ignored for any other status.
func newTrashFixtureStamped(t *testing.T, createdAt time.Time, status string, trashedAt *time.Time) *trashFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.WaitForWarmup() // BookFilesAtPath (PurgeVersion's guard) needs the memdb

	bookDir := t.TempDir()
	if _, err := store.CreateBook(&database.Book{ID: "b1", Title: "Trash Window Book", FilePath: filepath.Join(bookDir, "book.m4b")}); err != nil {
		t.Fatalf("create book: %v", err)
	}
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: "b1", Status: status, Format: "m4b", Source: "imported",
		CreatedAt: createdAt, IngestDate: createdAt, TrashedAt: trashedAt,
	})
	if err != nil {
		t.Fatalf("create version: %v", err)
	}
	slot := VersionSlotDir(bookDir, ver.ID)
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	versionF := filepath.Join(slot, "part1.m4b")
	if err := os.WriteFile(versionF, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateBookFile(&database.BookFile{
		BookID: "b1", FilePath: versionF, VersionID: ver.ID, TrackNumber: 1,
	}); err != nil {
		t.Fatalf("create book file: %v", err)
	}
	return &trashFixture{store: store, bookDir: bookDir, versionF: versionF, ver: ver}
}

func (f *trashFixture) reread(t *testing.T) *database.BookVersion {
	t.Helper()
	v, err := f.store.GetBookVersion(f.ver.ID)
	if err != nil || v == nil {
		t.Fatalf("reread version: %v (nil=%v)", err, v == nil)
	}
	return v
}

// trash runs the handler's path into the trash: MarkTrashed, then persist.
func (f *trashFixture) trash(t *testing.T, now time.Time) {
	t.Helper()
	v := f.reread(t)
	if err := MarkTrashed(v, now); err != nil {
		t.Fatalf("mark trashed: %v", err)
	}
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatalf("trash: %v", err)
	}
}

func (f *trashFixture) requireFile(t *testing.T, wantPresent bool) {
	t.Helper()
	_, err := os.Stat(f.versionF)
	switch {
	case wantPresent && err != nil:
		t.Fatalf("version file is gone: %v", err)
	case !wantPresent && !os.IsNotExist(err):
		t.Fatalf("version file still on disk (stat err %v)", err)
	}
}

func (f *trashFixture) requireBookFileRowKept(t *testing.T) {
	t.Helper()
	files, err := f.store.GetBookFiles("b1")
	if err != nil {
		t.Fatalf("get book files: %v", err)
	}
	if len(files) != 1 || files[0].VersionID != f.ver.ID {
		t.Fatalf("book_file rows = %+v, want the one row for version %s: purge must never delete book_file rows", files, f.ver.ID)
	}
}

// The reviewer's repro: a 30-day-old version trashed now, one cleanup run.
// Before the fix: purged=1, file gone.
func TestCleanupTrashed_OldVersionTrashedNowIsKept(t *testing.T) {
	f := newTrashFixture(t, time.Now().Add(-30*day), database.BookVersionStatusAlt)
	f.trash(t, time.Now())

	if n, _, _ := cleanupNow(f.store); n != 0 {
		t.Fatalf("purged %d, want 0: a version trashed seconds ago was purged because it was created 30 days ago", n)
	}
	f.requireFile(t, true)
	if got := f.reread(t); got.Status != database.BookVersionStatusTrash {
		t.Fatalf("status = %s, want trash", got.Status)
	}
}

func TestCleanupTrashed_WindowCountsFromTrashedAt(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name        string
		trashedAgo  time.Duration
		wantPurged  int
		wantFileOnD bool
	}{
		{"trashed 1 day ago", 1 * day, 0, true},
		{"trashed 13 days ago", 13 * day, 0, true},
		{"trashed 14 days less a minute ago", 14*day - time.Minute, 0, true},
		{"trashed exactly 14 days ago", 14 * day, 1, false},
		{"trashed 15 days ago", 15 * day, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Created yesterday: an old CreatedAt must not be what purges it,
			// and a new one must not be what saves it.
			trashedAt := now.Add(-tc.trashedAgo)
			f := newTrashFixtureStamped(t, now.Add(-1*day), database.BookVersionStatusTrash, &trashedAt)

			if n, _, _ := cleanupAt(f.store, now); n != tc.wantPurged {
				t.Fatalf("purged %d, want %d", n, tc.wantPurged)
			}
			f.requireFile(t, tc.wantFileOnD)
			f.requireBookFileRowKept(t)
			got := f.reread(t)
			if tc.wantPurged == 1 {
				if got.Status != database.BookVersionStatusInactivePurged || got.PurgedDate == nil {
					t.Fatalf("status=%s purged_date=%v, want inactive_purged with a date", got.Status, got.PurgedDate)
				}
				if got.TrashedAt != nil {
					t.Fatalf("purged version kept TrashedAt %v", got.TrashedAt)
				}
			} else if got.Status != database.BookVersionStatusTrash {
				t.Fatalf("status = %s, want trash", got.Status)
			}
		})
	}
}

// A version trashed before TrashedAt existed decodes with nil. It must be
// stamped and kept, then purged a full window after the stamp -- not on the
// first run after deploy.
func TestCleanupTrashed_LegacyRowIsStampedNotPurged(t *testing.T) {
	now := time.Now()
	// Created straight into the trash with no stamp: what a pre-fix row looks like.
	f := newTrashFixture(t, now.Add(-90*day), database.BookVersionStatusTrash)
	if f.reread(t).TrashedAt != nil {
		t.Fatal("fixture: legacy trashed row should have no TrashedAt")
	}

	if n, _, _ := cleanupAt(f.store, now); n != 0 {
		t.Fatalf("first run purged %d legacy version(s), want 0", n)
	}
	f.requireFile(t, true)
	got := f.reread(t)
	if got.Status != database.BookVersionStatusTrash {
		t.Fatalf("status = %s, want trash", got.Status)
	}
	if got.TrashedAt == nil || !got.TrashedAt.Equal(now) {
		t.Fatalf("TrashedAt = %v, want stamped with the run time %v", got.TrashedAt, now)
	}

	// Still inside the window counted from the stamp.
	if n, _, _ := cleanupAt(f.store, now.Add(13*day)); n != 0 {
		t.Fatalf("run at stamp+13d purged %d, want 0", n)
	}
	if got := f.reread(t); !got.TrashedAt.Equal(now) {
		t.Fatalf("second run moved the stamp to %v", got.TrashedAt)
	}
	f.requireFile(t, true)

	// Past it.
	if n, _, _ := cleanupAt(f.store, now.Add(14*day+time.Minute)); n != 1 {
		t.Fatalf("run at stamp+14d purged %d, want 1", n)
	}
	f.requireFile(t, false)
	f.requireBookFileRowKept(t)
}

// failingUpdateStore makes every UpdateBookVersion fail.
type failingUpdateStore struct{ *database.PebbleStore }

func (failingUpdateStore) UpdateBookVersion(*database.BookVersion) error {
	return errors.New("injected write failure")
}

// If the stamp cannot be written the version is still not purged.
func TestCleanupTrashed_LegacyRowStampFailureNeverPurges(t *testing.T) {
	f := newTrashFixture(t, time.Now().Add(-90*day), database.BookVersionStatusTrash)

	if n, _, _ := cleanupAt(failingUpdateStore{f.store}, time.Now().Add(365*day)); n != 0 {
		t.Fatalf("purged %d, want 0", n)
	}
	f.requireFile(t, true)
	if got := f.reread(t); got.TrashedAt != nil || got.Status != database.BookVersionStatusTrash {
		t.Fatalf("status=%s TrashedAt=%v, want trash with no stamp", got.Status, got.TrashedAt)
	}
}

// staleListStore returns a trash listing captured earlier, standing in for a
// restore that lands between the cleanup's list and its purge.
type staleListStore struct {
	*database.PebbleStore
	snapshot []database.BookVersion
}

func (s staleListStore) ListTrashedBookVersions() ([]database.BookVersion, error) {
	return s.snapshot, nil
}

func TestCleanupTrashed_RestoreAfterListingWins(t *testing.T) {
	now := time.Now()
	trashedAt := now.Add(-30 * day)
	f := newTrashFixtureStamped(t, now.Add(-30*day), database.BookVersionStatusTrash, &trashedAt)
	snapshot, err := f.store.ListTrashedBookVersions()
	if err != nil || len(snapshot) != 1 {
		t.Fatalf("list trashed: %v (%d)", err, len(snapshot))
	}

	v := f.reread(t)
	if err := MarkRestored(v); err != nil {
		t.Fatalf("mark restored: %v", err)
	}
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatalf("restore: %v", err)
	}

	if n, _, _ := cleanupAt(staleListStore{f.store, snapshot}, now); n != 0 {
		t.Fatalf("purged %d restored version(s), want 0", n)
	}
	f.requireFile(t, true)
	if got := f.reread(t); got.Status != database.BookVersionStatusAlt || got.TrashedAt != nil {
		t.Fatalf("status=%s TrashedAt=%v, want alt with no stamp", got.Status, got.TrashedAt)
	}
}

// Restore clears the stamp, and trashing again starts a fresh window -- even
// when the caller hands the store a stale stamp.
func TestTrashedAt_RestoreClearsAndRetrashRestamps(t *testing.T) {
	now := time.Now()
	trashedAt := now.Add(-20 * day)
	f := newTrashFixtureStamped(t, now.Add(-30*day), database.BookVersionStatusTrash, &trashedAt)

	v := f.reread(t)
	if err := MarkRestored(v); err != nil {
		t.Fatalf("mark restored: %v", err)
	}
	if v.TrashedAt != nil {
		t.Fatal("MarkRestored left TrashedAt set on the caller's copy")
	}
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := f.reread(t); got.TrashedAt != nil || got.Status != database.BookVersionStatusAlt {
		t.Fatalf("after restore: status=%s TrashedAt=%v, want alt with no stamp", got.Status, got.TrashedAt)
	}

	// Re-trash through a path that carries the old 20-day-old stamp along.
	stale := now.Add(-20 * day)
	v = f.reread(t)
	v.Status = database.BookVersionStatusTrash
	v.TrashedAt = &stale
	before := time.Now()
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatalf("re-trash: %v", err)
	}
	got := f.reread(t)
	if got.TrashedAt == nil || got.TrashedAt.Before(before) {
		t.Fatalf("re-trash kept stale stamp %v; entering the trash must stamp now", got.TrashedAt)
	}
	if n, _, _ := cleanupNow(f.store); n != 0 {
		t.Fatalf("purged %d re-trashed version(s), want 0", n)
	}
	f.requireFile(t, true)
}

// While a row stays in the trash the store never moves its stamp, in either
// direction. The race: a writer loads a trashed row (stamp T1, 20 days old);
// the user restores it and trashes it again (stamp T2 = now); the stale writer
// then writes its copy back, still trash + T1. The store used to take the
// caller's stamp, so the next cleanup purged a version trashed minutes ago.
func TestTrashedAt_StaleWriteWhileStillTrashedKeepsLaterStamp(t *testing.T) {
	now := time.Now()
	t1 := now.Add(-20 * day)
	f := newTrashFixtureStamped(t, now.Add(-30*day), database.BookVersionStatusTrash, &t1)

	stale := f.reread(t) // trash + T1
	if stale.TrashedAt == nil || !stale.TrashedAt.Equal(t1) {
		t.Fatalf("fixture: stale copy TrashedAt = %v, want %v", stale.TrashedAt, t1)
	}

	restored := f.reread(t)
	if err := MarkRestored(restored); err != nil {
		t.Fatalf("mark restored: %v", err)
	}
	if err := f.store.UpdateBookVersion(restored); err != nil {
		t.Fatalf("restore: %v", err)
	}
	f.trash(t, now) // T2
	t2 := f.reread(t).TrashedAt
	if t2 == nil || !t2.After(t1) {
		t.Fatalf("fixture: re-trash stamp %v, want after %v", t2, t1)
	}

	// The stale writer lands.
	if err := f.store.UpdateBookVersion(stale); err != nil {
		t.Fatalf("stale write: %v", err)
	}
	got := f.reread(t)
	if got.Status != database.BookVersionStatusTrash || got.TrashedAt == nil || !got.TrashedAt.Equal(*t2) {
		t.Fatalf("status=%s TrashedAt=%v, want trash with the later stamp %v", got.Status, got.TrashedAt, *t2)
	}
	if n, _, _ := cleanupAt(f.store, now); n != 0 {
		t.Fatalf("purged %d version(s), want 0: a stale write moved the stamp back and the cleanup purged a version trashed just now", n)
	}
	f.requireFile(t, true)
}

// The stored stamp wins over a newer caller stamp too: staying in the trash
// does not restart the window. Only a nil stored stamp (a legacy row) accepts
// the caller's.
func TestTrashedAt_StaysPutWhileTrashed(t *testing.T) {
	now := time.Now()
	orig := now.Add(-5 * day)
	f := newTrashFixtureStamped(t, now.Add(-30*day), database.BookVersionStatusTrash, &orig)

	v := f.reread(t)
	newer := now
	v.TrashedAt = &newer
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	if got := f.reread(t); got.TrashedAt == nil || !got.TrashedAt.Equal(orig) {
		t.Fatalf("TrashedAt = %v after a newer caller stamp, want unchanged %v", got.TrashedAt, orig)
	}

	v = f.reread(t)
	v.TrashedAt = nil
	if err := f.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	if got := f.reread(t); got.TrashedAt == nil || !got.TrashedAt.Equal(orig) {
		t.Fatalf("TrashedAt = %v after a nil caller stamp, want unchanged %v", got.TrashedAt, orig)
	}

	// Legacy row: stored nil, so the caller's stamp is accepted.
	legacy := newTrashFixture(t, now.Add(-30*day), database.BookVersionStatusTrash)
	v = legacy.reread(t)
	v.TrashedAt = &newer
	if err := legacy.store.UpdateBookVersion(v); err != nil {
		t.Fatal(err)
	}
	if got := legacy.reread(t); got.TrashedAt == nil || !got.TrashedAt.Equal(newer) {
		t.Fatalf("legacy TrashedAt = %v, want the caller's %v", got.TrashedAt, newer)
	}
}

// Every way into the trash stamps TrashedAt and every way out clears it.
//
// Entry paths found (2026-10-11): the trash handler
// (internal/server/version_lifecycle.go handleTrashVersion, via MarkTrashed),
// covered end to end by TestHandleTrashVersion_StampsTrashedAt. No other code
// sets BookVersionStatusTrash: swap moves versions between active, alt and
// swapping_*; AutoPromoteAlt only promotes alt to active; ingest creates
// active or alt. The store stamps any write that enters the trash, so the
// rows below cover a direct UpdateBookVersion from every other status too --
// a future path that forgets MarkTrashed is still stamped.
func TestTrashedAt_EveryPathIn_StampsEveryPathOut_Clears(t *testing.T) {
	stale := time.Now().Add(-40 * day)
	type entry struct {
		name  string
		from  string
		enter func(v *database.BookVersion)
	}
	entries := []entry{
		{"handler MarkTrashed from active", database.BookVersionStatusActive, func(v *database.BookVersion) { _ = MarkTrashed(v, time.Now()) }},
		{"handler MarkTrashed from alt", database.BookVersionStatusAlt, func(v *database.BookVersion) { _ = MarkTrashed(v, time.Now()) }},
	}
	for _, from := range []string{
		database.BookVersionStatusActive,
		database.BookVersionStatusAlt,
		database.BookVersionStatusPending,
		database.BookVersionStatusSwappingIn,
		database.BookVersionStatusSwappingOut,
		// inactive_purged is not here: its files are gone and the store refuses to
		// put it back in the trash (database.ErrVersionPurged;
		// TestMarkTrashed_RefusesAPurgedVersionAndSoDoesTheStore).
		database.BookVersionStatusBlockedForRedownload,
	} {
		entries = append(entries,
			entry{"raw status write from " + from, from, func(v *database.BookVersion) {
				v.Status = database.BookVersionStatusTrash
			}},
			entry{"raw status write with stale stamp from " + from, from, func(v *database.BookVersion) {
				v.Status = database.BookVersionStatusTrash
				v.TrashedAt = &stale
			}},
		)
	}
	for _, tc := range entries {
		t.Run("in/"+tc.name, func(t *testing.T) {
			f := newTrashFixture(t, time.Now().Add(-60*day), tc.from)
			v := f.reread(t)
			before := time.Now()
			tc.enter(v)
			if err := f.store.UpdateBookVersion(v); err != nil {
				t.Fatalf("enter trash: %v", err)
			}
			after := time.Now()
			got := f.reread(t)
			if got.TrashedAt == nil || got.TrashedAt.Before(before) || got.TrashedAt.After(after) {
				t.Fatalf("TrashedAt = %v, want within [%v, %v]", got.TrashedAt, before, after)
			}
			if n, _, _ := cleanupNow(f.store); n != 0 {
				t.Fatalf("cleanup purged %d freshly trashed version(s)", n)
			}
			f.requireFile(t, true)
		})
	}

	exits := []struct {
		name string
		exit func(t *testing.T, f *trashFixture)
	}{
		{"restore (MarkRestored)", func(t *testing.T, f *trashFixture) {
			v := f.reread(t)
			if err := MarkRestored(v); err != nil {
				t.Fatal(err)
			}
			if err := f.store.UpdateBookVersion(v); err != nil {
				t.Fatal(err)
			}
		}},
		{"raw status write to alt, stamp left on the struct", func(t *testing.T, f *trashFixture) {
			v := f.reread(t)
			v.Status = database.BookVersionStatusAlt
			if err := f.store.UpdateBookVersion(v); err != nil {
				t.Fatal(err)
			}
		}},
		{"swap pulls it in (swapping_in)", func(t *testing.T, f *trashFixture) {
			v := f.reread(t)
			v.Status = database.BookVersionStatusSwappingIn
			if err := f.store.UpdateBookVersion(v); err != nil {
				t.Fatal(err)
			}
		}},
		{"purge (PurgeVersion)", func(t *testing.T, f *trashFixture) {
			if err := PurgeVersion(f.store, f.reread(t), testGuard); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range exits {
		t.Run("out/"+tc.name, func(t *testing.T) {
			f := newTrashFixture(t, time.Now().Add(-60*day), database.BookVersionStatusAlt)
			f.trash(t, time.Now())
			if f.reread(t).TrashedAt == nil {
				t.Fatal("fixture: trash did not stamp")
			}
			tc.exit(t, f)
			if got := f.reread(t); got.TrashedAt != nil {
				t.Fatalf("status=%s still has TrashedAt %v", got.Status, got.TrashedAt)
			}
		})
	}
}

// A repeated trash request keeps the original stamp: it neither restarts nor
// shortens the window.
func TestMarkTrashed_AlreadyTrashedKeepsStamp(t *testing.T) {
	orig := time.Now().Add(-5 * day)
	v := &database.BookVersion{Status: database.BookVersionStatusTrash, TrashedAt: &orig}
	if err := MarkTrashed(v, time.Now()); err != nil {
		t.Fatal(err)
	}
	if !v.TrashedAt.Equal(orig) {
		t.Fatalf("TrashedAt = %v, want kept at %v", v.TrashedAt, orig)
	}

	// A legacy trashed row with no stamp gets one.
	legacy := &database.BookVersion{Status: database.BookVersionStatusTrash}
	now := time.Now()
	if err := MarkTrashed(legacy, now); err != nil {
		t.Fatal(err)
	}
	if legacy.TrashedAt == nil || !legacy.TrashedAt.Equal(now) {
		t.Fatalf("legacy TrashedAt = %v, want %v", legacy.TrashedAt, now)
	}
}
