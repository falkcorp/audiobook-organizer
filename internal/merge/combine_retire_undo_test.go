// file: internal/merge/combine_retire_undo_test.go
// version: 1.0.0
// guid: 169db87a-6f56-43c0-91a1-9a1fa5f526ea
// last-edited: 2026-10-23

package merge

import (
	"errors"
	"testing"

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
