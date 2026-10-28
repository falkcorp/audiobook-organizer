// file: internal/merge/combine_retire_undo_test.go
// version: 1.4.1
// guid: 169db87a-6f56-43c0-91a1-9a1fa5f526ea
// last-edited: 2026-10-28

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
	// live and reasons are what PlanShell answers; planErr fails ReadRetireOp.
	live    bool
	reasons []string
	planErr error
	// pendingIDs are the shell's rows the plan reports as held by the retire.
	pendingIDs []string
	// revertErr fails the Revert the plan returns.
	revertErr error
	// shell is the book whose soft-delete is read at Revert time: the retire
	// is reversed BEFORE the shell is restored.
	shell string
	// snapshot, when set, is the purge snapshot Revert brings a purged shell
	// back from (the shell exactly as the combine left it, trash state and
	// merged-into pointer included).
	snapshot *database.Book

	// lockErr is the busy refusal LockRetireOps answers. locked records each
	// call's op ids; held is how many locks are held now.
	lockErr error
	locked  [][]string
	held    int
	// heldAtRevert is what held was when each Revert ran.
	heldAtRevert []int

	opsRead, planned, reverted []string
	shellSoftDeleted           []bool
}

func (f *fakeRetireUndoer) LockRetireOps(opIDs []string) (func(), error) {
	f.locked = append(f.locked, append([]string(nil), opIDs...))
	if f.lockErr != nil {
		return nil, f.lockErr
	}
	f.held++
	return func() { f.held-- }, nil
}

// ReadRetireOp records each op read: UndoCombine reads an op once, however
// many of its shells it plans.
func (f *fakeRetireUndoer) ReadRetireOp(opID string) (RetireOp, error) {
	f.opsRead = append(f.opsRead, opID)
	if f.planErr != nil {
		return nil, f.planErr
	}
	return fakeRetireOp{f: f, opID: opID}, nil
}

type fakeRetireOp struct {
	f    *fakeRetireUndoer
	opID string
}

func (o fakeRetireOp) PlanShell(shellID string) (*RetireReversal, error) {
	f := o.f
	key := o.opID + "/" + shellID
	f.planned = append(f.planned, key)
	rev := &RetireReversal{Live: f.live, Reasons: f.reasons, PendingFileIDs: f.pendingIDs}
	if f.live {
		rev.Revert = func() error {
			f.reverted = append(f.reverted, key)
			f.heldAtRevert = append(f.heldAtRevert, f.held)
			if f.snapshot != nil {
				if b, err := f.store.GetBookByID(f.shell); err == nil && b == nil {
					_, cerr := f.store.CreateBook(f.snapshot)
					require.NoError(f.t, cerr)
				}
			}
			b, err := f.store.GetBookByID(f.shell)
			require.NoError(f.t, err)
			f.shellSoftDeleted = append(f.shellSoftDeleted, b != nil && b.IsSoftDeleted())
			return f.revertErr
		}
	}
	return rev, nil
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
	assert.Equal(t, []string{"retire-op-1/" + f.absA}, u.planned)
	assert.Empty(t, u.reverted, "a retire with nothing pending is dead: its link is read and nothing is reversed")
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

func TestUndoCombine_PlanErrorRefuses(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	ms.SetRetireUndoer(&fakeRetireUndoer{t: t, store: store, shell: f.absA, planErr: errors.New("read failed")})
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
	require.NoError(t, err, "the combine itself accepts a holder of retired versions")
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
	assert.False(t, ms.fileAlreadyUndone(j, fm, ""), "a retired row on its old owner is not where a completed undo puts it")

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

// A shell purged since the combine is restored when a live, clean retire link
// names it: step 0 recreates it (the purge snapshot), then the shell is
// restored. The check must not refuse it as purged before step 0 can run.
func TestUndoCombine_PurgedShellWithALiveCleanRetireIsRecreatedNotRefused(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	snap, err := store.GetBookByID(f.absA)
	require.NoError(t, err)
	require.NotNil(t, snap)
	require.NoError(t, store.DeleteBook(f.absA))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA, snapshot: snap}
	ms.SetRetireUndoer(u)

	res, err := ms.UndoCombine(journalID)
	require.NoError(t, err)
	assert.Contains(t, res.RestoredBooks, f.absA)
	assert.Equal(t, []string{"retire-op-1/" + f.absA}, u.reverted)
	assert.False(t, viewBook(t, store, f.absA).SoftDel, "the recreated shell is restored")
}

// Without a live link that can recreate it, a purged shell still refuses, with
// the plain reason and no write; and a link that cannot be reversed refuses
// naming the retire.
func TestUndoCombine_PurgedShellNothingCanRecreateRefusesWithAClearReason(t *testing.T) {
	t.Run("no link", func(t *testing.T) {
		store, f, ms, journalID := combined(t)
		require.NoError(t, store.DeleteBook(f.absA))
		_, err := ms.UndoCombine(journalID)
		text := refusalText(t, err)
		assert.Contains(t, text, f.absA)
		assert.Contains(t, text, "no longer exists (purged)")
	})
	t.Run("link with no undoer", func(t *testing.T) {
		store, f, ms, journalID := combined(t)
		require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
		require.NoError(t, store.DeleteBook(f.absA))
		_, err := ms.UndoCombine(journalID)
		text := refusalText(t, err)
		assert.Contains(t, text, "no longer exists (purged)")
		assert.Contains(t, text, "cannot recreate it")
		assert.Contains(t, text, "retire-op-1")
	})
	t.Run("link whose plan refuses", func(t *testing.T) {
		store, f, ms, journalID := combined(t)
		require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
		require.NoError(t, store.DeleteBook(f.absA))
		u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA,
			reasons: []string{"shell " + f.absA + " was purged and has no purge snapshot to recreate it from"}}
		ms.SetRetireUndoer(u)
		_, err := ms.UndoCombine(journalID)
		text := refusalText(t, err)
		assert.Contains(t, text, "cannot recreate it")
		assert.Contains(t, text, "no purge snapshot")
		assert.Empty(t, u.reverted)
	})
	t.Run("dead link", func(t *testing.T) {
		store, f, ms, journalID := combined(t)
		require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
		require.NoError(t, store.DeleteBook(f.absA))
		ms.SetRetireUndoer(&fakeRetireUndoer{t: t, store: store, live: false, shell: f.absA})
		_, err := ms.UndoCombine(journalID)
		assert.Contains(t, refusalText(t, err), "no longer exists (purged)", "a dead link recreates nothing")
	})
}

// A retire that took a file the shell was given after the combine (it hides it
// from the shell's file list) is still a file given since: a straight undo
// refuses, and says the retire holds it.
func TestUndoCombine_StraightUndoCountsTheRowsARetireTookAsGiven(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA, pendingIDs: []string{"01SYNTHETICTOOKBYRETIRE000"}}
	ms.SetRetireUndoer(u)
	preBooks, preFiles := snapshot(t, store, f)

	_, err := ms.UndoCombine(journalID)
	text := refusalText(t, err)
	assert.Contains(t, text, "has since been given 1 file(s)")
	assert.Contains(t, text, "taken by a retire")
	assert.Empty(t, u.reverted)
	postBooks, postFiles := snapshot(t, store, f)
	assert.Equal(t, preBooks, postBooks)
	assert.Equal(t, preFiles, postFiles)
}

// A retry after a failed attempt counts what the shell was given since the
// combine exactly as a first attempt does. The failed attempt left the shell
// trashed (step 0 failed before step 1 restored it), so the shell can be given
// a row after it and a retire can then take that row: the retire hides it from
// GetBookFiles, and it is still a file given since. The rows of the combine's
// own journal (Absorbed[].Files) are the only ones a retry expects on the
// shell.
func TestUndoCombine_RetryCountsRowsGainedAfterAFailedAttempt(t *testing.T) {
	failFirst := func(t *testing.T) (database.Store, undoFixture, *Service, string) {
		store, f, ms, journalID := combined(t)
		require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
		ms.SetRetireUndoer(&fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA, revertErr: errors.New("boom")})
		_, err := ms.UndoCombine(journalID)
		require.Error(t, err)
		j, gerr := ms.GetCombineJournal(journalID)
		require.NoError(t, gerr)
		require.Equal(t, CombineJournalUndoFailed, j.Status)
		return store, f, ms, journalID
	}

	t.Run("a row given since and held by a retire", func(t *testing.T) {
		store, f, ms, journalID := failFirst(t)
		require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-2"))
		u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA, pendingIDs: []string{"01SYNTHETICGAINEDROW00000"}}
		ms.SetRetireUndoer(u)
		preBooks, preFiles := snapshot(t, store, f)

		_, err := ms.UndoCombine(journalID)
		text := refusalText(t, err)
		assert.Contains(t, text, "has since been given 1 file(s)")
		assert.Contains(t, text, "taken by a retire")
		assert.Empty(t, u.reverted, "refused before step 0")
		postBooks, postFiles := snapshot(t, store, f)
		assert.Equal(t, preBooks, postBooks)
		assert.Equal(t, preFiles, postFiles)
	})
	t.Run("a row given since, live on the shell", func(t *testing.T) {
		store, f, ms, journalID := failFirst(t)
		row := &database.BookFile{ID: "01SYNTHETICGAINEDROW00000", BookID: f.absA, FilePath: "/tmp/undo/gained/01.mp3", Format: "mp3", TrackNumber: 1}
		require.NoError(t, store.CreateBookFile(row))
		ms.SetRetireUndoer(&fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA})
		_, err := ms.UndoCombine(journalID)
		assert.Contains(t, refusalText(t, err), "has since been given 1 file(s)")
	})
	t.Run("a row given since and then given back by a revert outside the undo", func(t *testing.T) {
		// The retire was reversed on its own: the row is live on the shell
		// again and nothing names it as the shell's. It was never in the
		// combine's journal, so it is still a gift.
		store, f, ms, journalID := failFirst(t)
		row := &database.BookFile{ID: "01SYNTHETICGAVEBACKROW0000", BookID: f.absA, FilePath: "/tmp/undo/giveback/01.mp3", Format: "mp3", TrackNumber: 1}
		require.NoError(t, store.CreateBookFile(row))
		ms.SetRetireUndoer(&fakeRetireUndoer{t: t, store: store, live: false, shell: f.absA})
		_, err := ms.UndoCombine(journalID)
		assert.Contains(t, refusalText(t, err), "has since been given 1 file(s)")
	})
	t.Run("nothing given since", func(t *testing.T) {
		store, f, ms, journalID := failFirst(t)
		ms.SetRetireUndoer(&fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA})
		_, err := ms.UndoCombine(journalID)
		require.NoError(t, err, "the retry runs: the failed attempt's own rows are not gifts")
	})
}

// A retire op is read once, however many of the combine's shells it retired.
func TestUndoCombine_ReadsEachRetireOpOnce(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absB, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA}
	ms.SetRetireUndoer(u)
	_, err := ms.UndoCombine(journalID)
	require.NoError(t, err)
	assert.Equal(t, []string{"retire-op-1"}, u.opsRead, "one op, one read")
	assert.Equal(t, []string{"retire-op-1/" + f.absA, "retire-op-1/" + f.absB}, u.planned, "both shells planned from it")
}

// A shell retired twice (a second retire after a partial revert of the first)
// has two links; the undo plans both and reverses the newer retire first.
func TestUndoCombine_TwoRetireOpsOfOneShellAreBothReversedNewestFirst(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "01RETIREOPOLDER"))
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "02RETIREOPNEWER"))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA}
	ms.SetRetireUndoer(u)

	links, err := ListCombineRetireLinks(store, journalID)
	require.NoError(t, err)
	require.Len(t, links, 2, "the second op adds a link, it does not overwrite the first")

	_, err = ms.UndoCombine(journalID)
	require.NoError(t, err)
	assert.Equal(t, []string{"01RETIREOPOLDER/" + f.absA, "02RETIREOPNEWER/" + f.absA}, u.planned, "each link is planned exactly once, in link order")
	assert.Equal(t, []string{"02RETIREOPNEWER/" + f.absA, "01RETIREOPOLDER/" + f.absA}, u.reverted)
	assert.False(t, viewBook(t, store, f.absA).SoftDel)
}

// A busy retire op refuses the combine undo BEFORE its first write: the journal
// keeps its Applied status (marking it undo_failed first would make the next
// attempt run with the retry tolerance of a half-done undo that never started),
// and nothing is reversed or restored.
func TestUndoCombine_BusyRetireOpRefusesBeforeTheRetryMark(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA, lockErr: errors.New("retire op retire-op-1: busy")}
	ms.SetRetireUndoer(u)
	preBooks, preFiles := snapshot(t, store, f)

	_, err := ms.UndoCombine(journalID)
	var refused *CombineUndoRefusedError
	require.True(t, errors.As(err, &refused), "%v", err)
	assert.Contains(t, refused.Reasons[0], "busy")
	assert.Empty(t, u.reverted)
	postBooks, postFiles := snapshot(t, store, f)
	assert.Equal(t, preBooks, postBooks)
	assert.Equal(t, preFiles, postFiles)
	j, gerr := ms.GetCombineJournal(journalID)
	require.NoError(t, gerr)
	assert.Equal(t, CombineJournalApplied, j.Status, "no retry mark: nothing was started")
	assert.Empty(t, j.LastError)

	u.lockErr = nil
	_, err = ms.UndoCombine(journalID)
	require.NoError(t, err, "free now: the undo runs")
}

// The retire ops are locked once each (two shells of one op take one lock), held
// across every Revert, and released when the undo ends, whether it succeeds or
// not.
func TestUndoCombine_LocksEachLiveRetireOpOnceAndHoldsItThroughTheReversal(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absB, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: true, shell: f.absA, revertErr: errors.New("boom")}
	ms.SetRetireUndoer(u)

	_, err := ms.UndoCombine(journalID)
	require.Error(t, err)
	assert.Equal(t, [][]string{{"retire-op-1"}}, u.locked, "one op, one lock")
	assert.Equal(t, []int{1}, u.heldAtRevert, "held while the reversal ran")
	assert.Zero(t, u.held, "released after a failed undo")

	u.revertErr = nil
	_, err = ms.UndoCombine(journalID)
	require.NoError(t, err)
	assert.Zero(t, u.held, "released after a finished undo")
}

// A dead link takes no lock: nothing of it is reversed.
func TestUndoCombine_DeadLinkTakesNoLock(t *testing.T) {
	store, f, ms, journalID := combined(t)
	require.NoError(t, WriteCombineRetireLink(store, journalID, f.absA, "retire-op-1"))
	u := &fakeRetireUndoer{t: t, store: store, live: false, shell: f.absA, lockErr: errors.New("would refuse")}
	ms.SetRetireUndoer(u)
	_, err := ms.UndoCombine(journalID)
	require.NoError(t, err)
	assert.Empty(t, u.locked)
}
