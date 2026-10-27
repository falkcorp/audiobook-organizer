// file: internal/versions/lifecycle_retired_test.go
// version: 1.1.2
// guid: 0f4b7d29-6a1c-4e83-9d52-c8e1a7b30f64
// last-edited: 2026-10-27

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

// retiredFixture is a live holder book whose directory carries the files of a
// retired version: the retired row is a real file in a temp dir, re-keyed under
// the holder by RetireBookFiles, and the version also has a .versions slot with
// a file in it (the place PurgeVersion deletes from first).
type retiredFixture struct {
	store    *database.PebbleStore
	bookDir  string
	holderID string
	shellID  string
	ver      *database.BookVersion
	rowFile  string // the retired row's file on disk
	slotFile string // a file in the retired version's .versions slot
	rowID    string
}

// newRetiredFixture builds the fixture. withRow=false leaves the version with no
// retired row at all: the case the store's own refusal does not cover.
func newRetiredFixture(t *testing.T, withRow bool) *retiredFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.WaitForWarmup() // BookFilesAtPath (PurgeVersion's guard) needs the memdb

	bookDir := t.TempDir()
	holder, err := store.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(bookDir, "book.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	shellDir := t.TempDir()
	shell, err := store.CreateBook(&database.Book{Title: "Shell", FilePath: shellDir})
	if err != nil {
		t.Fatal(err)
	}
	retiredAt := time.Now()
	ver, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: &retiredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &retiredFixture{store: store, bookDir: bookDir, holderID: holder.ID, shellID: shell.ID, ver: ver}

	slot := VersionSlotDir(bookDir, ver.ID)
	if err := os.MkdirAll(slot, 0o755); err != nil {
		t.Fatal(err)
	}
	f.slotFile = filepath.Join(slot, "part1.mp3")
	if err := os.WriteFile(f.slotFile, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	if withRow {
		f.rowFile = filepath.Join(shellDir, "01.mp3")
		if err := os.WriteFile(f.rowFile, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		row := &database.BookFile{BookID: shell.ID, FilePath: f.rowFile}
		if err := store.CreateBookFile(row); err != nil {
			t.Fatal(err)
		}
		f.rowID = row.ID
		if err := store.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{row.ID}, SourceBookID: shell.ID}}, holder.ID, ver.ID); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *retiredFixture) reread(t *testing.T) *database.BookVersion {
	t.Helper()
	v, err := f.store.GetBookVersion(f.ver.ID)
	if err != nil || v == nil {
		t.Fatalf("reread version: %v (nil=%v)", err, v == nil)
	}
	return v
}

func requireOnDisk(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s is gone: %v", path, err)
	}
}

// MarkTrashed refuses a retired version and leaves the caller's copy alone; the
// control, an alt version, goes to the trash with a stamp.
func TestMarkTrashed_RefusesRetired(t *testing.T) {
	for _, withRow := range []bool{true, false} {
		name := "with a retired row"
		if !withRow {
			name = "without any retired row"
		}
		t.Run(name, func(t *testing.T) {
			f := newRetiredFixture(t, withRow)
			v := f.reread(t)
			err := MarkTrashed(v, time.Now())
			if !errors.Is(err, ErrRetiredVersion) {
				t.Fatalf("MarkTrashed on a retired version = %v, want ErrRetiredVersion", err)
			}
			if v.Status != database.BookVersionStatusRetired || v.TrashedAt != nil {
				t.Fatalf("status=%s TrashedAt=%v, want the retired version untouched", v.Status, v.TrashedAt)
			}
		})
	}

	control := &database.BookVersion{ID: "v-alt", Status: database.BookVersionStatusAlt}
	if err := MarkTrashed(control, time.Now()); err != nil {
		t.Fatalf("control: trashing an alt version failed: %v", err)
	}
	if control.Status != database.BookVersionStatusTrash || control.TrashedAt == nil {
		t.Fatalf("control: status=%s TrashedAt=%v, want trash with a stamp", control.Status, control.TrashedAt)
	}
}

// MarkRestored refuses a retired version, which must never become an alt; the
// control, a trashed version, becomes an alt.
func TestMarkRestored_RefusesRetired(t *testing.T) {
	f := newRetiredFixture(t, false)
	v := f.reread(t)
	if err := MarkRestored(v); !errors.Is(err, ErrRetiredVersion) {
		t.Fatalf("MarkRestored on a retired version = %v, want ErrRetiredVersion", err)
	}
	if v.Status != database.BookVersionStatusRetired {
		t.Fatalf("status = %s, want retired", v.Status)
	}

	stamp := time.Now()
	control := &database.BookVersion{ID: "v-trash", Status: database.BookVersionStatusTrash, TrashedAt: &stamp}
	if err := MarkRestored(control); err != nil {
		t.Fatalf("control: restoring a trashed version failed: %v", err)
	}
	if control.Status != database.BookVersionStatusAlt || control.TrashedAt != nil {
		t.Fatalf("control: status=%s TrashedAt=%v, want alt with no stamp", control.Status, control.TrashedAt)
	}
}

// PurgeVersion refuses a retired version BEFORE it touches the disk: the slot
// directory, the slot's file, the retired row's own file and the status all
// survive. The control, the same shape of version in the trash, is purged: its
// slot is removed, which shows the checks above can fail.
func TestPurgeVersion_RefusesRetiredBeforeTouchingDisk(t *testing.T) {
	for _, withRow := range []bool{true, false} {
		name := "with a retired row"
		if !withRow {
			name = "without any retired row"
		}
		t.Run(name, func(t *testing.T) {
			f := newRetiredFixture(t, withRow)
			err := PurgeVersion(f.store, f.reread(t), testGuard)
			if !errors.Is(err, ErrRetiredVersion) {
				t.Fatalf("PurgeVersion on a retired version = %v, want ErrRetiredVersion", err)
			}
			requireOnDisk(t, f.slotFile)
			if withRow {
				requireOnDisk(t, f.rowFile)
			}
			got := f.reread(t)
			if got.Status != database.BookVersionStatusRetired || got.PurgedDate != nil {
				t.Fatalf("status=%s PurgedDate=%v, want the retired version untouched", got.Status, got.PurgedDate)
			}
		})
	}

	// Control: a trashed version with a slot is purged and its slot removed.
	tf := newTrashFixture(t, time.Now().Add(-30*day), database.BookVersionStatusTrash)
	if err := PurgeVersion(tf.store, tf.reread(t), testGuard); err != nil {
		t.Fatalf("control: purging a trashed version failed: %v", err)
	}
	tf.requireFile(t, false)
}

// AutoPromoteAlt never promotes a retired version, even one newer than every
// alt: the alt wins and the retired version is not rewritten. With only a
// retired version there is nothing to promote and nothing is written.
func TestAutoPromoteAlt_IgnoresRetired(t *testing.T) {
	f := newRetiredFixture(t, true)
	retiredBefore := f.reread(t)

	alt, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holderID, Status: database.BookVersionStatusAlt, Format: "m4b", Source: "imported",
		IngestDate: retiredBefore.IngestDate.Add(-48 * time.Hour), // older than the retired version
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retiredBefore.IngestDate.After(alt.IngestDate) {
		t.Fatalf("fixture: the retired version (%v) must be newer than the alt (%v)", retiredBefore.IngestDate, alt.IngestDate)
	}

	if err := AutoPromoteAlt(f.store, f.holderID); err != nil {
		t.Fatalf("AutoPromoteAlt: %v", err)
	}
	gotAlt, _ := f.store.GetBookVersion(alt.ID)
	if gotAlt.Status != database.BookVersionStatusActive {
		t.Errorf("alt status = %s, want active (the control: the same call promotes a real alt)", gotAlt.Status)
	}
	gotRetired := f.reread(t)
	if gotRetired.Status != database.BookVersionStatusRetired {
		t.Errorf("retired version status = %s, want retired", gotRetired.Status)
	}
	if gotRetired.Version != retiredBefore.Version {
		t.Errorf("retired version was rewritten (row version %d -> %d)", retiredBefore.Version, gotRetired.Version)
	}

	// Only a retired version on the book: nothing to promote, nothing written.
	only := newRetiredFixture(t, true)
	before := only.reread(t)
	if err := AutoPromoteAlt(only.store, only.holderID); err != nil {
		t.Fatalf("AutoPromoteAlt with only a retired version: %v", err)
	}
	after := only.reread(t)
	if after.Status != database.BookVersionStatusRetired || after.Version != before.Version {
		t.Errorf("retired-only book: status=%s row version %d -> %d, want untouched", after.Status, before.Version, after.Version)
	}
	if active, _ := only.store.GetActiveVersionForBook(only.holderID); active != nil {
		t.Errorf("retired-only book got an active version %s", active.ID)
	}
}

// A retired version named by a stale trash listing is never purged, however
// old the listing says it is: a trashed alt is purged, the retired version beside
// it keeps its slot, its files and its status. The store never lists a retired
// version as trashed, so the listing is replaced with one that does name it,
// stamped as a trashed version past its window. What the test pins is that the
// cleanup acts on the re-read of the real row and never on the listing entry
// (it fails if the cleanup trusts the snapshot). The refusal inside PurgeVersion
// is a further guard the cleanup cannot reach while the re-read holds; it is
// covered directly by TestPurgeVersion_RefusesRetiredBeforeTouchingDisk.
func TestCleanupTrashedVersions_NeverPurgesRetired(t *testing.T) {
	f := newRetiredFixture(t, true)
	trashedAt := time.Now().Add(-40 * day)
	trashed, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.holderID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported",
		TrashedAt: &trashedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	trashedSlot := VersionSlotDir(f.bookDir, trashed.ID)
	if err := os.MkdirAll(trashedSlot, 0o755); err != nil {
		t.Fatal(err)
	}
	trashedFile := filepath.Join(trashedSlot, "part1.m4b")
	if err := os.WriteFile(trashedFile, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	listed, err := f.store.ListTrashedBookVersions()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range listed {
		if v.ID == f.ver.ID {
			t.Fatalf("the trash listing contains the retired version %s", v.ID)
		}
	}

	forged := f.reread(t)
	forged.Status = database.BookVersionStatusTrash
	forged.TrashedAt = &trashedAt
	snapshot := append(listed, *forged)

	if n, _, _ := cleanupAt(staleListStore{f.store, snapshot}, time.Now()); n != 1 {
		t.Fatalf("purged %d, want 1 (the trashed alt only)", n)
	}
	if _, err := os.Stat(trashedFile); !os.IsNotExist(err) {
		t.Errorf("control: the trashed version's file is still on disk (stat err %v)", err)
	}
	requireOnDisk(t, f.slotFile)
	requireOnDisk(t, f.rowFile)
	if got := f.reread(t); got.Status != database.BookVersionStatusRetired {
		t.Errorf("retired version status = %s, want retired", got.Status)
	}
}

// sharedPathFixture is a holder with a TRASHED version whose purge is due, and a
// retired version (holding a shell's rows) that names some of the same files:
//
//	ownOnly     own row of the trashed version, in its slot  -> removed
//	slotShared  own row + a retired row, in its slot         -> kept
//	rowShared   own row + a retired row, outside the slot    -> kept
//	retiredOnly retired row only, in the slot, no own row    -> kept (the old
//	            RemoveAll of the slot would have taken it)
//	unrowed     in the slot, no row of any kind              -> removed
type sharedPathFixture struct {
	store     *database.PebbleStore
	holderID  string
	trashed   *database.BookVersion
	ownOnly   string
	slotShare string
	rowShared string
	retOnly   string
	unrowed   string
}

func newSharedPathFixture(t *testing.T) *sharedPathFixture {
	t.Helper()
	store, err := database.NewPebbleStoreInMemory(filepath.Join(t.TempDir(), "pebble"))
	if err != nil {
		t.Fatalf("open pebble: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	store.WaitForWarmup() // BookFilesAtPath (PurgeVersion's guard) needs the memdb

	bookDir := t.TempDir()
	holder, err := store.CreateBook(&database.Book{Title: "Holder", FilePath: filepath.Join(bookDir, "book.m4b")})
	if err != nil {
		t.Fatal(err)
	}
	shell, err := store.CreateBook(&database.Book{Title: "Shell", FilePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	trashedAt := time.Now().Add(-40 * day)
	trashed, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusTrash, Format: "m4b", Source: "imported", TrashedAt: &trashedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	retiredAt := time.Now()
	retired, err := store.CreateBookVersion(&database.BookVersion{
		BookID: holder.ID, Status: database.BookVersionStatusRetired, Format: "mp3", Source: "retired_book",
		RetiredFromBookID: shell.ID, RetireOpID: "op-1", RetiredAt: &retiredAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	slot := VersionSlotDir(bookDir, trashed.ID)
	outside := t.TempDir()
	f := &sharedPathFixture{
		store: store, holderID: holder.ID, trashed: trashed,
		ownOnly:   filepath.Join(slot, "own_only.m4b"),
		slotShare: filepath.Join(slot, "slot_shared.m4b"),
		rowShared: filepath.Join(outside, "row_shared.m4b"),
		retOnly:   filepath.Join(slot, "retired_only.m4b"),
		unrowed:   filepath.Join(slot, "unrowed.m4b"),
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, p := range []string{f.ownOnly, f.slotShare, f.rowShared} {
		if err := store.CreateBookFile(&database.BookFile{
			BookID: holder.ID, FilePath: p, VersionID: trashed.ID, TrackNumber: i + 1,
		}); err != nil {
			t.Fatalf("own row %s: %v", p, err)
		}
	}
	var ids []string
	for i, p := range []string{f.slotShare, f.rowShared, f.retOnly} {
		row := &database.BookFile{BookID: shell.ID, FilePath: p, TrackNumber: i + 1}
		if err := store.CreateBookFile(row); err != nil {
			t.Fatalf("shell row %s: %v", p, err)
		}
		ids = append(ids, row.ID)
	}
	if err := store.RetireBookFiles([]database.BookFileMove{{FileIDs: ids, SourceBookID: shell.ID}}, holder.ID, retired.ID); err != nil {
		t.Fatal(err)
	}
	return f
}

func requireGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("%s is still on disk (stat err %v)", path, err)
	}
}

// Purging a trashed version removes only the files no other row names. A file
// a retired row still names survives, whether the version's own row shares it
// or it merely sits in the version's slot; the files nothing else names go.
// The removed files are the control: the same call does delete.
func TestPurgeVersion_KeepsFilesARetiredRowStillNames(t *testing.T) {
	f := newSharedPathFixture(t)
	v, err := f.store.GetBookVersion(f.trashed.ID)
	if err != nil || v == nil {
		t.Fatalf("reread: %v", err)
	}
	if err := PurgeVersion(f.store, v, testGuard); err != nil {
		t.Fatalf("PurgeVersion: %v", err)
	}
	requireOnDisk(t, f.slotShare)
	requireOnDisk(t, f.rowShared)
	requireOnDisk(t, f.retOnly)
	requireGone(t, f.ownOnly)
	requireGone(t, f.unrowed)
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusInactivePurged {
		t.Errorf("status = %s, want inactive_purged", got.Status)
	}
	// The retired rows are untouched.
	retired, err := f.store.GetRetiredBookFiles(f.holderID)
	if err != nil || len(retired) != 3 {
		t.Fatalf("retired rows = %d (%v), want 3", len(retired), err)
	}
}

// The same guard holds on the nightly cleanup path.
func TestCleanupTrashedVersions_KeepsFilesARetiredRowStillNames(t *testing.T) {
	f := newSharedPathFixture(t)
	if n, _, _ := cleanupAt(f.store, time.Now()); n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	requireOnDisk(t, f.slotShare)
	requireOnDisk(t, f.rowShared)
	requireOnDisk(t, f.retOnly)
	requireGone(t, f.ownOnly)
}

// pathLookupStore makes the path lookup fail the way a store without memdb does.
type pathLookupStore struct{ *database.PebbleStore }

func (pathLookupStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	return nil, fmt.Errorf("%w: injected", database.ErrBookFilesAtPathUnavailable)
}

// When it cannot tell who else names a path, PurgeVersion fails closed: it
// returns the error, deletes nothing (not even the files nothing shares) and
// leaves the version in the trash for the next run.
func TestPurgeVersion_FailsClosedWhenPathOwnersUnknown(t *testing.T) {
	f := newSharedPathFixture(t)
	v, _ := f.store.GetBookVersion(f.trashed.ID)
	err := PurgeVersion(pathLookupStore{f.store}, v, testGuard)
	if !errors.Is(err, database.ErrBookFilesAtPathUnavailable) {
		t.Fatalf("PurgeVersion = %v, want ErrBookFilesAtPathUnavailable", err)
	}
	for _, p := range []string{f.ownOnly, f.slotShare, f.rowShared, f.retOnly, f.unrowed} {
		requireOnDisk(t, p)
	}
	got, _ := f.store.GetBookVersion(f.trashed.ID)
	if got.Status != database.BookVersionStatusTrash || got.PurgedDate != nil {
		t.Errorf("status=%s PurgedDate=%v, want the version left in the trash", got.Status, got.PurgedDate)
	}
	if n, _, _ := cleanupAt(pathLookupStore{f.store}, time.Now()); n != 0 {
		t.Errorf("cleanup purged %d with the lookup down, want 0", n)
	}
	requireOnDisk(t, f.ownOnly)
}
