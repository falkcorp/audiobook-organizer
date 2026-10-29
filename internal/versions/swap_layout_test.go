// file: internal/versions/swap_layout_test.go
// version: 1.1.2
// guid: ceae3827-5dd4-4742-9093-b4d214299d8a
// last-edited: 2026-10-29

package versions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// These tests pin the layout RunVersionSwap assumes (swapLayoutBlocker): each
// scenario was reproduced as a swap that either lost the only copy of a file
// (twin rows, a symlink to the active file) or failed half way with no way back
// (the same cause), or moved files into the wrong folder (a stale or empty
// FilePath, an alt outside the book's folder). Every one must now be refused by
// the picker AND by RunVersionSwap called directly, with the reason named, and
// with nothing on disk or in the store changed.

// addAltAt makes an alt version of the book whose one file row names path. With
// content, the file is written there first.
func (f *swapFixture) addAltAt(path, content string) *database.BookVersion {
	f.t.Helper()
	v, err := f.store.CreateBookVersion(&database.BookVersion{
		BookID: f.book.ID, Status: database.BookVersionStatusAlt, Format: "m4b", Source: "deluge",
		IngestDate: time.Now().Add(-time.Hour),
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if content != "" {
		writeTestFile(f.t, path, content)
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, VersionID: v.ID, FilePath: path, Format: "m4b"}); err != nil {
		f.t.Fatal(err)
	}
	return v
}

// tree describes every entry under each root (type, link target, content), so
// "nothing moved" is a comparison of two values.
func tree(t *testing.T, roots ...string) string {
	t.Helper()
	var lines []string
	for _, root := range roots {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				target, _ := os.Readlink(p)
				lines = append(lines, fmt.Sprintf("%s -> %s", p, target))
			case d.IsDir():
				lines = append(lines, p+"/")
			default:
				b, _ := os.ReadFile(p)
				lines = append(lines, fmt.Sprintf("%s = %q", p, b))
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func (f *swapFixture) versionState() string {
	f.t.Helper()
	all, err := f.store.GetBookVersionsByBookID(f.book.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var parts []string
	for _, v := range all {
		parts = append(parts, fmt.Sprintf("%s:%s:v%d", v.ID, v.Status, v.Version))
	}
	sort.Strings(parts)
	rows, _ := f.store.GetBookFiles(f.book.ID)
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("row %s=%s", r.ID, r.FilePath))
	}
	b, _ := f.store.GetBookByID(f.book.ID)
	parts = append(parts, "book="+b.FilePath)
	return strings.Join(parts, "\n")
}

// requireRefused asserts the picker names why (no target) and that
// RunVersionSwap, forced onto the same pair, refuses with ErrSwapRefused and the
// same reason, and that neither changed the disk or the store.
func requireRefused(t *testing.T, f *swapFixture, alt *database.BookVersion, why string, watch ...string) {
	t.Helper()
	roots := append([]string{f.bookDir}, watch...)
	diskBefore, stateBefore := tree(t, roots...), f.versionState()

	_, err := f.pick()
	requireNoTarget(t, err, why)

	err = RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil)
	if !errors.Is(err, ErrSwapRefused) {
		t.Fatalf("RunVersionSwap err = %v, want ErrSwapRefused", err)
	}
	if !strings.Contains(err.Error(), why) {
		t.Errorf("RunVersionSwap err %q does not name %q", err.Error(), why)
	}
	if got := tree(t, roots...); got != diskBefore {
		t.Errorf("the disk changed:\nbefore:\n%s\nafter:\n%s", diskBefore, got)
	}
	if got := f.versionState(); got != stateBefore {
		t.Errorf("the store changed:\nbefore:\n%s\nafter:\n%s", stateBefore, got)
	}
}

// Twin rows: the alt's row names the very path the active version's row names.
// The swap used to rename that file into the active version's slot, call the
// move to the root done, and leave FilePath at a path that no longer existed.
func TestSwapLayout_TwinRowRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.addAltAt(f.rootAud, "")
	requireRefused(t, f, alt, "same file")
}

// An alt whose row is a symlink, in its own slot, to the active version's file:
// the stored path looks like a slot file, only the inode gives it away.
func TestSwapLayout_SymlinkToActiveFileRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.addAltAt(filepath.Join(f.bookDir, "placeholder"), "")
	link := filepath.Join(VersionSlotDir(f.bookDir, alt.ID), filepath.Base(f.rootAud))
	if err := os.MkdirAll(filepath.Dir(link), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(f.rootAud, link); err != nil {
		t.Fatal(err)
	}
	repointRow(t, f, alt.ID, link)
	requireRefused(t, f, alt, "same file")
}

// A hard link to the active file, in the alt's slot, is the same inode under
// another name.
func TestSwapLayout_HardLinkToActiveFileRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.addAltAt(filepath.Join(f.bookDir, "placeholder"), "")
	link := filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Year One.m4b")
	if err := os.MkdirAll(filepath.Dir(link), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(f.rootAud, link); err != nil {
		t.Skipf("hard links not supported here: %v", err)
	}
	repointRow(t, f, alt.ID, link)
	requireRefused(t, f, alt, "same file")
}

// A symlink in the slot to a file elsewhere: moving it would move the link and
// leave the file behind (and a relative link would dangle).
func TestSwapLayout_SymlinkToOtherFileRefused(t *testing.T) {
	f := newSwapFixture(t)
	elsewhere := filepath.Join(t.TempDir(), "downloads", "Hush.m4b")
	writeTestFile(t, elsewhere, "download")
	alt := f.addAltAt(filepath.Join(f.bookDir, "placeholder"), "")
	link := filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b")
	if err := os.MkdirAll(filepath.Dir(link), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	repointRow(t, f, alt.ID, link)
	requireRefused(t, f, alt, "symbolic link", filepath.Dir(elsewhere))
}

// A slot directory that is itself a symlink out of the book folder: the stored
// path has the slot in it, the resolved one does not.
func TestSwapLayout_SlotThatIsASymlinkRefused(t *testing.T) {
	f := newSwapFixture(t)
	outside := filepath.Join(t.TempDir(), "downloads")
	writeTestFile(t, filepath.Join(outside, "Hush.m4b"), "download")
	alt := f.addAltAt(filepath.Join(f.bookDir, "placeholder"), "")
	slot := VersionSlotDir(f.bookDir, alt.ID)
	if err := os.MkdirAll(filepath.Dir(slot), 0o775); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, slot); err != nil {
		t.Fatal(err)
	}
	repointRow(t, f, alt.ID, filepath.Join(slot, "Hush.m4b"))
	requireRefused(t, f, alt, "not inside its own slot", outside)
}

// An alt file in a download folder, or in another book's folder, is not the
// swap's to take.
func TestSwapLayout_AltOutsideBookFolderRefused(t *testing.T) {
	f := newSwapFixture(t)
	dl := filepath.Join(t.TempDir(), "downloads", "Hush.m4b")
	alt := f.addAltAt(dl, "download")
	requireRefused(t, f, alt, "not inside its own slot", filepath.Dir(dl))

	// A copy of a slot in a folder that is not the book's: the .versions/{id}
	// pair is in the path, the folder is not the book's.
	f2 := newSwapFixture(t)
	other := t.TempDir()
	alt2 := f2.addAltAt(filepath.Join(f2.bookDir, "placeholder"), "")
	copied := filepath.Join(VersionSlotDir(other, alt2.ID), "Hush.m4b")
	writeTestFile(t, copied, "another book's copy")
	repointRow(t, f2, alt2.ID, copied)
	requireRefused(t, f2, alt2, "not inside its own slot", other)

	// A file in another version's slot of the same book.
	f3 := newSwapFixture(t)
	third := f3.add(database.BookVersionStatusTrash, 5*time.Hour, "Long Shadows.mp3")
	_ = third
	alt3 := f3.addAltAt(filepath.Join(VersionSlotDir(f3.bookDir, third.ID), "Long Shadows.mp3"), "")
	requireRefused(t, f3, alt3, "not inside its own slot")
}

func repointRow(t *testing.T, f *swapFixture, versionID, path string) {
	t.Helper()
	rows, err := f.store.GetBookFiles(f.book.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.VersionID == versionID {
			r.FilePath = path
			if err := f.store.UpdateBookFile(r.ID, &r); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no row of version %s", versionID)
}

// Book.FilePath names a folder the active file is not in (the book was moved and
// the book row was not updated). The entering file would land in the stale
// folder and FilePath would be rewritten to it.
func TestSwapLayout_StaleBookPathRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	stale := filepath.Join(t.TempDir(), "Arkham", "The Long Halloween.m4b")
	if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error { b.FilePath = stale; return nil }); err != nil {
		t.Fatal(err)
	}
	requireRefused(t, f, alt, "book path is stale", filepath.Dir(stale))
	if _, err := os.Stat(filepath.Dir(stale)); !os.IsNotExist(err) {
		t.Errorf("the stale folder was created: %v", err)
	}
}

// An empty FilePath makes the book's folder ".", the process's working
// directory; a relative one is the same trap.
func TestSwapLayout_EmptyOrRelativeBookPathRefused(t *testing.T) {
	for name, path := range map[string]string{"empty": "", "relative": "Gotham/The Long Halloween.m4b"} {
		t.Run(name, func(t *testing.T) {
			f := newSwapFixture(t)
			alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
			if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error { b.FilePath = path; return nil }); err != nil {
				t.Fatal(err)
			}
			cwd := t.TempDir()
			t.Chdir(cwd)
			why := "no file path"
			if path != "" {
				why = "not absolute"
			}
			requireRefused(t, f, alt, why, cwd)
			if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
				t.Errorf("the working directory has %d entries, want none", len(entries))
			}
		})
	}
}

// The control: the same fixture with the alt in its slot swaps, so the refusals
// above are the layout's and not the fixture's.
func TestSwapLayout_AltInItsSlotSwaps(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	if err := RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

// The step-3 failure: the leaving version's file has moved into its slot, then
// the entering file cannot be taken out of its own slot (a folder the process
// may not write to, the same shape as EXDEV or a read-only mount). The swap puts
// the leaving file back, then the statuses (and with them the active index), and
// reports the error.
func TestRunVersionSwap_Step3FailureRestoresFilesAndStatuses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	altSlot := VersionSlotDir(f.bookDir, alt.ID)
	if err := os.Chmod(altSlot, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(altSlot, 0o755) })
	diskBefore := tree(t, f.bookDir)
	activeBefore, _ := f.store.GetBookVersion(f.active.ID)

	err := RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "move-from-versions") {
		t.Fatalf("err = %v, want the step-3 move failure", err)
	}
	if errors.Is(err, ErrSwapRefused) {
		t.Errorf("a failure after the swap started is not a refusal: %v", err)
	}
	if got := tree(t, f.bookDir); got != diskBefore {
		t.Errorf("the files were not put back:\nbefore:\n%s\nafter:\n%s", diskBefore, got)
	}
	if v, _ := f.store.GetBookVersion(f.active.ID); v.Status != database.BookVersionStatusActive {
		t.Errorf("leaving version status = %s, want active again", v.Status)
	}
	if v, _ := f.store.GetBookVersion(alt.ID); v.Status != database.BookVersionStatusAlt {
		t.Errorf("entering version status = %s, want alt again", v.Status)
	}
	if got, _ := f.store.GetActiveVersionForBook(f.book.ID); got == nil || got.ID != f.active.ID {
		t.Errorf("active version = %v, want %s: the book is left without one", got, f.active.ID)
	}
	if b, _ := f.store.GetBookByID(f.book.ID); b.FilePath != f.rootAud {
		t.Errorf("book file_path = %s, want %s", b.FilePath, f.rootAud)
	}
	// The leaving version's slot was created for the move and is gone again.
	if _, err := os.Stat(VersionSlotDir(f.bookDir, f.active.ID)); !os.IsNotExist(err) {
		t.Errorf("the leaving version's empty slot was left behind: %v", err)
	}
	// And a later trash of the book is not blocked by a swap that never finished:
	// the picker still sees the alt.
	_ = activeBefore
	if err := os.Chmod(altSlot, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := f.pick(); err != nil || got.ID != alt.ID {
		t.Errorf("after the rollback pick = %v, %v, want %s", got, err, alt.ID)
	}
}

// A status write that fails between the two marks (the gap the swap used to
// leave open) puts back the one that was written.
func TestRunVersionSwap_SecondMarkFailureRestoresFirst(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	store := &failingSwapStore{PebbleStore: f.store, failVersionStatus: database.BookVersionStatusSwappingIn}

	err := RunVersionSwap(context.Background(), store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "mark to swapping_in") {
		t.Fatalf("err = %v, want the failed second mark", err)
	}
	if v, _ := f.store.GetBookVersion(f.active.ID); v.Status != database.BookVersionStatusActive {
		t.Errorf("leaving version status = %s, want active again", v.Status)
	}
	if v, _ := f.store.GetBookVersion(alt.ID); v.Status != database.BookVersionStatusAlt {
		t.Errorf("entering version status = %s, want alt", v.Status)
	}
	if got, _ := f.store.GetActiveVersionForBook(f.book.ID); got == nil || got.ID != f.active.ID {
		t.Errorf("active version = %v, want %s", got, f.active.ID)
	}
}

// When the second mark fails and putting the leaving version back fails too, the
// leaving version is left in swapping_out: that has to surface as
// ErrSwapIncomplete (the handler answers it with a 409 that says a person must
// repair it), not as a plain error whose 500 claims everything was put back.
func TestRunVersionSwap_SecondMarkFailureThenRestoreFailureIsIncomplete(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	store := &failingSwapStore{
		PebbleStore:       f.store,
		failVersionStatus: database.BookVersionStatusSwappingIn,
		failAlsoStatus:    database.BookVersionStatusActive, // the restore of the leaving version
	}

	err := RunVersionSwap(context.Background(), store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil)
	if !errors.Is(err, ErrSwapIncomplete) {
		t.Fatalf("err = %v, want ErrSwapIncomplete", err)
	}
	if !strings.Contains(err.Error(), "mark to swapping_in") {
		t.Errorf("err = %v, want the failed second mark kept in the chain", err)
	}
	if v, _ := f.store.GetBookVersion(f.active.ID); v.Status != database.BookVersionStatusSwappingOut {
		t.Errorf("leaving version status = %s, want swapping_out (it could not be put back)", v.Status)
	}
	if v, _ := f.store.GetBookVersion(alt.ID); v.Status != database.BookVersionStatusAlt {
		t.Errorf("entering version status = %s, want alt (its mark never landed)", v.Status)
	}
}

// failingSwapStore fails the writes a test names.
type failingSwapStore struct {
	*database.PebbleStore
	failVersionStatus string // UpdateBookVersion to this status fails
	failAlsoStatus    string // ... and to this one too
	failFileID        string // ModifyBookFile of this row fails
	changeRowFirst    string // ModifyBookFile of this row first has the row repointed elsewhere
	failBookWrite     bool   // ModifyBook fails
}

func (s *failingSwapStore) UpdateBookVersion(v *database.BookVersion) error {
	if (s.failVersionStatus != "" && v.Status == s.failVersionStatus) ||
		(s.failAlsoStatus != "" && v.Status == s.failAlsoStatus) {
		return errors.New("injected: version write failed")
	}
	return s.PebbleStore.UpdateBookVersion(v)
}

func (s *failingSwapStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if fileID == s.failFileID {
		return nil, errors.New("injected: book_file write failed")
	}
	if fileID == s.changeRowFirst {
		if _, err := s.PebbleStore.ModifyBookFile(bookID, fileID, func(r *database.BookFile) error {
			r.FilePath = filepath.Join(filepath.Dir(r.FilePath), "Batman Incorporated.m4b")
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return s.PebbleStore.ModifyBookFile(bookID, fileID, fn)
}

func (s *failingSwapStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.failBookWrite {
		return nil, errors.New("injected: book write failed")
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func (f *swapFixture) fileRowID(versionID string) string {
	f.t.Helper()
	rows, _ := f.store.GetBookFiles(f.book.ID)
	for _, r := range rows {
		if r.VersionID == versionID {
			return r.ID
		}
	}
	f.t.Fatalf("no row of version %s", versionID)
	return ""
}

// After the files have moved, a failed database write is an error and not a
// warning: the handler must not go on to trash a version whose swap did not
// finish. Every row is still attempted, the statuses stay swapping_*, and the
// files stay where they went.
func TestRunVersionSwap_DatabaseWriteFailuresAreErrors(t *testing.T) {
	cases := map[string]func(f *swapFixture, alt *database.BookVersion) *failingSwapStore{
		"to-file row write fails": func(f *swapFixture, alt *database.BookVersion) *failingSwapStore {
			return &failingSwapStore{PebbleStore: f.store, failFileID: f.fileRowID(alt.ID)}
		},
		"from-file row write fails": func(f *swapFixture, alt *database.BookVersion) *failingSwapStore {
			return &failingSwapStore{PebbleStore: f.store, failFileID: f.fileRowID(f.active.ID)}
		},
		"row repointed by another writer meanwhile": func(f *swapFixture, alt *database.BookVersion) *failingSwapStore {
			return &failingSwapStore{PebbleStore: f.store, changeRowFirst: f.fileRowID(alt.ID)}
		},
		"book file_path write fails": func(f *swapFixture, alt *database.BookVersion) *failingSwapStore {
			return &failingSwapStore{PebbleStore: f.store, failBookWrite: true}
		},
		"final status write fails": func(f *swapFixture, alt *database.BookVersion) *failingSwapStore {
			return &failingSwapStore{PebbleStore: f.store, failVersionStatus: database.BookVersionStatusActive}
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSwapFixture(t)
			alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
			store := mk(f, alt)
			wroteBack := false
			err := RunVersionSwap(context.Background(), store, VersionSwapParams{
				BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
			}, nil, func(string) { wroteBack = true }, nil)
			if !errors.Is(err, ErrSwapIncomplete) {
				t.Fatalf("err = %v, want ErrSwapIncomplete", err)
			}
			if wroteBack {
				t.Error("the write-back was enqueued for a swap that did not finish")
			}
			// The files moved and stayed moved.
			if _, err := os.Stat(filepath.Join(f.bookDir, "Hush.m4b")); err != nil {
				t.Errorf("the entering file is not at the book folder: %v", err)
			}
			if _, err := os.Stat(filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))); err != nil {
				t.Errorf("the leaving file is not in its slot: %v", err)
			}
			// Not finalized: a version that is not active yet must not be reported
			// as the book's.
			if v, _ := f.store.GetBookVersion(alt.ID); v.Status == database.BookVersionStatusAlt {
				t.Errorf("entering version was put back to alt although its file moved")
			}
		})
	}
}

// A row another writer repointed is reported by name, and the other rows are
// still written (all attempted, none skipped behind the first failure).
func TestRunVersionSwap_RowRepointedMeanwhileIsNamed(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	store := &failingSwapStore{PebbleStore: f.store, changeRowFirst: f.fileRowID(alt.ID)}
	err := RunVersionSwap(context.Background(), store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "the row names") {
		t.Fatalf("err = %v, want the repointed row named", err)
	}
	rows, _ := f.store.GetBookFiles(f.book.ID)
	for _, r := range rows {
		if r.VersionID == f.active.ID && !strings.Contains(r.FilePath, ".versions") {
			t.Errorf("the leaving version's row was not repointed after the other row failed: %s", r.FilePath)
		}
	}
}

// undoMoves puts back exactly the files a Move* call reported as renamed. A file
// that is at its new path but was not renamed by this call is not its to move,
// whatever the disk looks like (it used to infer what moved from the disk).
func TestUndoMoves_PutsBackOnlyWhatWasRenamed(t *testing.T) {
	dir := t.TempDir()
	oldA, newA := filepath.Join(dir, "Year One.m4b"), filepath.Join(dir, "slot", "Year One.m4b")
	oldB, newB := filepath.Join(dir, "Year Two.m4b"), filepath.Join(dir, "slot", "Year Two.m4b")
	writeTestFile(t, newA, "a")
	writeTestFile(t, newB, "b")
	if err := undoMoves([]string{oldA, oldB}, []string{newA, newB}, []bool{true, false}); err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, oldA); got != "a" {
		t.Errorf("Year One was not put back: %q", got)
	}
	if _, err := os.Lstat(newA); !os.IsNotExist(err) {
		t.Errorf("Year One is still at its new path: %v", err)
	}
	if _, err := os.Lstat(oldB); !os.IsNotExist(err) {
		t.Errorf("Year Two, not renamed by the call, was put back: %v", err)
	}
	if got := readTestFile(t, newB); got != "b" {
		t.Errorf("Year Two was touched: %q", got)
	}
}

// An original path that is taken now is an error, and what is there is not
// overwritten: the file the call moved stays where it went, so nothing is lost.
func TestUndoMoves_OccupiedOriginalIsAnErrorNotAClobber(t *testing.T) {
	dir := t.TempDir()
	oldA, newA := filepath.Join(dir, "Hush.m4b"), filepath.Join(dir, "slot", "Hush.m4b")
	writeTestFile(t, newA, "the audio the swap moved")
	writeTestFile(t, oldA, "a stranger created meanwhile")

	err := undoMoves([]string{oldA}, []string{newA}, []bool{true})
	if err == nil {
		t.Fatal("undoMoves returned nil although the original path is occupied")
	}
	if !errors.Is(err, fs.ErrExist) || !strings.Contains(err.Error(), "occupied") {
		t.Errorf("err = %v, want it to say the original path is occupied", err)
	}
	if got := readTestFile(t, oldA); got != "a stranger created meanwhile" {
		t.Errorf("the stranger's file was overwritten: %q", got)
	}
	if got := readTestFile(t, newA); got != "the audio the swap moved" {
		t.Errorf("the moved file was lost: %q", got)
	}
}

// When a move fails and the leaving version's file cannot be put back because
// something took its place meanwhile, the swap does not put the statuses back
// and does not clobber: it returns ErrSwapIncomplete with both versions left in
// swapping_*, and every file is where it is. (It used to skip the file, restore
// the statuses and leave the book playing the stranger's file.)
func TestRunVersionSwap_UndoBlockedByAStrangerIsIncomplete(t *testing.T) {
	f := newSwapFixture(t)
	// The alt's file has the active file's name, so step 3 moves it onto the
	// root path the leaving file vacated.
	alt := f.add(database.BookVersionStatusAlt, time.Hour, filepath.Base(f.rootAud))
	altFile := filepath.Join(VersionSlotDir(f.bookDir, alt.ID), filepath.Base(f.rootAud))
	slotFile := filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))

	err := RunVersionSwap(context.Background(), f.store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, func(step string, _ int) {
		if step == "fs_move_from" {
			// Between step 2 and step 3 another writer takes the vacated path.
			writeTestFile(t, f.rootAud, "a stranger created meanwhile")
		}
	}, nil, nil)
	if !errors.Is(err, ErrSwapIncomplete) {
		t.Fatalf("err = %v, want ErrSwapIncomplete", err)
	}
	if errors.Is(err, ErrSwapRefused) {
		t.Errorf("a failure after the swap started is not a refusal: %v", err)
	}
	if !strings.Contains(err.Error(), "occupied") {
		t.Errorf("err = %v, want it to name the occupied original path", err)
	}
	if got := readTestFile(t, f.rootAud); got != "a stranger created meanwhile" {
		t.Errorf("the stranger's file was overwritten: %q", got)
	}
	if got := readTestFile(t, slotFile); got != "active" {
		t.Errorf("the leaving file is not in its slot: %q", got)
	}
	if got := readTestFile(t, altFile); got != "alt-"+alt.ID {
		t.Errorf("the entering file moved although its move failed: %q", got)
	}
	if v, _ := f.store.GetBookVersion(f.active.ID); v.Status != database.BookVersionStatusSwappingOut {
		t.Errorf("leaving version status = %s, want swapping_out (not put back)", v.Status)
	}
	if v, _ := f.store.GetBookVersion(alt.ID); v.Status != database.BookVersionStatusSwappingIn {
		t.Errorf("entering version status = %s, want swapping_in (not put back)", v.Status)
	}
}

// A version that is already swapping_* is refused before anything is written:
// RunVersionSwap does not resume it (ResumeVersionSwap does) and its files may be in two places.
func TestRunVersionSwap_RefusesAVersionAlreadySwapping(t *testing.T) {
	for _, st := range []string{database.BookVersionStatusSwappingOut, database.BookVersionStatusSwappingIn} {
		for _, side := range []string{"from", "to"} {
			t.Run(st+"/"+side, func(t *testing.T) {
				f := newSwapFixture(t)
				alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
				stuck := alt
				if side == "from" {
					stuck = f.active
				}
				v, _ := f.store.GetBookVersion(stuck.ID)
				v.Status = st
				if err := f.store.UpdateBookVersion(v); err != nil {
					t.Fatal(err)
				}
				diskBefore, stateBefore := tree(t, f.bookDir), f.versionState()

				err := RunVersionSwap(context.Background(), f.store, VersionSwapParams{
					BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
				}, nil, nil, nil)
				if !errors.Is(err, ErrSwapRefused) || !strings.Contains(err.Error(), "did not finish") {
					t.Fatalf("err = %v, want a refusal saying the earlier swap did not finish", err)
				}
				if got := tree(t, f.bookDir); got != diskBefore {
					t.Errorf("the disk changed:\nbefore:\n%s\nafter:\n%s", diskBefore, got)
				}
				if got := f.versionState(); got != stateBefore {
					t.Errorf("the store changed:\nbefore:\n%s\nafter:\n%s", stateBefore, got)
				}
			})
		}
	}
}
