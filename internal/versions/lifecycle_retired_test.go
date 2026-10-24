// file: internal/versions/lifecycle_retired_test.go
// version: 1.0.0
// guid: 0f4b7d29-6a1c-4e83-9d52-c8e1a7b30f64
// last-edited: 2026-10-23

package versions

import (
	"errors"
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
			err := PurgeVersion(f.store, f.reread(t))
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
	if err := PurgeVersion(tf.store, tf.reread(t)); err != nil {
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

// The trash cleanup never lists or purges a retired version, however old: a
// trashed alt is purged, the retired version beside it keeps its slot, its
// files and its status. A retired version forced into the cleanup's re-read
// path is refused by PurgeVersion itself.
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

	if n := cleanupTrashedVersionsAt(f.store, time.Now()); n != 1 {
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
