// file: internal/merge/combine_retire_undo_test.go
// version: 1.1.0
// guid: 169db87a-6f56-43c0-91a1-9a1fa5f526ea
// last-edited: 2026-10-23

package merge

import (
	"errors"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRetireUndoer stands in for audiobooks' undoer (merge cannot import it).
// It records the order of its calls against the library state it sees, and
// answers what the test set.
type fakeRetireUndoer struct {
	t     *testing.T
	store database.Store
	// live and reasons are what CheckRetireRevert answers; checkErr fails it.
	live     bool
	reasons  []string
	checkErr error
	// revertErr fails RevertRetire.
	revertErr error
	// shell is the book whose soft-delete is read at RevertRetire time: the
	// retire is reversed BEFORE the shell is restored.
	shell string

	checked, reverted []string
	shellSoftDeleted  []bool
}

func (f *fakeRetireUndoer) CheckRetireRevert(opID, shellID string) (bool, []string, error) {
	f.checked = append(f.checked, opID+"/"+shellID)
	return f.live, f.reasons, f.checkErr
}

func (f *fakeRetireUndoer) RevertRetire(opID, shellID string) error {
	f.reverted = append(f.reverted, opID+"/"+shellID)
	b, err := f.store.GetBookByID(f.shell)
	require.NoError(f.t, err)
	f.shellSoftDeleted = append(f.shellSoftDeleted, b != nil && b.IsSoftDeleted())
	return f.revertErr
}

func combined(t *testing.T) (database.Store, undoFixture, *Service, string) {
	t.Helper()
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	ms := NewService(store)
	res, err := ms.CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	require.NoError(t, err)
	require.NotEmpty(t, res.JournalID)
	return store, f, ms, res.JournalID
}

func snapshot(t *testing.T, store database.Store, f undoFixture) (map[string]bookView, map[string]fileView) {
	t.Helper()
	all := []string{f.survivor, f.absA, f.absB}
	books := map[string]bookView{}
	for _, id := range all {
		books[id] = viewBook(t, store, id)
	}
	return books, viewFiles(t, store, all...)
}

func TestWriteCombineRetireLink_RoundTripsAndRejectsBadIDs(t *testing.T) {
	store := setupTestStore(t)
	kv := store
	require.NoError(t, WriteCombineRetireLink(kv, "cj1", "shellA", "op1"))
	require.NoError(t, WriteCombineRetireLink(kv, "cj1", "shellA", "op1"), "idempotent")
	require.NoError(t, WriteCombineRetireLink(kv, "cj1", "shellB", "op2"))
	require.NoError(t, WriteCombineRetireLink(kv, "cj10", "shellC", "op3"))

	got, err := ListCombineRetireLinks(kv, "cj1")
	require.NoError(t, err)
	assert.Equal(t, []CombineRetireLink{{ShellID: "shellA", OpID: "op1"}, {ShellID: "shellB", OpID: "op2"}}, got,
		"cj10's link is not cj1's: the key's trailing separator keeps the prefixes apart")
	assert.Error(t, WriteCombineRetireLink(kv, "cj:1", "s", "op"))
	assert.Error(t, WriteCombineRetireLink(kv, "cj1", "", "op"))
	assert.Error(t, WriteCombineRetireLink(kv, "cj1", "s", ""))
}

func TestUndoCombine_ReversesLinkedRetireBeforeRestoringTheShell(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA}
	ms.SetRetireUndoer(u)

	res, err := ms.UndoCombine(journalID)
	require.NoError(t, err)
	assert.Contains(t, res.RestoredBooks, f.absA)
	assert.Equal(t, []string{"retire-op-1/" + f.absA}, u.reverted)
	assert.Equal(t, []bool{true}, u.shellSoftDeleted, "the retire is reversed while the shell is still as the combine left it, before the shell is restored")
	assert.False(t, viewBook(t, store, f.absA).SoftDel, "the shell is restored after")

	j, err := ms.GetCombineJournal(journalID)
	require.NoError(t, err)
	assert.Equal(t, CombineJournalUndone, j.Status)
	_, err = ms.UndoCombine(journalID)
	require.Error(t, err, "a second undo is refused: the combine is already undone")
}

func TestUndoCombine_DeadLinkIsIgnored(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: false, shell: f.absA}
	ms.SetRetireUndoer(u)

	_, err := ms.UndoCombine(journalID)
	require.NoError(t, err)
	assert.Equal(t, []string{"retire-op-1/" + f.absA}, u.checked)
	assert.Equal(t, []string{"retire-op-1/" + f.absA}, u.reverted, "RevertRetire is a no-op for a retire with nothing pending, and is harmless to call")
}

func TestUndoCombine_UnreversibleRetireRefusesBeforeAnyWrite(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA,
		reasons: []string{"change c1 (book_file_retire): book_file x was retired into version \"v9\" since the operation"}}
	ms.SetRetireUndoer(u)
	preBooks, preFiles := snapshot(t, store, f)

	_, err := ms.UndoCombine(journalID)
	var refused *CombineUndoRefusedError
	require.True(t, errors.As(err, &refused), "%v", err)
	joined := ""
	for _, r := range refused.Reasons {
		joined += r + "\n"
	}
	assert.Contains(t, joined, f.absA, "names the shell")
	assert.Contains(t, joined, "retire-op-1", "names the retire")
	assert.Contains(t, joined, "book_file x", "names the row and what changed")
	assert.Empty(t, u.reverted, "nothing is reversed when any retire cannot be")

	postBooks, postFiles := snapshot(t, store, f)
	assert.Equal(t, preBooks, postBooks)
	assert.Equal(t, preFiles, postFiles)
	j, err := ms.GetCombineJournal(journalID)
	require.NoError(t, err)
	assert.Equal(t, CombineJournalApplied, j.Status, "the journal is untouched, so the undo can be retried after the retire is dealt with")
}

func TestUndoCombine_LinkWithNoUndoerFailsClosed(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	preBooks, preFiles := snapshot(t, store, f)

	_, err := ms.UndoCombine(journalID)
	var refused *CombineUndoRefusedError
	require.True(t, errors.As(err, &refused), "%v", err)
	assert.Contains(t, refused.Reasons[0], "cannot reverse a retire")
	postBooks, postFiles := snapshot(t, store, f)
	assert.Equal(t, preBooks, postBooks)
	assert.Equal(t, preFiles, postFiles)
}

func TestUndoCombine_CheckErrorRefuses(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	ms.SetRetireUndoer(&fakeRetireUndoer{t: t, store: store, shell: f.absA, checkErr: errors.New("read failed")})
	_, err := ms.UndoCombine(journalID)
	var refused *CombineUndoRefusedError
	require.True(t, errors.As(err, &refused), "%v", err)
	assert.Contains(t, refused.Reasons[0], "read failed")
}

func TestUndoCombine_NoLinkNeedsNoUndoer(t *testing.T) {
	_, f, ms, journalID := combined(t)
	res, err := ms.UndoCombine(journalID)
	require.NoError(t, err)
	assert.Contains(t, res.RestoredBooks, f.absA)
}

func TestUndoCombine_RetireReversalFailureLeavesRetryableJournal(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA, revertErr: errors.New("boom")}
	ms.SetRetireUndoer(u)
	_, err := ms.UndoCombine(journalID)
	require.Error(t, err)
	assert.True(t, viewBook(t, store, f.absA).SoftDel, "the shell is not restored when its retire could not be reversed")
	j, gerr := ms.GetCombineJournal(journalID)
	require.NoError(t, gerr)
	assert.Equal(t, CombineJournalUndoFailed, j.Status)

	u.revertErr = nil
	_, err = ms.UndoCombine(journalID)
	require.NoError(t, err, "the retry reverses the retire and finishes")
}

// retireRowInto leaves the row fileID retired under holder, as another
// operation's retire (one with no combine link) would: it is first moved onto
// a scratch book, then retired from there into a version of holder.
func retireRowInto(t *testing.T, store database.Store, fileID, currentOwner, holder, versionID string) {
	t.Helper()
	scratch, err := store.CreateBook(&database.Book{Title: "Synthetic Scratch " + versionID, FilePath: "/tmp/undo/scratch-" + versionID})
	require.NoError(t, err)
	require.NoError(t, store.MoveBookFilesToBook([]string{fileID}, currentOwner, scratch.ID))
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	_, err = store.CreateBookVersion(&database.BookVersion{ID: versionID, BookID: holder, Status: database.BookVersionStatusRetired,
		RetiredFromBookID: scratch.ID, RetiredAt: &now})
	require.NoError(t, err)
	w, ok := database.AsCapability[database.RetiredBookFileWriter](store)
	require.True(t, ok)
	require.NoError(t, w.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{fileID}, SourceBookID: scratch.ID}}, holder, versionID))
}

func refusalText(t *testing.T, err error) string {
	t.Helper()
	var refused *CombineUndoRefusedError
	require.True(t, errors.As(err, &refused), "%v", err)
	joined := ""
	for _, r := range refused.Reasons {
		joined += r + "\n"
	}
	return joined
}

// An absorbed book that already held a retired version before the combine
// still holds it afterwards, and the undo must restore it without calling that
// version a file the book "has been given since". The include-retired read has
// to tell the two apart.
func TestUndoCombine_AShellsOwnRetiredVersionIsNotAGiftSinceTheCombine(t *testing.T) {
	store := setupTestStore(t)
	f := seedUndoFixture(t, store)
	ms := NewService(store)
	// A row of a scratch book retired into a version of absA, before the combine.
	scratch, err := store.CreateBook(&database.Book{Title: "Synthetic Scratch", FilePath: "/tmp/undo/scratch"})
	require.NoError(t, err)
	rowID := "01SYNTHETICRETIREDROW00000"
	require.NoError(t, store.CreateBookFile(&database.BookFile{ID: rowID, BookID: scratch.ID, FilePath: "/tmp/undo/scratch/01.mp3", Format: "mp3", TrackNumber: 1}))
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	_, err = store.CreateBookVersion(&database.BookVersion{ID: "ver-before", BookID: f.absA, Status: database.BookVersionStatusRetired, RetiredFromBookID: scratch.ID, RetiredAt: &now})
	require.NoError(t, err)
	w, ok := database.AsCapability[database.RetiredBookFileWriter](store)
	require.True(t, ok)
	require.NoError(t, w.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rowID}, SourceBookID: scratch.ID}}, f.absA, "ver-before"))

	res, err := ms.CombineBooks([]string{f.survivor, f.absA, f.absB}, f.survivor, nil)
	if err != nil {
		t.Skipf("the combine itself refuses a holder of retired versions: %v", err)
	}
	_, err = ms.UndoCombine(res.JournalID)
	require.NoError(t, err, "a retired version the shell held before the combine is not a gift since it")
}

// A row the combine moved onto the survivor was later retired back into the
// survivor (it sits there flagged and versioned). The undo must not move it
// to its old owner: that would rewrite a version the owner keeps.
func TestUndoCombine_RefusesARowRetiredIntoTheSurvivorSinceTheCombine(t *testing.T) {
	store, f, ms, journalID := combined(t)
	j, err := ms.GetCombineJournal(journalID)
	require.NoError(t, err)
	var fileID string
	for _, a := range j.Absorbed {
		if a.BookID == f.absA {
			fileID = a.Files[0].FileID
		}
	}
	retireRowInto(t, store, fileID, f.survivor, f.survivor, "ver-into-survivor")
	row, err := store.GetBookFileByID(f.survivor, fileID)
	require.NoError(t, err)
	require.True(t, row.Retired)

	_, err = ms.UndoCombine(journalID)
	require.Error(t, err)
	assert.Contains(t, refusalText(t, err), "retired into version")
	after, err := store.GetBookFileByID(f.survivor, fileID)
	require.NoError(t, err)
	require.NotNil(t, after, "the row stays where the retire put it")
	assert.True(t, after.Retired)
}

// The three row-level reads behind those refusals, called directly: a retired
// row is neither "already undone", nor renumbered, nor moved back.
func TestCombineUndoHelpers_TreatARetiredRowAsNotTheCombinesToRestore(t *testing.T) {
	store, f, ms, journalID := combined(t)
	j, err := ms.GetCombineJournal(journalID)
	require.NoError(t, err)
	var fm CombineFileMove
	for _, a := range j.Absorbed {
		if a.BookID == f.absA {
			fm = a.Files[0]
		}
	}
	require.Equal(t, f.absA, fm.FromBookID)
	// A retry: the failed attempt had already restored absA, and another
	// operation then retired the row into it.
	no := false
	_, err = store.ModifyBook(f.absA, func(b *database.Book) error { b.MarkedForDeletion = &no; b.MarkedForDeletionAt = nil; return nil })
	require.NoError(t, err)
	retireRowInto(t, store, fm.FileID, f.survivor, f.absA, "ver-helpers")
	assert.False(t, ms.fileAlreadyUndone(j, fm), "a retired row on its old owner is not where a completed undo puts it")

	// The same row retired under the survivor: numbers are not restored.
	store2, f2, ms2, journalID2 := combined(t)
	j2, err := ms2.GetCombineJournal(journalID2)
	require.NoError(t, err)
	var fm2 CombineFileMove
	for _, a := range j2.Absorbed {
		if a.BookID == f2.absA {
			fm2 = a.Files[0]
		}
	}
	retireRowInto(t, store2, fm2.FileID, f2.survivor, f2.survivor, "ver-helpers-2")
	err = ms2.restoreSurvivorFileNumbers(j2, fm2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "retired into version")
}
