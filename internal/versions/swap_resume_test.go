// file: internal/versions/swap_resume_test.go
// version: 1.1.0
// guid: 3b7f0c5e-9a14-4d82-8e63-5c2a1f6d9b70
// last-edited: 2026-10-29

package versions

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/filehash"
)

// faultStore is the real store with writes that fail on demand, so a real
// RunVersionSwap is left in each of the half-done states a resume has to read.
type faultStore struct {
	*database.PebbleStore
	failFileRow   string            // ModifyBookFile of this row fails
	failBook      bool              // ModifyBook fails
	failStatusFor map[string]string // version id -> status whose UpdateBookVersion fails
	failedWrites  int
	// failMoveRecord: RecordPathChange of a resume's per-file move fails.
	failMoveRecord bool
	// failPathLookup: BookFilesAtPath cannot be completed.
	failPathLookup bool
	// beforeRowWrite, when set, runs once before the first ModifyBookFile of the
	// row it names: another writer getting in between the plan and the write.
	beforeRowWrite func()
	beforeRowID    string
}

func (s *faultStore) RecordPathChange(c *database.BookPathChange) error {
	if s.failMoveRecord && c.ChangeType == swapResumeMoveChangeType {
		s.failedWrites++
		return errors.New("injected: path change record failed")
	}
	return s.PebbleStore.RecordPathChange(c)
}

func (s *faultStore) BookFilesAtPath(path string) ([]database.BookFile, error) {
	if s.failPathLookup {
		return nil, database.ErrBookFilesAtPathUnavailable
	}
	return s.PebbleStore.BookFilesAtPath(path)
}

func (s *faultStore) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	if s.beforeRowWrite != nil && fileID == s.beforeRowID {
		hook := s.beforeRowWrite
		s.beforeRowWrite = nil
		hook()
	}
	if s.failFileRow != "" && fileID == s.failFileRow {
		s.failedWrites++
		return nil, errors.New("injected: book_file write failed")
	}
	return s.PebbleStore.ModifyBookFile(bookID, fileID, fn)
}

func (s *faultStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.failBook {
		s.failedWrites++
		return nil, errors.New("injected: book write failed")
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

func (s *faultStore) UpdateBookVersion(v *database.BookVersion) error {
	if want, ok := s.failStatusFor[v.ID]; ok && want == v.Status {
		s.failedWrites++
		return errors.New("injected: version status write failed")
	}
	return s.PebbleStore.UpdateBookVersion(v)
}

func (f *swapFixture) faults() *faultStore { return &faultStore{PebbleStore: f.store} }

// rowOf is the file row of the version's first file.
func (f *swapFixture) rowOf(versionID string) database.BookFile {
	f.t.Helper()
	rows, err := f.store.GetBookFiles(f.book.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, r := range rows {
		if r.VersionID == versionID {
			return r
		}
	}
	f.t.Fatalf("no file row for version %s", versionID)
	return database.BookFile{}
}

func (f *swapFixture) status(id string) string {
	f.t.Helper()
	v, err := f.store.GetBookVersion(id)
	if err != nil || v == nil {
		f.t.Fatalf("get version %s: %v", id, err)
	}
	return v.Status
}

func (f *swapFixture) bookPath() string {
	f.t.Helper()
	b, err := f.store.GetBookByID(f.book.ID)
	if err != nil || b == nil {
		f.t.Fatalf("get book: %v", err)
	}
	return b.FilePath
}

// swapWith runs the real swap of the fixture's active version for alt through
// store and requires it to end ErrSwapIncomplete.
func (f *swapFixture) swapWith(store swapStore, alt *database.BookVersion) {
	f.t.Helper()
	err := RunVersionSwap(context.Background(), store, VersionSwapParams{
		BookID: f.book.ID, FromVersionID: f.active.ID, ToVersionID: alt.ID,
	}, nil, nil, nil)
	if !errors.Is(err, ErrSwapIncomplete) {
		f.t.Fatalf("swap err = %v, want ErrSwapIncomplete", err)
	}
}

func (f *swapFixture) resume() (*SwapResumeResult, error) {
	return ResumeVersionSwap(context.Background(), f.store, f.book.ID)
}

func (f *swapFixture) mustResume(action string) *SwapResumeResult {
	f.t.Helper()
	res, err := f.resume()
	if err != nil {
		f.t.Fatalf("resume: %v", err)
	}
	if res.Action != action {
		f.t.Fatalf("resume action = %s, want %s", res.Action, action)
	}
	return res
}

// requireRefusedResume asserts the resume refuses with a reason naming why and
// changes neither the disk nor the store.
func (f *swapFixture) requireRefusedResume(why string) {
	f.t.Helper()
	diskBefore, stateBefore := tree(f.t, f.bookDir), f.versionState()
	_, err := f.resume()
	if !errors.Is(err, ErrSwapResumeRefused) {
		f.t.Fatalf("resume err = %v, want ErrSwapResumeRefused", err)
	}
	if !strings.Contains(err.Error(), why) {
		f.t.Errorf("the refusal %q does not name %q", err.Error(), why)
	}
	if got := tree(f.t, f.bookDir); got != diskBefore {
		f.t.Errorf("the disk changed:\nbefore:\n%s\nafter:\n%s", diskBefore, got)
	}
	if got := f.versionState(); got != stateBefore {
		f.t.Errorf("the store changed:\nbefore:\n%s\nafter:\n%s", stateBefore, got)
	}
}

func (f *swapFixture) setStatus(id, status string) {
	f.t.Helper()
	v, err := f.store.GetBookVersion(id)
	if err != nil || v == nil {
		f.t.Fatalf("get version %s: %v", id, err)
	}
	v.Status = status
	if err := f.store.UpdateBookVersion(v); err != nil {
		f.t.Fatal(err)
	}
}

func (f *swapFixture) requireFile(path, content string) {
	f.t.Helper()
	if got := readTestFile(f.t, path); got != content {
		f.t.Errorf("%s = %q, want %q", path, got, content)
	}
}

func (f *swapFixture) requireAbsent(path string) {
	f.t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		f.t.Errorf("%s exists (err %v), want it gone", path, err)
	}
}

// requireSettled asserts exactly one version of the book is active, and that it is want.
func (f *swapFixture) requireSettled(want string) {
	f.t.Helper()
	all, err := f.store.GetBookVersionsByBookID(f.book.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var active []string
	for _, v := range all {
		if v.Status == database.BookVersionStatusActive {
			active = append(active, v.ID)
		}
		if isSwappingStatus(v.Status) {
			f.t.Errorf("version %s is still %s", v.ID, v.Status)
		}
	}
	if len(active) != 1 || active[0] != want {
		f.t.Errorf("active versions = %v, want exactly [%s]", active, want)
	}
	if act, _ := f.store.GetActiveVersionForBook(f.book.ID); act == nil || act.ID != want {
		f.t.Errorf("GetActiveVersionForBook = %+v, want %s", act, want)
	}
}

// The rows, the book's path and the disk agree: want is the version whose files
// are at the book's folder.
func (f *swapFixture) requireRowsAtRoot(versionID string, names ...string) {
	f.t.Helper()
	rows, _ := f.store.GetBookFiles(f.book.ID)
	got := map[string]bool{}
	for _, r := range rows {
		if r.VersionID == versionID {
			got[r.FilePath] = true
		}
	}
	for _, n := range names {
		if !got[filepath.Join(f.bookDir, n)] {
			f.t.Errorf("version %s has no row at %s (rows %v)", versionID, filepath.Join(f.bookDir, n), got)
		}
	}
}

// Step 4 of the swap fails on the entering version's row: both versions are
// swapping_*, the files are moved, the leaving row is repointed and the entering
// one is not. The resume repoints it, sets the book's path and settles the
// statuses without moving a file.
func TestResumeSwap_FinishesAfterARowWriteFailed(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failFileRow = f.rowOf(alt.ID).ID
	f.swapWith(fs, alt)
	if f.status(f.active.ID) != database.BookVersionStatusSwappingOut || f.status(alt.ID) != database.BookVersionStatusSwappingIn {
		t.Fatalf("statuses = %s/%s, want swapping_out/swapping_in", f.status(f.active.ID), f.status(alt.ID))
	}
	disk := tree(t, f.bookDir)

	res := f.mustResume(SwapResumeFinish)

	if res.Moved != 0 {
		t.Errorf("moved %d files, want 0: finish moves none", res.Moved)
	}
	if got := tree(t, f.bookDir); got != disk {
		t.Errorf("the finish changed the disk:\nbefore:\n%s\nafter:\n%s", disk, got)
	}
	f.requireSettled(alt.ID)
	if f.status(f.active.ID) != database.BookVersionStatusAlt {
		t.Errorf("leaving status = %s, want alt", f.status(f.active.ID))
	}
	newPrimary := filepath.Join(f.bookDir, "Hush.m4b")
	if got := f.bookPath(); got != newPrimary {
		t.Errorf("book path = %s, want %s", got, newPrimary)
	}
	f.requireRowsAtRoot(alt.ID, "Hush.m4b")
	slotRow := filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))
	if got := f.rowOf(f.active.ID).FilePath; got != slotRow {
		t.Errorf("leaving row = %s, want %s", got, slotRow)
	}
	if res.From == nil || res.To == nil || res.To.ID != alt.ID {
		t.Errorf("a finished swap hands the versions back for the Deluge follow: %+v %+v", res.From, res.To)
	}
	// Resumed: nothing left to resume.
	if _, err := f.resume(); !errors.Is(err, ErrNoSwapToResume) {
		t.Errorf("second resume err = %v, want ErrNoSwapToResume", err)
	}
}

// Every move and row write went through but the book's own path write failed.
func TestResumeSwap_FinishesAfterTheBookPathWriteFailed(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failBook = true
	f.swapWith(fs, alt)
	if got := f.bookPath(); got != f.rootAud {
		t.Fatalf("book path = %s, want it unchanged (%s)", got, f.rootAud)
	}

	f.mustResume(SwapResumeFinish)

	f.requireSettled(alt.ID)
	if got, want := f.bookPath(), filepath.Join(f.bookDir, "Hush.m4b"); got != want {
		t.Errorf("book path = %s, want %s", got, want)
	}
}

// The final write of the leaving version to alt failed: both are swapping_*, the
// rows and the book are done.
func TestResumeSwap_FinishesAfterTheLeavingStatusWriteFailed(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failStatusFor = map[string]string{f.active.ID: database.BookVersionStatusAlt}
	f.swapWith(fs, alt)

	f.mustResume(SwapResumeFinish)

	f.requireSettled(alt.ID)
	if f.status(f.active.ID) != database.BookVersionStatusAlt {
		t.Errorf("leaving status = %s, want alt", f.status(f.active.ID))
	}
}

// The rarer case of the fragment: every move and row write succeeded, the
// leaving version reached alt and the write of the entering version to active
// failed. The entering version is swapping_in and the leaving one alt. The resume
// marks the entering version active and writes nothing else of the leaving one.
func TestResumeSwap_FinishesWhenOnlyTheFinalStatusWriteFailed(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failStatusFor = map[string]string{alt.ID: database.BookVersionStatusActive}
	f.swapWith(fs, alt)
	if f.status(f.active.ID) != database.BookVersionStatusAlt || f.status(alt.ID) != database.BookVersionStatusSwappingIn {
		t.Fatalf("statuses = %s/%s, want alt/swapping_in", f.status(f.active.ID), f.status(alt.ID))
	}
	leaving, _ := f.store.GetBookVersion(f.active.ID)
	disk := tree(t, f.bookDir)

	res := f.mustResume(SwapResumeFinish)

	f.requireSettled(alt.ID)
	if got := tree(t, f.bookDir); got != disk {
		t.Errorf("the disk changed:\nbefore:\n%s\nafter:\n%s", disk, got)
	}
	if after, _ := f.store.GetBookVersion(f.active.ID); after.Version != leaving.Version || after.Status != database.BookVersionStatusAlt {
		t.Errorf("the leaving version was written: row version %d -> %d, status %s", leaving.Version, after.Version, after.Status)
	}
	if res.LeavingVersionID != "" || res.EnteringVersionID != alt.ID || res.ActiveVersionID != alt.ID {
		t.Errorf("result = %+v: only the entering version was left in swapping_*", res)
	}
	if res.From != nil || res.To == nil || res.To.ID != alt.ID {
		t.Errorf("From/To = %+v / %+v: the Deluge follow gets the entering version and no leaving one", res.From, res.To)
	}
}

// The same rare case, then the retried DELETE of the leaving (alt) version is
// refused at the server (see the server tests); here the resume is also right
// when that version has been trashed since: the entering version is swapping_in
// alone, the book has no active version and its files are at the folder.
func TestResumeSwap_FinishesWhenTheLeavingVersionWasTrashedMeanwhile(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failStatusFor = map[string]string{alt.ID: database.BookVersionStatusActive}
	f.swapWith(fs, alt)
	f.setStatus(f.active.ID, database.BookVersionStatusTrash)

	f.mustResume(SwapResumeFinish)

	f.requireSettled(alt.ID)
	if f.status(f.active.ID) != database.BookVersionStatusTrash {
		t.Errorf("the trashed version is %s, want it still trash", f.status(f.active.ID))
	}
}

// The files of the leaving version are in its slot (step 2 done), the entering
// ones in theirs (step 3 failed) and the undo of step 2 failed too: both swapping_*.
// Finish would have to move files, so the swap is undone: the leaving files go back.
func TestResumeSwap_UndoesAfterTheLeavingFilesMovedAndTheEnteringOnesDidNot(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	slotFile := filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))
	if _, err := movePreservingAttrs(f.rootAud, slotFile); err != nil {
		t.Fatal(err)
	}
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)

	res := f.mustResume(SwapResumeUndo)

	if res.Moved != 1 {
		t.Errorf("moved %d, want 1", res.Moved)
	}
	f.requireFile(f.rootAud, "active")
	f.requireAbsent(slotFile)
	f.requireAbsent(VersionSlotDir(f.bookDir, f.active.ID)) // pruned, as the swap's own undo does
	f.requireFile(filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b"), "alt-"+alt.ID)
	f.requireSettled(f.active.ID)
	if f.status(alt.ID) != database.BookVersionStatusAlt {
		t.Errorf("entering status = %s, want alt", f.status(alt.ID))
	}
	if got := f.bookPath(); got != f.rootAud {
		t.Errorf("book path = %s, want %s", got, f.rootAud)
	}
}

// Nothing was moved before the swap stopped (both versions were marked, then a
// status restore failed): the undo is statuses only.
func TestResumeSwap_UndoesWhenNothingMoved(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	disk := tree(t, f.bookDir)

	res := f.mustResume(SwapResumeUndo)

	if res.Moved != 0 {
		t.Errorf("moved %d files, want 0", res.Moved)
	}
	if got := tree(t, f.bookDir); got != disk {
		t.Errorf("the disk changed:\nbefore:\n%s\nafter:\n%s", disk, got)
	}
	f.requireSettled(f.active.ID)
}

// A version with two files, one of which moved up to the folder before the
// swap stopped, the leaving version's files in their slot: the undo moves the
// moved entering file back first and then the leaving files, no-replace.
func TestResumeSwap_UndoesAPartlyMovedEnteringVersion(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Part 1.mp3")
	slot := VersionSlotDir(f.bookDir, alt.ID)
	part2 := filepath.Join(slot, "Part 2.mp3")
	writeTestFile(t, part2, "alt2-"+alt.ID)
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, VersionID: alt.ID, FilePath: part2, Format: "mp3"}); err != nil {
		t.Fatal(err)
	}
	leaveSlot := filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))
	if _, err := movePreservingAttrs(f.rootAud, leaveSlot); err != nil {
		t.Fatal(err)
	}
	if _, err := movePreservingAttrs(filepath.Join(slot, "Part 1.mp3"), filepath.Join(f.bookDir, "Part 1.mp3")); err != nil {
		t.Fatal(err)
	}
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)

	res := f.mustResume(SwapResumeUndo)

	if res.Moved != 2 {
		t.Errorf("moved %d, want 2 (the entering file back, then the leaving file)", res.Moved)
	}
	f.requireFile(f.rootAud, "active")
	f.requireFile(filepath.Join(slot, "Part 1.mp3"), "alt-"+alt.ID)
	f.requireFile(part2, "alt2-"+alt.ID)
	f.requireAbsent(filepath.Join(f.bookDir, "Part 1.mp3"))
	f.requireSettled(f.active.ID)
	// Each file the undo moved has its own record, so the moves can be audited.
	f.requireMoveRecords(
		[2]string{filepath.Join(f.bookDir, "Part 1.mp3"), filepath.Join(slot, "Part 1.mp3")},
		[2]string{leaveSlot, f.rootAud},
	)
}

// Two versions whose files share one name: the name tells nothing, the slots do.
// Swap done (leaving file in its slot, entering file at the folder): finish.
func TestResumeSwap_SameNameFinishes(t *testing.T) {
	f := newSwapFixture(t)
	name := filepath.Base(f.rootAud)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, name)
	fs := f.faults()
	fs.failStatusFor = map[string]string{f.active.ID: database.BookVersionStatusAlt}
	f.swapWith(fs, alt)
	f.requireFile(f.rootAud, "alt-"+alt.ID)

	f.mustResume(SwapResumeFinish)

	f.requireSettled(alt.ID)
	f.requireFile(f.rootAud, "alt-"+alt.ID)
	f.requireFile(filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), name), "active")
}

// Same name, leaving file moved to its slot and the entering one not yet moved:
// the folder holds nothing, both slots hold a file. Undo moves the leaving file back.
func TestResumeSwap_SameNameMidSwapUndoes(t *testing.T) {
	f := newSwapFixture(t)
	name := filepath.Base(f.rootAud)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, name)
	if _, err := movePreservingAttrs(f.rootAud, filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), name)); err != nil {
		t.Fatal(err)
	}
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)

	f.mustResume(SwapResumeUndo)

	f.requireFile(f.rootAud, "active")
	f.requireFile(filepath.Join(VersionSlotDir(f.bookDir, alt.ID), name), "alt-"+alt.ID)
	f.requireSettled(f.active.ID)
}

// Same name, nothing moved: the folder's file is the leaving version's (the
// entering slot holds the entering file), so the undo moves nothing.
func TestResumeSwap_SameNameNothingMovedUndoesWithoutMoves(t *testing.T) {
	f := newSwapFixture(t)
	name := filepath.Base(f.rootAud)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, name)
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)

	res := f.mustResume(SwapResumeUndo)

	if res.Moved != 0 {
		t.Errorf("moved %d, want 0", res.Moved)
	}
	f.requireFile(f.rootAud, "active")
	f.requireSettled(f.active.ID)
}

// Same name and the file at the folder with NO slot file: one file for two rows,
// and the disk does not say whose it is.
func TestResumeSwap_SameNameWithOneFileForTwoRowsIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	name := filepath.Base(f.rootAud)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, name)
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	if err := os.Remove(filepath.Join(VersionSlotDir(f.bookDir, alt.ID), name)); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("the disk does not show where each is")
}

// The same file at the folder AND in the entering slot: a stranger at the
// destination is not taken for a moved file.
func TestResumeSwap_AFileAtBothPlacesIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	writeTestFile(t, filepath.Join(f.bookDir, "Hush.m4b"), "a stranger's Hush")
	f.requireRefusedResume("is at both")
}

func TestResumeSwap_AMissingFileIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	if err := os.Remove(filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b")); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("it is missing")
}

func TestResumeSwap_ASymbolicLinkIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	slotFile := filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b")
	target := filepath.Join(t.TempDir(), "elsewhere.m4b")
	writeTestFile(t, target, "elsewhere")
	if err := os.Remove(slotFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, slotFile); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("symbolic link")
}

// A version made active while the swap was stuck (an ingest creates one for a book
// with none): resuming would leave two active versions.
func TestResumeSwap_AnotherActiveVersionIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	late := f.add(database.BookVersionStatusActive, 0, "")
	f.requireRefusedResume("version " + late.ID + " is active as well")
}

func TestResumeSwap_AFileRowOfNoVersionIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	orphan := filepath.Join(f.bookDir, "Orphan.m4b")
	writeTestFile(t, orphan, "orphan")
	if err := f.store.CreateBookFile(&database.BookFile{BookID: f.book.ID, FilePath: orphan, Format: "m4b"}); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("belongs to no version")
}

// The file at the book's folder is also named by a row of ANOTHER book.
func TestResumeSwap_AFileAnotherBooksRowNamesIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	other, err := f.store.CreateBook(&database.Book{Title: "Hush", FilePath: f.rootAud, Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: other.ID, FilePath: f.rootAud, Format: "m4b"}); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("also named by another file row")
}

func TestResumeSwap_ARowedSizeThatDisagreesWithTheFileIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	name := filepath.Base(f.rootAud)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, name)
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	row := f.rowOf(f.active.ID)
	if _, err := f.store.ModifyBookFile(f.book.ID, row.ID, func(r *database.BookFile) error {
		r.FileSize = 999999
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("its row says 999999")
}

func TestResumeSwap_MoreThanOneSwappingInIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	a := f.add(database.BookVersionStatusSwappingIn, time.Hour, "Hush.m4b")
	b := f.add(database.BookVersionStatusSwappingIn, 2*time.Hour, "Knightfall.m4b")
	_, _ = a, b
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.requireRefusedResume("2 are swapping_in")
}

func TestResumeSwap_NothingStuckIsReported(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	disk, state := tree(t, f.bookDir), f.versionState()
	_, err := f.resume()
	if !errors.Is(err, ErrNoSwapToResume) {
		t.Fatalf("err = %v, want ErrNoSwapToResume", err)
	}
	if tree(t, f.bookDir) != disk || f.versionState() != state {
		t.Error("a resume with nothing to resume wrote something")
	}
}

// Only the leaving version is still swapping_out: its files never moved (or
// moved back) and the restore of its status failed.
func TestResumeSwap_ALoneSwappingOutVersionIsMadeActiveAgain(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	disk := tree(t, f.bookDir)

	res := f.mustResume(SwapResumeUndo)

	if tree(t, f.bookDir) != disk {
		t.Error("the disk changed")
	}
	f.requireSettled(f.active.ID)
	if res.EnteringVersionID != "" || res.LeavingVersionID != f.active.ID {
		t.Errorf("result = %+v", res)
	}
}

// swapping_out alone with its files in its slot: whose file the folder's is
// cannot be told.
func TestResumeSwap_ALoneSwappingOutVersionWithItsFilesInItsSlotIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	if _, err := movePreservingAttrs(f.rootAud, filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))); err != nil {
		t.Fatal(err)
	}
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.requireRefusedResume("in its slot, not in the book's folder")
}

// The entering version alone is swapping_in beside an active version (the
// leaving version's restore succeeded, the entering one's did not): its files
// are in its slot, and it goes back to alt.
func TestResumeSwap_ALoneSwappingInVersionBesideTheActiveOneGoesBackToAlt(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	disk := tree(t, f.bookDir)

	res := f.mustResume(SwapResumeUndo)

	if tree(t, f.bookDir) != disk {
		t.Error("the disk changed")
	}
	f.requireSettled(f.active.ID)
	if f.status(alt.ID) != database.BookVersionStatusAlt {
		t.Errorf("entering status = %s, want alt", f.status(alt.ID))
	}
	if res.ActiveVersionID != f.active.ID {
		t.Errorf("active version = %s, want %s", res.ActiveVersionID, f.active.ID)
	}
}

// swapping_in beside an active version, with the entering file at the folder:
// two versions claim the folder.
func TestResumeSwap_ALoneSwappingInVersionBesideAnActiveOneAtTheFolderIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusSwappingIn, time.Hour, "Hush.m4b")
	if _, err := movePreservingAttrs(filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b"), filepath.Join(f.bookDir, "Hush.m4b")); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("which of them the book plays cannot be told")
}

// A resume that starts and cannot finish (a row write fails) says so with
// ErrSwapIncomplete, and the next resume, with the store healthy again, reads the
// disk anew and settles the book.
func TestResumeSwap_AFailedResumeCanBeRepeated(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	leaveSlot := filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))
	if _, err := movePreservingAttrs(f.rootAud, leaveSlot); err != nil {
		t.Fatal(err)
	}
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)

	fs := f.faults()
	fs.failFileRow = f.rowOf(f.active.ID).ID
	_, err := ResumeVersionSwap(context.Background(), fs, f.book.ID)
	if !errors.Is(err, ErrSwapIncomplete) {
		t.Fatalf("err = %v, want ErrSwapIncomplete", err)
	}
	// The undo had moved the file back; the row still names the slot.
	f.requireFile(f.rootAud, "active")
	if f.status(f.active.ID) != database.BookVersionStatusSwappingOut {
		t.Errorf("a resume that failed settled a status: %s", f.status(f.active.ID))
	}

	f.mustResume(SwapResumeUndo)
	f.requireSettled(f.active.ID)
	if got := f.rowOf(f.active.ID).FilePath; got != f.rootAud {
		t.Errorf("row = %s, want %s", got, f.rootAud)
	}
}

// The listing reports each stuck book with its path, its versions' files and what
// a resume would do (or why it would refuse); a settled book is not listed.
func TestListSwapIncompleteBooks(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")

	books, err := ListSwapIncompleteBooks(f.store)
	if err != nil || len(books) != 0 {
		t.Fatalf("healthy library: books %+v err %v, want none", books, err)
	}

	fs := f.faults()
	fs.failStatusFor = map[string]string{alt.ID: database.BookVersionStatusActive}
	f.swapWith(fs, alt)

	books, err = ListSwapIncompleteBooks(f.store)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("books = %+v, want 1", books)
	}
	b := books[0]
	newPrimary := filepath.Join(f.bookDir, "Hush.m4b")
	if b.BookID != f.book.ID || b.BookPath != newPrimary || b.Title != "The Long Halloween" {
		t.Errorf("book = %+v", b)
	}
	if b.Resume != SwapResumeFinish || b.ResumeRefusal != "" {
		t.Errorf("resume = %q refusal %q, want finish", b.Resume, b.ResumeRefusal)
	}
	// swapping_in first, then the alt partner, each with its file rows.
	if len(b.Versions) != 2 || b.Versions[0].ID != alt.ID || b.Versions[0].Status != database.BookVersionStatusSwappingIn {
		t.Fatalf("versions = %+v", b.Versions)
	}
	if len(b.Versions[0].Files) != 1 || b.Versions[0].Files[0].Path != newPrimary {
		t.Errorf("entering files = %+v", b.Versions[0].Files)
	}
	if b.Versions[1].ID != f.active.ID || b.Versions[1].Status != database.BookVersionStatusAlt || len(b.Versions[1].Files) != 1 {
		t.Errorf("leaving = %+v", b.Versions[1])
	}

	// An ambiguous disk is listed with the reason, not hidden.
	writeTestFile(t, filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b"), "a stranger")
	books, err = ListSwapIncompleteBooks(f.store)
	if err != nil || len(books) != 1 {
		t.Fatalf("books %+v err %v", books, err)
	}
	if books[0].Resume != "" || !strings.Contains(books[0].ResumeRefusal, "is at both") {
		t.Errorf("resume = %q refusal %q, want a refusal that names the double file", books[0].Resume, books[0].ResumeRefusal)
	}
}

// modifyRow edits a file row in place.
func (f *swapFixture) modifyRow(id string, fn func(*database.BookFile)) {
	f.t.Helper()
	if _, err := f.store.ModifyBookFile(f.book.ID, id, func(r *database.BookFile) error {
		fn(r)
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

// requireMoveRecords asserts the book's path history holds, in order, one
// per-file move record for each {from, to} pair (the history is listed newest
// first, so the pairs are compared as a set).
func (f *swapFixture) requireMoveRecords(want ...[2]string) {
	f.t.Helper()
	hist, err := f.store.GetBookPathHistory(f.book.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	var got [][2]string
	for _, h := range hist {
		if h.ChangeType == swapResumeMoveChangeType {
			got = append(got, [2]string{h.OldPath, h.NewPath})
		}
	}
	less := func(s [][2]string) func(i, j int) bool {
		return func(i, j int) bool { return s[i][0]+"\x00"+s[i][1] < s[j][0]+"\x00"+s[j][1] }
	}
	sort.Slice(got, less(got))
	sort.Slice(want, less(want))
	if len(got) != len(want) {
		f.t.Fatalf("move records = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			f.t.Errorf("move record = %v, want %v", got[i], want[i])
		}
	}
}

// addActive makes another version of the book active beside the fixture's, the
// way an ingest into a book with none does (CreateBookVersion refuses a second
// active version, so it is made alt and then marked).
func (f *swapFixture) addActive() *database.BookVersion {
	f.t.Helper()
	v := f.add(database.BookVersionStatusAlt, 0, "")
	f.setStatus(v.ID, database.BookVersionStatusActive)
	return v
}

// enteringMovedToTheFolder is a swap that stopped after step 3 moved the
// entering version's file up to the folder and before its row followed: both
// versions are swapping_*, the entering row still names the slot.
func (f *swapFixture) enteringMovedToTheFolder() (alt *database.BookVersion, root, slot string) {
	f.t.Helper()
	alt = f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	slot = filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b")
	root = filepath.Join(f.bookDir, "Hush.m4b")
	if _, err := movePreservingAttrs(slot, root); err != nil {
		f.t.Fatal(err)
	}
	return alt, root, slot
}

// A real swap of two versions whose files share a name stops with the entering
// version still swapping_in and the leaving one back to active (the move fails,
// the undo succeeds, and only the leaving version's status is restored). The
// active version's file is at the folder and the entering one's in its slot: the
// resume must not take the active file for the entering version's, or the book
// can be neither resumed, trashed nor swapped again.
func TestResumeSwap_SameNameBesideTheActiveVersionAfterARealSwapGoesBackToAlt(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode that makes the swap's move fail")
	}
	f := newSwapFixture(t)
	name := filepath.Base(f.rootAud)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, name)
	versionsDir := VersionsDir(f.bookDir)
	if err := os.Chmod(versionsDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(versionsDir, 0o775) })
	fs := f.faults()
	fs.failStatusFor = map[string]string{alt.ID: database.BookVersionStatusAlt}
	f.swapWith(fs, alt)
	if err := os.Chmod(versionsDir, 0o775); err != nil {
		t.Fatal(err)
	}
	if f.status(f.active.ID) != database.BookVersionStatusActive || f.status(alt.ID) != database.BookVersionStatusSwappingIn {
		t.Fatalf("statuses = %s/%s, want active/swapping_in", f.status(f.active.ID), f.status(alt.ID))
	}
	disk := tree(t, f.bookDir)

	books, err := ListSwapIncompleteBooks(f.store)
	if err != nil || len(books) != 1 || books[0].Resume != SwapResumeUndo {
		t.Fatalf("listing = %+v err %v, want one book that resumes by undo", books, err)
	}
	res := f.mustResume(SwapResumeUndo)

	if res.Moved != 0 {
		t.Errorf("moved %d files, want 0", res.Moved)
	}
	if got := tree(t, f.bookDir); got != disk {
		t.Errorf("the resume changed the disk:\nbefore:\n%s\nafter:\n%s", disk, got)
	}
	f.requireSettled(f.active.ID)
	if f.status(alt.ID) != database.BookVersionStatusAlt {
		t.Errorf("entering status = %s, want alt", f.status(alt.ID))
	}
	if res.ActiveVersionID != f.active.ID {
		t.Errorf("active version = %s, want %s", res.ActiveVersionID, f.active.ID)
	}
	if got := f.rowOf(f.active.ID).FilePath; got != f.rootAud {
		t.Errorf("the active version's row = %s, want it untouched at %s", got, f.rootAud)
	}
	if got, want := f.rowOf(alt.ID).FilePath, filepath.Join(VersionSlotDir(f.bookDir, alt.ID), name); got != want {
		t.Errorf("the entering version's row = %s, want %s", got, want)
	}
	if got, err := f.pick(); err != nil || got.ID != alt.ID {
		t.Errorf("the book can be swapped again: pick = %+v err %v", got, err)
	}
}

// The same name beside the active version, but the entering file is missing from
// its slot: the file at the folder is the active version's and is not taken for
// the entering version's.
func TestResumeSwap_SameNameBesideTheActiveVersionWithTheSlotFileMissingIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	name := filepath.Base(f.rootAud)
	alt := f.add(database.BookVersionStatusSwappingIn, time.Hour, name)
	if err := os.Remove(filepath.Join(VersionSlotDir(f.bookDir, alt.ID), name)); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("it is missing")
}

// A stranger at the folder under the entering file's name, the file itself gone
// from its slot, and a row that recorded neither size nor hash: the stranger is
// not moved into the slot and the row is not pointed at it.
func TestResumeSwap_AFileAtTheFolderThatNothingVouchesForIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	if err := os.Remove(filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b")); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(f.bookDir, "Hush.m4b"), "a stranger's Hush")
	f.modifyRow(f.rowOf(alt.ID).ID, func(r *database.BookFile) { r.FileSize = 0 })

	f.requireRefusedResume("neither a size nor a hash")
}

// The row recorded a hash and the file at the other place has another: refused,
// whatever the size says.
func TestResumeSwap_AFileWhoseHashDisagreesWithTheRowIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt, root, _ := f.enteringMovedToTheFolder()
	other := filepath.Join(t.TempDir(), "Hush elsewhere.m4b")
	writeTestFile(t, other, "not the entering version's audio")
	wrong, err := filehash.BookFileHash(other)
	if err != nil {
		t.Fatal(err)
	}
	f.modifyRow(f.rowOf(alt.ID).ID, func(r *database.BookFile) { r.FileSize = 0; r.FileHash = wrong })
	f.requireFile(root, "alt-"+alt.ID)

	f.requireRefusedResume("is not the one its row recorded")
}

// The row recorded a hash and no size, and the file at the other place has that
// hash: it is the version's file, moved by the swap, and the undo moves it back
// and records the move.
func TestResumeSwap_AFileWhoseHashMatchesTheRowIsMovedBack(t *testing.T) {
	f := newSwapFixture(t)
	alt, root, slot := f.enteringMovedToTheFolder()
	hash, err := filehash.BookFileHash(root)
	if err != nil {
		t.Fatal(err)
	}
	f.modifyRow(f.rowOf(alt.ID).ID, func(r *database.BookFile) { r.FileSize = 0; r.FileHash = hash })

	res := f.mustResume(SwapResumeUndo)

	if res.Moved != 1 {
		t.Errorf("moved %d, want 1", res.Moved)
	}
	f.requireFile(slot, "alt-"+alt.ID)
	f.requireAbsent(root)
	f.requireSettled(f.active.ID)
	f.requireMoveRecords([2]string{root, slot})
}

// A record of the move that cannot be written stops the move before it is made;
// the book stays resumable.
func TestResumeSwap_AMoveWhoseRecordCannotBeWrittenIsNotMade(t *testing.T) {
	f := newSwapFixture(t)
	alt, root, slot := f.enteringMovedToTheFolder()
	fs := f.faults()
	fs.failMoveRecord = true

	_, err := ResumeVersionSwap(context.Background(), fs, f.book.ID)

	if !errors.Is(err, ErrSwapIncomplete) {
		t.Fatalf("err = %v, want ErrSwapIncomplete", err)
	}
	f.requireFile(root, "alt-"+alt.ID)
	f.requireAbsent(slot)
	f.mustResume(SwapResumeUndo)
	f.requireSettled(f.active.ID)
}

// A row's file in a folder that is neither the book's nor the version's own slot
// is never looked for under its name at the book's folder or the slot: the name
// could be anyone's.
func TestResumeSwap_ARowOutsideTheBookFolderAndTheSlotIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	elsewhere := filepath.Join(t.TempDir(), "Hush.m4b")
	writeTestFile(t, elsewhere, "alt-"+alt.ID)
	f.modifyRow(f.rowOf(alt.ID).ID, func(r *database.BookFile) { r.FilePath = elsewhere })

	f.requireRefusedResume("neither the book's folder")
}

// .versions is a link to a folder somewhere else: the slots through it resolve
// consistently, so only the link itself gives it away.
func TestResumeSwap_AVersionsFolderThatIsALinkIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	moved := filepath.Join(t.TempDir(), "versions")
	if err := os.Rename(VersionsDir(f.bookDir), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, VersionsDir(f.bookDir)); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("symbolic link")
}

// The entering version's slot is a link to a folder somewhere else.
func TestResumeSwap_ASlotThatIsALinkIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	slot := VersionSlotDir(f.bookDir, alt.ID)
	moved := filepath.Join(t.TempDir(), "slot")
	if err := os.Rename(slot, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, slot); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("symbolic link")
}

// A slot that is a file, not a folder.
func TestResumeSwap_ASlotThatIsAFileIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	slot := VersionSlotDir(f.bookDir, alt.ID)
	if err := os.RemoveAll(slot); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, slot, "not a folder")
	f.requireRefusedResume("is a file, not a folder")
}

// swapping_out alone with another version active beside it: making it active
// again would leave two.
func TestResumeSwap_ALoneSwappingOutVersionBesideAnActiveOneIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	late := f.addActive()
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.requireRefusedResume("version " + late.ID + " is active beside the swapping_out version")
}

// A file row whose path is a folder, not a file: it is not moved.
func TestResumeSwap_ANonRegularFileIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	slotFile := filepath.Join(VersionSlotDir(f.bookDir, alt.ID), "Hush.m4b")
	if err := os.Remove(slotFile); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(slotFile, "Chapter 1.mp3"), "a folder named like the file")
	f.modifyRow(f.rowOf(alt.ID).ID, func(r *database.BookFile) { r.FileSize = 0 })

	f.requireRefusedResume("not a regular file")
}

// Two or more versions active beside the swapping_in one: which of them the
// book plays cannot be told.
func TestResumeSwap_ALoneSwappingInVersionBesideTwoActiveOnesIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	f.addActive()
	f.add(database.BookVersionStatusSwappingIn, time.Hour, "Hush.m4b")
	f.requireRefusedResume("2 versions are active beside the swapping_in version")
}

// Another writer repoints a row between the plan and the write: the row is not
// overwritten, the resume says it did not finish, and a later resume (which
// reads the disk and the rows again) refuses what it then cannot account for.
func TestResumeSwap_ARowRepointedByAnotherWriterIsNotOverwritten(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failFileRow = f.rowOf(alt.ID).ID
	f.swapWith(fs, alt)
	enteringRow := f.rowOf(alt.ID)
	// A path inside the book's folder, so the row is still the book's.
	taken := filepath.Join(f.bookDir, "Hush (another edition).m4b")

	racing := f.faults()
	racing.beforeRowID = enteringRow.ID
	racing.beforeRowWrite = func() {
		f.modifyRow(enteringRow.ID, func(r *database.BookFile) { r.FilePath = taken })
	}
	_, err := ResumeVersionSwap(context.Background(), racing, f.book.ID)

	if !errors.Is(err, ErrSwapIncomplete) {
		t.Fatalf("err = %v, want ErrSwapIncomplete", err)
	}
	if got := f.rowOf(alt.ID).FilePath; got != taken {
		t.Errorf("the row = %s, want the other writer's %s left alone", got, taken)
	}
	if f.status(alt.ID) != database.BookVersionStatusSwappingIn {
		t.Errorf("a resume that failed settled a status: %s", f.status(alt.ID))
	}
}

// A row of another book at the path an undo moves the file TO (not only the one
// it is at) is refused.
func TestResumeSwap_ADestinationAnotherBooksRowNamesIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	leaveSlot := filepath.Join(VersionSlotDir(f.bookDir, f.active.ID), filepath.Base(f.rootAud))
	if _, err := movePreservingAttrs(f.rootAud, leaveSlot); err != nil {
		t.Fatal(err)
	}
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	other, err := f.store.CreateBook(&database.Book{Title: "Hush", FilePath: f.rootAud, Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: other.ID, FilePath: f.rootAud, Format: "m4b"}); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("also named by another file row")
}

// A row of another book written under the real spelling of a path the book is
// reached through a link to is found too: the store matches path strings exactly.
func TestResumeSwap_AnotherBooksRowUnderAnotherSpellingOfThePathIsRefused(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	// The book is reached through a link to its folder.
	alias := filepath.Join(t.TempDir(), "library")
	if err := os.Symlink(f.bookDir, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ModifyBook(f.book.ID, func(b *database.Book) error {
		b.FilePath = filepath.Join(alias, filepath.Base(f.rootAud))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	slotThroughTheLink := filepath.Join(alias, VersionsDirName, alt.ID, "Hush.m4b")
	other, err := f.store.CreateBook(&database.Book{Title: "Hush", FilePath: "/library/other/Hush.m4b", Format: "m4b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.CreateBookFile(&database.BookFile{BookID: other.ID, FilePath: canonPath(slotThroughTheLink), Format: "m4b"}); err != nil {
		t.Fatal(err)
	}
	f.requireRefusedResume("also named by another file row")
}

// A path lookup that cannot be completed is a fault, not a statement about the
// library: the error is not a refusal (the server answers it 500).
func TestResumeSwap_APathLookupThatCannotBeCompletedIsAFaultNotARefusal(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	f.setStatus(f.active.ID, database.BookVersionStatusSwappingOut)
	f.setStatus(alt.ID, database.BookVersionStatusSwappingIn)
	fs := f.faults()
	fs.failPathLookup = true
	disk, state := tree(t, f.bookDir), f.versionState()

	_, err := ResumeVersionSwap(context.Background(), fs, f.book.ID)

	if !errors.Is(err, database.ErrBookFilesAtPathUnavailable) {
		t.Fatalf("err = %v, want ErrBookFilesAtPathUnavailable", err)
	}
	if errors.Is(err, ErrSwapResumeRefused) || errors.Is(err, ErrSwapIncomplete) {
		t.Errorf("err = %v: a lookup that failed is neither a refusal nor a started resume", err)
	}
	if tree(t, f.bookDir) != disk || f.versionState() != state {
		t.Error("a resume whose plan failed wrote something")
	}
}

// A swap that got as far as the status writes with only the entering version
// left swapping_in hands that version back for the Deluge follow.
func TestResumeSwap_AFinishWithOnlyTheEnteringVersionHandsItBackForDeluge(t *testing.T) {
	f := newSwapFixture(t)
	alt := f.add(database.BookVersionStatusAlt, time.Hour, "Hush.m4b")
	fs := f.faults()
	fs.failStatusFor = map[string]string{alt.ID: database.BookVersionStatusActive}
	f.swapWith(fs, alt)

	res := f.mustResume(SwapResumeFinish)

	if res.To == nil || res.To.ID != alt.ID || res.From != nil {
		t.Errorf("From/To = %+v / %+v, want only To = the entering version", res.From, res.To)
	}
}
