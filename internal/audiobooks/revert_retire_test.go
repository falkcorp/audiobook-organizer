// file: internal/audiobooks/revert_retire_test.go
// version: 1.3.0
// guid: 7cdf45d6-39e8-4d1b-9e70-6ed21b7ada58
// last-edited: 2026-10-24

package audiobooks

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// retireRevertFx is a holder, a trashed shell with two rows, and a real store.
// Every value is synthetic.
type retireRevertFx struct {
	st            *database.PebbleStore
	holder, shell *database.Book
	rows          []*database.BookFile
}

func newRetireRevertFx(t *testing.T) *retireRevertFx {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.WaitForWarmup()
	holder, err := st.CreateBook(&database.Book{Title: "Synthetic Holder", FilePath: "/lib/syn/holder"})
	require.NoError(t, err)
	shell, err := st.CreateBook(&database.Book{Title: "Synthetic Shell", FilePath: "/lib/syn/shell"})
	require.NoError(t, err)
	fx := &retireRevertFx{st: st, holder: holder, shell: shell}
	for i, r := range []database.BookFile{
		{FilePath: "/lib/syn/shell/01.mp3", FileHash: "hash-a", TrackNumber: 1, Duration: 60, FileSize: 600, ITunesPersistentID: "PID-SYN-A"},
		{FilePath: "/lib/syn/shell/02.mp3", FileHash: "hash-b", TrackNumber: 2, Duration: 70, FileSize: 700},
	} {
		row := r
		row.BookID = shell.ID
		require.NoError(t, st.CreateBookFile(&row), "row %d", i)
		fx.rows = append(fx.rows, &row)
	}
	// A retire acts on a trashed shell; the writer refuses a live one.
	trashBook(t, st, shell)
	return fx
}

// trashBook soft-deletes b, as a combine or an earlier retire leaves a shell.
func trashBook(t *testing.T, st *database.PebbleStore, b *database.Book) {
	t.Helper()
	trashed, at := true, time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)
	b.MarkedForDeletion, b.MarkedForDeletionAt = &trashed, &at
	_, err := st.UpdateBook(b.ID, b)
	require.NoError(t, err)
}

// untrashBook puts a trashed book back, so it can hold a retired version.
func untrashBook(t *testing.T, st *database.PebbleStore, b *database.Book) {
	t.Helper()
	live := false
	b.MarkedForDeletion, b.MarkedForDeletionAt = &live, nil
	_, err := st.UpdateBook(b.ID, b)
	require.NoError(t, err)
}

func (fx *retireRevertFx) ids() []string {
	out := make([]string, len(fx.rows))
	for i, r := range fx.rows {
		out[i] = r.ID
	}
	return out
}

func (fx *retireRevertFx) writer(opID string) *repairs.Writer {
	return repairs.NewWriter(fx.st, fx.st, "fx", "bulk_update", "repairs-").WithJournal(fx.st, fx.st, opID)
}

func (fx *retireRevertFx) retire(t *testing.T, w *repairs.Writer, version string) {
	t.Helper()
	require.NoError(t, w.RetireVersion(repairs.RetireSpec{
		Shell: fx.shell.ID, Holder: fx.holder.ID, VersionID: version, FileIDs: fx.ids(),
		Format: "mp3", Source: "retired_book", Now: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
	}))
}

// view is a row with the fields a retire and its revert may legitimately
// rewrite (UpdatedAt) cleared, so equality means "restored exactly".
func view(t *testing.T, st *database.PebbleStore, bookID, fileID string) database.BookFile {
	t.Helper()
	f, err := st.GetBookFileByID(bookID, fileID)
	require.NoError(t, err)
	require.NotNil(t, f, "row %s under %s", fileID, bookID)
	c := *f
	c.UpdatedAt = time.Time{}
	return c
}

func (fx *retireRevertFx) snapshot(t *testing.T) map[string]database.BookFile {
	t.Helper()
	out := map[string]database.BookFile{}
	for _, id := range fx.ids() {
		out[id] = view(t, fx.st, fx.shell.ID, id)
	}
	return out
}

func TestRevertRetire_RoundTripRestoresRowsAndIndexExactly(t *testing.T) {
	fx := newRetireRevertFx(t)
	before := fx.snapshot(t)
	byPath, err := fx.st.GetBookFileByPath(fx.rows[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, fx.rows[0].ID, byPath.ID)
	byPID, err := fx.st.GetBookFileByPID("PID-SYN-A")
	require.NoError(t, err)
	require.Equal(t, fx.rows[0].ID, byPID.ID)

	fx.retire(t, fx.writer("op-rt"), "ver-rt")
	// Retired: the live lookups no longer find the row, the version exists.
	gone, err := fx.st.GetBookFileByPath(fx.rows[0].FilePath)
	require.NoError(t, err)
	require.Nil(t, gone, "a retired row is not a live file at its path")
	v, err := fx.st.GetBookVersion("ver-rt")
	require.NoError(t, err)
	require.NotNil(t, v)

	res, err := NewRevertService(fx.st).RevertOperation("op-rt")
	require.NoError(t, err)
	assert.Equal(t, 3, res.Restored, "the version row and both file rows")
	assert.Zero(t, res.Failed)

	assert.Equal(t, before, fx.snapshot(t), "every row is back under the shell exactly as it was: flag, version id and all")
	byPath, err = fx.st.GetBookFileByPath(fx.rows[0].FilePath)
	require.NoError(t, err)
	require.NotNil(t, byPath)
	assert.Equal(t, fx.rows[0].ID, byPath.ID, "the path index names the row again")
	assert.Equal(t, fx.shell.ID, byPath.BookID)
	byPID, err = fx.st.GetBookFileByPID("PID-SYN-A")
	require.NoError(t, err)
	require.NotNil(t, byPID)
	assert.Equal(t, fx.rows[0].ID, byPID.ID)
	v, err = fx.st.GetBookVersion("ver-rt")
	require.NoError(t, err)
	assert.Nil(t, v, "the retired version is deleted")
	held, err := fx.st.GetBookFiles(fx.holder.ID)
	require.NoError(t, err)
	assert.Empty(t, held)

	changes, err := fx.st.GetOperationChanges("op-rt")
	require.NoError(t, err)
	for _, c := range changes {
		assert.NotNil(t, c.RevertedAt, "%s is marked reverted", c.ChangeType)
	}
	_, err = NewRevertService(fx.st).RevertOperation("op-rt")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already been reverted")
}

// The documented rule (PLAN 2.x, retired_book_files.go): a path another LIVE
// row also names keeps its single-owner key through a retire, and through the
// revert. The test fixes that rule so a change to either side is noticed.
func TestRevertRetire_SharedPathLiveRowKeepsTheKey(t *testing.T) {
	fx := newRetireRevertFx(t)
	other, err := fx.st.CreateBook(&database.Book{Title: "Synthetic Other", FilePath: "/lib/syn/other"})
	require.NoError(t, err)
	live := &database.BookFile{BookID: other.ID, FilePath: fx.rows[0].FilePath, FileHash: "hash-live", TrackNumber: 1}
	require.NoError(t, fx.st.CreateBookFile(live))
	owner := func() string {
		f, err := fx.st.GetBookFileByPath(fx.rows[0].FilePath)
		require.NoError(t, err)
		require.NotNil(t, f)
		return f.ID
	}
	require.Contains(t, []string{live.ID, fx.rows[0].ID}, owner())
	ownerBefore := owner()

	fx.retire(t, fx.writer("op-shared"), "ver-shared")
	assert.Equal(t, live.ID, owner(), "the live row keeps the key while the shell's row is retired")

	_, err = NewRevertService(fx.st).RevertOperation("op-shared")
	require.NoError(t, err)
	assert.Equal(t, ownerBefore, owner(), "and the key is where it was before the retire")
	assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, fx.rows[0].ID).BookID)
}

func TestRevertRetire_PurgedShellRefusesWhenNothingCanRecreateIt(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-purged"), "ver-purged")
	require.NoError(t, fx.st.DeleteBook(fx.shell.ID))

	res, err := NewRevertService(fx.st).RevertOperation("op-purged")
	require.Error(t, err)
	require.NotNil(t, res)
	assert.Equal(t, 3, res.Failed, "both file rows refuse (shell gone, nothing can bring it back) and the version row waits for them")
	assert.Contains(t, err.Error(), "purged")
	for _, id := range fx.ids() {
		f, gerr := fx.st.GetBookFileByID(fx.holder.ID, id)
		require.NoError(t, gerr)
		require.NotNil(t, f)
		assert.True(t, f.Retired, "nothing was written")
	}
	// The preflight says the same before the user clicks.
	report, perr := undo.PreflightUndoConflicts(fx.st, "op-purged")
	require.NoError(t, perr)
	assert.Len(t, report.BookMissing, 2)
}

// recreatingStore is the PR 6 seam: a purge snapshot that can bring a shell
// back with its id. It records what it recreated.
type recreatingStore struct {
	*database.PebbleStore
	snapshots map[string]database.Book
	recreated []string
}

func (s *recreatingStore) HasPurgeSnapshot(id string) (bool, error) {
	_, ok := s.snapshots[id]
	return ok, nil
}

func (s *recreatingStore) RecreatePurgedBook(id string) error {
	b := s.snapshots[id]
	if _, err := s.PebbleStore.CreateBook(&b); err != nil {
		return err
	}
	s.recreated = append(s.recreated, id)
	return nil
}

func TestRevertRetire_RecreatesPurgedShellBeforeTheShellsOtherRows(t *testing.T) {
	fx := newRetireRevertFx(t)
	require.NoError(t, fx.st.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "EXT-SYN-1", BookID: fx.shell.ID}))
	w := fx.writer("op-recreate")
	fx.retire(t, w, "ver-recreate")
	// The retire's other journaled steps, newer than the retire rows: the
	// shell's external id moves to the holder and its merged-into pointer is set.
	require.NoError(t, w.Step(fx.shell.ID, undo.ChangeTypeExternalIDReassign, "external_id:itunes/EXT-SYN-1", fx.shell.ID, fx.holder.ID, func() error {
		return fx.st.ReassignExternalID("itunes", "EXT-SYN-1", fx.holder.ID)
	}))
	require.NoError(t, w.Journal(fx.shell.ID, undo.ChangeTypeBookMergedInto, "merged_into_book_id", "", fx.holder.ID))
	_, err := fx.st.ModifyBook(fx.shell.ID, func(b *database.Book) error {
		h := fx.holder.ID
		b.MergedIntoBookID = &h
		return nil
	})
	require.NoError(t, err)

	snap, err := fx.st.GetBookByID(fx.shell.ID)
	require.NoError(t, err)
	require.NoError(t, fx.st.DeleteBook(fx.shell.ID))
	store := &recreatingStore{PebbleStore: fx.st, snapshots: map[string]database.Book{fx.shell.ID: *snap}}

	// The preflight does not call the shell's rows book_missing.
	report, err := undo.PreflightUndoConflicts(store, "op-recreate")
	require.NoError(t, err)
	assert.Empty(t, report.BookMissing, "a shell a purge snapshot can recreate is restorable")

	res, err := NewRevertService(store).RevertOperation("op-recreate")
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, []string{fx.shell.ID}, store.recreated, "recreated once, by the pre-pass, before any row ran")
	owner, err := fx.st.GetBookByExternalID("itunes", "EXT-SYN-1")
	require.NoError(t, err)
	assert.Equal(t, fx.shell.ID, owner, "the external id is back on the shell")
	got, err := fx.st.GetBookByID(fx.shell.ID)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Nil(t, got.MergedIntoBookID)
	for _, id := range fx.ids() {
		assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, id).BookID)
	}
}

func TestRevertRetire_AlreadyUnretiredRowIsAlreadyRestored(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-resume"), "ver-resume")
	// Someone un-retired one row by hand: it is back on the shell, un-flagged.
	require.NoError(t, fx.st.UnretireBookFiles([]database.BookFileMove{{FileIDs: []string{fx.rows[0].ID}, SourceBookID: fx.holder.ID}},
		fx.shell.ID, "ver-resume", nil))

	res, err := NewRevertService(fx.st).RevertOperation("op-resume")
	require.NoError(t, err)
	assert.Equal(t, 1, res.AlreadyRestored)
	assert.Equal(t, 3, res.Restored)
}

func TestRevertRetire_VersionDeleteWaitsForItsRows(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-dep"), "ver-dep")
	changes, err := fx.st.GetOperationChanges("op-dep")
	require.NoError(t, err)
	var versionRow *database.OperationChange
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeBookVersionCreate {
			versionRow = c
		}
	}
	require.NotNil(t, versionRow)

	err = NewRevertService(fx.st).revertChangeIn(versionRow, nil)
	require.Error(t, err)
	assert.Equal(t, undo.ReasonDependentNotReverted, undo.RefusalReason(err), "rows still name the version")
	v, gerr := fx.st.GetBookVersion("ver-dep")
	require.NoError(t, gerr)
	assert.NotNil(t, v, "nothing was deleted")
}

func TestRevertRetire_RehomeIsReversedBeforeTheRetireRows(t *testing.T) {
	fx := newRetireRevertFx(t)
	w := fx.writer("op-chain")
	fx.retire(t, w, "ver-chain")
	newHolder, err := fx.st.CreateBook(&database.Book{Title: "Synthetic New Holder", FilePath: "/lib/syn/new"})
	require.NoError(t, err)
	require.NoError(t, w.RehomeRetiredVersion("ver-chain", newHolder.ID))

	res, err := NewRevertService(fx.st).RevertOperation("op-chain")
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, 4, res.Restored, "version, two rows and the rehome")
	for _, id := range fx.ids() {
		assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, id).BookID)
	}
	v, err := fx.st.GetBookVersion("ver-chain")
	require.NoError(t, err)
	assert.Nil(t, v)
}

// A journaled step on a row that has since been retired into another book's
// version is changed-since: the row now belongs to that version.
func TestRevertRetire_RetiredRowIsChangedSinceForEarlierOperations(t *testing.T) {
	fx := newRetireRevertFx(t)
	rowID := fx.rows[1].ID
	f := fx.rows[1]
	f.TrackNumber = 5
	require.NoError(t, fx.st.UpdateBookFile(f.ID, f))
	require.NoError(t, fx.st.CreateOperationChange(&database.OperationChange{
		ID: "chg-track", OperationID: "op-track", BookID: fx.shell.ID, ChangeType: undo.ChangeTypeBookFileTrack,
		FieldName: "book_file:" + rowID, OldValue: "2", NewValue: "5",
	}))
	fx.retire(t, fx.writer("op-later-retire"), "ver-later")

	report, err := undo.PreflightUndoConflicts(fx.st, "op-track")
	require.NoError(t, err)
	require.Len(t, report.CheckFailed, 1, "%+v", report)
	assert.Equal(t, undo.ReasonChangedSince, report.CheckFailed[0].Reason)

	res, err := NewRevertService(fx.st).RevertOperation("op-track")
	require.Error(t, err)
	assert.Equal(t, 1, res.Failed)
	assert.Equal(t, 1, res.ChangedSince)
	got, gerr := fx.st.GetBookFileByID(fx.holder.ID, rowID)
	require.NoError(t, gerr)
	assert.Equal(t, 5, got.TrackNumber, "the retired row is not overwritten")
	assert.True(t, got.Retired)
}

func TestRevertOperationForBooks_RevertsOneShellAtATime(t *testing.T) {
	fx := newRetireRevertFx(t)
	shell2, err := fx.st.CreateBook(&database.Book{Title: "Synthetic Shell Two", FilePath: "/lib/syn/shell2"})
	require.NoError(t, err)
	row2 := &database.BookFile{BookID: shell2.ID, FilePath: "/lib/syn/shell2/01.mp3", FileHash: "hash-c", TrackNumber: 1}
	require.NoError(t, fx.st.CreateBookFile(row2))
	trashBook(t, fx.st, shell2)
	w := fx.writer("op-two")
	fx.retire(t, w, "ver-one")
	require.NoError(t, w.RetireVersion(repairs.RetireSpec{Shell: shell2.ID, Holder: fx.holder.ID, VersionID: "ver-two",
		FileIDs: []string{row2.ID}, Format: "mp3", Source: "retired_book"}))

	rs := NewRevertService(fx.st)
	res, err := rs.RevertOperationForBooks("op-two", []string{fx.shell.ID})
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, 3, res.Restored)
	for _, id := range fx.ids() {
		assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, id).BookID)
	}
	still, err := fx.st.GetBookFileByID(fx.holder.ID, row2.ID)
	require.NoError(t, err)
	require.NotNil(t, still)
	assert.True(t, still.Retired, "the other shell's retire stands: selection is by shell and version, never by the holder every row shares")
	v, err := fx.st.GetBookVersion("ver-two")
	require.NoError(t, err)
	assert.NotNil(t, v)
	changes, err := fx.st.GetOperationChanges("op-two")
	require.NoError(t, err)
	reverted := 0
	for _, c := range changes {
		if c.RevertedAt != nil {
			reverted++
		}
	}
	assert.Equal(t, 3, reverted, "only the rows of the named shell are marked")

	res, err = rs.RevertOperationForBooks("op-two", []string{shell2.ID})
	require.NoError(t, err, "%+v", res)
	assert.Equal(t, 2, res.Restored)
	_, err = rs.RevertOperationForBooks("op-two", []string{fx.shell.ID})
	require.Error(t, err, "nothing left to revert for the first shell")
	_, err = rs.RevertOperationForBooks("op-two", nil)
	require.Error(t, err)
}

func TestRetireUndoer_PlanAndRevertAShell(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-undoer"), "ver-undoer")
	u := NewRevertService(fx.st).retireUndoer()

	plan, err := planRetire(u, "op-undoer", fx.shell.ID)
	require.NoError(t, err)
	assert.True(t, plan.Live)
	assert.Empty(t, plan.Reasons)
	assert.ElementsMatch(t, fx.ids(), plan.PendingFileIDs, "the shell's rows the retire holds")
	require.NotNil(t, plan.Revert)
	// The plan wrote nothing.
	for _, id := range fx.ids() {
		f, gerr := fx.st.GetBookFileByID(fx.holder.ID, id)
		require.NoError(t, gerr)
		require.NotNil(t, f)
		assert.True(t, f.Retired)
	}

	// An edit of a retired row since makes the retire unreversible, named.
	edited := view(t, fx.st, fx.holder.ID, fx.rows[0].ID)
	_, err = fx.st.ModifyBookFile(fx.holder.ID, edited.ID, func(f *database.BookFile) error {
		f.VersionID = "ver-someone-else"
		return nil
	})
	require.NoError(t, err)
	plan, err = planRetire(u, "op-undoer", fx.shell.ID)
	require.NoError(t, err)
	assert.True(t, plan.Live)
	require.NotEmpty(t, plan.Reasons)
	assert.Contains(t, plan.Reasons[0], fx.rows[0].ID)
	require.Error(t, plan.Revert(), "a refused plan's Revert refuses too, writing nothing")
	_, err = fx.st.ModifyBookFile(fx.holder.ID, edited.ID, func(f *database.BookFile) error {
		f.VersionID = "ver-undoer"
		return nil
	})
	require.NoError(t, err)

	plan, err = planRetire(u, "op-undoer", fx.shell.ID)
	require.NoError(t, err)
	require.Empty(t, plan.Reasons)
	require.NoError(t, plan.Revert())
	for _, id := range fx.ids() {
		assert.Equal(t, fx.shell.ID, view(t, fx.st, fx.shell.ID, id).BookID)
	}
	plan, err = planRetire(u, "op-undoer", fx.shell.ID)
	require.NoError(t, err)
	assert.False(t, plan.Live, "a reverted retire is a dead link")
	assert.Empty(t, plan.PendingFileIDs)
	assert.Nil(t, plan.Revert)
}

func TestRetireUndoer_RefusesRowsTheCombineUndoDoesNotReverse(t *testing.T) {
	fx := newRetireRevertFx(t)
	fx.retire(t, fx.writer("op-extra"), "ver-extra")
	require.NoError(t, fx.st.CreateOperationChange(&database.OperationChange{
		ID: "chg-title", OperationID: "op-extra", BookID: fx.shell.ID, ChangeType: "metadata_update",
		FieldName: "title", OldValue: "a", NewValue: "b",
	}))
	u := NewRevertService(fx.st).retireUndoer()
	plan, err := planRetire(u, "op-extra", fx.shell.ID)
	require.NoError(t, err)
	assert.True(t, plan.Live)
	require.Len(t, plan.Reasons, 1)
	assert.Contains(t, plan.Reasons[0], "metadata_update")
	err = plan.Revert()
	require.Error(t, err)
	f, gerr := fx.st.GetBookFileByID(fx.holder.ID, fx.rows[0].ID)
	require.NoError(t, gerr)
	assert.True(t, f.Retired, "refused before anything was written")
}

func TestRevertScope_SelectsByShellAndVersionNeverByHolder(t *testing.T) {
	place := func(book, ver string) string { return undo.RetirePlace{BookID: book, VersionID: ver}.Encode() }
	rows := []*database.OperationChange{
		{ID: "r1", BookID: "holder", ChangeType: undo.ChangeTypeBookFileRetire, FieldName: "book_file:f1", OldValue: place("s1", ""), NewValue: place("holder", "v1")},
		{ID: "r2", BookID: "holder", ChangeType: undo.ChangeTypeBookFileRetire, FieldName: "book_file:f2", OldValue: place("s2", ""), NewValue: place("holder", "v2")},
		{ID: "ext", BookID: "s1", ChangeType: undo.ChangeTypeExternalIDReassign, FieldName: "external_id:itunes/x", OldValue: "s1", NewValue: "holder"},
		{ID: "mi", BookID: "s2", ChangeType: undo.ChangeTypeBookMergedInto, FieldName: "merged_into_book_id", NewValue: "holder"},
		{ID: "rehome1", BookID: "h2", ChangeType: undo.ChangeTypeRetiredVersionRehome, FieldName: undo.BookVersionField("v1"), OldValue: "holder", NewValue: "h2"},
		{ID: "rehome2", BookID: "h2", ChangeType: undo.ChangeTypeRetiredVersionRehome, FieldName: undo.BookVersionField("v2"), OldValue: "holder", NewValue: "h2"},
		{ID: "rehomeFromShell", BookID: "h2", ChangeType: undo.ChangeTypeRetiredVersionRehome, FieldName: undo.BookVersionField("v7"), OldValue: "s1", NewValue: "h2"},
		{ID: "plan", BookID: "s1", ChangeType: undo.ChangeTypeRepairPlanRecord, FieldName: "row:1"},
		{ID: "bad", BookID: "holder", ChangeType: undo.ChangeTypeBookFileRetire, FieldName: "book_file:f9", OldValue: "{", NewValue: "{"},
	}
	var got []string
	for _, c := range newRevertScope([]string{"s1"}).filter(rows) {
		got = append(got, c.ID)
	}
	assert.Equal(t, []string{"r1", "ext", "rehome1", "rehomeFromShell", "bad"}, got,
		"a rehome that took a version off the shell is the shell's; a retire row that does not parse is selected, to be refused by name")
}

func TestRevertRetire_MissingStoreSurfaceRefusesRatherThanPanics(t *testing.T) {
	// A store that is not the Pebble store has no retired-row writer.
	s := &ledgerStub{book: &database.Book{ID: "holder"}}
	rs := NewRevertService(s)
	err := rs.revertBookFileRetire(&database.OperationChange{
		ID: "c", BookID: "holder", ChangeType: undo.ChangeTypeBookFileRetire, FieldName: "book_file:f1",
		OldValue: undo.RetirePlace{BookID: "shell"}.Encode(), NewValue: undo.RetirePlace{BookID: "holder", VersionID: "v"}.Encode(),
	})
	require.Error(t, err)
	assert.Equal(t, undo.ReasonFieldUnreadable, undo.RefusalReason(err))
}

// A row that already carried a (live) version id before the retire gets that
// id back, not an empty one: the retire row journals it as the prior id.
func TestRevertRetire_RestoresAPriorVersionID(t *testing.T) {
	fx := newRetireRevertFx(t)
	_, err := fx.st.CreateBookVersion(&database.BookVersion{ID: "ver-prior", BookID: fx.shell.ID, Status: database.BookVersionStatusActive, Format: "mp3", Source: "manual"})
	require.NoError(t, err)
	_, err = fx.st.ModifyBookFile(fx.shell.ID, fx.rows[1].ID, func(f *database.BookFile) error { f.VersionID = "ver-prior"; return nil })
	require.NoError(t, err)
	before := fx.snapshot(t)
	require.Equal(t, "ver-prior", before[fx.rows[1].ID].VersionID)

	fx.retire(t, fx.writer("op-prior"), "ver-after")
	_, err = NewRevertService(fx.st).RevertOperation("op-prior")
	require.NoError(t, err)
	assert.Equal(t, before, fx.snapshot(t), "the prior version id is back with everything else")
}

// planRetire plans one shell of one retire op: a read of the op, then the plan.
func planRetire(u merge.RetireUndoer, opID, shellID string) (*merge.RetireReversal, error) {
	op, err := u.ReadRetireOp(opID)
	if err != nil {
		return nil, err
	}
	return op.PlanShell(shellID)
}
