// file: internal/versions/swap_sources_test.go
// version: 1.0.0
// guid: 5a1c9e37-2d84-4b60-9f3a-8e7b1d4c6a02
// last-edited: 2026-10-28

package versions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The swap moves a file only when its source is there and its destination is
// free. These tests are the second review's blockers and the folder checks of the
// preflight: a missing alt file used to be "adopted" from a stray file with the
// same name (the source was missing and the destination present, which the
// moves took for "already moved"), the book switched to a file that was not the
// version's, and the purge deleted the real audio.

// addAltRowNoFile makes an alt version whose one file row names path in its
// slot, and writes nothing there: the file is missing.
func (f *swapFixture) addAltRowNoFile(name string) (*database.BookVersion, string) {
	f.t.Helper()
	v := f.add(database.BookVersionStatusAlt, time.Hour, "")
	path := filepath.Join(VersionSlotDir(f.bookDir, v.ID), name)
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, VersionID: v.ID, FilePath: path, Format: "m4b"}); err != nil {
		f.t.Fatal(err)
	}
	return v, path
}

// The alt's file is gone from its slot and ANOTHER book's file with the same name
// sits in the book folder (a shared folder). The swap used to call the move done,
// make the stranger's file the book's, and a later purge deleted the active
// version's audio.
func TestSwapSources_StrayRootFileIsNotAdoptedAsAMissingAlt(t *testing.T) {
	f := newSwapFixture(t)
	stray := filepath.Join(f.bookDir, "Hush.m4b")
	writeTestFile(t, stray, "another book's Hush")
	other, err := f.store.CreateBook(&database.Book{Title: "Hush", FilePath: stray, Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: other.ID, FilePath: stray, Format: "m4b"}); err != nil {
		t.Fatal(err)
	}
	alt, slotFile := f.addAltRowNoFile("Hush.m4b")

	requireRefused(t, f, alt, "is missing")

	if got := readTestFile(t, stray); got != "another book's Hush" {
		t.Errorf("the stranger's file changed: %q", got)
	}
	if got := readTestFile(t, f.rootAud); got != "active" {
		t.Errorf("the active file changed: %q", got)
	}
	if _, err := os.Lstat(slotFile); !os.IsNotExist(err) {
		t.Errorf("a file appeared at the alt's slot path: %v", err)
	}
}

// The same on the leaving side: the active file is gone and a stray file with
// its name sits in the leaving version's slot. It was taken for the active file
// already moved.
func TestSwapSources_MissingActiveFileWithAStrayInItsSlotIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	if err := os.Remove(f.rootAud); err != nil {
		t.Fatal(err)
	}
	strayInSlot := filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))
	writeTestFile(t, strayInSlot, "stray in the from slot")

	requireRefused(t, f, alt, "the active version's file")
	requireRefused(t, f, alt, "is missing")
	if got := readTestFile(t, strayInSlot); got != "stray in the from slot" {
		t.Errorf("the stray changed: %q", got)
	}
}

// The other half of the rule: the alt's file is there but a file already sits at
// its destination in the book folder, so nothing moves and nothing is replaced.
func TestSwapSources_TakenDestinationIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	stray := filepath.Join(f.bookDir, "Hush.m4b")
	writeTestFile(t, stray, "a stranger already at the destination")
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	requireRefused(t, f, alt, "already there")
	if got := readTestFile(t, stray); got != "a stranger already at the destination" {
		t.Errorf("the stranger's file changed: %q", got)
	}
}

// The folder checks: .versions and both slots are each absent or a real folder
// of the book.
func TestSwapSlots_LeavingSlotThatIsASymlinkOutIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	foreign := filepath.Join(t.TempDir(), "Blackgate")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, VersionSlotDir(f.bookDir, f.active.ID)); err != nil {
		t.Fatal(err)
	}
	requireRefused(t, f, alt, "symbolic link", foreign)
	if entries, _ := os.ReadDir(foreign); len(entries) != 0 {
		t.Errorf("the foreign folder got %d entries", len(entries))
	}
}

func TestSwapSlots_VersionsFolderThatIsASymlinkIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "Arkham", ".versions")
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "")
	writeTestFile(t, filepath.Join(elsewhere, alt.ID, "Hush.m4b"), "alt")
	if err := os.Symlink(elsewhere, VersionsDir(f.bookDir)); err != nil {
		t.Fatal(err)
	}
	row := filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b")
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, VersionID: alt.ID, FilePath: row, Format: "m4b"}); err != nil {
		t.Fatal(err)
	}
	requireRefused(t, f, alt, "symbolic link", filepath.Dir(elsewhere))
}

func TestSwapSlots_LeavingSlotThatIsAFileIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	writeTestFile(t, VersionSlotDir(f.bookDir, f.active.ID), "not a folder")
	requireRefused(t, f, alt, "is a file, not a folder")
}

// A library root reached through a symlink is not a symlinked slot: the book's
// folder is compared with its links resolved, so the whole swap through the link
// goes through.
func TestSwapSlots_BookFolderReachedThroughASymlinkedRootSwaps(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	root := filepath.Join(t.TempDir(), "library")
	if err := os.Symlink(f.bookDir, root); err != nil {
		t.Fatal(err)
	}
	via := func(p string) string {
		rel, err := filepath.Rel(f.bookDir, p)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Join(root, rel)
	}
	if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error { b.FilePath = via(f.rootAud); return nil }); err != nil {
		t.Fatal(err)
	}
	repointRow(t, f, f.active.ID, via(f.rootAud))
	repointRow(t, f, alt.ID, via(filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b")))

	got, err := f.pick()
	if err != nil || got.ID != alt.ID {
		t.Fatalf("pick = %v, %v, want %s", got, err, alt.ID)
	}
	if err := RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, filepath.Join(f.bookDir, "Hush.m4b")); got != "alt-"+alt.ID {
		t.Errorf("the entering file is not at the book folder: %q", got)
	}
}

// pruneEmptySlot removes only a real, empty folder: a regular file or a symlink
// at the slot's path is somebody's, and os.Remove would take it.
func TestPruneEmptySlot_LeavesAFileOrALinkAtTheSlotPath(t *testing.T) {
	t.Run("regular file", func(t *testing.T) {
		bookDir := t.TempDir()
		slot := VersionSlotDir(bookDir, "01DARKVICTORY")
		writeTestFile(t, slot, "not a folder")
		pruneEmptySlot(bookDir, "01DARKVICTORY")
		if got := readTestFile(t, slot); got != "not a folder" {
			t.Errorf("the file at the slot path was removed or changed: %q", got)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		bookDir := t.TempDir()
		target := filepath.Join(t.TempDir(), "Arkham")
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		slot := VersionSlotDir(bookDir, "01DARKVICTORY")
		if err := os.MkdirAll(filepath.Dir(slot), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, slot); err != nil {
			t.Fatal(err)
		}
		pruneEmptySlot(bookDir, "01DARKVICTORY")
		if fi, err := os.Lstat(slot); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the link at the slot path was removed: %v", err)
		}
	})
	t.Run("empty folder is pruned with .versions", func(t *testing.T) {
		bookDir := t.TempDir()
		slot := VersionSlotDir(bookDir, "01DARKVICTORY")
		if err := os.MkdirAll(slot, 0o755); err != nil {
			t.Fatal(err)
		}
		pruneEmptySlot(bookDir, "01DARKVICTORY")
		if _, err := os.Lstat(VersionsDir(bookDir)); !os.IsNotExist(err) {
			t.Errorf(".versions was left behind: %v", err)
		}
	})
}

// The active version's own file being a symbolic link is refused, with that
// reason: moving a link leaves its target behind and a relative link breaks when
// it moves. (Nothing else in the preflight stops it, so this test is the only
// thing between the check and a swap that moves the link.)
func TestSwapLayout_ActiveFileThatIsASymlinkRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	real := filepath.Join(t.TempDir(), "Gotham", "The Long Halloween.m4b")
	writeTestFile(t, real, "audio behind the link")
	if err := os.Remove(f.rootAud); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, f.rootAud); err != nil {
		t.Fatal(err)
	}

	_, err := f.pick()
	requireNoTarget(t, err, "the active version's file", "is a symbolic link")
	requireRefused(t, f, alt, "is a symbolic link", filepath.Dir(real))
}

// A trailing slash on the book's path names a folder: it is refused as "not a
// file", not as a stale path (filepath.Dir of it is the file's own name, so the
// "book folder" would be the audio file).
func TestSwapLayout_TrailingSlashBookPathIsNotAFile(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error { b.FilePath = f.rootAud + "/"; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := f.pick()
	requireNoTarget(t, err, "so it is not a file")
	if strings.Contains(err.Error(), "stale") {
		t.Errorf("a trailing slash is reported as stale: %v", err)
	}
	requireRefused(t, f, alt, "so it is not a file")
}
